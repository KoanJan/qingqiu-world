package runtime

import (
	"encoding/json"
	"errors"
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"

	"gorm.io/gorm"
)

// persistDecision atomically accepts a Decide result, its actions, and the
// chat read boundary. The returned bool is false for an already-decided Event.
// It never records a failed LLM call as an intentional zero-action decision.
func persistDecision(personID int64, situation *Situation, result *DecisionResult) (bool, error) {
	if situation == nil || result == nil || !result.Accepted {
		return false, errors.New("cannot persist an unaccepted decision")
	}
	eventID := int64(0)
	var event *eventqueue.AgentEvent
	if situation.Source == SituationSourceExternal {
		event = situation.Matter.Event
		if event == nil || event.EventID <= 0 {
			return false, errors.New("external decision has no durable event")
		}
		eventID = event.EventID
	}

	tx := database.DB.Begin()
	if tx.Error != nil {
		return false, tx.Error
	}
	defer tx.Rollback()
	if err := dops.RequireActivePersonTx(tx, personID); err != nil {
		return false, err
	}

	if eventID > 0 {
		expectedType, err := durableEventType(event.Type)
		if err != nil {
			return false, err
		}
		var source model.Event
		if err := tx.Select("id", "event_type").First(&source, eventID).Error; err != nil {
			return false, fmt.Errorf("external decision has no source Event %d: %w", eventID, err)
		}
		if source.EventType != expectedType {
			return false, fmt.Errorf("external Event %d has type %d, expected %d", eventID, source.EventType, expectedType)
		}
		var existing model.Decision
		err = tx.Where("person_id = ? AND event_id = ?", personID, eventID).Take(&existing).Error
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("load prior decision: %w", err)
		}
	}

	decision := model.Decision{PersonID: personID, EventID: eventID}
	if err := tx.Create(&decision).Error; err != nil {
		return false, fmt.Errorf("create decision: %w", err)
	}
	for i := range result.Actions {
		act := &result.Actions[i]
		planJSON, err := encodeActionPlan(*act)
		if err != nil {
			return false, fmt.Errorf("encode action %d: %w", i, err)
		}
		record := model.Action{
			DecisionID: decision.ID,
			Type:       act.Type,
			PlanJSON:   planJSON,
			Background: act.Background,
			Reason:     act.Reason,
			Status:     model.ActionStatusInProgress,
		}
		if err := tx.Create(&record).Error; err != nil {
			return false, fmt.Errorf("create action %d: %w", i, err)
		}
		act.ID = record.ID
	}

	if event != nil && event.Type == eventqueue.EventTypeNewPrivateChatMessage &&
		situation.Matter.Comprehension != nil && situation.Matter.Comprehension.Chat != nil {
		chat := situation.Matter.Comprehension.Chat
		if chat.ReadMessageRange[1] > chat.ReadMessageRange[0] {
			if err := tx.Model(&model.ParticipantSession{}).
				Where("session_id = ? AND participant_id = ? AND last_read_message_id < ?", event.SessionID, personID, chat.ReadMessageRange[1]).
				Update("last_read_message_id", chat.ReadMessageRange[1]).Error; err != nil {
				return false, fmt.Errorf("advance decision read boundary: %w", err)
			}
		}
	}
	if err := tx.Commit().Error; err != nil {
		return false, fmt.Errorf("commit decision: %w", err)
	}
	for _, act := range result.Actions {
		if err := memory.RefreshSource(model.MemorySourceAction, act.ID); err != nil {
			applogger.Error("failed to index accepted action", "action_id", act.ID, "error", err)
		}
	}
	return true, nil
}

