package memory

import (
	"context"
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/vectorutils"

	applogger "qingqiu-world-server/internal/logger"
)

// createEvent creates an event record and returns the event_id.
func createEvent(ctx context.Context, eventType int, refID int64) (int64, error) {
	event := &model.Event{
		EventType: eventType,
		RefID:     refID,
	}
	if err := database.DB.Create(event).Error; err != nil {
		return 0, fmt.Errorf("failed to create event: %w", err)
	}

	applogger.Debug("Event created",
		"event_id", event.ID,
		"event_type", eventType,
		"ref_id", refID,
	)
	return event.ID, nil
}

// createEventWithEmbedding creates an event record and generates/stores its
// embedding in a single operation.
//
// If embeddingSvc is nil, the vector storage step is skipped silently.
func createEventWithEmbedding(ctx context.Context, eventType int, refID int64, content string) (int64, error) {
	eventID, err := createEvent(ctx, eventType, refID)
	if err != nil {
		return 0, err
	}

	if embeddingSvc != nil {
		if err := storeEventEmbedding(ctx, eventID, content); err != nil {
			applogger.Error("Failed to store event embedding, event created without vector",
				"event_id", eventID, "error", err)
		}
	}

	return eventID, nil
}

// storeEventEmbedding generates an embedding for the event content and
// persists it to the event_vectors table.
func storeEventEmbedding(ctx context.Context, eventID int64, content string) error {
	embedding, err := embeddingSvc.EmbedSingle(ctx, content)
	if err != nil {
		return fmt.Errorf("embedding generation failed: %w", err)
	}

	blob := vectorutils.Float32SliceToBlob(embedding)
	ev := &model.EventVector{
		EventID:   eventID,
		Embedding: blob,
	}
	if err := database.DB.Create(ev).Error; err != nil {
		return fmt.Errorf("failed to store event vector: %w", err)
	}

	applogger.Debug("Event vector stored",
		"event_id", eventID,
		"dimension", len(embedding),
	)
	return nil
}

// createObservation creates a mechanical observation for an agent.
// No LLM — content is retrieved on demand via event_id → events.
func createObservation(ctx context.Context, personID, eventID int64) error {
	obs := newObservation(personID, eventID)

	if err := database.DB.Create(obs).Error; err != nil {
		return fmt.Errorf("failed to create observation: %w", err)
	}

	applogger.Debug("Observation created",
		"person_id", personID,
		"event_id", eventID,
		"obs_id", obs.ID,
	)
	return nil
}
