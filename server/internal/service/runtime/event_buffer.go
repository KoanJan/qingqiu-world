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

func serializeEventPayload(event *eventqueue.AgentEvent) (string, error) {
	switch event.Type {
	case eventqueue.EventTypeNewPrivateChatMessage:
		payload, ok := event.Payload.(*eventqueue.NewMessagePayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid new message payload")
		}
		data, err := json.Marshal(payload)
		return string(data), err
	case eventqueue.EventTypeScheduled:
		payload, ok := event.Payload.(*eventqueue.ScheduledEventPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid scheduled event payload")
		}
		data, err := json.Marshal(payload)
		return string(data), err
	case eventqueue.EventTypeWorkCompleted:
		payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid work completed payload")
		}
		data, err := json.Marshal(payload)
		return string(data), err
	case eventqueue.EventTypeAlarmCreated:
		payload, ok := event.Payload.(*eventqueue.AlarmCreatedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid alarm created payload")
		}
		data, err := json.Marshal(payload)
		return string(data), err
	default:
		return "", fmt.Errorf("unsupported event type %d", event.Type)
	}
}

func deserializeBufferedEvent(buffer model.AgentEventBuffer) (*eventqueue.AgentEvent, error) {
	event := &eventqueue.AgentEvent{
		Type:      eventqueue.AgentEventType(buffer.EventType),
		SessionID: buffer.SessionID,
		EventID:   buffer.EventID,
	}
	switch event.Type {
	case eventqueue.EventTypeNewPrivateChatMessage:
		payload := &eventqueue.NewMessagePayload{}
		if err := json.Unmarshal([]byte(buffer.PayloadJSON), payload); err != nil {
			return nil, err
		}
		event.Payload = payload
	case eventqueue.EventTypeScheduled:
		payload := &eventqueue.ScheduledEventPayload{}
		if err := json.Unmarshal([]byte(buffer.PayloadJSON), payload); err != nil {
			return nil, err
		}
		event.Payload = payload
	case eventqueue.EventTypeWorkCompleted:
		payload := &eventqueue.WorkCompletedPayload{}
		if err := json.Unmarshal([]byte(buffer.PayloadJSON), payload); err != nil {
			return nil, err
		}
		event.Payload = payload
	case eventqueue.EventTypeAlarmCreated:
		payload := &eventqueue.AlarmCreatedPayload{}
		if err := json.Unmarshal([]byte(buffer.PayloadJSON), payload); err != nil {
			return nil, err
		}
		event.Payload = payload
	default:
		return nil, fmt.Errorf("unsupported event type %d", event.Type)
	}
	return event, nil
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
