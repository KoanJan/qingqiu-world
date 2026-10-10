package memory

import (
	"fmt"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// eventSourceContent resolves the authoritative domain row for lexical
// indexing and recall. A self-held Event retains its original JSON snapshot.
func eventSourceContent(event model.Event) (string, time.Time, error) {
	if event.RefID == 0 {
		if event.PayloadJSON == "" {
			return "", time.Time{}, fmt.Errorf("event %d has no source", event.ID)
		}
		return event.PayloadJSON, event.CreatedAt, nil
	}
	switch event.EventType {
	case model.EventTypeMessage:
		var row model.Message
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		return fmt.Sprintf("Person %d said in session %d: %s", row.PersonID, row.SessionID, row.Content), row.CreatedAt, nil
	case model.EventTypeBiography:
		var row model.AgentBiography
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		return row.Content, event.CreatedAt, nil
	case model.EventTypeJinshu, model.EventTypeJinshuSent:
		var row model.Jinshu
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		return fmt.Sprintf("Jinshu from person %d to person %d, topic %s, description %s", row.FromPersonID, row.ToPersonID, row.Topic, row.Description), event.CreatedAt, nil
	case model.EventTypeWorkCompleted:
		var row model.Work
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		content := fmt.Sprintf("Work %d completion event for %s; current status %d", row.ID, row.Description, row.Status)
		var handoff model.FocusHandoff
		// Only a handoff already recorded when the Event occurred can describe
		// that observation. A later handoff must remain a separate source.
		result := database.DB.Where("work_id = ? AND person_id = ? AND created_at <= ?", row.ID, row.PersonID, event.CreatedAt).
			Order("created_at DESC, id DESC").Limit(1).Find(&handoff)
		if result.Error != nil {
			return "", time.Time{}, result.Error
		}
		if result.RowsAffected > 0 {
			content += "; Focus reported: " + handoff.Summary
		} else {
			content += "; no Focus result was recorded by this Event"
		}
		return content, event.CreatedAt, nil
	case model.EventTypePSDigest:
		var row model.PSDigest
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		return "Private-space digest: " + row.Digest, event.CreatedAt, nil
	case model.EventTypeScheduled:
		var row model.ScheduledEvent
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		return "Your scheduled reminder fired: " + row.Message, event.CreatedAt, nil
	default:
		return "", time.Time{}, fmt.Errorf("unsupported referenced event type %d", event.EventType)
	}
}

// DescribeObservedEvent reads one authoritative event for a short-lived
// cognitive context. It applies the same observation and source permissions as
// recall, and keeps reported outcomes attributed to their source.
func DescribeObservedEvent(personID, eventID int64) (string, error) {
	var event model.Event
	if err := database.DB.First(&event, eventID).Error; err != nil {
		return "", err
	}
	allowed, err := canReadEvent(personID, event)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", fmt.Errorf("person %d cannot read event %d", personID, eventID)
	}
	content, _, err := eventSourceContent(event)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %s", eventTypeName(event.EventType), content), nil
}

// canReadEvent checks both observation and the current domain authorization.
func canReadEvent(personID int64, event model.Event) (bool, error) {
	var observed int64
	if err := database.DB.Model(&model.AgentObservation{}).Where("person_id = ? AND event_id = ?", personID, event.ID).Count(&observed).Error; err != nil {
		return false, err
	}
	if observed == 0 {
		return false, nil
	}
	if event.RefID == 0 {
		return true, nil
	}
	switch event.EventType {
	case model.EventTypeMessage:
		var row model.Message
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return false, err
		}
		return canReadSession(personID, row.SessionID), nil
	case model.EventTypeJinshu:
		var row model.Jinshu
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return false, err
		}
		return row.ToPersonID == personID, nil
	case model.EventTypeJinshuSent:
		var row model.Jinshu
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return false, err
		}
		return row.FromPersonID == personID, nil
	case model.EventTypeWorkCompleted:
		var row model.Work
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return false, err
		}
		return row.PersonID == personID, nil
	case model.EventTypeBiography:
		var row model.AgentBiography
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return false, err
		}
		return row.PersonID == personID, nil
	case model.EventTypePSDigest:
		var row model.PSDigest
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return false, err
		}
		return row.PersonID == personID, nil
	case model.EventTypeScheduled:
		var row model.ScheduledEvent
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return false, err
		}
		return row.PersonID == personID, nil
	default:
		return false, fmt.Errorf("no authorization rule for event type %d", event.EventType)
	}
}