// durableEventType explicitly maps queue delivery types to their persisted
// fact types. Their integer values are unrelated and must never be cast.
func durableEventType(eventType eventqueue.AgentEventType) (model.EventType, error) {
	switch eventType {
	case eventqueue.EventTypeNewPrivateChatMessage:
		return model.EventTypeMessage, nil
	case eventqueue.EventTypeScheduled:
		return model.EventTypeScheduled, nil
	case eventqueue.EventTypeWorkCompleted:
		return model.EventTypeWorkCompleted, nil
	case eventqueue.EventTypeBiography:
		return model.EventTypeBiography, nil
	case eventqueue.EventTypeNewJinshuReceived:
		return model.EventTypeJinshu, nil
	case eventqueue.EventTypeJinshuListed:
		return model.EventTypeJinshuListed, nil
	case eventqueue.EventTypeJinshuSent:
		return model.EventTypeJinshuSent, nil
	case eventqueue.EventTypeJinshuSentListed:
		return model.EventTypeJinshuSentListed, nil
	case eventqueue.EventTypePSCompleted:
		return model.EventTypePSDigest, nil
	case eventqueue.EventTypeOwnedSpaceInspected:
		return model.EventTypeOwnedSpaceInspected, nil
	case eventqueue.EventTypeExecutionSlotAvailable:
		return model.EventTypeExecutionSlotAvailable, nil
	case eventqueue.EventTypeSystemNotification:
		return model.EventTypeSystemNotification, nil
	default:
		return 0, fmt.Errorf("external event type %d has no durable mapping", eventType)
	}
}

