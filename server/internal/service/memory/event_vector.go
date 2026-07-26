package memory

import (
	"context"

	applogger "qingqiu-world-server/internal/logger"
)

// VectorizationTask is a message that needs event creation, embedding
// generation, and observation mirroring.  Submitted by business-layer call
// sites (handler, runtime) and processed serially by the vectorization
// goroutine.
type VectorizationTask struct {
	MessageID int64
	SessionID int64
	Content   string
}

const vectorizationChannelSize = 256

// vectorizerCh is the buffered channel for submitting vectorization tasks.
var vectorizerCh = make(chan VectorizationTask, vectorizationChannelSize)

// ---------------------------------------------------------------------------
// External API
// ---------------------------------------------------------------------------

// SubmitVectorization enqueues a message for event vectorization.
// Non-blocking: drops the task (with a warning log) if the queue is full.
func SubmitVectorization(task VectorizationTask) {
	select {
	case vectorizerCh <- task:
	default:
		applogger.Error("Vectorization service queue full, task dropped",
			"message_id", task.MessageID)
	}
}

// ---------------------------------------------------------------------------
// internal
// ---------------------------------------------------------------------------

// startEventVectorization runs the event vectorization loop. Drains remaining
// tasks when ctx is cancelled, then returns.
func startEventVectorization(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			drainVectorizerRemaining()
			return
		case task := <-vectorizerCh:
			ingestMessage(ctx, task.MessageID, task.SessionID, task.Content)
		}
	}
}

func drainVectorizerRemaining() {
	for {
		select {
		case task := <-vectorizerCh:
			ingestMessage(context.Background(), task.MessageID, task.SessionID, task.Content)
		default:
			return
		}
	}
}
