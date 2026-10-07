package runtime

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
)

// emitSelfHeldResult atomically persists a runtime result Event, its private
// top-level Action source, and the end of that Action before queue delivery.
func (r *agentRuntime) emitSelfHeldResult(queueType eventqueue.AgentEventType, eventType model.EventType, sessionID int64, payload any, actionID int64, trigger *eventqueue.TriggerAction) error {
	tx := database.DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	eventID, err := memory.RecordSelfHeldEventTx(tx, eventType, payload)
	if err != nil {
		return err
	}
	if actionID > 0 {
		if err := recordActionEffect(tx, actionID, model.ActionEffectSelfHeldEvent, eventID); err != nil {
			return err
		}
		if err := tx.Model(&model.Action{}).Where("id = ?", actionID).
			Update("status", model.ActionStatusEnded).Error; err != nil {
			return err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("commit self-held result: %w", err)
	}
	refreshMemorySource(model.MemorySourceEvent, eventID)
	if actionID > 0 {
		refreshMemorySource(model.MemorySourceAction, actionID)
	}
	eventqueue.SendEvent(r.agentConfigID, &eventqueue.AgentEvent{
		Type: queueType, SessionID: sessionID, EventID: eventID,
		Payload: payload, TriggerAction: trigger,
	})
	return nil
}

// emitReferencedResult records an outcome whose domain object already
// carries the result, then links the producing Action in its private table.
func (r *agentRuntime) emitReferencedResult(queueType eventqueue.AgentEventType, eventType model.EventType, sessionID, refID int64, payload any, actionID int64, effectType model.ActionEffectType, trigger *eventqueue.TriggerAction) error {
	tx := database.DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	eventID, err := memory.RecordReferencedEventTx(tx, eventType, refID)
	if err != nil {
		return err
	}
	if actionID > 0 {
		if err := recordActionEffect(tx, actionID, effectType, refID); err != nil {
			return err
		}
		if err := tx.Model(&model.Action{}).Where("id = ?", actionID).
			Update("status", model.ActionStatusEnded).Error; err != nil {
			return err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("commit referenced result: %w", err)
	}
	refreshMemorySource(model.MemorySourceEvent, eventID)
	if actionID > 0 {
		refreshMemorySource(model.MemorySourceAction, actionID)
	}
	eventqueue.SendEvent(r.agentConfigID, &eventqueue.AgentEvent{
		Type: queueType, SessionID: sessionID, EventID: eventID,
		Payload: payload, TriggerAction: trigger,
	})
	return nil
}
