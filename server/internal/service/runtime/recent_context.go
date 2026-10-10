package runtime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/memory"
)

const (
	recentContextCandidates = 8
	recentContextEntries    = 4
	recentContextRunes      = 1600
)

// recentExperienceEntry groups an observation with the choice it prompted.
// A heartbeat has only a decision; a self-observation may have no decision.
type recentExperienceEntry struct {
	eventID  int64
	decision *model.Decision
	when     time.Time
}

// buildRecentExperienceSummary presents a small chronological continuation of
// the agent's own observed events, choices, and recorded effects. It never
// selects by semantic similarity or borrows unread messages from a session.
func buildRecentExperienceSummary(personID, currentEventID int64) string {
	var observations []model.AgentObservation
	observationQuery := database.DB.Where("person_id = ?", personID)
	decisionQuery := database.DB.Where("person_id = ?", personID)
	if currentEventID > 0 {
		observationQuery = observationQuery.Where("event_id <> ?", currentEventID)
		decisionQuery = decisionQuery.Where("event_id <> ?", currentEventID)
	}
	if err := observationQuery.
		Order("id DESC").Limit(recentContextCandidates).Find(&observations).Error; err != nil {
		applogger.Error("recent experience: failed to load observations", "person_id", personID, "error", err)
		return ""
	}
	var decisions []model.Decision
	if err := decisionQuery.
		Order("id DESC").Limit(recentContextCandidates).Find(&decisions).Error; err != nil {
		applogger.Error("recent experience: failed to load decisions", "person_id", personID, "error", err)
		return ""
	}

	byEvent := make(map[int64]*recentExperienceEntry, len(observations))
	entries := make([]*recentExperienceEntry, 0, len(observations)+len(decisions))
	for _, observed := range observations {
		entry := &recentExperienceEntry{eventID: observed.EventID, when: observed.CreatedAt}
		byEvent[observed.EventID] = entry
		entries = append(entries, entry)
	}
	for i := range decisions {
		decision := &decisions[i]
		if decision.EventID == 0 {
			entries = append(entries, &recentExperienceEntry{decision: decision, when: decision.CreatedAt})
			continue
		}
		entry := byEvent[decision.EventID]
		if entry == nil {
			// A Decision alone does not grant access to an Event. Check the
			// observation before extending the recent window beyond its first page.
			var count int64
			if err := database.DB.Model(&model.AgentObservation{}).
				Where("person_id = ? AND event_id = ?", personID, decision.EventID).Count(&count).Error; err != nil {
				applogger.Error("recent experience: failed to verify observation", "event_id", decision.EventID, "error", err)
				continue
			}
			if count == 0 {
				applogger.Error("recent experience: decision has no observation", "decision_id", decision.ID, "event_id", decision.EventID)
				continue
			}
			entry = &recentExperienceEntry{eventID: decision.EventID}
			byEvent[decision.EventID] = entry
			entries = append(entries, entry)
		}
		entry.decision = decision
		if decision.CreatedAt.After(entry.when) {
			entry.when = decision.CreatedAt
		}
	}
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].when.Equal(entries[j].when) {
			return entries[i].eventID > entries[j].eventID
		}
		return entries[i].when.After(entries[j].when)
	})
	if len(entries) > recentContextEntries {
		entries = entries[:recentContextEntries]
	}
	var lines []string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		line := "- " + entry.when.Format("01-02 15:04:05") + " "
		if entry.eventID > 0 {
			description, err := memory.DescribeObservedEvent(personID, entry.eventID)
			if err != nil {
				applogger.Error("recent experience: failed to read observed event", "person_id", personID, "event_id", entry.eventID, "error", err)
				continue
			}
			line += shortContextText(description, 240)
		} else {
			line += "A heartbeat gave you a chance to act."
		}
		if entry.decision != nil {
			line += "\n  " + describeRecentDecision(*entry.decision)
		}
		lines = append(lines, line)
	}
	// Keep whole entries and prefer the newest ones when a verbose source
	// exhausts the prompt budget.
	for len(lines) > 1 && len([]rune(strings.Join(lines, "\n"))) > recentContextRunes {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

// describeRecentDecision keeps intentions separate from recorded effects.
func describeRecentDecision(decision model.Decision) string {
	var actions []model.Action
	if err := database.DB.Where("decision_id = ?", decision.ID).Order("id").Limit(4).Find(&actions).Error; err != nil {
		applogger.Error("recent experience: failed to load actions", "decision_id", decision.ID, "error", err)
		return "Decision recorded; actions unavailable"
	}
	if len(actions) == 0 {
		return "You chose not to act"
	}
	var parts []string
	for _, action := range actions {
		part := "You chose to " + action.Type.Label()
		if action.Type == model.ActionTypeRouteFocusedWork || action.Type == model.ActionTypeCancelFocusedWork {
			var target struct {
				TargetWorkID int64 `json:"target_work_id"`
			}
			if err := json.Unmarshal([]byte(action.PlanJSON), &target); err != nil {
				applogger.Error("recent experience: invalid Work control plan", "action_id", action.ID, "error", err)
			} else if target.TargetWorkID > 0 {
				part += fmt.Sprintf(" (work_id=%d)", target.TargetWorkID)
			}
		}
		if action.Reason != "" {
			part += fmt.Sprintf(". Your stated reason: %q", shortContextText(action.Reason, 90))
		}
		var effects []model.ActionEffect
		if err := database.DB.Where("action_id = ?", action.ID).Order("id").Limit(3).Find(&effects).Error; err != nil {
			applogger.Error("recent experience: failed to load action effects", "action_id", action.ID, "error", err)
		}
		for _, effect := range effects {
			switch effect.EffectType {
			case model.ActionEffectWork:
				part += fmt.Sprintf("; a work was created (work_id=%d)", effect.EffectID)
			case model.ActionEffectMessage:
				part += fmt.Sprintf("; a message was sent (message_id=%d)", effect.EffectID)
			case model.ActionEffectWorkControl:
				part += fmt.Sprintf("; the work accepted the control (work_id=%d)", effect.EffectID)
			default:
				part += "; recorded an effect"
			}
		}
		if len(effects) == 0 && action.Status == model.ActionStatusEnded &&
			(action.Type == model.ActionTypeCancelFocusedWork || action.Type == model.ActionTypeRouteFocusedWork) {
			part += "; no accepted control was recorded"
		}
		parts = append(parts, part)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "You chose these actions together, without an execution order:\n  - " + strings.Join(parts, "\n  - ")
}

// buildWorkControlContext reads explicit control attempts for the Work named by
// the current completion event. It includes failed attempts because an accepted
// ActionEffect alone would hide a stop request that raced with completion.
func buildWorkControlContext(personID, workID int64) string {
	if workID <= 0 {
		return ""
	}
	var work model.Work
	if err := database.DB.Where("id = ? AND person_id = ?", workID, personID).Take(&work).Error; err != nil {
		applogger.Error("work control context: Work unavailable", "person_id", personID, "work_id", workID, "error", err)
		return ""
	}
	type controlRecord struct {
		ID             int64
		Type           model.ActionType
		Reason         string
		Status         model.ActionStatus
		TriggerEventID int64 `gorm:"column:trigger_event_id"`
	}
	var controls []controlRecord
	// This exact expression has an index in database.EnsureRecallIndexes.
	const targetExpr = "CASE WHEN json_valid(actions.plan_json) THEN CAST(json_extract(actions.plan_json, '$.target_work_id') AS INTEGER) ELSE 0 END"
	err := database.DB.Table("actions").Select("actions.id, actions.type, actions.reason, actions.status, decisions.event_id AS trigger_event_id").
		Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where("decisions.person_id = ? AND actions.type IN ?", personID,
			[]model.ActionType{model.ActionTypeRouteFocusedWork, model.ActionTypeCancelFocusedWork}).
		Where(targetExpr+" = ?", workID).Order("actions.id DESC").Limit(4).Scan(&controls).Error
	if err != nil {
		applogger.Error("work control context: failed to load control attempts", "work_id", workID, "error", err)
		return ""
	}
	if len(controls) == 0 {
		return ""
	}
	var lines []string
	for i := len(controls) - 1; i >= 0; i-- {
		control := controls[i]
		kind := "Route"
		if control.Type == model.ActionTypeCancelFocusedWork {
			kind = "Cancel"
		}
		line := fmt.Sprintf("- You attempted to %s the work (work_id=%d)", strings.ToLower(kind), workID)
		if control.TriggerEventID > 0 {
			source, err := memory.DescribeObservedEvent(personID, control.TriggerEventID)
			if err != nil {
				applogger.Error("work control context: failed to read trigger", "action_id", control.ID, "event_id", control.TriggerEventID, "error", err)
			} else {
				line += "; prompted by: " + shortContextText(source, 190)
			}
		}
		if control.Reason != "" {
			line += fmt.Sprintf("; your stated reason: %q", shortContextText(control.Reason, 100))
		}
		var accepted int64
		if err := database.DB.Model(&model.ActionEffect{}).
			Where("action_id = ? AND effect_type = ? AND effect_id = ?", control.ID, model.ActionEffectWorkControl, workID).
			Count(&accepted).Error; err != nil {
			applogger.Error("work control context: failed to check acceptance", "action_id", control.ID, "error", err)
		}
		if accepted > 0 {
			line += "; the work accepted the control"
		} else if control.Status == model.ActionStatusEnded {
			line += "; no accepted control was recorded"
		} else {
			line += "; still in progress"
		}
		lines = append(lines, line)
	}
	return "Your attempts to change this work (an attempt alone does not prove an effect):\n" + strings.Join(lines, "\n") + "\n"
}

// shortContextText keeps a persisted source readable within the Situation's
// fixed budget without changing whether it is an observation or a report.
func shortContextText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