// readRecallItem reloads an authoritative source and rechecks access after
// candidate selection. Its text describes recorded claims and outcomes without
// treating an Action's intention as proof that the intended effect occurred.
func readRecallItem(personID int64, kind model.MemorySourceKind, id int64) (RecallItem, bool, error) {
	item := RecallItem{SourceKind: kind, SourceID: id}
	switch kind {
	case model.MemorySourceEvent:
		var event model.Event
		if err := database.DB.First(&event, id).Error; err != nil {
			return item, false, err
		}
		allowed, err := canReadEvent(personID, event)
		if err != nil || !allowed {
			return item, false, err
		}
		content, occurred, err := eventSourceContent(event)
		if err != nil {
			return item, false, err
		}
		item.OccurredAt = occurred
		item.Text = fmt.Sprintf("Observed %s: %s", eventTypeName(event.EventType), limitRecallText(content))
		var decision model.Decision
		lookup := database.DB.Where("person_id = ? AND event_id = ?", personID, event.ID).Limit(1).Find(&decision)
		if lookup.Error != nil {
			return item, false, lookup.Error
		}
		if lookup.RowsAffected > 0 {
			item.Text += fmt.Sprintf(" Your later decision_id=%d can be used to inspect its chosen actions.", decision.ID)
		}
		effectType, effectID := event.EffectTarget()
		if effectID > 0 {
			var origins []model.ActionEffect
			if err := database.DB.Table("action_effects").Select("action_effects.*").
				Joins("JOIN actions ON actions.id = action_effects.action_id").
				Joins("JOIN decisions ON decisions.id = actions.decision_id").
				Where("decisions.person_id = ? AND action_effects.effect_type = ? AND action_effects.effect_id = ?", personID, effectType, effectID).
				Limit(5).Find(&origins).Error; err != nil {
				return item, false, err
			}
			for _, origin := range origins {
				item.Text += fmt.Sprintf(" Produced by your action_id=%d.", origin.ActionID)
			}
		}
	case model.MemorySourceAction:
		var row model.Action
		if err := database.DB.First(&row, id).Error; err != nil {
			return item, false, err
		}
		var decision model.Decision
		if err := database.DB.First(&decision, row.DecisionID).Error; err != nil {
			return item, false, err
		}
		if decision.PersonID != personID {
			return item, false, nil
		}
		item.OccurredAt = row.CreatedAt
		status := "ongoing"
		if row.Status == model.ActionStatusEnded {
			status = "ended"
		}
		item.Text = fmt.Sprintf("Your %s action from decision_id=%d (trigger event_id=%d), execution %s. At the time: background=%s; reason=%s; plan=%s", row.Type.Label(), decision.ID, decision.EventID, status, limitRecallText(row.Background), limitRecallText(row.Reason), limitRecallText(row.PlanJSON))
		var effects []model.ActionEffect
		var effectCount int64
		if err := database.DB.Model(&model.ActionEffect{}).Where("action_id = ?", row.ID).Count(&effectCount).Error; err != nil {
			return item, false, err
		}
		if err := database.DB.Where("action_id = ?", row.ID).Limit(10).Find(&effects).Error; err != nil {
			return item, false, err
		}
		if effectCount > 0 {
			item.Text += fmt.Sprintf("; %d recorded effect link(s). Result events can be read with recall_event(action_id=%d)", effectCount, row.ID)
		}
		for _, effect := range effects {
			item.Text += fmt.Sprintf("; recorded %s effect id=%d", effectTypeName(effect.EffectType), effect.EffectID)
		}
		if effectCount > int64(len(effects)) {
			item.Text += "; additional effect links are not expanded here"
		}
	case model.MemorySourceWork:
		var row model.Work
		if err := database.DB.First(&row, id).Error; err != nil {
			return item, false, err
		}
		if row.PersonID != personID {
			return item, false, nil
		}
		item.OccurredAt = row.CreatedAt
		item.Text = fmt.Sprintf("Your work %d in session %d, current status %s, phase %d: %s", row.ID, row.SessionID, workStatusName(row.Status), row.FocusPhase, limitRecallText(row.Description))
		var effects []model.ActionEffect
		var originCount int64
		if err := database.DB.Model(&model.ActionEffect{}).Where("effect_type = ? AND effect_id = ?", model.ActionEffectWork, row.ID).Count(&originCount).Error; err != nil {
			return item, false, err
		}
		if err := database.DB.Where("effect_type = ? AND effect_id = ?", model.ActionEffectWork, row.ID).Limit(10).Find(&effects).Error; err != nil {
			return item, false, err
		}
		for _, effect := range effects {
			var action model.Action
			if err := database.DB.First(&action, effect.ActionID).Error; err == nil {
				item.Text += fmt.Sprintf("; created by your action_id=%d, decision_id=%d", action.ID, action.DecisionID)
			}
		}
		if originCount > int64(len(effects)) {
			item.Text += "; further origin links are not expanded here"
		}
	case model.MemorySourceFocusHandoff:
		var row model.FocusHandoff
		if err := database.DB.First(&row, id).Error; err != nil {
			return item, false, err
		}
		if row.PersonID != personID {
			return item, false, nil
		}
		item.OccurredAt = row.CreatedAt
		item.Text = fmt.Sprintf("Your Focus handoff for work_id=%d, session_id=%d, status=%s. Orientation: %s. Summary: %s. Recorded findings: %s. Artifact references: %s. Unresolved: %s. Next: %s", row.WorkID, row.SessionID, handoffStatusName(row.Status), limitRecallText(row.Orientation), limitRecallText(row.Summary), limitRecallText(row.ConfirmedFindings), limitRecallText(row.ArtifactReferences), limitRecallText(row.Unresolved), limitRecallText(row.NextStep))
	default:
		return item, false, fmt.Errorf("unknown memory source kind %d", kind)
	}
	return item, true, nil
}

