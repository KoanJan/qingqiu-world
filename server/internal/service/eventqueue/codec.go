package eventqueue

import (
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/model"
)

// bufferedEventPayload is the wire format for a buffered agent event. It wraps
// the type-specific payload together with the provenance field that lives on
// AgentEvent (not inside the payload), so replay can restore it without a
// schema change.
type bufferedEventPayload struct {
	Payload       json.RawMessage `json:"payload"`
	TriggerAction *TriggerAction  `json:"trigger_action,omitempty"`
}

// Serialize marshals the event payload and the AgentEvent-level
// TriggerAction into a single JSON string for the buffer table.
func Serialize(event *AgentEvent) (string, error) {
	var payloadJSON []byte
	var err error
	switch event.Type {
	case EventTypeNewPrivateChatMessage:
		payload, ok := event.Payload.(*NewMessagePayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid new message payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeScheduled:
		payload, ok := event.Payload.(*ScheduledEventPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid scheduled event payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeWorkCompleted:
		payload, ok := event.Payload.(*WorkCompletedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid work completed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeAlarmCreated:
		payload, ok := event.Payload.(*AlarmCreatedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid alarm created payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeBiography:
		payload, ok := event.Payload.(*BiographyPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid biography payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeNewJinshuReceived:
		payload, ok := event.Payload.(*JinshuReceivedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu received payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeJinshuListed:
		payload, ok := event.Payload.(*JinshuListedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu listed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeJinshuSent:
		payload, ok := event.Payload.(*JinshuSentPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu sent payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeJinshuSentListed:
		payload, ok := event.Payload.(*JinshuSentListedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid jinshu sent listed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeOwnedSpaceInspected:
		payload, ok := event.Payload.(*OwnedSpaceInspectedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid owned space inspected payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypePSCompleted:
		payload, ok := event.Payload.(*PSCompletedPayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid private space completed payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeExecutionSlotAvailable:
		payload, ok := event.Payload.(*ExecutionSlotAvailablePayload)
		if !ok || payload == nil {
			return "", fmt.Errorf("invalid execution slot payload")
		}
		payloadJSON, err = json.Marshal(payload)
	case EventTypeSystemNotification:
		payload, ok := event.Payload.(string)
		if !ok {
			return "", fmt.Errorf("invalid system notification payload")
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

// Deserialize restores typed payload and provenance from a durable delivery.
func Deserialize(buffer model.AgentEventBuffer) (*AgentEvent, error) {
	event := &AgentEvent{
		Type:      AgentEventType(buffer.EventType),
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
func unmarshalEventPayload(event *AgentEvent, raw json.RawMessage) error {
	switch event.Type {
	case EventTypeNewPrivateChatMessage:
		payload := &NewMessagePayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeScheduled:
		payload := &ScheduledEventPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeWorkCompleted:
		payload := &WorkCompletedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeAlarmCreated:
		payload := &AlarmCreatedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeBiography:
		payload := &BiographyPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeNewJinshuReceived:
		payload := &JinshuReceivedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeJinshuListed:
		payload := &JinshuListedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeJinshuSent:
		payload := &JinshuSentPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeJinshuSentListed:
		payload := &JinshuSentListedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeOwnedSpaceInspected:
		payload := &OwnedSpaceInspectedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypePSCompleted:
		payload := &PSCompletedPayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeExecutionSlotAvailable:
		payload := &ExecutionSlotAvailablePayload{}
		if err := json.Unmarshal(raw, payload); err != nil {
			return err
		}
		event.Payload = payload
	case EventTypeSystemNotification:
		var payload string
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		event.Payload = payload
	default:
		return fmt.Errorf("unsupported event type %d", event.Type)
	}
	return nil
}
