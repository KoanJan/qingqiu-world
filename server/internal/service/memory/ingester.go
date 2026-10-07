package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/vectorutils"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	applogger "qingqiu-world-server/internal/logger"
)

// createEvent creates an event record and returns the event_id.
func createEvent(eventType model.EventType, refID int64) (int64, error) {
	id, err := createEventRecord(eventType, refID, "")
	if err == nil {
		tryRefresh(model.MemorySourceEvent, id)
	}
	return id, err
}

// RecordReferencedEvent records a runtime occurrence whose durable content
// lives in an existing domain record.
func RecordReferencedEvent(eventType model.EventType, refID int64) (int64, error) {
	return createEvent(eventType, refID)
}

// RecordReferencedEventTx creates a referenced Event inside the producer's
// database transaction so the domain result and its Event commit together.
func RecordReferencedEventTx(tx *gorm.DB, eventType model.EventType, refID int64) (int64, error) {
	return createEventRecordWithDB(tx, eventType, refID, "")
}

// RecordSelfHeldEvent records a runtime result that has no durable domain
// object. Its typed queue payload is preserved as a JSON fact snapshot.
func RecordSelfHeldEvent(eventType model.EventType, payload any) (int64, error) {
	id, err := RecordSelfHeldEventTx(database.DB, eventType, payload)
	if err == nil {
		tryRefresh(model.MemorySourceEvent, id)
	}
	return id, err
}

// tryRefresh updates the derived lexical index after a source commits.
// Indexing failure is logged without undoing the authoritative source write.
func tryRefresh(kind model.MemorySourceKind, id int64) {
	if err := RefreshSource(kind, id); err != nil {
		applogger.Error("memory term index refresh failed", "source_kind", kind, "source_id", id, "error", err)
	}
}

// RecordSelfHeldEventTx persists a self-held snapshot inside a caller-owned
// transaction, allowing its private ActionEffect to commit atomically.
func RecordSelfHeldEventTx(tx *gorm.DB, eventType model.EventType, payload any) (int64, error) {
	if payload == nil {
		return 0, fmt.Errorf("self-held event type %d has nil payload", eventType)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode self-held event type %d: %w", eventType, err)
	}
	return createEventRecordWithDB(tx, eventType, 0, string(encoded))
}

// createEventRecord is the sole insertion boundary for referenced and
// self-held events. A zero ref must carry a nonempty valid JSON snapshot.
func createEventRecord(eventType model.EventType, refID int64, payloadJSON string) (int64, error) {
	return createEventRecordWithDB(database.DB, eventType, refID, payloadJSON)
}

// createEventRecordWithDB applies the same source invariant to every writer.
func createEventRecordWithDB(tx *gorm.DB, eventType model.EventType, refID int64, payloadJSON string) (int64, error) {
	if eventType <= 0 || (refID > 0) == (payloadJSON != "") || refID < 0 {
		return 0, fmt.Errorf("invalid event source: type=%d ref_id=%d has_payload=%t", eventType, refID, payloadJSON != "")
	}
	if payloadJSON != "" && !json.Valid([]byte(payloadJSON)) {
		return 0, fmt.Errorf("invalid JSON snapshot for event type %d", eventType)
	}
	event := &model.Event{
		EventType:   eventType,
		RefID:       refID,
		PayloadJSON: payloadJSON,
	}
	if err := tx.Create(event).Error; err != nil {
		return 0, fmt.Errorf("failed to create event: %w", err)
	}

	applogger.Debug("Event created",
		"event_id", event.ID,
		"event_type", eventType,
		"ref_id", refID,
	)
	return event.ID, nil
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

// CreateObservation creates a mechanical observation record for an agent.
// No LLM — content is retrieved on demand via event_id → events.
//
// This is the consumption-side entry point. Agent runtimes call this after
// receiving an event from eventqueue to record that the agent has observed
// the event.
func CreateObservation(personID, eventID int64) error {
	obsID, err := createObservationWithDB(database.DB, personID, eventID)
	if err != nil {
		return err
	}
	if obsID > 0 {
		applogger.Debug("Observation created",
			"person_id", personID,
			"event_id", eventID,
			"obs_id", obsID,
		)
	}
	return nil
}

// CreateObservationTx inserts an observation in a caller-owned transaction.
// It returns the new ID, or zero if the observation already existed. The
// caller may report creation only after its transaction commits successfully.
func CreateObservationTx(tx *gorm.DB, personID, eventID int64) (int64, error) {
	return createObservationWithDB(tx, personID, eventID)
}

// createObservationWithDB inserts one perspective on an Event in the supplied
// transaction. Replayed deliveries leave the existing observation unchanged.
func createObservationWithDB(db *gorm.DB, personID, eventID int64) (int64, error) {
	if personID <= 0 || eventID <= 0 {
		return 0, fmt.Errorf("invalid observation: person_id=%d event_id=%d", personID, eventID)
	}
	obs := newObservation(personID, eventID)

	// A replay or a batched message may reach this boundary more than once.
	// Preserve the original observation and its later importance updates.
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(obs)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to create observation: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, nil
	}
	return obs.ID, nil
}
