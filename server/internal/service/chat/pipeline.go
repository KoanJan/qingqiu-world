package chat

import (
	"context"
	"fmt"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
)

// pipeline holds the state for a single chat processing execution.
// It is short-lived (only exists during one Process call) and carries
// shared data between pipeline stages.
//
// Per the project convention, pipeline methods fetch agent data at point of
// use via agent.GetAgent(aiPersonID) rather than holding ac/llmConfig pointers.
type pipeline struct {
	session          *model.Session
	aiPersonID       int64
	readMessageRange [2]int64
	trigger          *Trigger // set by caller, augmented by loadMessages

	// guidance is the execution intent from the Decide phase (ChatPlan.Guidance).
	guidance string

	// Loaded in loadMessages
	messageCount int64
	windowSize   int
	kbIDs        []int64
	// partnerName is the conversation partner's name — the other participant
	// in this session (human in user-agent sessions, another agent in A2A
	// sessions). partnerPersonID is that partner's person ID, used to scope
	// entity profile lookups. Both replace a former hardcoded human-user
	// assumption that broke A2A addressing.
	partnerName     string
	partnerPersonID int64

	// Results from pipeline stages
	personStateResult  *comprehend.PersonState
	historySegments    []comprehend.Segment
	kbSegments         []comprehend.Segment
	needsClarification bool
	clarification      string
	taskResult         *TaskResultForAssembly
}

// loadMessages loads the trigger message from the database (when applicable),
// and initializes session-level parameters (message count, window size, KB IDs).
//
// The Trigger is set by the caller and only supplemented here:
//   - TriggerMessage: loads the message by readMessageRange[1].
//   - TriggerAlarm: loads the original user message text.
//   - TriggerNone: no DB load needed.
func (p *pipeline) loadMessages() error {
	if p.trigger == nil {
		p.trigger = &Trigger{Type: TriggerNone}
	}

	if p.trigger.Type == TriggerMessage && p.readMessageRange[1] > 0 {
		msg := &model.Message{}
		if err := database.DB.First(msg, p.readMessageRange[1]).Error; err != nil {
			return fmt.Errorf("range endpoint message not found: %w", err)
		}
		p.trigger.Message = msg
	}

	if p.trigger.Type == TriggerAlarm {
		if p.readMessageRange[1] > 0 {
			var msg model.Message
			if err := database.DB.First(&msg, p.readMessageRange[1]).Error; err != nil {
				return fmt.Errorf("alarm: range endpoint message not found: %w", err)
			}
			if p.trigger.Alarm != nil {
				p.trigger.Alarm.OriginalMessage = msg.Content
			}
		}
	}

	if err := database.DB.Model(&model.Message{}).Where("session_id = ?", p.session.ID).Count(&p.messageCount).Error; err != nil {
		applogger.Error("failed to count messages for chat pipeline", "session_id", p.session.ID, "error", err)
	}
	p.windowSize = config.Get().SummaryWindowSize

	a, err := agent.GetAgent(p.aiPersonID)
	if err != nil {
		applogger.Error("loadMessages: failed to get agent", "person_id", p.aiPersonID, "error", err)
		return err
	}
	p.kbIDs = getKnowledgeBaseIDs(&a.Config)

	// Resolve the conversation partner — the other participant in this
	// session — so context assembly and person-state description refer to the
	// actual partner (human in user-agent sessions, another agent in A2A
	// sessions) instead of a hardcoded human user.
	if partner, err := dops.GetSessionOtherParticipant(p.session.ID, a.Person.ID); err != nil {
		applogger.Error("loadMessages: failed to resolve session partner",
			"session_id", p.session.ID, "self_person_id", a.Person.ID, "error", err)
	} else if partner != nil {
		p.partnerName = partner.Name
		p.partnerPersonID = partner.PersonID
	}

	applogger.Info("Starting chat processing",
		"session_id", p.session.ID,
		"read_message_range", p.readMessageRange,
		"message_count", p.messageCount,
		"window_size", p.windowSize,
		"kb_count", len(p.kbIDs),
	)
	return nil
}

// assembleContext assembles the LLM prompt messages based on context engineering rules.
// Returns (messages, earlyContent, earlyReturn). When earlyReturn is true,
// earlyContent contains the response string and the pipeline should terminate early.
func (p *pipeline) assembleContext(ctx context.Context) ([]llm.Message, string, bool) {
	if p.messageCount < int64(p.windowSize) {
		return p.assembleSimpleContext()
	}
	return p.assembleEngineeredContext(ctx)
}

// assembleSimpleContext handles the V < N branch: skip context engineering,
// use all messages directly without summary or narrative.
func (p *pipeline) assembleSimpleContext() ([]llm.Message, string, bool) {
	applogger.Info("V < N, skipping context engineering",
		"V", p.messageCount, "N", p.windowSize,
	)

	recentMessages := p.getContextMessages(int(p.messageCount))

	// Signal narrative generation if recent messages have accumulated enough.
	// The narrative goroutine internally triggers summary generation if needed.
	if len(recentMessages) >= p.windowSize {
		comprehend.SignalNarrative(p.session.ID, p.aiPersonID)
	}

	a, err := agent.GetAgent(p.aiPersonID)
	if err != nil {
		applogger.Error("assembleSimpleContext: failed to get agent", "person_id", p.aiPersonID, "error", err)
		return nil, userFriendlyErrorMessage, true
	}

	characterSettings := a.Config.CharacterSettings

	entityProfileSection := formatEntityProfileSection(
		memory.LoadProfileForEntity(p.aiPersonID, model.EntityTypePerson, p.partnerPersonID),
		p.partnerName,
	)

	// Convert person state to natural language description for prompt injection
	var personStateDescription string
	if p.personStateResult != nil {
		personStateDescription = p.personStateResult.ToNaturalLanguage(p.partnerName)
	}

	messages := assembleContext(
		characterSettings,
		entityProfileSection,
		"",
		recentMessages,
		p.kbSegments,
		-1,
		personStateDescription,
		p.taskResult,
		p.partnerName,
		p.aiPersonID,
		p.guidance,
		formatAlarmNotification(p.trigger),
	)
	return messages, "", false
}

