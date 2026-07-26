package memory

import (
	"math"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/vectorutils"

	applogger "qingqiu-world-server/internal/logger"
)

// Algorithm parameters.
const (
	// importance scoring
	importanceInitial  = 0.5
	importanceGain     = 0.1
	keyMomentThreshold = 0.85

	// relevance propagation factors
	propagateAdjacent            = 0.5
	propagateSimilar             = 0.2
	propagateSameSessionFactor   = 0.15
	propagateSimilarityThreshold = 0.8

	// decay: daily multiplicative decay factor for importance.
	// All observations decay each day: importance *= decayFactor.
	// decayFactor < 1 ensures smooth, asymptotic decay that never reaches zero.
	// Example trajectory (no further boosts): 0.85 → 0.83 → 0.82 → 0.80 → ...
	// An observation at 0.55 decays to ~0.50 after 5 days, ~0.30 after 30 days.
	decayFactor          = 0.98
	scoreCooldownMinutes = 10
)

// Scoring functions for agent observations.  Implements the forgetting model's
// core algorithm: importance asymptote toward 1.0 on repeated retrieval hits,
// with anti-hot protection. All functions are stateless — they operate on
// observation records passed by reference.

// onRetrievalHit updates an observation's scores when it is retrieved and
// injected into context. This is the primary activation pathway:
//
//	last_accessed_at = NOW()
//
//	// importance update with anti-hot protection
//	if NOW() - last_scored_at >= SCORE_COOLDOWN_MINUTES:
//	    Δ_base = α * (1 - importance)
//	    importance' = importance + Δ_base
//	    last_scored_at = NOW()
//
// Returns the delta applied to importance (0 if cooldown prevented update),
// which is used by the relevance propagator to spread activation to related
// observations.
func onRetrievalHit(obs *model.AgentObservation) float64 {
	now := time.Now()

	// Access-related fields are always updated unconditionally
	obs.LastAccessedAt = now

	// Anti-hot protection: skip importance scoring if within cooldown
	if now.Sub(obs.LastScoredAt) < scoreCooldownMinutes*time.Minute {
		return 0
	}

	// importance asymptote toward 1.0
	deltaBase := importanceGain * (1.0 - obs.Importance)
	obs.Importance += deltaBase
	obs.LastScoredAt = now

	return deltaBase
}

// newObservation creates a new AgentObservation with default scores.
func newObservation(personID, eventID int64) *model.AgentObservation {
	now := time.Now()
	return &model.AgentObservation{
		PersonID:       personID,
		EventID:        eventID,
		Importance:     importanceInitial,
		LastAccessedAt: now,
		LastScoredAt:   now,
	}
}

// propagateParams encodes the parameters needed for relevance propagation.
type propagateParams struct {
	// DeltaBase is the importance delta from the hit observation
	DeltaBase float64
	// HitEventID is the event_id of the observation that was hit
	HitEventID int64
	// HitSessionID is the session_id of the hit event (for same-session propagation)
	HitSessionID int64
	// HitEmbedding is the embedding vector of the hit event (for semantic propagation)
	HitEmbedding []float32
	// AllObservationIDs is the list of all (observation_id, event_id, session_id) tuples
	// for the agent, used to find adjacent events
	AllObservationIDs []observationWithContext
}

// observationWithContext bundles an observation ID with its associated event
// and session context for propagation computations.
type observationWithContext struct {
	ObservationID int64
	EventID       int64
	SessionID     int64
}

// propagateRelevance spreads importance gain from a hit observation to
// related observations. The propagation is one-way (only increases scores)
// and replaces the v2 Semantic layer clustering.
//
// Rules:
//   - Adjacent in time (±1): delta × 0.5, (±2): delta × 0.2
//   - Semantically similar (cosine > 0.8): delta × 0.2
//   - Same session: delta × 0.15
//
// Each target independently checks the cooldown to prevent hot-loop inflation.
func propagateRelevance(params propagateParams) {
	// Build a reverse index: event_id → position in the ordered list
	// to find temporal neighbors efficiently.
	eventPositions := make(map[int64]int)
	for i, oc := range params.AllObservationIDs {
		eventPositions[oc.EventID] = i
	}

	hitPos, hitFound := eventPositions[params.HitEventID]
	if !hitFound {
		return
	}

	processed := make(map[int64]bool)
	processed[params.HitEventID] = true // Don't propagate to self

	// Temporal adjacency propagation
	propagateTemporal(params, hitPos, processed, &params.AllObservationIDs)

	// Semantic similarity propagation (if hit embedding is available)
	if params.HitEmbedding != nil {
		propagateSemantic(params, &params.AllObservationIDs, processed)
	}

	// Same-session propagation
	propagateSameSession(params, &params.AllObservationIDs, processed)

	applogger.Debug("Relevance propagation completed",
		"hit_event_id", params.HitEventID,
		"delta_base", params.DeltaBase,
	)
}

