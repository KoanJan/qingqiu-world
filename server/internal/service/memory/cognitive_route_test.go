package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/vectorutils"
)

// routeTestMemory creates an existing memory with a current version.
func routeTestMemory(t *testing.T, personID int64, description string, vector []float32, updatedAt time.Time) model.CognitiveMemory {
	t.Helper()
	memory := model.CognitiveMemory{PersonID: personID, Description: description, Embedding: vectorutils.Float32SliceToBlob(vector),
		ImpressionEmbedding: vectorutils.Float32SliceToBlob(vector), CreatedAt: updatedAt, UpdatedAt: updatedAt}
	if err := database.DB.Create(&memory).Error; err != nil {
		t.Fatal(err)
	}
	// GORM's autoUpdateTime may override the supplied time, so pin it after
	// insert to make the routing watermark deterministic.
	if err := database.DB.Model(&model.CognitiveMemory{}).Where("id = ?", memory.ID).UpdateColumn("updated_at", updatedAt).Error; err != nil {
		t.Fatal(err)
	}
	version := model.CognitiveMemoryVersion{MemoryID: memory.ID, Revision: 1, Impression: "Current impression",
		Understanding: "Current understanding", ProcessID: memory.ID + 1000}
	if err := database.DB.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	return memory
}

// routeTestDaily creates one completed-day account generated at createdAt.
// Shard and group identity must stay unique per owner and date.
func routeTestDaily(t *testing.T, personID int64, shard int, vector []float32, createdAt time.Time) model.DailyMemory {
	t.Helper()
	row := model.DailyMemory{PersonID: personID, LocalDate: energy.Now().AddDate(0, 0, -1).Format("2006-01-02"),
		Shard: shard, GroupType: 1, GroupID: int64(shard + 1), Summary: "A dated account", StartAt: createdAt, EndAt: createdAt,
		Embedding: vectorutils.Float32SliceToBlob(vector), CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&model.DailyMemory{}).Where("id = ?", row.ID).UpdateColumn("created_at", createdAt).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// TestCognitiveRouteUsesMaximumSimilarity proves the router ranks by the
// strongest single dated account. Memory A has one perfect and one orthogonal
// account (average 0.5); memory B has two moderately aligned accounts (average
// 0.707). A must win because its single strong account is the evidence that an
// average would have starved.
func TestCognitiveRouteUsesMaximumSimilarity(t *testing.T) {
	recallTestDB(t)
	ctx := context.Background()
	now := energy.Now()
	dayStart := now.Add(-12 * time.Hour)
	updatedAt := now.Add(-48 * time.Hour)
	strong := routeTestMemory(t, 7, "A recurring thread", []float32{1, 0}, updatedAt)
	moderate := routeTestMemory(t, 7, "A steady thread", []float32{0.7, 0.7}, updatedAt)
	routeTestDaily(t, 7, 0, []float32{0, 1}, now.Add(-2*time.Hour))
	routeTestDaily(t, 7, 1, []float32{1, 0}, now.Add(-time.Hour))
	routeTestDaily(t, 7, 2, []float32{1, 0}, now.Add(-30*time.Minute))
	candidates, _, err := SelectCognitiveUpdateCandidates(ctx, 7, dayStart, 5, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%+v", candidates)
	}
	if candidates[0].MemoryID != strong.ID || candidates[1].MemoryID != moderate.ID {
		t.Fatalf("order is not by maximum similarity: %+v", candidates)
	}
	if candidates[0].Similarity < 0.99 || candidates[1].Similarity < 0.69 || candidates[1].Similarity > 0.72 {
		t.Fatalf("similarities: %+v", candidates)
	}
	if candidates[0].Revision != 1 || candidates[0].Impression != "Current impression" {
		t.Fatalf("current version was not attached: %+v", candidates[0])
	}
	if len(candidates[0].Evidence) != cognitiveRouteEvidenceCap {
		t.Fatalf("evidence cap not applied: %d", len(candidates[0].Evidence))
	}
}

// TestCognitiveRouteThresholdAndQuotaExclusions covers the minimum similarity,
// today's spent review slots, and the per-memory evidence watermark.
func TestCognitiveRouteThresholdAndQuotaExclusions(t *testing.T) {
	recallTestDB(t)
	ctx := context.Background()
	now := energy.Now()
	dayStart := now.Add(-12 * time.Hour)
	updatedAt := now.Add(-48 * time.Hour)
	aligned := routeTestMemory(t, 7, "Aligned", []float32{1, 0}, updatedAt)
	partial := routeTestMemory(t, 7, "Partial", []float32{0.7, 0.7}, updatedAt)
	reviewedToday := routeTestMemory(t, 7, "Reviewed today", []float32{1, 0}, updatedAt)
	attemptedToday := routeTestMemory(t, 7, "Attempted today", []float32{1, 0}, updatedAt)
	staleWatermark := routeTestMemory(t, 7, "Stale watermark", []float32{1, 0}, updatedAt)
	routeTestDaily(t, 7, 0, []float32{1, 0}, now.Add(-time.Hour))
	// A review that finished today spends the memory's slot even when its
	// updated_at would otherwise look old.
	if err := database.DB.Model(&model.CognitiveMemory{}).Where("id = ?", reviewedToday.ID).UpdateColumn("updated_at", now).Error; err != nil {
		t.Fatal(err)
	}
	// An ended process without an outcome still spends today's attempt.
	if err := database.DB.Create(&model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessEnded,
		Kind: model.CognitiveProcessKindUpdate, MemoryID: attemptedToday.ID}).Error; err != nil {
		t.Fatal(err)
	}
	// This account predates the stale memory's own review, so it is not
	// incremental evidence for that memory.
	if err := database.DB.Model(&model.CognitiveMemory{}).Where("id = ?", staleWatermark.ID).UpdateColumn("updated_at", now.Add(-30*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	// A high threshold keeps only the perfect match.
	candidates, _, err := SelectCognitiveUpdateCandidates(ctx, 7, dayStart, 5, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].MemoryID != aligned.ID {
		t.Fatalf("threshold did not filter: %+v", candidates)
	}
	candidates, _, err = SelectCognitiveUpdateCandidates(ctx, 7, dayStart, 5, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]bool, len(candidates))
	for _, candidate := range candidates {
		seen[candidate.MemoryID] = true
	}
	if seen[reviewedToday.ID] {
		t.Fatal("a memory reviewed today was routed again")
	}
	if seen[attemptedToday.ID] {
		t.Fatal("a memory already attempted today was routed again")
	}
	if seen[staleWatermark.ID] {
		t.Fatal("an account older than the review watermark was used")
	}
	if !seen[partial.ID] {
		t.Fatal("an eligible partial match was dropped")
	}
}

// TestCognitiveRouteUsesCompletedProcessWatermark covers a SQLite aggregate
// timestamp: a completed review excludes older dated accounts but retains
// accounts generated after that review.
func TestCognitiveRouteUsesCompletedProcessWatermark(t *testing.T) {
	recallTestDB(t)
	ctx := context.Background()
	now := energy.Now()
	dayStart := now.Add(-time.Hour)
	memory := routeTestMemory(t, 7, "A recurring concern", []float32{1, 0}, now.Add(-72*time.Hour))
	routeTestDaily(t, 7, 0, []float32{1, 0}, now.Add(-48*time.Hour))
	process := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessEnded,
		Kind: model.CognitiveProcessKindUpdate, Outcome: model.CognitiveProcessOutcomeConfirmed,
		MemoryID: memory.ID, UpdatedAt: now.Add(-24 * time.Hour)}
	if err := database.DB.Create(&process).Error; err != nil {
		t.Fatal(err)
	}
	newer := routeTestDaily(t, 7, 1, []float32{1, 0}, now.Add(-12*time.Hour))
	candidates, _, err := SelectCognitiveUpdateCandidates(ctx, 7, dayStart, 5, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].MemoryID != memory.ID || len(candidates[0].Evidence) != 1 || candidates[0].Evidence[0].ID != newer.ID {
		t.Fatalf("completed review watermark was ignored: %+v", candidates)
	}
}

