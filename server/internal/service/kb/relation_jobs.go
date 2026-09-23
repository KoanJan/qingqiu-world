package kb

import (
	"context"
	"fmt"
	"time"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

const (
	relationMaintenanceInterval = 5 * time.Minute
	relationJobBatchLimit       = 5
	relationJobMaxAttempts      = 3
)

var relationMaintenanceWakeCh = make(chan struct{}, 1)

// EnqueueFocusRelationAnalysisJob creates one idempotent background job for
// all KB calls made by one workload. The current workload producer is
// FocusedWork, hence the historical function name; Relation Discovery remains
// workload-driven, not a global KB scan.
func EnqueueFocusRelationAnalysisJob(workID int64) error {
	if workID <= 0 {
		return fmt.Errorf("work_id is required")
	}
	traceCount, err := dops.CountKBUsageTracesByWorkID(workID)
	if err != nil {
		applogger.Error("relation job: failed to count workload KB traces", "work_id", workID, "error", err)
		return err
	}
	if traceCount == 0 {
		applogger.Debug("relation job: workload has no KB traces, skipping enqueue", "work_id", workID)
		return nil
	}
	key := hashRelationText(fmt.Sprintf("relation-job|focus|%d|%s", workID, DefaultRelationPolicyVersion))
	job := model.KBRelationJob{
		JobType:        model.KBRelationJobTypeAnalyzeFocus,
		State:          model.KBRelationJobStatePending,
		SourceWorkID:   workID,
		IdempotencyKey: key,
		PayloadJSON:    "{}",
	}
	created, err := dops.EnsureKBRelationJob(&job)
	if err != nil {
		applogger.Error("relation job: failed to enqueue workload analysis job", "work_id", workID, "error", err)
		return err
	}
	applogger.Info("relation job: workload analysis job available", "work_id", workID, "job_id", job.ID, "trace_count", traceCount, "created", created, "state", job.State)
	if job.State == model.KBRelationJobStatePending {
		wakeRelationMaintenance("workload_analysis_enqueued", "work_id", workID, "job_id", job.ID)
	}
	return nil
}

// EnqueueRelationRevalidationJob creates one idempotent job for a stale
// relation. Revalidation is deterministic and never calls the LLM.
func EnqueueRelationRevalidationJob(relationID int64) error {
	if relationID <= 0 {
		return fmt.Errorf("relation_id is required")
	}
	key := hashRelationText(fmt.Sprintf("relation-job|revalidate|%d|%s", relationID, DefaultRelationPolicyVersion))
	job := model.KBRelationJob{
		JobType:        model.KBRelationJobTypeRevalidateRelation,
		State:          model.KBRelationJobStatePending,
		RelationID:     relationID,
		IdempotencyKey: key,
		PayloadJSON:    "{}",
	}
	created, err := dops.EnsureKBRelationJob(&job)
	if err != nil {
		applogger.Error("relation job: failed to enqueue revalidation job", "relation_id", relationID, "error", err)
		return err
	}
	applogger.Info("relation job: revalidation job available", "relation_id", relationID, "job_id", job.ID, "created", created, "state", job.State)
	if job.State == model.KBRelationJobStatePending {
		wakeRelationMaintenance("revalidation_enqueued", "relation_id", relationID, "job_id", job.ID)
	}
	return nil
}

// StartRelationMaintenance starts the background consumer for pending relation
// jobs. It only consumes explicit pending jobs; it never scans the entire KB.
func StartRelationMaintenance(ctx context.Context) {
	applogger.Info("KB relation maintenance started", "interval", relationMaintenanceInterval.String(), "batch_limit", relationJobBatchLimit)
	processPendingRelationJobs(ctx, relationJobBatchLimit, "startup")

	ticker := time.NewTicker(relationMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			applogger.Info("KB relation maintenance stopped")
			return
		case <-ticker.C:
			processPendingRelationJobs(ctx, relationJobBatchLimit, "ticker")
		case <-relationMaintenanceWakeCh:
			processPendingRelationJobs(ctx, relationJobBatchLimit, "wake")
		}
	}
}

