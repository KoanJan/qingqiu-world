package memory

import (
	"context"

	"qingqiu-world-server/internal/model"

	applogger "qingqiu-world-server/internal/logger"
)

// embeddingTask carries the data needed for async embedding generation.
// The event record is already persisted synchronously by RecordEvent;
// the background goroutine only needs eventID + content to call the
// embedding API and store the resulting vector.
type embeddingTask struct {
	eventID int64
	content string
}

const vectorizationChannelSize = 256

// vectorizerCh is the buffered channel for async embedding generation.
// It is purely a vectorization work queue — no event creation, no
// observation creation. Those are handled by RecordEvent (sync) and
// CreateObservation (caller-side) respectively.
var vectorizerCh = make(chan embeddingTask, vectorizationChannelSize)

// ---------------------------------------------------------------------------
// External API — production
// ---------------------------------------------------------------------------

// RecordEvent creates a memory event record for a message and enqueues
// async embedding generation. This is the production-side entry point.
//
// Sync: inserts the event row (fast local DB write) and returns eventID.
// Async: if embeddingSvc is configured, enqueues embedding generation to
// vectorizerCh. The background goroutine generates and stores the vector.
//
// This function does NOT create observations — that is the consumer's job.
// Agent runtimes call CreateObservation after receiving the event from
// eventqueue.
func RecordEvent(messageID int64, content string) (int64, error) {
	eventID, err := createEvent(model.EventTypeMessage, messageID)
	if err != nil {
		return 0, err
	}

	// Enqueue async embedding generation if embedding service is configured.
	if embeddingSvc != nil {
		select {
		case vectorizerCh <- embeddingTask{eventID: eventID, content: content}:
		default:
			applogger.Error("Embedding queue full, embedding generation skipped",
				"event_id", eventID)
		}
	}

	return eventID, nil
}

// ---------------------------------------------------------------------------
// internal — embedding background loop
// ---------------------------------------------------------------------------

// startEventVectorization runs the embedding generation loop. Drains
// remaining tasks when ctx is cancelled, then returns.
func startEventVectorization(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			drainVectorizerRemaining()
			return
		case task := <-vectorizerCh:
			if err := storeEventEmbedding(ctx, task.eventID, task.content); err != nil {
				applogger.Error("Failed to store event embedding",
					"event_id", task.eventID, "error", err)
			}
		}
	}
}

// drainVectorizerRemaining processes any queued embedding tasks before
// shutdown.
func drainVectorizerRemaining() {
	for {
		select {
		case task := <-vectorizerCh:
			if err := storeEventEmbedding(context.Background(), task.eventID, task.content); err != nil {
				applogger.Error("Failed to store event embedding (drain)",
					"event_id", task.eventID, "error", err)
			}
		default:
			return
		}
	}
}