// hasAcceptedDecision avoids repeating Comprehend and the LLM call when a
// durable Event is replayed after its decision was already committed.
func hasAcceptedDecision(personID, eventID int64) (bool, error) {
	if personID <= 0 || eventID <= 0 {
		return false, fmt.Errorf("invalid decision lookup: person=%d event=%d", personID, eventID)
	}
	var count int64
	if err := database.DB.Model(&model.Decision{}).
		Where("person_id = ? AND event_id = ?", personID, eventID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// encodeActionPlan serializes only the plan for the selected action type.
// Background and Reason have their own columns and are never duplicated here.
func encodeActionPlan(act action.Action) (string, error) {
	var plan any
	switch act.Type {
	case action.Chat:
		plan = act.ChatPlan
	case action.StartFocusedWork:
		plan = act.WorkPlan
	case action.RouteFocusedWork, action.CancelFocusedWork:
		plan = act.WorkGuidance
	case action.CreateAlarm:
		plan = act.AlarmPlan
	case action.UpdateBio:
		plan = act.BioUpdate
	case action.EnterPrivateSpace:
		plan = struct{}{}
	case action.ListReceivedJinshu:
		plan = act.ListReceivedJinshuParams
	case action.SendJinshu:
		plan = act.SendJinshuPlan
	case action.ListSentJinshu:
		plan = act.ListSentJinshuParams
	case action.InspectOwnedSpace:
		plan = act.OwnedSpaceInspectionPlan
	case action.WaitForExecutionSlot:
		plan = act.WaitForExecutionSlotPlan
	default:
		return "", fmt.Errorf("unknown action type %d", act.Type)
	}
	if plan == nil {
		return "", fmt.Errorf("action type %d has no plan", act.Type)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// endAction marks execution complete without claiming a successful effect.
func endAction(actionID int64) error {
	if actionID <= 0 {
		return errors.New("cannot end action without persisted ID")
	}
	updated := database.DB.Model(&model.Action{}).Where("id = ? AND status = ?", actionID, model.ActionStatusInProgress).
		Update("status", model.ActionStatusEnded)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected > 0 {
		return nil
	}
	var action model.Action
	if err := database.DB.Select("status").First(&action, actionID).Error; err != nil {
		return fmt.Errorf("action %d not found while ending: %w", actionID, err)
	}
	if action.Status != model.ActionStatusEnded {
		return fmt.Errorf("action %d has unexpected status %d", actionID, action.Status)
	}
	return nil
}

// endActionLogged closes a non-chat execution path while retaining the
// distinction between execution ending and a successful business result.
func endActionLogged(actionID int64, operation string) {
	if err := endAction(actionID); err != nil {
		applogger.Error("failed to end action", "action_id", actionID, "operation", operation, "error", err)
	}
}

// recordActionEffect adds a private, application-validated source relation.
func recordActionEffect(tx *gorm.DB, actionID int64, effectType model.ActionEffectType, effectID int64) error {
	if actionID <= 0 || effectID <= 0 || effectType < model.ActionEffectWork || effectType > model.ActionEffectPSDigest {
		return fmt.Errorf("invalid action effect: action=%d type=%d effect=%d", actionID, effectType, effectID)
	}
	var action model.Action
	if err := tx.Select("status").Where("id = ? AND status = ?", actionID, model.ActionStatusInProgress).
		Take(&action).Error; err != nil {
		return fmt.Errorf("effect has no running source action %d: %w", actionID, err)
	}
	return tx.Create(&model.ActionEffect{ActionID: actionID, EffectType: effectType, EffectID: effectID}).Error
}

// recordAcceptedWorkControl links a Route or Cancel Action only after the
// running Work accepted the directive. The Action's ended state is separate
// from whether this effect could be persisted.
func (r *agentRuntime) recordAcceptedWorkControl(actionID, workID int64) {
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		return recordActionEffect(tx, actionID, model.ActionEffectWorkControl, workID)
	}); err != nil {
		applogger.Error("failed to record accepted work control", "person_id", r.agentPersonID, "action_id", actionID, "work_id", workID, "error", err)
		return
	}
	refreshMemorySource(model.MemorySourceAction, actionID)
}

// recoverInterruptedActions closes executions that cannot still be running
// after a process restart. Their persisted effects remain available as facts;
// execution is never retried merely because the status was left open.
func recoverInterruptedActions() {
	var actions []model.Action
	if err := database.DB.Where("status = ? AND type != ?", model.ActionStatusInProgress, model.ActionTypeWaitForExecutionSlot).Find(&actions).Error; err != nil {
		applogger.Error("failed to load interrupted actions", "error", err)
		return
	}
	if len(actions) == 0 {
		return
	}
	ids := make([]int64, 0, len(actions))
	for _, action := range actions {
		ids = append(ids, action.ID)
	}
	if err := database.DB.Model(&model.Action{}).Where("id IN ? AND status = ?", ids, model.ActionStatusInProgress).
		Update("status", model.ActionStatusEnded).Error; err != nil {
		applogger.Error("failed to close interrupted actions", "error", err)
		return
	}
	var effects []model.ActionEffect
	if err := database.DB.Where("action_id IN ?", ids).Find(&effects).Error; err != nil {
		applogger.Error("failed to inspect interrupted action effects", "error", err)
	}
	effectCounts := make(map[int64]int)
	for _, effect := range effects {
		effectCounts[effect.ActionID]++
	}
	for _, action := range actions {
		applogger.Warn("interrupted action ended without retry", "action_id", action.ID,
			"decision_id", action.DecisionID, "type", action.Type,
			"recorded_effects", effectCounts[action.ID])
	}
}

// endDeceasedPersonActions closes any finite Action stranded when its owner
// died while generating a result or handing it to the commit worker. It runs
// only after that person's Runtime has fully exited.
func endDeceasedPersonActions(personID int64) error {
	var actions []model.Action
	if err := database.DB.Table("actions").Select("actions.*").
		Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where("decisions.person_id = ? AND actions.status = ?", personID, model.ActionStatusInProgress).
		Find(&actions).Error; err != nil {
		return fmt.Errorf("load deceased person's unfinished actions: %w", err)
	}
	for _, action := range actions {
		if err := endAction(action.ID); err != nil {
			return fmt.Errorf("end deceased person's action %d: %w", action.ID, err)
		}
		applogger.Warn("deceased person's unfinished action ended without retry", "person_id", personID,
			"action_id", action.ID, "type", action.Type)
	}
	return nil
}