// TestCognitiveRouteReportsMissingVectors keeps a vector gap visible instead of
// silently dropping the date from ranking.
func TestCognitiveRouteReportsMissingVectors(t *testing.T) {
	recallTestDB(t)
	ctx := context.Background()
	now := energy.Now()
	dayStart := now.Add(-12 * time.Hour)
	updatedAt := now.Add(-48 * time.Hour)
	memory := routeTestMemory(t, 7, "Needs evidence", []float32{1, 0}, updatedAt)
	routeTestDaily(t, 7, 0, []float32{1, 0}, now.Add(-time.Hour))
	empty := model.DailyMemory{PersonID: 7, LocalDate: energy.Now().AddDate(0, 0, -1).Format("2006-01-02"),
		Shard: 1, GroupType: 1, GroupID: 2, Summary: "Missing vector", StartAt: now, EndAt: now,
		Embedding: []byte{}, CreatedAt: now, UpdatedAt: now}
	if err := database.DB.Create(&empty).Error; err != nil {
		t.Fatal(err)
	}
	candidates, coverage, err := SelectCognitiveUpdateCandidates(ctx, 7, dayStart, 5, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].MemoryID != memory.ID {
		t.Fatalf("candidates=%+v", candidates)
	}
	if !strings.Contains(coverage, "lack a summary vector") {
		t.Fatalf("coverage did not report the gap: %s", coverage)
	}
}
