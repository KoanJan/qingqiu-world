package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/comprehend"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
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
	// partnerName is the conversation partner's name — the other participant
	// in this session (human in user-agent sessions, another agent in A2A
	// sessions). partnerPersonID is that partner's person ID, used to scope
	// entity profile lookups. Both replace a former hardcoded human-user
	// assumption that broke A2A addressing.
	partnerName     string
	partnerPersonID int64
	selfName        string // Agent's own name, for identity anchoring in chat generation

	// Results from pipeline stages
	personStateResult *comprehendTypes.PersonState
	historySegments   []comprehendTypes.Segment
	historyKeywords   []string
	kbSegments        []comprehendTypes.Segment
	focusedWorkResult *FocusedWorkResultForAssembly
}

// loadMessages loads the trigger message from the database (when applicable),
// and initializes session-level parameters (message count, window size).
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

// assembleSimpleContext handles the V < N branch using the observed messages
// within this read boundary, without summary or narrative retrieval.
func (p *pipeline) assembleSimpleContext() ([]llm.Message, string, bool) {
	applogger.Info("V < N, skipping context engineering",
		"V", p.messageCount, "N", p.windowSize,
	)

	// A new session has no history to load; the memory API requires a positive limit.
	var recentMessages []model.Message
	if p.messageCount > 0 {
		recentMessages = p.getContextMessages(int(p.messageCount))
	}

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
		p.focusedWorkResult,
		p.partnerName,
		p.selfName,
		p.aiPersonID,
		p.guidance,
		formatAlarmNotification(p.trigger),
	)
	return messages, "", false
}

// getContextMessages loads only messages visible to this agent and no newer
// than the range being answered.
func (p *pipeline) getContextMessages(limit int) []model.Message {
	messages, err := memory.ListObservedMessages(p.aiPersonID, p.session.ID, p.readMessageRange[1], limit, nil)
	if err != nil {
		applogger.Error("failed to load bounded chat context messages", "session_id", p.session.ID, "error", err)
		return nil
	}
	return messages
}

// assembleEngineeredContext handles the V >= N branch using the cached
// narrative, observed recent messages, and retrieval planned by Comprehend.
func (p *pipeline) assembleEngineeredContext(ctx context.Context) ([]llm.Message, string, bool) {
	contextResult := getContext(p.session.ID, p.aiPersonID, p.readMessageRange[1], p.windowSize)

	// Merge knowledge base segments with chat history segments
	recentIDs := make(map[int64]struct{}, len(contextResult.RecentMessages))
	for _, message := range contextResult.RecentMessages {
		recentIDs[message.ID] = struct{}{}
	}
	relevantSegments := make([]comprehendTypes.Segment, 0, len(p.historySegments)+len(p.kbSegments))
	for _, segment := range p.historySegments {
		if _, repeated := recentIDs[segment.MessageID]; !repeated {
			relevantSegments = append(relevantSegments, segment)
		}
	}
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
	for _, seg := range relevantSegments {
		if seg.Source == comprehendTypes.SourceChatHistory && seg.MessageID > 0 {
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
		p.focusedWorkResult,
		p.partnerName,
		p.selfName,
		p.aiPersonID,
		p.guidance,
		formatAlarmNotification(p.trigger),
	)
	return messages, "", false
}

// generateResponse sends the assembled messages through the single structured
// Chat call whose result is committed atomically as one Message.
func (p *pipeline) generateResponse(ctx context.Context, messages []llm.Message) (*ChatResult, error) {
	// Check cancellation before starting the LLM call
	if ctx.Err() != nil {
		return &ChatResult{}, ctx.Err()
	}

	a, err := agent.GetAgent(p.aiPersonID)
	if err != nil {
		applogger.Error("generateResponse: failed to get agent", "person_id", p.aiPersonID, "error", err)
		return &ChatResult{}, err
	}

	chatModel := llm.NewChatModelWithTemperature(
		a.LLM.BaseURL, a.LLM.APIKey, a.LLM.ModelID, llm.TemperatureCreative,
	)

	result, err := chatModel.ChatWithJSONSchema(ctx, messages, llm.JSONSchemaDefinition{
		Name:        "ChatResponse",
		Description: "The final agent reply and its natural-language speech expression instruction",
		Strict:      true,
		Schema:      llm.GenerateSchema[structuredChatResponse](),
	})
	if err != nil {
		return &ChatResult{}, fmt.Errorf("chat structured response: %w", err)
	}
	var output structuredChatResponse
	if err := json.Unmarshal([]byte(result), &output); err != nil {
		applogger.Error("chat returned invalid structured response", "session_id", p.session.ID, "error", err)
		return &ChatResult{}, fmt.Errorf("decode chat structured response: %w", err)
	}
	chatResult, err := newChatResult(output)
	if err != nil {
		applogger.Error("chat returned invalid structured response", "session_id", p.session.ID, "error", err)
		return &ChatResult{}, err
	}

	applogger.Info("Chat processing completed",
		"session_id", p.session.ID,
		"response_length", len(output.Content),
	)
	return chatResult, nil
}

// structuredChatResponse is the one-call Chat result committed to Message.
type structuredChatResponse struct {
	Content               string `json:"content" jsonschema:"description=The final user-visible reply,required"`
	ExpressionInstruction string `json:"expression_instruction" jsonschema:"description=Required non-empty natural-language instruction describing how to express the reply in speech; never return an empty string,required,minLength=1"`
}

// newChatResult enforces semantic constraints that JSON Schema cannot fully
// express, notably rejecting strings that contain only whitespace.
func newChatResult(output structuredChatResponse) (*ChatResult, error) {
	if strings.TrimSpace(output.Content) == "" {
		return nil, fmt.Errorf("chat structured response content is empty")
	}
	expressionInstruction := strings.TrimSpace(output.ExpressionInstruction)
	if expressionInstruction == "" {
		return nil, fmt.Errorf("chat structured response expression_instruction is empty")
	}
	return &ChatResult{
		Content:               output.Content,
		ExpressionInstruction: expressionInstruction,
	}, nil
}

// postProcess handles post-response work.
// Note: Summary generation is now triggered at the message creation level
// (after any message is committed, regardless of sender), not here.
func (p *pipeline) postProcess(ctx context.Context) {
	// Note: summary generation is now triggered at the message creation level
	// (after any message is committed, regardless of sender), not here.
}
