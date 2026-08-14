package runtime

import (
	"fmt"
	"os"
	"strings"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
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
	Event         *eventqueue.AgentEvent         // non-nil when Source == External
	Comprehension *comprehendTypes.Comprehension // non-nil when Source == External
	Description   string                         // non-empty when Source == Internal
}

// buildExternalSituation constructs a Situation from an external event
// and its comprehension result.
func buildExternalSituation(event *eventqueue.AgentEvent, comp *comprehendTypes.Comprehension, energy int, activeWorksSummary string) *Situation {
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
	jinshuContext := buildJinshuContext(personID)
	return sessionsContext + personsContext + privateSpaceContext + jinshuContext
}

// jinshuContextRecent bounds how many recent received/sent jinshu records are
// injected into the heartbeat prompt, matching the session context limit to
// avoid prompt bloat.
const jinshuContextRecent = 5

// buildJinshuContext surveys the agent's recent jinshu (锦书) activity. The
// received section includes the sender and read status; the sent section
// includes the receiver but intentionally omits read status, which is
// receiver-only information.
func buildJinshuContext(personID int64) string {
	received, err := dops.ListReceivedJinshu(personID, 0, jinshuContextRecent)
	if err != nil {
		applogger.Error("buildJinshuContext: failed to list received jinshu",
			"person_id", personID, "error", err)
		received = nil
	}

	sent, err := dops.ListSentJinshu(personID, 0, jinshuContextRecent)
	if err != nil {
		applogger.Error("buildJinshuContext: failed to list sent jinshu",
			"person_id", personID, "error", err)
		sent = nil
	}

	// Resolve the referenced sender/receiver names in one batch.
	idSet := make(map[int64]struct{})
	for _, r := range received {
		idSet[r.FromPersonID] = struct{}{}
	}
	for _, r := range sent {
		idSet[r.ToPersonID] = struct{}{}
	}
	ids := make([]int64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	names, err := dops.GetPersonNames(ids)
	if err != nil {
		applogger.Error("buildJinshuContext: failed to resolve person names", "error", err)
		names = map[int64]string{}
	}

	var sb strings.Builder
	sb.WriteString("\n=== YOUR JINSHU (锦书) ===\n")

	if len(received) == 0 {
		sb.WriteString("Received: (none)\n")
	} else {
		sb.WriteString("Recently received:\n")
		for _, r := range received {
			sender := names[r.FromPersonID]
			if sender == "" {
				sender = fmt.Sprintf("person_%d", r.FromPersonID)
			}
			read := "unread"
			if r.IsRead {
				read = "read"
			}
			fmt.Fprintf(&sb, "- from %s, topic: %s, %s, %s\n",
				sender, r.Topic, read, r.CreatedAt.Format("2006-01-02 15:04"))
		}
	}

	if len(sent) == 0 {
		sb.WriteString("Sent: (none)\n")
	} else {
		sb.WriteString("Recently sent:\n")
		for _, r := range sent {
			receiver := names[r.ToPersonID]
			if receiver == "" {
				receiver = fmt.Sprintf("person_%d", r.ToPersonID)
			}
			fmt.Fprintf(&sb, "- to %s, topic: %s, %s\n",
				receiver, r.Topic, r.CreatedAt.Format("2006-01-02 15:04"))
		}
	}

	return sb.String()
}

// buildPrivateSpaceContext surveys the agent's private-space state:
// workspace directory contents and recent log entries.
// Only the space/ subdirectory is visible — system files like log.jsonl
// and the parent directory path are not exposed to the agent.
func buildPrivateSpaceContext(personID int64) string {
	workDirPath := privatespace.GetWorkDirPath(personID)

	// Read directory listing from the agent's workspace subdirectory only.
	entries, err := readDirSummary(workDirPath)
	if err != nil {
		entries = "(unavailable)"
	}

	logContext := privatespace.BuildRecentLogContext(personID, 5)

	var sb strings.Builder
	sb.WriteString("\n=== YOUR PRIVATE SPACE ===\n")
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
