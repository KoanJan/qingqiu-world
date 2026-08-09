package runtime

import (
	"fmt"
	"os"
	"strings"

	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/privatespace"
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
	Description   string                          // non-empty when Source == Internal
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
// event, it surveys the agent's social world and private space so Decide
// can evaluate whether to form an intention.
func buildHeartbeatDescription(personID int64) string {
	sessionsContext := buildSessionsContext(personID)
	personsContext := buildContactablePersonsContext(personID)
	privateSpaceContext := buildPrivateSpaceContext(personID)
	return sessionsContext + personsContext + privateSpaceContext
}

// buildPrivateSpaceContext surveys the agent's private-space state:
// directory contents and recent log entries.
func buildPrivateSpaceContext(personID int64) string {
	dirPath := privatespace.GetDirPath(personID)

	// Read directory listing.
	entries, err := readDirSummary(dirPath)
	if err != nil {
		entries = "(unavailable)"
	}

	logContext := privatespace.BuildRecentLogContext(personID, 5)

	var sb strings.Builder
	sb.WriteString("\n=== YOUR PRIVATE SPACE ===\n")
	sb.WriteString(fmt.Sprintf("Directory: %s\n", dirPath))
	sb.WriteString(fmt.Sprintf("Contents: %s\n", entries))
	if logContext != "" {
		sb.WriteString("\n" + logContext)
	}
	return sb.String()
}

// readDirSummary returns a compact summary of directory contents.
func readDirSummary(dirPath string) (string, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "(empty)", nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", "), nil
}
