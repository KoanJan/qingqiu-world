// Package chat implements the core chat processing pipeline.
//
// This package is designed as a package-level service: use the Process()
// function directly. No struct instances need to be created or passed around.
//
// The pipeline includes:
//   - User state inference (including needs_world_interaction detection)
//   - Query preprocessing (routing, clarification, RAG optimization)
//   - Agent execution for world-interaction requests
//   - Context engineering (summary, retrieval, assembly)
//   - LLM streaming responses
//   - Summary generation triggers
//
// Draft-based architecture:
// The pipeline does NOT write to the messages table directly. It returns all
// results through ChatResult, and the caller (Work) commits them to a draft
// and then to messages atomically. This eliminates the placeholder message pattern.
package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/chat/chatcontext"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/task"

	applogger "qingqiu-world-server/internal/logger"
)

// User-friendly error message for unexpected failures
const userFriendlyErrorMessage = "Sorry, something went wrong on the server. Please try again later."

// ChatResult holds the output of the chat processing pipeline.
// In the draft-based architecture, the pipeline does not write to the messages
// table directly. Instead, it returns all results through this struct, and the
// caller (Work) commits them to a draft and then to messages atomically.
type ChatResult struct {
	Content string // The generated response content
}

// TriggerOverrideType identifies the kind of trigger override, which determines
// how the pipeline assembles context for the LLM.
type TriggerOverrideType int

const (
	// TriggerOverrideNone indicates no override (default user message flow).
	TriggerOverrideNone TriggerOverrideType = iota
	// TriggerOverrideScheduledAlarm indicates the trigger is a scheduled alarm
	// firing. The pipeline must present this as a system notification ("your
	// alarm went off"), NOT as a new user request.
	TriggerOverrideScheduledAlarm
)

// TriggerOverride provides supplementary context for non-direct triggers.
// Unlike the trigger message (which is the user message that caused the
// pipeline run), the override carries additional context from the trigger
// mechanism itself.
//
// The Type field determines how the pipeline assembles the final context:
//   - TriggerOverrideScheduledAlarm: the original user message is preserved as
//     reference only, and the override content (agent's self-reminder) becomes
//     the primary action context. The pipeline constructs a system-level alarm
//     notification semantic so the LLM understands "your alarm went off, act now"
//     rather than "the user is asking you to set an alarm again".
type TriggerOverride struct {
	Type    TriggerOverrideType // Kind of trigger override
	Content string              // Supplementary trigger context (e.g., alarm self-reminder)
}

// pipeline holds the state for a single chat processing execution.
// It is short-lived (only exists during one Process call) and carries
// shared data between pipeline stages.
type pipeline struct {
	session          *model.Session
	ac               *model.AgentConfig
	llmConfig        *model.LLMConfig
	readMessageRange [2]int64
	triggerOverride  *TriggerOverride // Non-nil when the trigger is not a persisted message (e.g., scheduled event)
	draftID          int64            // Draft ID for interaction records

	// Loaded in loadMessages
	triggerMessage model.Message
	sessionID      int64
	messageCount   int64
	windowSize     int
	kbIDs          []int64
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
	taskResult         *chatcontext.TaskResultForAssembly
	guidance           string // Execution intent from Decide phase
}

// ExecuteChat handles the chat execution path (WorkTypeChat).
// This is the "simple reply" path: context assembly + LLM streaming response.
//
// After the cognitive order refactoring, comprehension results are passed in
// from the Comprehend phase, so this function skips the redundant
// preprocessing, person state inference, and KB retrieval steps.
// It goes directly to context assembly and response generation.
func ExecuteChat(
	ctx context.Context,
	session *model.Session,
	ac *model.AgentConfig,
	llmConfig *model.LLMConfig,
	draftID int64,
	readMessageRange [2]int64,
	triggerOverride *TriggerOverride,
	comprehension *ComprehensionInput,
) (*ChatResult, error) {

	p := &pipeline{
		session:            session,
		ac:                 ac,
		llmConfig:          llmConfig,
		readMessageRange:   readMessageRange,
		triggerOverride:    triggerOverride,
		draftID:            draftID,
		personStateResult:  comprehension.PersonState,
		historySegments:    comprehension.HistorySegments,
		kbSegments:         comprehension.KBSegments,
		needsClarification: comprehension.NeedsClarification,
		clarification:      comprehension.Clarification,
		guidance:           comprehension.Guidance,
	}

	// If a TaskResult is provided (from a completed TaskWork), convert it
	// for context assembly so the response can reference the execution outcome.
	if comprehension.TaskResult != nil {
		p.taskResult = &chatcontext.TaskResultForAssembly{
			Status: comprehension.TaskResult.Status,
		}
		if comprehension.TaskResult.Output != "" {
			p.taskResult.Result = comprehension.TaskResult.Output
		}
		if comprehension.TaskResult.Error != "" {
			p.taskResult.Reason = comprehension.TaskResult.Error
		}
		if comprehension.TaskResult.Notes != "" {
			p.taskResult.Notes = comprehension.TaskResult.Notes
		}
	}

	if err := p.loadMessages(); err != nil {
		return &ChatResult{Content: userFriendlyErrorMessage}, err
	}

	// Skip preprocessing, inference, KB retrieval, and agent execution —
	// all of these were done in the Comprehend phase.
	// Go directly to context assembly and response.

	messages, earlyContent, earlyReturn := p.assembleContext(ctx)
	if earlyReturn {
		return &ChatResult{Content: earlyContent}, nil
	}

	fullContent, err := p.streamResponse(ctx, messages)
	if err != nil {
		return &ChatResult{Content: fullContent}, err
	}

	p.postProcess(ctx)

	return &ChatResult{
		Content: fullContent,
	}, nil
}

