package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
)

// bufferedEventPayload is the wire format for a buffered agent event. It wraps
// the type-specific payload together with the provenance field that lives on
// AgentEvent (not inside the payload), so replay can restore it without a
// schema change.
type bufferedEventPayload struct {
	Payload       json.RawMessage           `json:"payload"`
	TriggerAction *eventqueue.TriggerAction `json:"trigger_action,omitempty"`
}

// serializeEventPayload marshals the event payload and the AgentEvent-level
// TriggerAction into a single JSON string for the buffer table.
func serializeEventPayload(event *eventqueue.AgentEvent) (string, error) {
	var payloadJSON []byte
	var err error
	switch event.Type {
	case eventqueue.EventTypeNewPrivateChatMessage:
		payload, ok := event.Payload.(*eventqueue.NewMessagePayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid new message payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeScheduled:
		payload, ok := event.Payload.(*eventqueue.ScheduledEventPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid scheduled event payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeWorkCompleted:
		payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid work completed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeAlarmCreated:
		payload, ok := event.Payload.(*eventqueue.AlarmCreatedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid alarm created payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeBiography:
		payload, ok := event.Payload.(*eventqueue.BiographyPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid biography payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeNewJinshuReceived:
		payload, ok := event.Payload.(*eventqueue.JinshuReceivedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu received payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeJinshuReadCompleted:
		payload, ok := event.Payload.(*eventqueue.JinshuReadCompletedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu read completed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeJinshuListed:
		payload, ok := event.Payload.(*eventqueue.JinshuListedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu listed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeJinshuSent:
		payload, ok := event.Payload.(*eventqueue.JinshuSentPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu sent payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case eventqueue.EventTypeJinshuSentListed:
		payload, ok := event.Payload.(*eventqueue.JinshuSentListedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu sent listed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	default:
		return "", fmt.Errorf("unsupported event type %d", event.Type)
	}
	if err != nil {
		return "", err
	}

	wrapper := bufferedEventPayload{
		Payload:       payloadJSON,
		TriggerAction: event.TriggerAction,
	}
	data, err := json.Marshal(wrapper)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func deserializeBufferedEvent(buffer model.AgentEventBuffer) (*eventqueue.AgentEvent, error) {
	event := &eventqueue.AgentEvent{
		Type:      eventqueue.AgentEventType(buffer.EventType),
		SessionID: buffer.SessionID,
		EventID:   buffer.EventID,
	}

	// Prefer the wrapped format. Rows buffered before TriggerAction moved to
	// AgentEvent contain the raw payload object; unmarshalling that into the
	// wrapper leaves Payload empty, so fall back to raw payload parsing.
	var wrapper bufferedEventPayload
	if err := json.Unmarshal([]byte(buffer.PayloadJSON), &wrapper); err == nil && len(wrapper.Payload) > 0 {
		event.TriggerAction = wrapper.TriggerAction
		return event, unmarshalEventPayload(event, wrapper.Payload)
	}
	return event, unmarshalEventPayload(event, []byte(buffer.PayloadJSON))
}

// unmarshalEventPayload decodes the type-specific payload into the event.
func unmarshalEventPayload(event *eventqueue.AgentEvent, raw json.RawMessage) error {
	switch event.Type {
	case eventqueue.EventTypeNewPrivateChatMessage:
		payload := &eventqueue.NewMessagePayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeScheduled:
		payload := &eventqueue.ScheduledEventPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeWorkCompleted:
		payload := &eventqueue.WorkCompletedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeAlarmCreated:
		payload := &eventqueue.AlarmCreatedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeBiography:
		payload := &eventqueue.BiographyPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeNewJinshuReceived:
		payload := &eventqueue.JinshuReceivedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeJinshuReadCompleted:
		payload := &eventqueue.JinshuReadCompletedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeJinshuListed:
		payload := &eventqueue.JinshuListedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeJinshuSent:
		payload := &eventqueue.JinshuSentPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case eventqueue.EventTypeJinshuSentListed:
		payload := &eventqueue.JinshuSentListedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	default:
		return fmt.Errorf("unsupported event type %d", event.Type)
	}
	return nil
}

func (r *agentRuntime) bufferEvent(event *eventqueue.AgentEvent) error {
	payloadJSON, err := serializeEventPayload(event)
	if err != nil {
		return err
	}
	if err := dops.CreateAgentEventBuffer(&model.AgentEventBuffer{
		PersonID:    r.agentPersonID,
		EventType:   int(event.Type),
		SessionID:   event.SessionID,
		EventID:     event.EventID,
		PayloadJSON: payloadJSON,
	}); err != nil {
		return err
	}
	applogger.Info("buffered agent event due to insufficient energy",
		"person_id", r.agentPersonID,
		"event_type", event.Type,
		"session_id", event.SessionID,
		"event_id", event.EventID,
	)
	return dops.SetAgentSleepSinceIfEmpty(r.agentPersonID, time.Now())
}

func (r *agentRuntime) replayBufferedEvents(ctx context.Context) {
	buffers, err := dops.ListAgentEventBuffers(r.agentPersonID)
	if err != nil {
		applogger.Error("failed to list buffered agent events", "person_id", r.agentPersonID, "error", err)
		return
	}
	if len(buffers) > 0 {
		applogger.Info("replaying buffered agent events",
			"person_id", r.agentPersonID,
			"buffer_count", len(buffers),
		)
	}
	for _, buffer := range buffers {
		if ctx.Err() != nil {
			return
		}
		event, err := deserializeBufferedEvent(buffer)
		if err != nil {
			applogger.Error("failed to decode buffered agent event", "buffer_id", buffer.ID, "error", err)
			if err := dops.DeleteAgentEventBuffer(buffer.ID); err != nil {
				applogger.Error("failed to delete invalid buffered agent event", "buffer_id", buffer.ID, "error", err)
			}
			continue
		}
		applogger.Info("replaying buffered agent event",
			"person_id", r.agentPersonID,
			"buffer_id", buffer.ID,
			"event_type", event.Type,
			"session_id", event.SessionID,
			"event_id", event.EventID,
		)
		if !r.handleEvent(ctx, event, true) {
			applogger.Info("paused buffered event replay due to insufficient energy",
				"person_id", r.agentPersonID,
				"buffer_id", buffer.ID,
				"event_id", event.EventID,
			)
			return
		}
		if err := dops.DeleteAgentEventBuffer(buffer.ID); err != nil {
			applogger.Error("failed to delete replayed agent event buffer", "buffer_id", buffer.ID, "error", err)
		}
	}
	if len(buffers) > 0 {
		if err := dops.ClearAgentSleepSince(r.agentPersonID); err != nil {
			applogger.Error("failed to clear agent sleep state after replay", "person_id", r.agentPersonID, "error", err)
		}
		applogger.Info("completed buffered agent event replay",
			"person_id", r.agentPersonID,
			"buffer_count", len(buffers),
		)
	}
}