// wakeRelationMaintenance nudges the background consumer to process pending
// jobs immediately. The signal is coalesced so producers never block runtime
// work if the consumer is already awake or not yet started.
func wakeRelationMaintenance(reason string, fields ...interface{}) {
	select {
	case relationMaintenanceWakeCh <- struct{}{}:
		applogger.Debug("relation job: maintenance wake signalled", append([]interface{}{"reason", reason}, fields...)...)
	default:
		applogger.Debug("relation job: maintenance wake already pending", append([]interface{}{"reason", reason}, fields...)...)
	}
}

// ProcessPendingRelationJobs processes a bounded batch of pending relation
// jobs. It is safe for heartbeat or tests to call directly.
func ProcessPendingRelationJobs(ctx context.Context, limit int) {
	processPendingRelationJobs(ctx, limit, "manual")
}

func processPendingRelationJobs(ctx context.Context, limit int, trigger string) {
	if limit <= 0 {
		limit = relationJobBatchLimit
	}
	jobs, err := dops.ListPendingKBRelationJobs(limit)
	if err != nil {
		applogger.Error("relation job: failed to load pending jobs", "trigger", trigger, "error", err)
		return
	}
	applogger.Debug("relation job: pending batch loaded", "trigger", trigger, "count", len(jobs), "limit", limit)
	if len(jobs) == 0 {
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			applogger.Info("relation job: processing stopped by context", "trigger", trigger, "job_id", job.ID)
			return
		}
		claimed, err := dops.ClaimPendingKBRelationJob(job.ID)
		if err != nil {
			applogger.Warn("relation job: failed to claim job", "trigger", trigger, "job_id", job.ID, "error", err)
			continue
		}
		if !claimed {
			applogger.Warn("relation job: job is no longer pending", "trigger", trigger, "job_id", job.ID)
			continue
		}
		applogger.Debug("relation job: claimed job", "trigger", trigger, "job_id", job.ID, "job_type", job.JobType, "source_work_id", job.SourceWorkID, "relation_id", job.RelationID)
		err = processRelationJob(ctx, job)
		if err != nil {
			recordRelationJobFailure(job, err)
			continue
		}
		completeRelationJob(job.ID)
		applogger.Debug("relation job: completed job", "trigger", trigger, "job_id", job.ID, "job_type", job.JobType)
	}
}

// processRelationJob dispatches one claimed job to the deterministic
// revalidation path or the LLM-backed candidate extraction path.
func processRelationJob(ctx context.Context, job model.KBRelationJob) error {
	switch job.JobType {
	case model.KBRelationJobTypeAnalyzeFocus:
		llmConfig := dops.GetSystemLLMConfig()
		if llmConfig == nil {
			return fmt.Errorf("system LLM config unavailable")
		}
		return AnalyzeKBUsageFocus(ctx, job.SourceWorkID, llmConfig)
	case model.KBRelationJobTypeRevalidateRelation:
		return RevalidateStaleRelation(job.RelationID)
	default:
		return fmt.Errorf("unknown relation job type %d", job.JobType)
	}
}

// recordRelationJobFailure stores the failure reason and either retries later
// or marks the job failed after the bounded attempt limit.
func recordRelationJobFailure(job model.KBRelationJob, err error) {
	nextState := model.KBRelationJobStatePending
	if job.Attempts+1 >= relationJobMaxAttempts {
		nextState = model.KBRelationJobStateFailed
	}
	if updateErr := dops.UpdateKBRelationJobFailure(job.ID, nextState, err.Error()); updateErr != nil {
		applogger.Error("relation job: failed to record failure", "job_id", job.ID, "error", updateErr)
	}
	applogger.Warn("relation job: processing failed", "job_id", job.ID, "job_type", job.JobType, "next_state", nextState, "error", err)
}

// completeRelationJob marks a successfully processed job completed and clears
// any previous transient error text.
func completeRelationJob(jobID int64) {
	if err := dops.CompleteKBRelationJob(jobID); err != nil {
		applogger.Error("relation job: failed to mark completed", "job_id", jobID, "error", err)
	}
}
