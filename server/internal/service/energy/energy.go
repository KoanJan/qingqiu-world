// Package energy implements the agent energy budget system.
//
// It provides:
//   - Global fixed timezone management (persisted to <DATA_ROOT>/tz.txt,
//     locked on first startup, unaffected by later OS timezone changes)
//   - Daily energy recovery (lazy, multi-day catch-up, capped at MaxEnergy)
//   - Energy deduction (called by the Decide phase after actions are produced)
//
// The energy state is persisted in the agent_states table, keyed by person_id.
// All date math (today, days-between) uses the global fixed timezone.
package energy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

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
)

// ErrInsufficientEnergy is returned when energy < cost.
var ErrInsufficientEnergy = errors.New("insufficient energy")

// ---------------------------------------------------------------------------
// Global fixed timezone
// ---------------------------------------------------------------------------

var (
	globalTz *time.Location
	tzOnce   sync.Once
)

// Init loads the global fixed timezone from <dataRoot>/tz.txt.
//
// If the file does not exist, detects the local IANA timezone name and writes
// it to the file (lock-on-first-start). Subsequent calls reuse the file
// content regardless of OS timezone changes.
//
// Must be called once at application startup, before any energy operation.
// Failures are logged and fall back to UTC; they do not block startup.
func Init() error {
	var initErr error
	tzOnce.Do(func() {
		tzFile := filepath.Join(config.Get().DataRoot, "tz.txt")

		// Try reading the existing file first.
		if data, err := os.ReadFile(tzFile); err == nil {
			tzName := strings.TrimSpace(string(data))
			loc, lerr := time.LoadLocation(tzName)
			if lerr != nil {
				applogger.Error("energy.Init: failed to load timezone from file, fallback to UTC",
					"tz", tzName, "error", lerr)
				globalTz = time.UTC
			} else {
				globalTz = loc
			}
			applogger.Info("energy.Init: loaded timezone from file", "tz", globalTz.String())
			return
		} else if !os.IsNotExist(err) {
			// Unexpected read error (permission, IO). Fall back to UTC but
			// surface the error so the caller can log it.
			initErr = fmt.Errorf("read tz.txt: %w", err)
			globalTz = time.UTC
			return
		}

		// File does not exist: detect local timezone and persist it.
		tzName := detectLocalTimezone()
		loc, lerr := time.LoadLocation(tzName)
		if lerr != nil {
			applogger.Error("energy.Init: failed to load detected timezone, fallback to UTC",
				"tz", tzName, "error", lerr)
			globalTz = time.UTC
		} else {
			globalTz = loc
		}

		// Best-effort write. A write failure is logged but non-fatal: the
		// in-memory timezone is still set, and the next startup will retry.
		if werr := os.WriteFile(tzFile, []byte(tzName), 0644); werr != nil {
			applogger.Error("energy.Init: failed to write tz.txt",
				"path", tzFile, "error", werr)
		}
		applogger.Info("energy.Init: detected and saved timezone", "tz", tzName)
	})
	return initErr
}

// Timezone returns the global fixed timezone.
// Falls back to UTC if Init has not been called or failed.
func Timezone() *time.Location {
	if globalTz == nil {
		return time.UTC
	}
	return globalTz
}

// Now returns the current physical time in the global fixed timezone.
func Now() time.Time {
	return time.Now().In(Timezone())
}

// Today returns today's date string "YYYY-MM-DD" in the global fixed timezone.
func Today() string {
	return Now().Format("2006-01-02")
}

// detectLocalTimezone attempts to determine the local IANA timezone name.
//
// Strategy (in order):
//  1. TZ environment variable
//  2. Parse the /etc/localtime symlink target (macOS/Linux)
//  3. Fallback to "UTC"
func detectLocalTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		const marker = "zoneinfo/"
		if idx := strings.Index(link, marker); idx >= 0 {
			return link[idx+len(marker):]
		}
	}
	return "UTC"
}

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
