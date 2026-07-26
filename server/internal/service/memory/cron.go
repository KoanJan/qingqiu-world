package memory

import (
	"context"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

// runDailyCron executes the daily maintenance loop. Runs immediately on
// start, then every 24 hours. Exits when ctx is cancelled.
//
// Maintenance: all observations undergo daily multiplicative importance decay
// (importance *= decayFactor). Observations with importance close to or at
// zero are skipped to avoid unnecessary writes.
func runDailyCron(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// Run immediately on startup
	runDailyMaintenance()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runDailyMaintenance()
		}
	}
}

// runDailyMaintenance applies daily importance decay to all active observations.
//
// Uses batch SQL updates for efficiency — a single multiplication per batch
// rather than row-by-row loading and saving.
//
// Returns counts for observability:
//   - decayed: number of observations that had decay applied
//   - errors: number of batch errors encountered
func runDailyMaintenance() (decayed, errors int) {
	applogger.Info("Starting daily memory maintenance (importance decay)")

	// Apply decay in batches to all observations with importance above a
	// practical floor. Observations at ≤ 1e-6 are effectively zero and
	// are skipped to limit unbounded accumulation of near-zero rows.
	batchSize := 2000
	offset := 0

	for {
		var ids []int64
		if err := database.DB.Model(&model.AgentObservation{}).
			Where("importance > 1e-6").
			Select("id").
			Limit(batchSize).
			Offset(offset).
			Pluck("id", &ids).Error; err != nil {
			applogger.Error("Failed to load observation IDs for decay", "error", err)
			errors++
			break
		}

		if len(ids) == 0 {
			break
		}

		result := database.DB.Model(&model.AgentObservation{}).
			Where("id IN ?", ids).
			UpdateColumn("importance", gorm.Expr("importance * ?", decayFactor))

		if result.Error != nil {
			applogger.Error("Failed to apply decay batch",
				"offset", offset,
				"error", result.Error,
			)
			errors++
		} else {
			decayed += int(result.RowsAffected)
		}

		offset += batchSize
	}

	applogger.Info("Daily memory maintenance completed",
		"decayed", decayed,
		"errors", errors,
	)
	return
}
