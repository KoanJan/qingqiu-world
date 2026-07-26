package memory

import (
	"context"
	"sync"
	"sync/atomic"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/vectorutils"

	applogger "qingqiu-world-server/internal/logger"
)

// Package-level state for the memory system singleton.
var (
	embeddingSvc *llm.EmbeddingService
	initOnce     sync.Once
	ready        atomic.Bool
)

// Init sets the embedding service reference for the memory system.
// Must be called once during application startup, before any memory operations.
// Idempotent: only the first call has effect.
// embeddingSvc may be nil if only profile generation is needed.
func Init(es *llm.EmbeddingService) {
	initOnce.Do(func() {
		embeddingSvc = es
		ready.Store(true)
		applogger.Info("Memory system initialized")
	})
}

func panicIfNotReady() {
	if !ready.Load() {
		panic("Memory system not initialized")
	}
}

// Start launches background services (event vectorization, daily maintenance
// cron) tied to ctx. When ctx is cancelled, goroutines drain remaining work
// and exit gracefully. Init must be called first.
func Start(ctx context.Context) {
	panicIfNotReady()
	go startEventVectorization(ctx)
	go runDailyCron(ctx)
	applogger.Info("Memory background services started")
}

// OnRetrievalHit is the package-level entry point for applying retrieval hits
// from context-engineering to the memory system. It is safe to call
// before Init(); the call is silently ignored when the memory service is
// not configured.
func OnRetrievalHit(personID int64, messageIDs []int64) {
	panicIfNotReady()
	applyRetrievalHits(personID, messageIDs)
}

// CheckProfileDensity is the package-level entry point for dimension B
// profile-density checks. Safe to call before Init().
func CheckProfileDensity(ctx context.Context, personID int64) int {
	panicIfNotReady()
	return checkDensity(ctx, personID)
}

// ingestMessage creates event + embedding + observations for a newly created
// message. This is the central ingestion hook called from the business layer
// (API handler for user messages, runtime for agent messages).
func ingestMessage(ctx context.Context, messageID, sessionID int64, content string) {
	// Create event + embedding in one step
	eventID, err := createEventWithEmbedding(ctx, model.EventTypeMessage, messageID, content)
	if err != nil {
		applogger.Error("Failed to ingest message event",
			"message_id", messageID, "error", err)
		return
	}

	// Create observations for all agents participating in this session.
	// Agents are identified by Person type=AI via join with persons table.
	var participants []model.ParticipantSession
	var partErr error
	participants, partErr = dops.GetSessionParticipantsByPersonType(sessionID, model.PersonTypeAI)
	if partErr != nil {
		applogger.Error("failed to load participants for observation creation", "session_id", sessionID, "error", partErr)
		return
	}

	for _, p := range participants {
		if err := createObservation(ctx, p.ParticipantID, eventID); err != nil {
			applogger.Error("Failed to create observation for agent",
				"agent_config_id", p.ParticipantID,
				"event_id", eventID,
				"error", err,
			)
		}
	}

	applogger.Debug("Message ingested into memory system",
		"message_id", messageID,
		"event_id", eventID,
		"agent_count", len(participants),
	)
}

