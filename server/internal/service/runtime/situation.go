package runtime

import (
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/eventqueue"
)

// Situation is a runtime-only DTO that serves as the unified input to
// Decide. It is assembled by application code, never returned by LLM.
//
// Top-level semantics: a subject with capacity and commitments, given a
// decision opportunity from a source, facing the concrete matter it must
// judge.
type Situation struct {
	Source  SituationSource
	Subject SituationSubject
	Matter  SituationMatter
}

// SituationSource indicates whether this decision opportunity came from
// an external event or an internal heartbeat.
type SituationSource int

const (
	// SituationSourceExternal means this decision was triggered by an
	// external event from the event queue (user message, alarm, work
	// completion, etc.). Energy cost: CostPassive.
	SituationSourceExternal SituationSource = iota
	// SituationSourceInternal means this decision was triggered by an
	// internal heartbeat — the agent's own observation of its state.
	// Energy cost: CostActive.
	SituationSourceInternal
)

// SituationSubject is the agent's present capacity and commitments.
type SituationSubject struct {
	Energy             int
	ActiveWorksSummary string
}

// SituationMatter carries the concrete data Decide needs to evaluate.
// It is a rich type — it does not decompose existing structures into a
// generic abstraction. Consumers read different fields depending on
// Source:
//   - External: Event and Comprehension are populated.
//   - Internal: Description is populated.
type SituationMatter struct {
	Event         *eventqueue.AgentEvent          // non-nil when Source == External
	Comprehension *comprehend.ComprehensionResult // non-nil when Source == External
	Description   string                           // non-empty when Source == Internal
}

// buildExternalSituation constructs a Situation from an external event
// and its comprehension result.
func buildExternalSituation(event *eventqueue.AgentEvent, comp *comprehend.ComprehensionResult, energy int, activeWorksSummary string) *Situation {
	return &Situation{
		Source: SituationSourceExternal,
		Subject: SituationSubject{
			Energy:             energy,
			ActiveWorksSummary: activeWorksSummary,
		},
		Matter: SituationMatter{
			Event:         event,
			Comprehension: comp,
		},
	}
}

// buildHeartbeatSituation constructs a Situation from internal heartbeat
// observation. The description carries the agent's self-observation summary.
func buildHeartbeatSituation(description string, energy int, activeWorksSummary string) *Situation {
	return &Situation{
		Source: SituationSourceInternal,
		Subject: SituationSubject{
			Energy:             energy,
			ActiveWorksSummary: activeWorksSummary,
		},
		Matter: SituationMatter{
			Description: description,
		},
	}
}

// buildHeartbeatDescription collects the agent's self-observation into a
// natural language description for the heartbeat Situation. This is the
// internal counterpart of Comprehend — instead of understanding an external
// event, it surveys the agent's social world so Decide can evaluate whether
// to form an intention.
func buildHeartbeatDescription(personID int64) string {
	sessionsContext := buildSessionsContext(personID)
	personsContext := buildContactablePersonsContext(personID)
	return sessionsContext + personsContext
}
