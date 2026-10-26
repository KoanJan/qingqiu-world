// Package types defines the shared data types of the comprehend domain.
//
// These types form the contract between the comprehend router (the parent
// package) and its event-type-specific sub-packages (for example, chat). They
// live in a separate leaf package so both the router and the sub-packages can
// reference them without creating an import cycle.
package types

import (
	"fmt"
	"strings"
	"time"
)

// ComprehensionType identifies which event-type-specific comprehension a
// Comprehension holds.
type ComprehensionType int

const (
	// ComprehensionTypeNone marks a comprehension for an event that carries
	// no cognitive content to interpret (scheduled alarm, alarm creation,
	// group-chat membership change, system notification). It is the zero
	// value: a zero-value Comprehension therefore means "nothing was
	// comprehended", never an error. The event description is used as-is and
	// no LLM pass is performed.
	ComprehensionTypeNone ComprehensionType = iota
	// ComprehensionTypeChat marks a private chat message comprehension.
	ComprehensionTypeChat
	// ComprehensionTypeBiography marks a biography event comprehension. A
	// biography is the agent's own origin record, so its comprehension is
	// non-LLM: the event description is the record itself, with no extra
	// payload.
	ComprehensionTypeBiography
	// ComprehensionTypeWorkCompleted marks a work-completed event
	// comprehension. Work completion is the agent's own execution result,
	// so its comprehension is non-LLM: the event description (guidance plus
	// status) is the understanding itself, with no extra payload.
	ComprehensionTypeWorkCompleted
)

// Comprehension is the unified result of the comprehension phase. It carries
// the event description plus the event-type-specific payload selected by Type.
type Comprehension struct {
	// Type selects the payload field that holds the comprehension result.
	Type ComprehensionType
	// EventDescription is the natural-language description of the incoming
	// event, shared across all event types.
	EventDescription string
	// Chat holds the chat-specific comprehension when Type is
	// ComprehensionTypeChat; nil otherwise.
	Chat *ChatComprehension
}

// ChatComprehension holds the outcome of the comprehension phase.
// It represents the agent's understanding of the incoming event,
// including what the other party means and what information is relevant.
//
// Comprehend only collects information — it does not make judgments.
type ChatComprehension struct {
	// ReadMessageRange is the accepted message-ID interval for this batch.
	ReadMessageRange [2]int64
	// ReadMessageIDs is the exact batch used in this comprehension, not an
	// inferred continuous range in the global message ID sequence.
	ReadMessageIDs []int64
	// HistorySearch carries lexical hints, not pre-read chat history.
	HistorySearch *HistorySearch
	// KBRetrieval carries independent knowledge-base lookup context.
	KBRetrieval *KBRetrieval

	// PersonState holds the inferred state of the other party
	// (emotion, purpose, situation).
	PersonState *PersonState

	// Narrative is the cached background of this conversation, when available.
	// It is an interpretation of older messages, not a record of recent speech.
	Narrative string
	// RecentMessages preserves observed speech in this conversation so Decide
	// can resolve references that the current message alone cannot identify.
	RecentMessages []ConversationMessage
}

// HistorySearch carries lexical hints to Chat after its target session is known.
type HistorySearch struct {
	// Keywords are applied only after Chat chooses its target session.
	Keywords []string
}

// KBRetrieval describes a KB investigation suggested during comprehension.
// Segments is populated only by legacy/direct retrieval paths; Focus-owned KB
// work uses Query and KnowledgeBaseIDs to decide whether to start a Focus.
type KBRetrieval struct {
	Query            string
	KnowledgeBaseIDs []int64
	Segments         []Segment
}

// ConversationMessage is a domain-level message used during comprehension.
type ConversationMessage struct {
	ID         int64
	PersonID   int64
	PersonName string
	Content    string
	CreatedAt  time.Time
	// OwnAction is the agent's recorded intention when it sent this message.
	// It is absent for other people's messages and for legacy messages without
	// a recorded Chat action.
	OwnAction *MessageActionContext
}

// MessageActionContext describes the agent's own action at the time of speech.
// It is historical context, not a current instruction or a claim about how
// the other person interpreted the message.
type MessageActionContext struct {
	Background string
	Reason     string
	Guidance   string
}

// SessionInfo holds session-level parameters needed for comprehension.
// These are loaded once from the database and passed to Comprehend
// to avoid repeated queries.
type SessionInfo struct {
	SessionID    int64
	MessageCount int64
	WindowSize   int
	// AuthorizedKBs lists the knowledge bases the agent is granted to access,
	// loaded from kb_access at session-build time. It is both the LLM's
	// decision input (which KBs may be searched) and the enforcement set for
	// retrieval (only granted KBs can actually be searched).
	AuthorizedKBs []KBDescriptor
	// PartnerName is the name of the conversation partner — the other
	// participant in this session, resolved from participant_sessions.
	// This is the actual person the agent is talking to (human in user-agent
	// sessions, another agent in A2A sessions), replacing a former hardcoded
	// human-user assumption that broke A2A addressing.
	PartnerName string
}

