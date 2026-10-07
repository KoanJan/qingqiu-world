package runtime

import (
	"fmt"
	"os"
	"strings"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
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
	// Source identifies whether an event or a heartbeat opened this decision.
	Source SituationSource
	// Subject describes the agent's current capacity and ongoing commitments.
	Subject SituationSubject
	// Environment describes generally available external surroundings.
	Environment SituationEnvironment
	// Matter describes only the event or heartbeat being considered now.
	Matter SituationMatter
	// generalReady prevents rebuilding the shared state within one decision.
	generalReady bool
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
	// Energy is the agent's currently available energy.
	Energy int
	// ActiveWorksSummary lists current Focus works and any omitted count.
	ActiveWorksSummary string
	// ActiveActionsSummary lists ongoing top-level actions, including chats.
	ActiveActionsSummary string
}

// SituationEnvironment contains the bounded, generally available world
// roster. It is assembled without the current event or session as an input.
type SituationEnvironment struct {
	// Sessions lists available conversation destinations without their history.
	Sessions string
	// Persons lists people the agent can currently contact.
	Persons string
	// Resources summarizes owned resources without reading their contents.
	Resources string
}

// SituationMatter carries the concrete data Decide needs to evaluate.
// It is a rich type — it does not decompose existing structures into a
// generic abstraction. Consumers read different fields depending on
// Source:
//   - External: Event and Comprehension are populated.
//   - Internal: Description is populated.
type SituationMatter struct {
	// Event is the triggering external occurrence; nil for a heartbeat.
	Event *eventqueue.AgentEvent
	// Comprehension is the direct understanding of Event; nil for a heartbeat.
	Comprehension *comprehendTypes.Comprehension
	// Description names the heartbeat opportunity; empty for an external event.
	Description string
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

// buildHeartbeatDescription names the decision opportunity. General state is
// assembled by populateGeneralSituation for either trigger type.
func buildHeartbeatDescription(personID int64) string {
	return "A quiet heartbeat gives you a chance to decide whether to act."
}

// populateGeneralSituation applies the same event-independent state builder
// to external and heartbeat decisions. Matter remains the only trigger-specific
// component.
func populateGeneralSituation(personID int64, situation *Situation) {
	if situation == nil {
		return
	}
	var actions []model.Action
	if err := database.DB.Table("actions").Select("actions.*").Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where("decisions.person_id = ? AND actions.status = ?", personID, model.ActionStatusInProgress).
		Order("actions.created_at DESC").Limit(10).Find(&actions).Error; err != nil {
		applogger.Error("populateGeneralSituation: active actions", "person_id", personID, "error", err)
	}
	var activeCount int64
	if err := database.DB.Table("actions").Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where("decisions.person_id = ? AND actions.status = ?", personID, model.ActionStatusInProgress).Count(&activeCount).Error; err != nil {
		applogger.Error("populateGeneralSituation: count active actions", "person_id", personID, "error", err)
	}
	var actionLines []string
	for _, a := range actions {
		actionLines = append(actionLines, fmt.Sprintf("- ongoing %s action: %s; reason: %s; plan: %s", a.Type.Label(), truncateWorkDescription(a.Background), truncateWorkDescription(a.Reason), truncateWorkDescription(a.PlanJSON)))
	}
	if activeCount > int64(len(actions)) {
		actionLines = append(actionLines, fmt.Sprintf("- %d further ongoing actions can be recalled", activeCount-int64(len(actions))))
	}
	situation.Subject.ActiveActionsSummary = strings.Join(actionLines, "\n")
	var works []model.Work
	if err := database.DB.Where("person_id = ? AND status = ?", personID, model.WorkStatusRunning).Order("id DESC").Limit(10).Find(&works).Error; err != nil {
		applogger.Error("populateGeneralSituation: active works", "person_id", personID, "error", err)
	}
	var workCount int64
	if err := database.DB.Model(&model.Work{}).Where("person_id = ? AND status = ?", personID, model.WorkStatusRunning).Count(&workCount).Error; err != nil {
		applogger.Error("populateGeneralSituation: count active works", "person_id", personID, "error", err)
	}
	var workLines []string
	for _, w := range works {
		workLines = append(workLines, fmt.Sprintf("- Work #%d, session_id=%d, running: %s", w.ID, w.SessionID, truncateWorkDescription(w.Description)))
	}
	if workCount > int64(len(works)) {
		workLines = append(workLines, fmt.Sprintf("- %d further active works can be recalled", workCount-int64(len(works))))
	}
	situation.Subject.ActiveWorksSummary = strings.Join(workLines, "\n")
	situation.Environment.Sessions = buildSessionsRoster(personID)
	situation.Environment.Persons = buildContactablePersonsContext(personID)
	situation.Environment.Resources = buildOwnedResourceOverview(personID)
	situation.generalReady = true
}

// buildOwnedResourceOverview describes current resource availability only.
// Private-space logs are historical material and do not belong in Environment.
func buildOwnedResourceOverview(personID int64) string {
	entries, err := readDirSummary(privatespace.GetWorkDirPath(personID))
	if err != nil {
		applogger.Error("buildOwnedResourceOverview: listing failed", "person_id", personID, "error", err)
		return "Owned resource overview unavailable."
	}
	return "Owned resource overview: " + entries
}

// buildSessionsRoster exposes routing choices without loading message history
// or mutable impressions into the general environment.
func buildSessionsRoster(personID int64) string {
	var rows []model.ParticipantSession
	if err := database.DB.Where("participant_id = ?", personID).Order("last_active_at DESC").Limit(20).Find(&rows).Error; err != nil {
		applogger.Error("buildSessionsRoster: list failed", "person_id", personID, "error", err)
		return "Sessions unavailable."
	}
	var count int64
	if err := database.DB.Model(&model.ParticipantSession{}).Where("participant_id = ?", personID).Count(&count).Error; err != nil {
		applogger.Error("buildSessionsRoster: count failed", "person_id", personID, "error", err)
	}
	var lines []string
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SessionID)
	}
	otherBySession := make(map[int64][]int64)
	if len(ids) > 0 {
		var others []model.ParticipantSession
		if err := database.DB.Where("session_id IN ? AND participant_id != ?", ids, personID).Find(&others).Error; err != nil {
			applogger.Error("buildSessionsRoster: participants failed", "person_id", personID, "error", err)
		}
		for _, other := range others {
			otherBySession[other.SessionID] = append(otherBySession[other.SessionID], other.ParticipantID)
		}
	}
	personIDs := make([]int64, 0)
	for _, group := range otherBySession {
		personIDs = append(personIDs, group...)
	}
	names, err := dops.GetPersonNames(personIDs)
	if err != nil {
		applogger.Error("buildSessionsRoster: names failed", "person_id", personID, "error", err)
		names = map[int64]string{}
	}
	for _, row := range rows {
		var peers []string
		peerIDs := otherBySession[row.SessionID]
		for _, id := range peerIDs[:min(len(peerIDs), 5)] {
			name := names[id]
			if name == "" {
				name = fmt.Sprintf("person_%d", id)
			}
			peers = append(peers, name)
		}
		if len(peerIDs) > 5 {
			peers = append(peers, fmt.Sprintf("%d more participants", len(peerIDs)-5))
		}
		lines = append(lines, fmt.Sprintf("- session_id=%d, with %s (last active %s)", row.SessionID, strings.Join(peers, ", "), row.LastActiveAt.Format("2006-01-02 15:04")))
	}
	if count > int64(len(rows)) {
		lines = append(lines, fmt.Sprintf("- %d further sessions not shown", count-int64(len(rows))))
	}
	if len(lines) == 0 {
		return "Your sessions: none."
	}
	return "Your sessions:\n" + strings.Join(lines, "\n")
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
	for _, e := range entries[:min(len(entries), 20)] {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	if len(entries) > 20 {
		names = append(names, fmt.Sprintf("%d more entries not shown", len(entries)-20))
	}
	return strings.Join(names, ", "), nil
}