func (p *pipeline) getContextMessages(limit int) []model.Message {
	query := database.DB.Where("session_id = ?", p.session.ID)
	if p.readMessageRange[1] > 0 {
		query = query.Where("id <= ?", p.readMessageRange[1])
	}
	var messages []model.Message
	if err := query.Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		applogger.Error("failed to load bounded chat context messages", "session_id", p.session.ID, "error", err)
		return nil
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages
}

// assembleEngineeredContext handles the V >= N branch: apply full context
// engineering pipeline including summary, retrieval, and assembly.
// Waits for async preprocessing to complete before using the result.
func (p *pipeline) assembleEngineeredContext(ctx context.Context) ([]llm.Message, string, bool) {
	// Handle clarification needed case — return clarification as content
	// without writing to messages table (caller handles draft commit)
	if p.needsClarification {
		applogger.Info("Query needed clarification", "session_id", p.session.ID)
		return []llm.Message{}, p.clarification, true
	}
	contextResult := getContext(p.session.ID, p.aiPersonID, p.readMessageRange[1], p.windowSize)

	// Merge knowledge base segments with chat history segments
	relevantSegments := append([]comprehend.Segment{}, p.historySegments...)
	if len(p.kbSegments) > 0 {
		relevantSegments = append(relevantSegments, p.kbSegments...)
	}

	// Use cached narrative (generated in background with summary)
	var backgroundStory string
	if contextResult.Narrative != "" {
		backgroundStory = contextResult.Narrative
	}

	// Convert person state to natural language description for prompt injection
	var personStateDescription string
	if p.personStateResult != nil {
		personStateDescription = p.personStateResult.ToNaturalLanguage(p.partnerName)
	}

	// Signal narrative generation if recent messages have accumulated enough.
	// The narrative goroutine internally triggers summary generation if needed.
	if len(contextResult.RecentMessages) >= p.windowSize {
		comprehend.SignalNarrative(p.session.ID, p.aiPersonID)
	}

	// Calculate message sequence numbers for metadata
	var summaryVersion int
	if contextResult.SummaryVersion != -1 {
		summaryVersion = contextResult.SummaryVersion
	}

	a, err := agent.GetAgent(p.aiPersonID)
	if err != nil {
		applogger.Error("assembleEngineeredContext: failed to get agent", "person_id", p.aiPersonID, "error", err)
		return nil, userFriendlyErrorMessage, true
	}

	characterSettings := a.Config.CharacterSettings

	// Apply RAG retrieval hits to the memory system: chat-history segments
	// that were retrieved count as observation retrieval hits, boosting
	// importance scores.
	var ragHitIDs []int64
	for _, seg := range p.historySegments {
		if seg.Source == comprehend.SourceChatHistory && seg.MessageID > 0 {
			ragHitIDs = append(ragHitIDs, seg.MessageID)
		}
	}
	if len(ragHitIDs) > 0 {
		memory.OnRetrievalHit(p.aiPersonID, ragHitIDs)
	}

	entityProfileSection := formatEntityProfileSection(
		memory.LoadProfileForEntity(p.aiPersonID, model.EntityTypePerson, p.partnerPersonID),
		p.partnerName,
	)

	messages := assembleContext(
		characterSettings,
		entityProfileSection,
		backgroundStory,
		contextResult.RecentMessages,
		relevantSegments,
		summaryVersion,
		personStateDescription,
		p.taskResult,
		p.partnerName,
		p.aiPersonID,
		p.guidance,
		formatAlarmNotification(p.trigger),
	)
	return messages, "", false
}

// streamResponse sends the assembled messages to the LLM and collects the
// complete response. The LLM stream API is still used (to avoid long blocking),
// but chunks are accumulated internally without per-chunk callbacks or DB updates.
func (p *pipeline) streamResponse(ctx context.Context, messages []llm.Message) (string, error) {
	// Check cancellation before starting the LLM call
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	a, err := agent.GetAgent(p.aiPersonID)
	if err != nil {
		applogger.Error("streamResponse: failed to get agent", "person_id", p.aiPersonID, "error", err)
		return "", err
	}

	chatModel := llm.NewChatModelWithTemperature(
		a.LLM.BaseURL, a.LLM.APIKey, a.LLM.ModelID, llm.TemperatureCreative,
	)

	stream, err := chatModel.ChatStream(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("failed to start stream: %w", err)
	}
	applogger.Info("Starting LLM stream", "session_id", p.session.ID)

	fullContent, err := chatModel.ConsumeStream(stream, nil)
	if err != nil {
		return fullContent, err
	}

	applogger.Info("Chat processing completed",
		"session_id", p.session.ID,
		"response_length", len(fullContent),
	)
	return fullContent, nil
}

// postProcess handles post-response tasks.
// Note: Summary generation is now triggered at the message creation level
// (after any message is committed, regardless of sender), not here.
func (p *pipeline) postProcess(ctx context.Context) {
	// Note: summary generation is now triggered at the message creation level
	// (after any message is committed, regardless of sender), not here.
}