// applyRetrievalHits applies a retrieval hit to observations associated with the given
// chat-history message IDs. Called when context engineering retrieval
// fetches historical segments for the LLM prompt — these segments
// represent prior events the agent's memory system should recognise as
// having been recalled.
func applyRetrievalHits(personID int64, messageIDs []int64) {
	if len(messageIDs) == 0 {
		return
	}

	// Find events for these messages (event_type=1 = EventTypeMessage)
	var events []model.Event
	if err := database.DB.Where("event_type = ? AND ref_id IN ?", model.EventTypeMessage, messageIDs).
		Find(&events).Error; err != nil {
		applogger.Error("processRetrievalHit: failed to load events", "error", err)
		return
	}

	if len(events) == 0 {
		return
	}

	eventIDs := make([]int64, len(events))
	for i, e := range events {
		eventIDs[i] = e.ID
	}

	// Load observations for this agent that correspond to these events
	var observations []model.AgentObservation
	if err := database.DB.Where("person_id = ? AND event_id IN ?", personID, eventIDs).
		Find(&observations).Error; err != nil {
		applogger.Error("processRetrievalHit: failed to load observations", "error", err)
		return
	}

	hitCount := 0
	for i := range observations {
		obs := &observations[i]
		delta := onRetrievalHit(obs)
		if delta > 0 {
			hitCount++
		}

		// Persist the updated scores
		if err := database.DB.Model(&model.AgentObservation{}).Where("id = ?", obs.ID).Updates(map[string]interface{}{
			"importance":       obs.Importance,
			"last_accessed_at": obs.LastAccessedAt,
			"last_scored_at":   obs.LastScoredAt,
		}).Error; err != nil {
			applogger.Error("processRetrievalHit: failed to persist observation scores", "obs_id", obs.ID, "error", err)
		}

		// Propagate relevance (time-adjacent, similar, same-session)
		// for hits that moved the importance needle.
		if delta > 0 {
			go propagateRetrievalHit(obs, delta)
		}
	}

	if hitCount > 0 {
		applogger.Info("Retrieval hit applied to memory observations",
			"person_id", personID,
			"message_ids", len(messageIDs),
			"observation_count", len(observations),
			"hit_count", hitCount,
		)
	}
}

// propagateRetrievalHit runs relevance propagation for a retrieval-hit observation.
// Uses a best-effort approach: loads observations from the same session and
// applies temporal adjacency propagation.
func propagateRetrievalHit(obs *model.AgentObservation, delta float64) {
	sessionID := getEventSessionID(obs.EventID)
	if sessionID == 0 {
		return
	}

	var sessionObservations []model.AgentObservation
	if err := database.DB.Where("person_id = ? AND event_id IN (SELECT id FROM events WHERE event_type = ? AND ref_id IN (SELECT id FROM messages WHERE session_id = ?))",
		obs.PersonID, model.EventTypeMessage, sessionID).
		Find(&sessionObservations).Error; err != nil {
		applogger.Error("propagateRetrievalHit: failed to load session observations", "person_id", obs.PersonID, "session_id", sessionID, "error", err)
		return
	}

	// Build the context slice needed by PropagateRelevance
	obsWithCtx := make([]observationWithContext, len(sessionObservations))
	for i, o := range sessionObservations {
		obsWithCtx[i] = observationWithContext{
			ObservationID: o.ID,
			EventID:       o.EventID,
			SessionID:     sessionID,
		}
	}

	// Load the hit event's embedding for semantic propagation
	var hitEmbedding []float32
	var ev model.EventVector
	if err := database.DB.Where("event_id = ?", obs.EventID).First(&ev).Error; err != nil {
		applogger.Error("propagateRetrievalHit: failed to load event vector, semantic propagation will be skipped",
			"event_id", obs.EventID, "error", err)
	} else {
		hitEmbedding = vectorutils.BlobToFloat32Slice(ev.Embedding)
	}

	params := propagateParams{
		DeltaBase:         delta,
		HitEventID:        obs.EventID,
		HitSessionID:      sessionID,
		HitEmbedding:      hitEmbedding,
		AllObservationIDs: obsWithCtx,
	}

	propagateRelevance(params)
}

// getEventSessionID returns the session_id for an event.
// For message events, this comes from the messages table.
func getEventSessionID(eventID int64) int64 {
	var event model.Event
	if err := database.DB.First(&event, eventID).Error; err != nil {
		return 0
	}

	if event.EventType == model.EventTypeMessage {
		var msg model.Message
		if err := database.DB.First(&msg, event.RefID).Error; err != nil {
			return 0
		}
		return msg.SessionID
	}

	return 0
}
