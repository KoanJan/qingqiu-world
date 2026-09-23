package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// EnsureKBRelationJob creates an idempotent relation-maintenance job and
// returns whether this call inserted a new row. Scheduling decisions remain in
// the kb service; this function only owns the persistence boundary.
func EnsureKBRelationJob(job *model.KBRelationJob) (bool, error) {
	if job == nil {
		return false, fmt.Errorf("kb relation job is required")
	}
	result := database.DB.Where("idempotency_key = ?", job.IdempotencyKey).Attrs(*job).FirstOrCreate(job)
	if result.Error != nil {
		return false, fmt.Errorf("ensure kb relation job: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

// ListPendingKBRelationJobs loads a bounded pending batch in FIFO order.
func ListPendingKBRelationJobs(limit int) ([]model.KBRelationJob, error) {
	var jobs []model.KBRelationJob
	if err := database.DB.Where("state = ?", model.KBRelationJobStatePending).
		Order("id ASC").Limit(limit).Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("list pending kb relation jobs: %w", err)
	}
	return jobs, nil
}

// ClaimPendingKBRelationJob atomically moves one pending job to running and
// increments its attempt counter. The boolean is false when another worker has
// already claimed or changed the job.
func ClaimPendingKBRelationJob(jobID int64) (bool, error) {
	result := database.DB.Model(&model.KBRelationJob{}).
		Where("id = ? AND state = ?", jobID, model.KBRelationJobStatePending).
		Updates(map[string]interface{}{
			"state":    model.KBRelationJobStateRunning,
			"attempts": gorm.Expr("attempts + ?", 1),
		})
	if result.Error != nil {
		return false, fmt.Errorf("claim pending kb relation job: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

// UpdateKBRelationJobFailure records a failed job attempt and the scheduler's
// chosen next state. Retry policy stays outside dops.
func UpdateKBRelationJobFailure(jobID int64, nextState model.KBRelationJobState, lastError string) error {
	if err := database.DB.Model(&model.KBRelationJob{}).
		Where("id = ?", jobID).
		Updates(map[string]interface{}{
			"state":      nextState,
			"last_error": lastError,
		}).Error; err != nil {
		return fmt.Errorf("update kb relation job failure: %w", err)
	}
	return nil
}

// CompleteKBRelationJob marks a successfully processed job completed and
// clears any previous transient error text.
func CompleteKBRelationJob(jobID int64) error {
	if err := database.DB.Model(&model.KBRelationJob{}).
		Where("id = ?", jobID).
		Updates(map[string]interface{}{
			"state":      model.KBRelationJobStateCompleted,
			"last_error": "",
		}).Error; err != nil {
		return fmt.Errorf("complete kb relation job: %w", err)
	}
	return nil
}