// KBDescriptor is the minimal knowledge-base metadata used by the
// comprehension phase: the authorized-KB inventory injected into the
// retrieval-decision prompt and the allowed set for search validation.
type KBDescriptor struct {
	ID          int64
	Name        string
	Description string
}

// Segment source constants.
const (
	SourceChatHistory = iota + 1
	SourceKnowledgeBase
)

// Segment represents a retrieved context segment used in prompt assembly.
// MessageID is set for chat-history segments so the memory system can
// locate the corresponding observation and apply a retrieval hit.
type Segment struct {
	MessageID int64  `json:"message_id"`
	Content   string `json:"content"`
	Source    int    `json:"source"`
}

// PersonState represents the inferred person state from conversation context.
//
// Three-dimensional model:
//   - Emotion: person's current emotional state (affects response tone)
//   - Purpose: a coarse guess about the latest message's conversational goal
//   - Situation: person's physical context (affects response constraints)
//
// Field descriptions serve dual purpose:
//  1. Guide LLM structured output generation
//  2. Provide natural language fragments for prompt template assembly
type PersonState struct {
	Emotion   string        `json:"emotion" jsonschema:"description=The person's current emotional state: calm for relaxed or neutral, anxious for worried or uneasy, frustrated for annoyed or impatient (e.g. repeated failed attempts), urgent for time-pressured or emergency, curious for inquisitive or exploratory,enum=calm,enum=anxious,enum=frustrated,enum=urgent,enum=curious,required"`
	Purpose   PersonPurpose `json:"purpose" jsonschema:"description=Coarse purpose of the other person's latest message: 0 for other or unclear (including acknowledgments and closings without a new request); 1 for an explicit request for information or action (including advice or validation); 2 for expressing feelings without requesting a solution; 3 for social or non-goal-oriented chat. Do not force messages into a specific category.,enum=0,enum=1,enum=2,enum=3,required"`
	Situation string        `json:"situation" jsonschema:"description=Brief natural language description of the person's physical context if inferable from the conversation, such as time of day, device, environment, or activity. Use unknown if not inferable. Examples: at work on desktop, late evening on mobile, in a meeting, commuting,required"`
}

// PersonPurpose is a coarse conversational cue, not a required reply policy.
// Other is the zero value so absent or unclear purpose adds no prompt claim.
type PersonPurpose int

const (
	PersonPurposeOther PersonPurpose = iota
	PersonPurposeRequest
	PersonPurposeExpressFeeling
	PersonPurposeCasualChat
)

// Description returns a prompt-ready cue only when the category adds information.
func (purpose PersonPurpose) Description() string {
	switch purpose {
	case PersonPurposeRequest:
		return "requesting information or action"
	case PersonPurposeExpressFeeling:
		return "expressing feelings without requesting a solution"
	case PersonPurposeCasualChat:
		return "engaging in casual conversation"
	default:
		return ""
	}
}

// Valid reports whether a purpose belongs to the LLM output contract.
func (purpose PersonPurpose) Valid() bool {
	return purpose >= PersonPurposeOther && purpose <= PersonPurposeCasualChat
}

// emotionDescriptions maps emotion codes to natural language descriptions.
var emotionDescriptions = map[string]string{
	"calm":       "calm and relaxed",
	"anxious":    "anxious or worried",
	"frustrated": "frustrated or impatient",
	"urgent":     "under time pressure or in urgency",
	"curious":    "curious and exploratory",
}

// ToNaturalLanguage converts the structured person state into a natural language description
// suitable for injection into the prompt's instruction area.
// personName is the actual name of the person (empty = no profile set).
func (ps *PersonState) ToNaturalLanguage(personName string) string {
	emotionDesc := ps.Emotion
	if desc, ok := emotionDescriptions[ps.Emotion]; ok {
		emotionDesc = desc
	}
	subject := personName

	parts := []string{
		fmt.Sprintf("%s appears %s", subject, emotionDesc),
	}
	if purposeDesc := ps.Purpose.Description(); purposeDesc != "" {
		parts = append(parts, "is "+purposeDesc)
	}
	if ps.Situation != "" && ps.Situation != "unknown" {
		parts = append(parts, fmt.Sprintf("is likely %s", ps.Situation))
	}

	return strings.Join(parts, ", ") + "."
}
