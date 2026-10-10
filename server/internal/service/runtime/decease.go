package runtime

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"

	"gorm.io/gorm"
)

// DeceaseAgent closes one agent's activity and records one observable world fact for known peers.
func DeceaseAgent(agentConfigID, personID int64) (bool, error) {
	var event *eventqueue.AgentEvent
	var recipients []int64
	changed := false
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var person model.Person
		if err := tx.First(&person, personID).Error; err != nil {
			return err
		}
		var err error
		changed, err = dops.MarkAIPersonDeceasedTx(tx, personID)
		if err != nil || !changed {
			return err
		}
		// Explicit shared-session and Jinshu IDs determine who can learn this fact.
		if err := tx.Raw(`SELECT DISTINCT p.id FROM persons p
			WHERE p.type = ? AND p.status = ? AND p.id != ? AND (
				EXISTS (SELECT 1 FROM participant_sessions mine JOIN participant_sessions theirs
					ON mine.session_id = theirs.session_id
					WHERE mine.participant_id = ? AND theirs.participant_id = p.id)
				OR EXISTS (SELECT 1 FROM jinshus j WHERE
					(j.from_person_id = ? AND j.to_person_id = p.id) OR
					(j.to_person_id = ? AND j.from_person_id = p.id))
			)`, model.PersonTypeAI, model.PersonStatusActive, personID, personID, personID, personID).
			Scan(&recipients).Error; err != nil {
			return fmt.Errorf("find known peers: %w", err)
		}
		payload := fmt.Sprintf("%s has died and will no longer take part in conversations or unfinished matters.", person.Name)
		eventID, err := memory.RecordSelfHeldEventTx(tx, model.EventTypeSystemNotification, payload)
		if err != nil {
			return err
		}
		event = &eventqueue.AgentEvent{Type: eventqueue.EventTypeSystemNotification, EventID: eventID, Payload: payload}
		serialized, err := serializeEventPayload(event)
		if err != nil {
			return err
		}
		for _, recipientID := range recipients {
			if err := tx.Create(&model.AgentEventBuffer{PersonID: recipientID, EventType: int(event.Type), EventID: eventID, PayloadJSON: serialized}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return changed, err
	}
	// A repeated request must finish an earlier interrupted shutdown too. The
	// state transition is idempotent, while stopping the Runtime and closing
	// actions are safe to retry after the world fact has been committed.
	StopRuntime(agentConfigID)
	CancelAlarmsForPerson(personID)
	if err := endDeceasedPersonActions(personID); err != nil {
		return changed, err
	}
	if !changed {
		return false, nil
	}
	refreshMemorySource(model.MemorySourceEvent, event.EventID)
	for _, recipientID := range recipients {
		ac, err := dops.GetAgentConfigByPersonID(recipientID)
		if err != nil {
			applogger.Error("deceased event: missing recipient config", "recipient_person_id", recipientID, "event_id", event.EventID, "error", err)
			continue
		}
		eventqueue.SendEvent(ac.ID, event)
	}
	return true, nil
}