// ExecuteTask handles the task execution path (WorkTypeTask).
// This is the "agent" path: task rewriting + task execution + context assembly
// + LLM response summarizing the task result.
//
// After the cognitive order refactoring, comprehension results are passed in
// from the Comprehend phase, so this function skips the redundant
// preprocessing, person state inference, and KB retrieval steps.
// ComprehensionInput carries comprehension results from the runtime's
// Comprehend phase into the chat execution functions. This allows
// ExecuteChat to skip redundant preprocessing, person state inference,
// and KB retrieval that were already done before Decide().
type ComprehensionInput struct {
	PersonState        *comprehend.PersonState
	HistorySegments    []comprehend.Segment
	KBSegments         []comprehend.Segment
	NeedsClarification bool
	Clarification      string
	Guidance           string           // Execution intent from Decide phase: what to say (chat) or what to do (task)
	TaskResult         *task.TaskResult // Task execution result (only set for ChatWork after TaskWork completion)
}

// loadMessages loads the trigger message from the database,
// and initializes session-level parameters (message count, window size, KB IDs).
//
// When triggerOverride is set, its content is injected into the pipeline
// according to the override type:
//   - TriggerOverrideScheduledAlarm: the original user message is preserved as
//     reference, but the primary context is a system-level alarm notification.
//     This prevents the LLM from misinterpreting the alarm as a new user request
//     to set another alarm (which would cause an infinite loop).
func (p *pipeline) loadMessages() error {
	if p.readMessageRange[1] > 0 {
		if err := database.DB.First(&p.triggerMessage, p.readMessageRange[1]).Error; err != nil {
			return fmt.Errorf("range endpoint message not found: %w", err)
		}
	} else {
		p.triggerMessage = model.Message{SessionID: p.session.ID}
	}

	// Inject trigger override based on type.
	if p.triggerOverride != nil && p.triggerOverride.Content != "" {
		switch p.triggerOverride.Type {
		case TriggerOverrideScheduledAlarm:
			// Scheduled alarm: construct a system notification semantic.
			// The LLM must understand "your alarm went off, act now",
			// NOT "the user is asking you to set an alarm again".
			// The original user message is preserved as reference only,
			// while the agent's self-reminder is the primary action context.
			p.triggerMessage.Content = fmt.Sprintf(
				"[ALARM NOTIFICATION] An alarm you set has just triggered. This is NOT a new request — you set this alarm yourself earlier. Take action now based on your self-reminder below.\n\nYour self-reminder: %s\n\n[Original message for reference: %s]",
				p.triggerOverride.Content,
				p.triggerMessage.Content,
			)
		default:
			// Unknown override type: fall back to appending as supplementary context
			p.triggerMessage.Content = fmt.Sprintf(
				"%s\n\n[Supplementary Context: %s]",
				p.triggerMessage.Content,
				p.triggerOverride.Content,
			)
		}
	}

	p.sessionID = p.session.ID
	if err := database.DB.Model(&model.Message{}).Where("session_id = ?", p.sessionID).Count(&p.messageCount).Error; err != nil {
		applogger.Error("failed to count messages for chat pipeline", "session_id", p.sessionID, "error", err)
	}
	p.windowSize = config.Get().SummaryWindowSize
	p.kbIDs = getKnowledgeBaseIDs(p.ac)

	// Resolve the conversation partner — the other participant in this
	// session — so context assembly and person-state description refer to the
	// actual partner (human in user-agent sessions, another agent in A2A
	// sessions) instead of a hardcoded human user.
	if partner, err := dops.GetSessionOtherParticipant(p.sessionID, p.ac.PersonID); err != nil {
		applogger.Error("loadMessages: failed to resolve session partner",
			"session_id", p.sessionID, "self_person_id", p.ac.PersonID, "error", err)
	} else if partner != nil {
		p.partnerName = partner.Name
		p.partnerPersonID = partner.PersonID
	}

	applogger.Info("Starting chat processing",
		"session_id", p.sessionID,
		"read_message_range", p.readMessageRange,
		"draft_id", p.draftID,
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
		comprehend.SignalNarrative(p.sessionID, p.ac.PersonID)
	}

	characterSettings := p.ac.CharacterSettings

	entityProfileSection := chatcontext.FormatEntityProfileSection(
		memory.LoadProfileForEntity(p.ac.PersonID, model.EntityTypePerson, p.partnerPersonID),
		p.partnerName,
	)

	// Convert person state to natural language description for prompt injection
	var personStateDescription string
	if p.personStateResult != nil {
		personStateDescription = p.personStateResult.ToNaturalLanguage(p.partnerName)
	}

	messages := chatcontext.AssembleContext(
		characterSettings,
		entityProfileSection,
		"",
		recentMessages,
		p.kbSegments,
		-1,
		personStateDescription,
		p.taskResult,
		p.partnerName,
		p.ac.PersonID,
		p.guidance,
	)
	return messages, "", false
}

