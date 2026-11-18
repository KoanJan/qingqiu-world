// Package energy implements the agent energy budget system.
//
// It provides:
//   - World-calendar calculations through the world time source
//   - Daily energy recovery (lazy, multi-day catch-up, capped at MaxEnergy)
//   - Energy deduction (called by the Decide phase after actions are produced)
//
// The energy state is persisted in the agent_states table, keyed by person_id.
// All date math (today, days-between) uses the global fixed timezone.
package energy

import (
	"errors"
	"fmt"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/world"

	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Constants & errors
// ---------------------------------------------------------------------------

// MaxEnergy is the energy cap at any moment.
const MaxEnergy = 200

// DailyRecovery is the energy granted per day.
const DailyRecovery = 100

// Cost is the energy cost per Decide call.
type Cost int

const (
	// CostPassive applies when Decide is triggered by an eventqueue event.
	CostPassive Cost = 1
	// CostActive applies when Decide is triggered by a heartbeat (reserved
	// for future active-behavior paths; not triggered in 0.1.1).
	CostActive Cost = 5
	// CostPrivateSpace is the per-iteration cost for the private-space loop.
	// Kept intentionally low (equal to CostPassive) so the agent does not
	// avoid using its private space due to energy pressure.
	CostPrivateSpace Cost = 1
)

// ErrInsufficientEnergy is returned when energy < cost.
var ErrInsufficientEnergy = errors.New("insufficient energy")

// Timezone returns the world's persisted timezone.
func Timezone() *time.Location { return world.Timezone() }

// Now returns the current world time.
func Now() time.Time { return world.Now() }

// Today returns the current world date.
func Today() string { return world.Today() }

// ---------------------------------------------------------------------------
// Energy recovery
// ---------------------------------------------------------------------------

// RecoverEnergy applies daily energy recovery for an agent (identified by
// personID) and returns the refreshed AgentState.
//
// Multi-day recovery: if lastRecoveredDate was N days ago (in the global
// fixed timezone), adds N*DailyRecovery energy, capped at MaxEnergy.
// Idempotent: if already recovered today, no-op.
//
// On first access (no agent_states row), creates a row with:
//
//	energy=DailyRecovery, last_recovered_date=today.
//
// The returned *AgentState is the post-recovery snapshot. Callers use it
// both for hard-block pre-check (Energy >= cost) and as input to Decide's
// system message. Between RecoverEnergy and DeductEnergy the agent event
// loop is single-goroutine, so the snapshot stays consistent within one
// Decide cycle.
func RecoverEnergy(personID int64) (*model.AgentState, error) {
	var state model.AgentState
	err := database.DB.Where("person_id = ?", personID).First(&state).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// First access: create the row with initial energy.
			today := Today()
			state = model.AgentState{
				PersonID:          personID,
				Energy:            DailyRecovery,
				LastRecoveredDate: today,
			}
			if err := database.DB.Create(&state).Error; err != nil {
				return nil, fmt.Errorf("create agent_state: %w", err)
			}
			applogger.Info("energy: initialized agent_state",
				"person_id", personID, "energy", state.Energy, "date", today)
			return &state, nil
		}
		return nil, fmt.Errorf("query agent_state: %w", err)
	}

	today := Today()
	if state.LastRecoveredDate == today {
		// Already recovered today.
		return &state, nil
	}

	// Multi-day catch-up.
	days, err := daysBetween(state.LastRecoveredDate, today)
	if err != nil {
		// Unparseable date — log and skip recovery to avoid corrupting state.
		applogger.Error("energy: failed to compute days between, skip recovery",
			"person_id", personID,
			"last_recovered_date", state.LastRecoveredDate,
			"today", today,
			"error", err)
		return &state, nil
	}
	if days <= 0 {
		// Clock skew (last > today). Treat as no-op.
		applogger.Warn("energy: last_recovered_date is not before today, skip recovery",
			"person_id", personID,
			"last_recovered_date", state.LastRecoveredDate,
			"today", today)
		return &state, nil
	}

	newEnergy := state.Energy + days*DailyRecovery
	if newEnergy > MaxEnergy {
		newEnergy = MaxEnergy
	}

	if err := database.DB.Model(&model.AgentState{}).
		Where("person_id = ?", personID).
		Updates(map[string]interface{}{
			"energy":              newEnergy,
			"last_recovered_date": today,
		}).Error; err != nil {
		return nil, fmt.Errorf("update agent_state: %w", err)
	}

	applogger.Info("energy: daily recovery applied",
		"person_id", personID,
		"days", days,
		"before", state.Energy,
		"after", newEnergy,
		"date", today)

	state.Energy = newEnergy
	state.LastRecoveredDate = today
	return &state, nil
}

// daysBetween returns the whole-day difference between two "YYYY-MM-DD"
// date strings (today - last). Returns 0 if last is on or after today.
func daysBetween(lastDate, todayDate string) (int, error) {
	const layout = "2006-01-02"
	last, err := time.Parse(layout, lastDate)
	if err != nil {
		return 0, fmt.Errorf("parse last date %q: %w", lastDate, err)
	}
	now, err := time.Parse(layout, todayDate)
	if err != nil {
		return 0, fmt.Errorf("parse today date %q: %w", todayDate, err)
	}
	diff := now.Sub(last)
	days := int(diff.Hours() / 24)
	return days, nil
}

// ---------------------------------------------------------------------------
// Energy deduction
// ---------------------------------------------------------------------------

// DeductEnergy subtracts `cost` energy from the agent (by personID). The
// caller must ensure sufficiency first via the AgentState returned by
// RecoverEnergy (hard-block semantics: agentState.Energy >= cost).
//
// Returns ErrInsufficientEnergy if energy < cost (defensive — should not
// happen when the caller pre-checks). Never lets energy go negative.
func DeductEnergy(personID int64, cost Cost) error {
	var state model.AgentState
	if err := database.DB.Where("person_id = ?", personID).First(&state).Error; err != nil {
		return fmt.Errorf("query agent_state: %w", err)
	}
	if state.Energy < int(cost) {
		applogger.Error("energy: deduct failed, insufficient energy",
			"person_id", personID,
			"energy", state.Energy,
			"cost", cost)
		return ErrInsufficientEnergy
	}

	newEnergy := state.Energy - int(cost)
	if err := database.DB.Model(&model.AgentState{}).
		Where("person_id = ?", personID).
		Update("energy", newEnergy).Error; err != nil {
		return fmt.Errorf("update energy: %w", err)
	}
	return nil
}

// LoadStates batch-loads agent_states by person_id list.
//
// Returns a map[personID]*AgentState. PersonIDs without a row are simply
// absent from the map (callers should treat missing keys as energy=100).
// This is a read-only query — no recovery or side effects.
func LoadStates(personIDs []int64) map[int64]*model.AgentState {
	if len(personIDs) == 0 {
		return map[int64]*model.AgentState{}
	}
	var states []model.AgentState
	if err := database.DB.Where("person_id IN ?", personIDs).Find(&states).Error; err != nil {
		applogger.Error("energy: LoadStates failed", "error", err)
		return map[int64]*model.AgentState{}
	}
	result := make(map[int64]*model.AgentState, len(states))
	for i := range states {
		result[states[i].PersonID] = &states[i]
	}
	return result
}