// propagateTemporal spreads importance to events adjacent in time.
func propagateTemporal(
	params propagateParams,
	hitPos int,
	processed map[int64]bool,
	allObs *[]observationWithContext,
) {
	// ±1 position (immediately adjacent): factor 0.5
	adjacent1 := []int{hitPos - 1, hitPos + 1}
	for _, pos := range adjacent1 {
		if pos >= 0 && pos < len(*allObs) {
			oc := (*allObs)[pos]
			if !processed[oc.ObservationID] {
				applyPropagationToObservation(oc.ObservationID, params.DeltaBase*propagateAdjacent)
				processed[oc.ObservationID] = true
			}
		}
	}

	// ±2 position (two hops): factor 0.2
	adjacent2 := []int{hitPos - 2, hitPos + 2}
	for _, pos := range adjacent2 {
		if pos >= 0 && pos < len(*allObs) {
			oc := (*allObs)[pos]
			if !processed[oc.ObservationID] {
				applyPropagationToObservation(oc.ObservationID, params.DeltaBase*propagateSimilar)
				processed[oc.ObservationID] = true
			}
		}
	}
}

// propagateSameSession spreads importance to observations in the same session.
func propagateSameSession(
	params propagateParams,
	allObs *[]observationWithContext,
	processed map[int64]bool,
) {
	for _, oc := range *allObs {
		if processed[oc.ObservationID] {
			continue
		}
		if oc.SessionID == params.HitSessionID {
			applyPropagationToObservation(oc.ObservationID, params.DeltaBase*propagateSameSessionFactor)
			processed[oc.ObservationID] = true
		}
	}
}

// propagateSemantic spreads importance to observations with semantically
// similar event vectors (cosine similarity > propagateSimilarityThreshold).
// This is the most expensive propagation rule and should be called with
// reasonable batch sizes.
func propagateSemantic(
	params propagateParams,
	allObs *[]observationWithContext,
	processed map[int64]bool,
) {
	// Load embeddings for all unprocessed observations and compare
	for _, oc := range *allObs {
		if processed[oc.ObservationID] {
			continue
		}

		// Load event vector for this observation
		var ev model.EventVector
		if err := database.DB.First(&ev, "event_id = ?", oc.EventID).Error; err != nil {
			applogger.Error("failed to find event vector during context scoring", "event_id", oc.EventID, "error", err)
			continue
		}
		if len(ev.Embedding) == 0 {
			continue
		}

		storedEmbedding := vectorutils.BlobToFloat32Slice(ev.Embedding)
		if storedEmbedding == nil {
			continue
		}

		similarity := vectorutils.CosineSimilarity(params.HitEmbedding, storedEmbedding)
		if similarity > propagateSimilarityThreshold {
			applyPropagationToObservation(oc.ObservationID, params.DeltaBase*propagateSimilar)
			processed[oc.ObservationID] = true
		}
	}
}

// applyPropagationToObservation applies a propagation delta to a single
// observation, with anti-hot protection. The observation is loaded from DB,
// updated if cooldown allows, and persisted back.
func applyPropagationToObservation(obsID int64, delta float64) {
	var obs model.AgentObservation
	if err := database.DB.First(&obs, obsID).Error; err != nil {
		return
	}

	// Anti-hot protection for propagation targets
	if time.Since(obs.LastScoredAt) < scoreCooldownMinutes*time.Minute {
		return
	}

	obs.Importance = math.Min(1.0, obs.Importance+delta)
	obs.LastScoredAt = time.Now()

	if err := database.DB.Save(&obs).Error; err != nil {
		applogger.Error("Failed to save propagated observation",
			"obs_id", obsID, "error", err)
	}
}
