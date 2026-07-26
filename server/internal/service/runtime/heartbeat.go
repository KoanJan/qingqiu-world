package runtime

import (
	"context"

	"qingqiu-world-server/internal/service/experience"
	"qingqiu-world-server/internal/service/memory"

	applogger "qingqiu-world-server/internal/logger"
)

// Heartbeat check frequency constants.
const (
	memoryDensityCheckInterval = 6 // Every 6 heartbeat ticks
	reflectionCheckInterval    = 1 // Every 1 heartbeat ticks
	learningCheckInterval      = 1 // Every 30 heartbeat ticks (low frequency — learning is a long-term decision)
)

// handleHeartbeat processes a heartbeat tick for periodic maintenance.
//
// The heartbeat handles periodic checks (obligations, memory density).
// Responsiveness is guaranteed by the eventqueue — user messages, scheduled
// events, etc. all trigger agent actions via interrupts. Heartbeat does not
// need to poll for unread messages or drive proactive messaging.
func (r *agentRuntime) handleHeartbeat(ctx context.Context) {
	if len(r.activeWorks) > 0 {
		// Agent is busy — no heartbeat processing needed
		return
	}

	r.heartbeatTick++
	r.idleTicks++

	// Memory density check (every 6 ticks)
	if r.heartbeatTick%memoryDensityCheckInterval == 0 {
		r.checkMemoryDensity(ctx)
	}

	// Reflection check (every tick)
	if r.heartbeatTick%reflectionCheckInterval == 0 {
		r.checkReflection(ctx)
	}

	// Learning check (every 30 ticks — low frequency, long-term decision)
	if r.heartbeatTick%learningCheckInterval == 0 {
		r.checkLearning(ctx)
	}
}

// checkMemoryDensity runs memory density check: detects when enough long-term
// observations have accumulated around an entity to trigger EntityProfile
// generation.
func (r *agentRuntime) checkMemoryDensity(ctx context.Context) {
	triggered := memory.CheckProfileDensity(ctx, r.agentPersonID)
	if triggered > 0 {
		applogger.Info("memory density check: EntityProfile generation triggered",
			"agent_config_id", r.agentConfigID,
			"profiles_triggered", triggered,
		)
	} else {
		applogger.Debug("memory density check: no profiles triggered",
			"agent_config_id", r.agentConfigID,
		)
	}
}

// checkReflection scans all sessions for the agent and triggers experience
// extraction via LLM reflection for sessions whose notes have changed since
// the last reflection.
func (r *agentRuntime) checkReflection(ctx context.Context) {
	experience.CheckReflection(ctx, r.agentPersonID)
	applogger.Debug("reflection check completed",
		"agent_config_id", r.agentConfigID,
	)
}

// checkLearning evaluates whether the agent should learn any public experiences
// based on its long-term interaction patterns captured in session entity_profiles.
//
// Runs asynchronously — the LLM call and copy work execute in a separate goroutine
// to avoid blocking the event loop. A learningInProgress flag prevents duplicate
// triggers while a learning cycle is still running.
// Triggers at low frequency (every 30 ticks) since learning is a long-term decision.
func (r *agentRuntime) checkLearning(ctx context.Context) {
	if !r.learningInProgress.CompareAndSwap(false, true) {
		applogger.Debug("learning check skipped: already in progress",
			"agent_config_id", r.agentConfigID)
		return
	}
	go func() {
		defer r.learningInProgress.Store(false)
		experience.CheckLearning(ctx, r.agentPersonID)
	}()
	applogger.Debug("learning check dispatched",
		"agent_config_id", r.agentConfigID,
	)
}
