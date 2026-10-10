package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/aos"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
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

// SituationSubject is the agent's present capacity, commitments, and bounded
// recent cognitive continuity across otherwise independent decision calls.
type SituationSubject struct {
	// Energy is the agent's currently available energy.
	Energy int
	// ActiveWorksSummary lists current Focus works and any omitted count.
	ActiveWorksSummary string
	// ActiveActionsSummary lists ongoing top-level actions, including chats.
	ActiveActionsSummary string
	// ExecutionSlotSummary names the sustained loop currently holding the slot.
	ExecutionSlotSummary string
	// RecentExperienceSummary groups previously observed facts with the
	// agent's subsequent choices and recorded effects, regardless of Session.
	RecentExperienceSummary string
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
		actionLines = append(actionLines, formatOngoingAction(a))
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
		line := fmt.Sprintf("- Active work (work_id=%d): %q", w.ID, truncateWorkDescription(w.Description))
		if database.DB.Migrator().HasTable(&model.WorkspaceUse{}) {
			if selected, err := dops.GetDefaultWorkWorkspace(personID, w.ID); err == nil {
				line += fmt.Sprintf("; default workspace: %s (workspace_id=%d, path=%q)", selected.Name, selected.ID, selected.RelativePath)
			} else {
				applogger.Error("populateGeneralSituation: running Work has no default Workspace", "person_id", personID, "work_id", w.ID, "error", err)
			}
		}
		workLines = append(workLines, line)
	}
	if workCount > int64(len(works)) {
		workLines = append(workLines, fmt.Sprintf("- %d further active works can be recalled", workCount-int64(len(works))))
	}
	situation.Subject.ActiveWorksSummary = strings.Join(workLines, "\n")
	situation.Environment.Sessions = buildSessionsRoster(personID)
	situation.Environment.Persons = buildContactablePersonsContext(personID)
	situation.Environment.Resources = buildOwnedResourceOverview(personID)
	currentEventID := int64(0)
	if situation.Matter.Event != nil {
		currentEventID = situation.Matter.Event.EventID
	}
	situation.Subject.RecentExperienceSummary = buildRecentExperienceSummary(personID, currentEventID)
	situation.generalReady = true
}

// formatOngoingAction presents only the plan facts that help with the next
// choice. Persisted PlanJSON is a storage format, not agent-facing language.
func formatOngoingAction(record model.Action) string {
	line := fmt.Sprintf("- You have an ongoing action to %s (action_id=%d)", record.Type.Label(), record.ID)
	if record.Background != "" {
		line += fmt.Sprintf(". Your understanding at the time: %q", truncateWorkDescription(record.Background))
	}
	if record.Reason != "" {
		line += fmt.Sprintf(". Your stated reason: %q", truncateWorkDescription(record.Reason))
	}
	switch record.Type {
	case model.ActionTypeChat:
		var plan action.ChatPlan
		if record.PlanJSON != "" {
			if err := json.Unmarshal([]byte(record.PlanJSON), &plan); err != nil {
				applogger.Error("formatOngoingAction: invalid Chat plan", "action_id", record.ID, "error", err)
			}
		}
		if plan.Guidance != "" {
			line += fmt.Sprintf(". Intended message: %q", truncateWorkDescription(plan.Guidance))
		}
		if plan.SessionID > 0 {
			line += fmt.Sprintf(". Destination: an existing conversation (session_id=%d)", plan.SessionID)
		} else if plan.SessionID < 0 && plan.RecipientPersonID > 0 {
			names, err := dops.GetPersonNames([]int64{plan.RecipientPersonID})
			name := names[plan.RecipientPersonID]
			if err != nil {
				applogger.Error("formatOngoingAction: recipient identity unavailable", "action_id", record.ID, "person_id", plan.RecipientPersonID, "error", err)
			} else if name == "" {
				applogger.Error("formatOngoingAction: recipient identity missing", "action_id", record.ID, "person_id", plan.RecipientPersonID)
			}
			if name == "" {
				name = "an unknown person"
			}
			line += fmt.Sprintf(". Destination: a new conversation with %s (person_id=%d)", name, plan.RecipientPersonID)
		}
	case model.ActionTypeWaitForExecutionSlot:
		var plan action.WaitForExecutionSlotPlan
		if record.PlanJSON != "" {
			if err := json.Unmarshal([]byte(record.PlanJSON), &plan); err != nil {
				applogger.Error("formatOngoingAction: invalid wait plan", "action_id", record.ID, "error", err)
			}
		}
		if plan.Intention != "" {
			line += fmt.Sprintf(". Waiting to reconsider: %q", truncateWorkDescription(plan.Intention))
		}
	}
	return line
}

// buildOwnedResourceOverview describes current resource availability only.
// Private-space logs are historical material and do not belong in Environment.
func buildOwnedResourceOverview(personID int64) string {
	entries, err := readDirSummary(aos.GetPrivateSpacePath(personID))
	if err != nil {
		applogger.Error("buildOwnedResourceOverview: listing failed", "person_id", personID, "error", err)
		return "Your private space could not be listed."
	}
	result := "Your private space currently contains: " + entries
	if database.DB.Migrator().HasTable(&model.Workspace{}) {
		workspaces, count, err := dops.ListRecentWorkspaces(personID, 5)
		if err != nil {
			applogger.Error("buildOwnedResourceOverview: Workspaces unavailable", "person_id", personID, "error", err)
		} else {
			var lines []string
			for _, item := range workspaces {
				lines = append(lines, fmt.Sprintf("- %s (workspace_id=%d, path=%q): %s", item.Name, item.ID, item.RelativePath, truncateWorkDescription(item.Purpose)))
			}
			result += fmt.Sprintf("\nYour registered workspaces: %d", count)
			if len(lines) > 0 {
				result += "\n" + strings.Join(lines, "\n")
			}
		}
	}
	return result
}

// buildSessionsRoster exposes routing choices without loading message history
// or mutable impressions into the general environment.
func buildSessionsRoster(personID int64) string {
	var rows []model.ParticipantSession
	if err := database.DB.Where("participant_id = ? AND session_id IN (SELECT id FROM sessions WHERE status = ?)", personID, model.SessionStatusActive).Order("last_active_at DESC").Limit(20).Find(&rows).Error; err != nil {
		applogger.Error("buildSessionsRoster: list failed", "person_id", personID, "error", err)
		return "Sessions unavailable."
	}
	var count int64
	if err := database.DB.Model(&model.ParticipantSession{}).Where("participant_id = ? AND session_id IN (SELECT id FROM sessions WHERE status = ?)", personID, model.SessionStatusActive).Count(&count).Error; err != nil {
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
				if err == nil {
					applogger.Error("buildSessionsRoster: participant identity missing", "person_id", id, "session_id", row.SessionID)
				}
				name = fmt.Sprintf("unknown participant (person_id=%d)", id)
			}
			peers = append(peers, name)
		}
		if len(peerIDs) > 5 {
			peers = append(peers, fmt.Sprintf("%d more participants", len(peerIDs)-5))
		}
		if len(peers) == 0 {
			peers = append(peers, "no other participants")
		}
		lines = append(lines, fmt.Sprintf("- Conversation (session_id=%d) with %s; last recorded activity: %s", row.SessionID, strings.Join(peers, ", "), row.LastActiveAt.Format("2006-01-02 15:04")))
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
		names = append(names, fmt.Sprintf("%q", name))
	}
	if len(entries) > 20 {
		names = append(names, fmt.Sprintf("%d more entries not shown", len(entries)-20))
	}
	return strings.Join(names, ", "), nil
}
