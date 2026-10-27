package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"

	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

// executionSlot identifies the agent's one sustained Focus, private-space,
// or cognitive activity without conflating their domain identities.
type executionSlot struct {
	workID             int64
	private            bool
	cognitiveProcessID int64
	done               chan struct{}
}

// acquireExecutionSlot reserves the one sustained loop before it is started.
func (r *agentRuntime) acquireExecutionSlot(workID int64, private bool) bool {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	if r.slot != nil {
		return false
	}
	r.slot = &executionSlot{workID: workID, private: private, done: make(chan struct{})}
	return true
}

// acquireCognitiveSlot reserves the same sustained slot without presenting a
// cognitive process as a Work or using a synthetic Work ID.
func (r *agentRuntime) acquireCognitiveSlot(processID int64) bool {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	if processID <= 0 || r.slot != nil {
		return false
	}
	r.slot = &executionSlot{cognitiveProcessID: processID, done: make(chan struct{})}
	return true
}

// releaseExecutionSlot runs only after the owner's loop has actually exited.
func (r *agentRuntime) releaseExecutionSlot(workID int64, private bool) {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	if r.slot == nil || r.slot.cognitiveProcessID != 0 || r.slot.workID != workID || r.slot.private != private {
		return
	}
	r.releaseSlotLocked()
}

// releaseCognitiveSlot releases only the matching cognitive owner.
func (r *agentRuntime) releaseCognitiveSlot(processID int64) {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	if r.slot == nil || r.slot.cognitiveProcessID != processID {
		return
	}
	r.releaseSlotLocked()
}

func (r *agentRuntime) releaseSlotLocked() {
	// Keep new loop admission behind the durable notifications. A waiter
	// must learn that this release occurred even if another loop starts soon.
	released := r.slot
	r.slot = nil
	r.notifyWaitingActionsLocked()
	close(released.done)
	if released.cognitiveProcessID != 0 {
		// A cognitive process ending or pausing is not a new stimulus. Waking
		// the worker here would immediately create another process because no
		// paused process remains. The next heartbeat or Event will wake it.
		return
	}
	// Wake the cognition worker so it can reconsider admission; the event loop
	// itself does not wait for that decision.
	select {
	case r.cognitionWake <- struct{}{}:
	default:
	}
}

// registerExecutionWait leaves the Action in progress until one availability fact is recorded.
func (r *agentRuntime) registerExecutionWait(actionID int64) {
	r.slotMu.Lock()
	if r.slot == nil {
		r.notifyWaitingActionsLocked()
		r.slotMu.Unlock()
		return
	}
	cognitiveProcessID := r.slot.cognitiveProcessID
	if r.slot.private && r.privateSpaceLoop != nil {
		r.privateSpaceLoop.RequestYield()
	}
	r.slotMu.Unlock()
	if cognitiveProcessID > 0 {
		r.pauseCognitiveProcess(cognitiveProcessID)
	}
	applogger.Info("registered execution slot wait", "person_id", r.agentPersonID, "action_id", actionID)
}

// notifyWaitingActions records one fact per pending wait; the slot may be taken again before Decide.
func (r *agentRuntime) notifyWaitingActions() {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	if r.slot != nil {
		return
	}
	r.notifyWaitingActionsLocked()
}

// notifyWaitingActionsLocked coordinates the free-slot fact with admission.
// Callers hold slotMu so a new loop cannot hide a release before notification.
func (r *agentRuntime) notifyWaitingActionsLocked() {
	var waiting []model.Action
	if err := database.DB.Table("actions").Select("actions.*").
		Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where("decisions.person_id = ? AND actions.type = ? AND actions.status = ?", r.agentPersonID, model.ActionTypeWaitForExecutionSlot, model.ActionStatusInProgress).
		Order("actions.id").Find(&waiting).Error; err != nil {
		applogger.Error("failed to load execution slot waits", "person_id", r.agentPersonID, "error", err)
		return
	}
	for _, wait := range waiting {
		if err := r.notifyOneWaitingAction(wait); err != nil {
			applogger.Error("failed to notify execution slot wait", "action_id", wait.ID, "error", err)
		}
	}
}

// notifyOneWaitingAction commits the availability Event, source link, Action end, and replay record together.
func (r *agentRuntime) notifyOneWaitingAction(wait model.Action) error {
	var plan action.WaitForExecutionSlotPlan
	if err := json.Unmarshal([]byte(wait.PlanJSON), &plan); err != nil {
		return fmt.Errorf("decode wait plan: %w", err)
	}
	payload := &eventqueue.ExecutionSlotAvailablePayload{WaitActionID: wait.ID, Intention: plan.Intention}
	var outgoing *eventqueue.AgentEvent
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var person model.Person
		if err := tx.Select("status").First(&person, r.agentPersonID).Error; err != nil {
			return err
		}
		if person.Status != model.PersonStatusActive {
			return nil
		}
		var current model.Action
		if err := tx.Where("id = ? AND status = ?", wait.ID, model.ActionStatusInProgress).Take(&current).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		eventID, err := memory.RecordSelfHeldEventTx(tx, model.EventTypeExecutionSlotAvailable, payload)
		if err != nil {
			return err
		}
		if err := recordActionEffect(tx, wait.ID, model.ActionEffectSelfHeldEvent, eventID); err != nil {
			return err
		}
		if err := tx.Model(&model.Action{}).Where("id = ?", wait.ID).Update("status", model.ActionStatusEnded).Error; err != nil {
			return err
		}
		outgoing = &eventqueue.AgentEvent{Type: eventqueue.EventTypeExecutionSlotAvailable, EventID: eventID, Payload: payload}
		serialized, err := serializeEventPayload(outgoing)
		if err != nil {
			return err
		}
		return tx.Create(&model.AgentEventBuffer{PersonID: r.agentPersonID, EventType: int(outgoing.Type), EventID: eventID, PayloadJSON: serialized}).Error
	})
	if err != nil || outgoing == nil {
		return err
	}
	refreshMemorySource(model.MemorySourceEvent, outgoing.EventID)
	refreshMemorySource(model.MemorySourceAction, wait.ID)
	eventqueue.SendEvent(r.agentConfigID, outgoing)
	return nil
}

// currentExecutionSlot returns a snapshot of the current owner's exit signal.
func (r *agentRuntime) currentExecutionSlot() *executionSlot {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	return r.slot
}

// executionSlotSummary presents capacity without exposing scheduler details.
func (r *agentRuntime) executionSlotSummary() string {
	slot := r.currentExecutionSlot()
	if slot == nil {
		return "available"
	}
	if slot.private {
		return "occupied by your current private-space activity"
	}
	if slot.cognitiveProcessID > 0 {
		return "occupied by your background cognition"
	}
	return fmt.Sprintf("occupied by work (work_id=%d)", slot.workID)
}

// awaitExecutionSlotRelease prevents a completion event from overtaking loop exit.
func (r *agentRuntime) awaitExecutionSlotRelease(ctx context.Context, workID int64, private bool) {
	slot := r.currentExecutionSlot()
	if slot == nil || slot.cognitiveProcessID != 0 || slot.workID != workID || slot.private != private {
		return
	}
	select {
	case <-slot.done:
	case <-ctx.Done():
	}
}
