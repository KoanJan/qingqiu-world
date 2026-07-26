package tools

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// SearchChatHistoriesTool searches chat messages across sessions the agent
// participates in. Results are filtered by keyword match (case-insensitive
// substring) and grouped by session in the output.
type SearchChatHistoriesTool struct {
	personID      int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewSearchChatHistoriesTool creates a SearchChatHistoriesTool for the given person.
func NewSearchChatHistoriesTool(personID int64) *SearchChatHistoriesTool {
	return &SearchChatHistoriesTool{
		personID: personID,
	}
}

// Name returns the tool name.
func (s *SearchChatHistoriesTool) Name() ToolName { return ToolNameSearchChatHistories }

// Description returns a brief description of the tool.
func (s *SearchChatHistoriesTool) Description() string {
	return "Search chat history messages across sessions you participate in"
}

// Schema returns the LLM function definition for the tool.
func (s *SearchChatHistoriesTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: s.Name().String(),
		Description: "Search chat history messages across sessions you participate in. " +
			"Use this when you need to recall what was said earlier in a conversation.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"session_id": map[string]interface{}{
					"type":        "integer",
					"description": "Optional session ID to limit search to a specific session. If omitted, all sessions you participate in are searched.",
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Keyword or phrase to search for in message content.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of results to return (default: 20).",
				},
			},
			"required": []string{"query"},
		},
	}
}

// chatMessageResult holds a matched chat message for the result list.
type chatMessageResult struct {
	ID         int64  `json:"id"`
	SessionID  int64  `json:"session_id"`
	SenderName string `json:"sender_name"`
	Content    string `json:"content"` // truncated for display
	CreatedAt  string `json:"created_at"`
}

// Execute searches chat history for messages matching the query.
func (s *SearchChatHistoriesTool) Execute(args map[string]interface{}) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("query must be a non-empty string")
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}
	}

	var results []chatMessageResult
	var err error

	if sid, ok := args["session_id"].(float64); ok {
		results, err = s.searchSpecificSession(int64(sid), query, limit)
	} else {
		results, err = s.searchAllSessions(query, limit)
	}

	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return fmt.Sprintf("No messages matched query '%s'.", query), nil
	}

	applogger.Info("search_chat_histories completed",
		"person_id", s.personID,
		"query", query,
		"result_count", len(results),
	)

	return formatResults(query, results), nil
}

// searchSpecificSession searches messages in a single session by keyword.
// Returns an error if the person does not participate in that session.
func (s *SearchChatHistoriesTool) searchSpecificSession(sessionID int64, query string, limit int) ([]chatMessageResult, error) {
	// Verify participation.
	var count int64
	database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ?", sessionID, s.personID).
		Count(&count)
	if count == 0 {
		return nil, fmt.Errorf("session %d not found or you do not participate in it", sessionID)
	}

	messages, err := s.loadMessages([]int64{sessionID})
	if err != nil {
		return nil, err
	}

	nameMap := loadPersonNameMap(messages)
	return s.filterMessages(messages, nameMap, query, limit), nil
}

// searchAllSessions searches messages across all sessions the person participates in.
func (s *SearchChatHistoriesTool) searchAllSessions(query string, limit int) ([]chatMessageResult, error) {
	sessionIDs, err := s.getParticipantSessionIDs()
	if err != nil {
		return nil, err
	}
	if len(sessionIDs) == 0 {
		return nil, nil
	}

	messages, err := s.loadMessages(sessionIDs)
	if err != nil {
		return nil, err
	}

	nameMap := loadPersonNameMap(messages)
	return s.filterMessages(messages, nameMap, query, limit), nil
}

// getParticipantSessionIDs returns all session IDs this person participates in.
func (s *SearchChatHistoriesTool) getParticipantSessionIDs() ([]int64, error) {
	var ids []int64
	err := database.DB.Model(&model.ParticipantSession{}).
		Where("participant_id = ?", s.personID).
		Pluck("session_id", &ids).Error
	return ids, err
}

// loadMessages returns all messages from the given sessions in descending time order.
// Uses index-only DB access (no LIKE) to avoid holding DB locks.
func (s *SearchChatHistoriesTool) loadMessages(sessionIDs []int64) ([]model.Message, error) {
	var messages []model.Message
	err := database.DB.
		Where("session_id IN ?", sessionIDs).
		Order("created_at DESC").
		Find(&messages).Error
	return messages, err
}

// loadPersonNameMap batch-loads person names for a set of messages.
func loadPersonNameMap(messages []model.Message) map[int64]string {
	personIDs := make(map[int64]bool, len(messages))
	for _, m := range messages {
		personIDs[m.PersonID] = true
	}
	if len(personIDs) == 0 {
		return nil
	}

	idList := make([]int64, 0, len(personIDs))
	for pid := range personIDs {
		idList = append(idList, pid)
	}

	var persons []model.Person
	database.DB.Where("id IN ?", idList).Find(&persons)

	nameMap := make(map[int64]string, len(persons))
	for _, p := range persons {
		nameMap[p.ID] = p.Name
	}
	return nameMap
}

// filterMessages filters messages by keyword match in memory and returns top-N results.
func (s *SearchChatHistoriesTool) filterMessages(messages []model.Message, nameMap map[int64]string, query string, limit int) []chatMessageResult {
	queryLower := strings.ToLower(query)
	var results []chatMessageResult

	for _, m := range messages {
		if !strings.Contains(strings.ToLower(m.Content), queryLower) {
			continue
		}
		senderName := nameMap[m.PersonID]
		if senderName == "" {
			senderName = fmt.Sprintf("person_%d", m.PersonID)
		}
		results = append(results, chatMessageResult{
			ID:         m.ID,
			SessionID:  m.SessionID,
			SenderName: senderName,
			Content:    m.Content,
			CreatedAt:  m.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// formatResults groups matched messages by session for readable output.
func formatResults(query string, results []chatMessageResult) string {
	// Group by session, maintaining recency order within each group.
	// Messages are already in DESC time order from DB.
	groups := make(map[int64][]chatMessageResult)
	var sessionOrder []int64

	for _, r := range results {
		if _, exists := groups[r.SessionID]; !exists {
			sessionOrder = append(sessionOrder, r.SessionID)
		}
		groups[r.SessionID] = append(groups[r.SessionID], r)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d message(s) matching '%s':\n", len(results), query))

	for _, sid := range sessionOrder {
		msgs := groups[sid]
		sb.WriteString(fmt.Sprintf("\n--- Session %d (%d match%s) ---\n",
			sid, len(msgs), plural(len(msgs))))

		for _, r := range msgs {
			sb.WriteString(fmt.Sprintf("[%s] %s:\n%s\n",
				r.CreatedAt, r.SenderName, r.Content))
		}
	}

	return sb.String()
}

// plural returns "s" for count != 1, empty string otherwise.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}