func (p *pipeline) getContextMessages(limit int) []model.Message {
	query := database.DB.Where("session_id = ?", p.sessionID)
	if p.readMessageRange[1] > 0 {
		query = query.Where("id <= ?", p.readMessageRange[1])
	}
	var messages []model.Message
	if err := query.Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		applogger.Error("failed to load bounded chat context messages", "session_id", p.sessionID, "error", err)
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
		applogger.Info("Query needed clarification", "session_id", p.sessionID)
		return []llm.Message{}, p.clarification, true
	}
	contextResult := chatcontext.GetContext(p.sessionID, p.ac.PersonID, p.readMessageRange[1], p.windowSize)

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
		comprehend.SignalNarrative(p.sessionID, p.ac.PersonID)
	}

	// Calculate message sequence numbers for metadata
	var summaryVersion int
	if contextResult.SummaryVersion != -1 {
		summaryVersion = contextResult.SummaryVersion
	}

	characterSettings := p.ac.CharacterSettings

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
		memory.OnRetrievalHit(p.ac.PersonID, ragHitIDs)
	}

	entityProfileSection := chatcontext.FormatEntityProfileSection(
		memory.LoadProfileForEntity(p.ac.PersonID, model.EntityTypePerson, p.partnerPersonID),
		p.partnerName,
	)

	messages := chatcontext.AssembleContext(
		characterSettings,
		entityProfileSection,
		backgroundStory,
		contextResult.RecentMessages,
		relevantSegments,
		summaryVersion,
		personStateDescription,
		p.taskResult,
		p.partnerName,
		p.ac.PersonID,
		p.guidance,
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

	chatModel := llm.NewChatModelWithTemperature(
		p.llmConfig.BaseURL, p.llmConfig.APIKey, p.llmConfig.ModelID, llm.TemperatureCreative,
	)

	stream, err := chatModel.ChatStream(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("failed to start stream: %w", err)
	}
	applogger.Info("Starting LLM stream", "session_id", p.sessionID)

	fullContent, err := chatModel.ConsumeStream(stream, nil)
	if err != nil {
		return fullContent, err
	}

	applogger.Info("Chat processing completed",
		"session_id", p.sessionID,
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

// getKnowledgeBaseIDs returns the knowledge base IDs associated with the agent.
func getKnowledgeBaseIDs(ac *model.AgentConfig) []int64 {
	if ac.KnowledgeBaseIDs == "" || ac.KnowledgeBaseIDs == "[]" {
		applogger.Info("Agent has no KBs configured", "agent_config_id", ac.ID, "knowledge_base_ids", ac.KnowledgeBaseIDs)
		return nil
	}

	var ids []int64
	if err := json.Unmarshal([]byte(ac.KnowledgeBaseIDs), &ids); err != nil {
		applogger.Error("Failed to parse agent config knowledge_base_ids", "agent_config_id", ac.ID, "raw", ac.KnowledgeBaseIDs, "error", err)
		return nil
	}

	var validIDs []int64
	for _, id := range ids {
		var kb model.KnowledgeBase
		if err := database.DB.First(&kb, id).Error; err == nil {
			validIDs = append(validIDs, id)
		} else {
			applogger.Error("KB ID not found in database", "agent_config_id", ac.ID, "kb_id", id, "error", err)
		}
	}

	applogger.Info("Agent config KB IDs resolved", "agent_config_id", ac.ID, "raw_ids", ids, "valid_ids", validIDs)
	return validIDs
}
