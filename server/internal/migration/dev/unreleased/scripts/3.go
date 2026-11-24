package scripts

import (
	"fmt"
	"reflect"

	"gorm.io/gorm"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
)

// Convert3 consolidates equivalent historical delivery copies before enforcing
// uniqueness. The caller's transaction rolls back all changes on a conflict;
// a retry is safe and never invents deliveries for historical Events.
func Convert3(tx *gorm.DB, _ string) error {
	var groups []struct {
		PersonID int64
		EventID  int64
	}
	if err := tx.Model(&model.AgentEventBuffer{}).Select("person_id, event_id").Group("person_id, event_id").Having("COUNT(*) > 1").Scan(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		var rows []model.AgentEventBuffer
		if err := tx.Where("person_id = ? AND event_id = ?", group.PersonID, group.EventID).Order("id").Find(&rows).Error; err != nil {
			return err
		}
		retained, err := eventqueue.Deserialize(rows[0])
		if err != nil {
			return fmt.Errorf("decode duplicate buffer %d: %w", rows[0].ID, err)
		}
		for _, row := range rows[1:] {
			other, err := eventqueue.Deserialize(row)
			if err != nil {
				return fmt.Errorf("decode duplicate buffer %d: %w", row.ID, err)
			}
			if retained.Type != other.Type || retained.SessionID != other.SessionID || !reflect.DeepEqual(retained.Payload, other.Payload) {
				return fmt.Errorf("conflicting deliveries for person %d event %d", group.PersonID, group.EventID)
			}
			if retained.TriggerAction == nil {
				retained.TriggerAction = other.TriggerAction
			}
			if other.TriggerAction != nil && !reflect.DeepEqual(retained.TriggerAction, other.TriggerAction) {
				return fmt.Errorf("conflicting delivery provenance for person %d event %d", group.PersonID, group.EventID)
			}
		}
		encoded, err := eventqueue.Serialize(retained)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.AgentEventBuffer{}).Where("id = ?", rows[0].ID).Update("payload_json", encoded).Error; err != nil {
			return err
		}
		if err := tx.Where("person_id = ? AND event_id = ? AND id != ?", group.PersonID, group.EventID, rows[0].ID).Delete(&model.AgentEventBuffer{}).Error; err != nil {
			return err
		}
	}
	// The released index has the same name but was not unique.
	if err := tx.Exec("DROP INDEX IF EXISTS idx_agent_event_buffer_person_event").Error; err != nil {
		return err
	}
	if err := tx.Exec("CREATE UNIQUE INDEX idx_agent_event_buffer_person_event ON agent_event_buffers(person_id, event_id)").Error; err != nil {
		return err
	}
	return tx.Exec("CREATE INDEX IF NOT EXISTS idx_agent_event_buffer_person_id ON agent_event_buffers(person_id, id)").Error
}
