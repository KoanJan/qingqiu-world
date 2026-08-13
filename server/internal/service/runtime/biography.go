package runtime

import (
	"fmt"
	"time"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
)

// biographyContentTemplate is the natural-language origin record delivered to
// an agent when it is created. The single %s is replaced with the person's
// created_at time, formatted as local time.
const biographyContentTemplate = `You came into existence in Qingqiu World at %s.

This marks the beginning of your history in this world.`

// buildBiographyContent renders an agent's origin statement with the given
// creation time. It is the stable payload carried by a biography event.
func buildBiographyContent(createdAt time.Time) string {
	return fmt.Sprintf(biographyContentTemplate, createdAt.Format("2006-01-02 15:04:05"))
}

// SendBiographyEvent is the production + distribution entry point for an
// agent's origin record. It:
//  1. persists the agent's AgentBiography (a factual statement that the agent
//     came into existence at a specific time),
//  2. records a biography memory event (events table, type=EventTypeBiography),
//  3. delivers it to the agent's runtime as a self-orienting biography event.
//
// It mirrors SendNewMessageEvent: the runtime event loop consumes the event and
// records an observation, so the agent gains awareness of its beginning.
//
// This MUST be called after StartRuntime, otherwise the event is dropped because
// no subscriber exists yet. The biography itself takes no decision or action —
// the runtime only observes it.
func SendBiographyEvent(agentConfigID, personID int64) {
	person, err := dops.GetPerson(personID)
	if err != nil {
		applogger.Error("SendBiographyEvent: failed to load person",
			"person_id", personID,
			"error", err,
		)
		return
	}

	content := buildBiographyContent(person.CreatedAt)
	biography, err := dops.CreateAgentBiography(personID, content)
	if err != nil {
		applogger.Error("SendBiographyEvent: failed to create biography",
			"person_id", personID,
			"error", err,
		)
		return
	}

	eventID, err := memory.RecordBiographyEvent(biography.ID)
	if err != nil {
		applogger.Error("SendBiographyEvent: failed to record biography event",
			"biography_id", biography.ID,
			"error", err,
		)
		return
	}

	eventqueue.SendEvent(agentConfigID, &eventqueue.AgentEvent{
		Type:    eventqueue.EventTypeBiography,
		EventID: eventID,
		Payload: &eventqueue.BiographyPayload{
			BiographyID: biography.ID,
			Content:     content,
		},
	})

	applogger.Info("biography event dispatched",
		"agent_config_id", agentConfigID,
		"person_id", personID,
		"biography_id", biography.ID,
		"event_id", eventID,
	)
}
