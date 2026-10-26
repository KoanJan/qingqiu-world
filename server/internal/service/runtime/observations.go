package runtime

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
)

// recordComprehendedObservations records only durable events actually supplied
// to Comprehend. A chat batch uses its exact message IDs, not an ID interval.
func recordComprehendedObservations(personID int64, event *eventqueue.AgentEvent, c *comprehendTypes.Comprehension) error {
	if event == nil || c == nil {
		return fmt.Errorf("observation requires an event and comprehension")
	}
	if event.EventID > 0 {
		var source model.Event
		if err := database.DB.First(&source, event.EventID).Error; err != nil {
			return fmt.Errorf("observation trigger source missing: %w", err)
		}
		expected, err := durableEventType(event.Type)
		if err != nil {
			return err
		}
		if source.EventType != expected {
			return fmt.Errorf("observation event %d type %d, expected %d", event.EventID, source.EventType, expected)
		}
		if err := memory.CreateObservation(personID, event.EventID); err != nil {
			return fmt.Errorf("observe trigger event %d: %w", event.EventID, err)
		}
	}
	if event.Type != eventqueue.EventTypeNewPrivateChatMessage || c.Chat == nil || len(c.Chat.ReadMessageIDs) == 0 {
		return nil
	}
	var sources []model.Event
	if err := database.DB.Where("event_type = ? AND ref_id IN ?", model.EventTypeMessage, c.Chat.ReadMessageIDs).
		Find(&sources).Error; err != nil {
		return fmt.Errorf("load message events: %w", err)
	}
	byMessage := make(map[int64][]int64, len(c.Chat.ReadMessageIDs))
	for _, source := range sources {
		byMessage[source.RefID] = append(byMessage[source.RefID], source.ID)
	}
	for _, messageID := range c.Chat.ReadMessageIDs {
		ids := byMessage[messageID]
		if len(ids) == 0 {
			return fmt.Errorf("comprehended message %d has no durable Event", messageID)
		}
		for _, id := range ids {
			if err := memory.CreateObservation(personID, id); err != nil {
				return fmt.Errorf("observe message %d event %d: %w", messageID, id, err)
			}
		}
	}
	return nil
}
