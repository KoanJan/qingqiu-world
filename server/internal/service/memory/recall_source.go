package memory

import (
	"fmt"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// eventSourceContent resolves the authoritative domain row for lexical
// indexing. Its person IDs are source data, not agent-facing speaker labels.
// A self-held Event retains its original JSON snapshot.
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

// presentedPersonName resolves one participant relative to the observer. A
// missing identity is logged and stays unknown instead of becoming a bare ID.
func presentedPersonName(observerID, personID, eventID int64) string {
	if personID == observerID {
		return "You"
	}
	names, err := dops.GetPersonNames([]int64{personID})
	if err != nil {
		applogger.Error("memory: event participant identity unavailable", "event_id", eventID, "person_id", personID, "error", err)
		return "Unknown person"
	}
	if name := names[personID]; name != "" {
		return name
	}
	applogger.Error("memory: event participant identity missing", "event_id", eventID, "person_id", personID)
	return "Unknown person"
}

// FormatChatSpeech presents the speaker and their words as one complete claim.
// Callers place the conversation and time outside this claim so record IDs do
// not appear to identify the speaker.
func FormatChatSpeech(speaker, content string) string {
	return fmt.Sprintf("%s said: %q.", speaker, content)
}

// presentedEventContent turns an observed source into a statement addressed to
// its observer. The neutral source text above remains stable for indexing.
func presentedEventContent(observerID int64, event model.Event) (string, time.Time, error) {
	if event.RefID == 0 {
		content, occurred, err := eventSourceContent(event)
		if err != nil {
			return "", time.Time{}, err
		}
		return "(recorded JSON payload) " + content, occurred, nil
	}
	switch event.EventType {
	case model.EventTypeMessage:
		var message model.Message
		if err := database.DB.First(&message, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		return fmt.Sprintf("Chat message from session (session_id=%d) — %s", message.SessionID, FormatChatSpeech(presentedPersonName(observerID, message.PersonID, event.ID), message.Content)), message.CreatedAt, nil
	case model.EventTypeJinshu, model.EventTypeJinshuSent:
		var row model.Jinshu
		if err := database.DB.First(&row, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		sender := presentedPersonName(observerID, row.FromPersonID, event.ID)
		recipient := presentedPersonName(observerID, row.ToPersonID, event.ID)
		if row.ToPersonID == observerID {
			recipient = "you"
		}
		return fmt.Sprintf("Jinshu (jinshu_id=%d) — %s sent it to %s about %q: %q.", row.ID, sender, recipient, row.Topic, row.Description), event.CreatedAt, nil
	case model.EventTypeWorkCompleted:
		var work model.Work
		if err := database.DB.First(&work, event.RefID).Error; err != nil {
			return "", time.Time{}, err
		}
		content := fmt.Sprintf("A work completion event was recorded for %q (work_id=%d). The work's current status is %s.", work.Description, work.ID, work.Status.Label())
		var handoff model.FocusHandoff
		result := database.DB.Where("work_id = ? AND person_id = ? AND created_at <= ?", work.ID, work.PersonID, event.CreatedAt).
			Order("created_at DESC, id DESC").Limit(1).Find(&handoff)
		if result.Error != nil {
			return "", time.Time{}, result.Error
		}
		if result.RowsAffected > 0 {
			content += " Focus reported: " + handoff.Summary
		} else {
			content += " No Focus result was recorded at that time."
		}
		return content, event.CreatedAt, nil
	default:
		return eventSourceContent(event)
	}
}

// presentedEventText keeps a fully attributed message or Jinshu readable as a
// sentence; other events still need their source type to explain the payload.
func presentedEventText(event model.Event, content string) string {
	switch event.EventType {
	case model.EventTypeMessage, model.EventTypeJinshu, model.EventTypeJinshuSent, model.EventTypeWorkCompleted:
		return content
	default:
		return fmt.Sprintf("%s: %s", eventTypeName(event.EventType), content)
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
	content, _, err := presentedEventContent(personID, event)
	if err != nil {
		return "", err
	}
	return presentedEventText(event, content), nil
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
		content, occurred, err := presentedEventContent(personID, event)
		if err != nil {
			return item, false, err
		}
		item.OccurredAt = occurred
		item.Text = "Observed: " + limitRecallText(presentedEventText(event, content))
		if event.EventType == model.EventTypeMessage {
			item.Text = limitRecallText(content)
		}
		if !strings.HasSuffix(item.Text, ".") && !strings.HasSuffix(item.Text, "!") && !strings.HasSuffix(item.Text, "?") {
			item.Text += "."
		}
		var decision model.Decision
		lookup := database.DB.Where("person_id = ? AND event_id = ?", personID, event.ID).Limit(1).Find(&decision)
		if lookup.Error != nil {
			return item, false, lookup.Error
		}
		if lookup.RowsAffected > 0 {
			item.Text += fmt.Sprintf(" You later made a decision about this event (decision_id=%d).", decision.ID)
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
				item.Text += fmt.Sprintf(" This record resulted from your action (action_id=%d).", origin.ActionID)
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
		item.Text = fmt.Sprintf("You chose to %s (decision_id=%d).", row.Type.Label(), decision.ID)
		if decision.EventID > 0 {
			item.Text += fmt.Sprintf(" This decision responded to an event (event_id=%d).", decision.EventID)
		}
		item.Text += fmt.Sprintf(" This action's recorded status is %s.", status)
		if row.Background != "" {
			item.Text += fmt.Sprintf(" Your understanding at the time: %q.", limitRecallText(row.Background))
		}
		if row.Reason != "" {
			item.Text += fmt.Sprintf(" Your stated reason: %q.", limitRecallText(row.Reason))
		}
		if row.PlanJSON != "" {
			item.Text += " Plan (JSON): " + limitRecallText(row.PlanJSON)
		}
		var effects []model.ActionEffect
		var effectCount int64
		if err := database.DB.Model(&model.ActionEffect{}).Where("action_id = ?", row.ID).Count(&effectCount).Error; err != nil {
			return item, false, err
		}
		if err := database.DB.Where("action_id = ?", row.ID).Limit(10).Find(&effects).Error; err != nil {
			return item, false, err
		}
		if effectCount > 0 {
			item.Text += fmt.Sprintf(" %d result link(s) were recorded. To inspect their events, use recall_event (action_id=%d).", effectCount, row.ID)
		}
		for _, effect := range effects {
			item.Text += fmt.Sprintf(" Recorded %s effect (%s=%d).", effectTypeName(effect.EffectType), effectTargetIDName(effect.EffectType), effect.EffectID)
		}
		if effectCount > int64(len(effects)) {
			item.Text += " Further result links are not shown."
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
		item.Text = fmt.Sprintf("Your work on %q (work_id=%d) currently has status %s.", limitRecallText(row.Description), row.ID, row.Status.Label())
		if row.SessionID > 0 {
			item.Text += fmt.Sprintf(" It originated in a conversation (session_id=%d).", row.SessionID)
		}
		if row.Status == model.WorkStatusRunning {
			item.Text += fmt.Sprintf(" Focus phase: %s.", row.FocusPhase.Label())
		}
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
				item.Text += fmt.Sprintf(" You started it through an action (action_id=%d). That action came from a decision (decision_id=%d).", action.ID, action.DecisionID)
			}
		}
		if originCount > int64(len(effects)) {
			item.Text += " Further origin links are not shown."
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
		item.Text = fmt.Sprintf("Focus reported on your work (work_id=%d).", row.WorkID)
		if row.SessionID > 0 {
			item.Text += fmt.Sprintf(" That work originated in a conversation (session_id=%d).", row.SessionID)
		}
		item.Text += fmt.Sprintf(" The handoff's recorded status is %s.", handoffStatusName(row.Status))
		for _, detail := range []struct{ label, value string }{
			{"Orientation", row.Orientation}, {"Summary", row.Summary}, {"Recorded findings", row.ConfirmedFindings},
			{"Artifact references", row.ArtifactReferences}, {"Unresolved", row.Unresolved}, {"Next step", row.NextStep},
		} {
			if detail.value != "" {
				item.Text += " " + detail.label + ": " + limitRecallText(detail.value) + "."
			}
		}
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

// effectTargetIDName gives a result reference the identifier expected by a
// follow-up recall tool instead of exposing an ambiguous generic effect_id.
func effectTargetIDName(t model.ActionEffectType) string {
	switch t {
	case model.ActionEffectWork, model.ActionEffectWorkControl:
		return "work_id"
	case model.ActionEffectMessage:
		return "message_id"
	case model.ActionEffectScheduledEvent:
		return "scheduled_event_id"
	case model.ActionEffectJinshu:
		return "jinshu_id"
	case model.ActionEffectSelfHeldEvent:
		return "event_id"
	case model.ActionEffectWorkspace:
		return "workspace_id"
	case model.ActionEffectPSDigest:
		return "ps_digest_id"
	default:
		return "record_id"
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