// limitRecallText bounds source excerpts by runes and marks omitted content.
func limitRecallText(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > 600 {
		return string(runes[:600]) + " [text abbreviated; original source has more]"
	}
	return s
}

// eventTypeName labels an Event's source for a human-readable recall result.
func eventTypeName(t model.EventType) string {
	switch t {
	case model.EventTypeMessage:
		return "chat message"
	case model.EventTypeBiography:
		return "biography"
	case model.EventTypeJinshu:
		return "jinshu delivery"
	case model.EventTypeWorkCompleted:
		return "work completion"
	case model.EventTypePSDigest:
		return "private-space digest"
	case model.EventTypeScheduled:
		return "scheduled alarm"
	case model.EventTypeJinshuListed:
		return "received-jinshu list"
	case model.EventTypeJinshuSent:
		return "jinshu sending result"
	case model.EventTypeJinshuSentListed:
		return "sent-jinshu list"
	case model.EventTypeOwnedSpaceInspected:
		return "owned-space inspection"
	case model.EventTypeExecutionSlotAvailable:
		return "execution slot available"
	case model.EventTypeSystemNotification:
		return "system notification"
	default:
		return "event"
	}
}

// effectTypeName labels an ActionEffect's target in recall output.
func effectTypeName(t model.ActionEffectType) string {
	switch t {
	case model.ActionEffectWork:
		return "work"
	case model.ActionEffectMessage:
		return "message"
	case model.ActionEffectScheduledEvent:
		return "scheduled alarm"
	case model.ActionEffectJinshu:
		return "jinshu"
	case model.ActionEffectSelfHeldEvent:
		return "result event"
	case model.ActionEffectWorkspace:
		return "workspace"
	case model.ActionEffectWorkControl:
		return "accepted work control"
	case model.ActionEffectPSDigest:
		return "private-space digest"
	default:
		return "unknown"
	}
}

// workStatusName renders a Work's persisted status in recall output.
func workStatusName(s model.WorkStatus) string {
	switch s {
	case model.WorkStatusRunning:
		return "running"
	case model.WorkStatusCompleted:
		return "completed"
	case model.WorkStatusFailed:
		return "failed"
	case model.WorkStatusAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}

// handoffStatusName renders a Focus handoff's persisted status in recall output.
func handoffStatusName(s model.FocusHandoffStatus) string {
	switch s {
	case model.FocusHandoffCompleted:
		return "completed"
	case model.FocusHandoffFailed:
		return "failed"
	case model.FocusHandoffCancelled:
		return "cancelled"
	case model.FocusHandoffInterrupted:
		return "interrupted"
	case model.FocusHandoffPaused:
		return "paused"
	default:
		return "unknown"
	}
}
