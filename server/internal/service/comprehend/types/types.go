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
	ReadMessageRange [2]int64
	HistorySearch    *HistorySearch
	KBRetrieval      *KBRetrieval

	// NeedsClarification indicates the query is too vague and needs
	// a clarification question before proceeding.
	NeedsClarification bool

	// Clarification contains the generated clarification question
	// when NeedsClarification is true.
	Clarification string

	// PersonState holds the inferred state of the other party
	// (emotion, purpose, situation).
	PersonState *PersonState

	// ActiveWorksSummary is a natural language description of the agent's
	// currently running works. This gives the Comprehend phase self-awareness:
	// when the user says "change the approach" or "stop", the agent knows
	// what it is currently doing and can understand the reference.
	ActiveWorksSummary string
}

// HistorySearch describes a completed keyword search over conversation history.
type HistorySearch struct {
	Keywords []string
	Segments []Segment
}

// KBRetrieval describes a completed vector retrieval from configured knowledge bases.
type KBRetrieval struct {
	Query    string
	Segments []Segment
}

// ConversationMessage is a domain-level message used during comprehension.
type ConversationMessage struct {
	PersonID   int64
	PersonName string
	Content    string
	CreatedAt  time.Time
}

// SessionInfo holds session-level parameters needed for comprehension.
// These are loaded once from the database and passed to Comprehend
// to avoid repeated queries.
type SessionInfo struct {
	SessionID    int64
	MessageCount int64
	WindowSize   int
	KBIDs        []int64
	// PartnerName is the name of the conversation partner — the other
	// participant in this session, resolved from participant_sessions.
	// This is the actual person the agent is talking to (human in user-agent
	// sessions, another agent in A2A sessions), replacing a former hardcoded
	// human-user assumption that broke A2A addressing.
	PartnerName string
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
//   - Purpose: person's current conversational goal (affects response content direction)
//   - Situation: person's physical context (affects response constraints)
//
// Intent type is implicitly derived from purpose + situation, not modeled separately.
//
// Field descriptions serve dual purpose:
//  1. Guide LLM structured output generation
//  2. Provide natural language fragments for prompt template assembly
type PersonState struct {
	Emotion   string `json:"emotion" jsonschema:"description=The person's current emotional state: calm for relaxed or neutral, anxious for worried or uneasy, frustrated for annoyed or impatient (e.g. repeated failed attempts), urgent for time-pressured or emergency, curious for inquisitive or exploratory,enum=calm,enum=anxious,enum=frustrated,enum=urgent,enum=curious,required"`
	Purpose   string `json:"purpose" jsonschema:"description=The person's current conversational goal: seek_help for needing a solution or fix, seek_advice for wanting recommendations or guidance, seek_confirmation for validating a decision or understanding, express_feeling for sharing emotions without expecting solutions, casual_chat for social or non-goal-oriented conversation,enum=seek_help,enum=seek_advice,enum=seek_confirmation,enum=express_feeling,enum=casual_chat,required"`
	Situation string `json:"situation" jsonschema:"description=Brief natural language description of the person's physical context if inferable from the conversation, such as time of day, device, environment, or activity. Use unknown if not inferable. Examples: at work on desktop, late evening on mobile, in a meeting, commuting,required"`
}

// emotionDescriptions maps emotion codes to natural language descriptions.
var emotionDescriptions = map[string]string{
	"calm":       "calm and relaxed",
	"anxious":    "anxious or worried",
	"frustrated": "frustrated or impatient",
	"urgent":     "under time pressure or in urgency",
	"curious":    "curious and exploratory",
}

// purposeDescriptions maps purpose codes to natural language descriptions.
var purposeDescriptions = map[string]string{
	"seek_help":         "seeking help with a problem",
	"seek_advice":       "looking for advice or recommendations",
	"seek_confirmation": "seeking confirmation or validation",
	"express_feeling":   "expressing feelings without expecting solutions",
	"casual_chat":       "engaging in casual conversation",
}

// ToNaturalLanguage converts the structured person state into a natural language description
// suitable for injection into the prompt's instruction area.
// personName is the actual name of the person (empty = no profile set).
func (ps *PersonState) ToNaturalLanguage(personName string) string {
	emotionDesc := ps.Emotion
	if desc, ok := emotionDescriptions[ps.Emotion]; ok {
		emotionDesc = desc
	}
	purposeDesc := ps.Purpose
	if desc, ok := purposeDescriptions[ps.Purpose]; ok {
		purposeDesc = desc
	}

	subject := personName

	parts := []string{
		fmt.Sprintf("%s appears %s", subject, emotionDesc),
		fmt.Sprintf("is %s", purposeDesc),
	}
	if ps.Situation != "" && ps.Situation != "unknown" {
		parts = append(parts, fmt.Sprintf("and is likely %s", ps.Situation))
	}

	return strings.Join(parts, ", ") + "."
}
