package memory

import (
	"context"
	"fmt"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/vectorutils"
)

const (
	// vectorBackfillInterval spaces repair passes so a legacy vector backlog
	// cannot monopolize the embedding provider at startup.
	vectorBackfillInterval = 5 * time.Minute
	// vectorBackfillBatch bounds one repair pass. Rows that keep failing are
	// logged and retried on a later pass rather than blocking newer rows.
	vectorBackfillBatch = 100
)

// vectorBackfillKind identifies which memory table owns a sampled row.
type vectorBackfillKind int

const (
	vectorBackfillDaily     vectorBackfillKind = 1
	vectorBackfillCognitive vectorBackfillKind = 2
)

// runVectorBackfill repairs rows written before empty vectors were rejected.
// Daily routing ranks stored vectors only, so a missing one silently removes
// evidence unless it is regenerated and reported.
func runVectorBackfill(ctx context.Context) {
	ticker := time.NewTicker(vectorBackfillInterval)
	defer ticker.Stop()
	for {
		if err := backfillMemoryVectors(ctx, vectorBackfillBatch); err != nil && ctx.Err() == nil {
			applogger.Error("memory vector backfill pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// backfillMemoryVectors samples both kinds of missing vectors in one queue.
// A failed row remains eligible, but cannot hold a fixed position ahead of
// other rows on every pass. Only lookup failures abort the pass.
func backfillMemoryVectors(ctx context.Context, budget int) error {
	if embeddingSvc == nil {
		return fmt.Errorf("memory vector backfill requires an embedding service")
	}
	if budget <= 0 {
		return nil
	}
	var items []struct {
		Kind vectorBackfillKind
		ID   int64
	}
	// SQLite randomizes the combined set before limiting it, so a backlog in
	// either table cannot permanently consume every slot in the pass.
	if err := database.DB.WithContext(ctx).Raw(`
		SELECT kind, id FROM (
			SELECT ? AS kind, id FROM daily_memories WHERE length(embedding) = 0
			UNION ALL
			SELECT ? AS kind, id FROM cognitive_memories
			WHERE length(embedding) = 0 OR length(impression_embedding) = 0
		) ORDER BY RANDOM() LIMIT ?`, vectorBackfillDaily, vectorBackfillCognitive, budget).Scan(&items).Error; err != nil {
		return err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch item.Kind {
		case vectorBackfillDaily:
			backfillDailyVector(ctx, item.ID)
		case vectorBackfillCognitive:
			backfillCognitiveVector(ctx, item.ID)
		default:
			applogger.Error("memory vector backfill selected an unknown kind", "kind", item.Kind, "memory_id", item.ID)
		}
	}
	return nil
}

// backfillDailyVector regenerates one dated account's summary vector without
// changing the time at which its summary was formed.
func backfillDailyVector(ctx context.Context, memoryID int64) {
	var row model.DailyMemory
	if err := database.DB.WithContext(ctx).Where("id = ?", memoryID).Take(&row).Error; err != nil {
		applogger.Error("daily memory vector backfill read failed", "memory_id", memoryID, "error", err)
		return
	}
	vector, err := embeddingSvc.EmbedSingle(ctx, row.Summary)
	if err != nil {
		applogger.Error("daily memory vector backfill failed", "memory_id", row.ID, "error", err)
		return
	}
	write := database.DB.WithContext(ctx).Model(&model.DailyMemory{}).Where("id = ?", row.ID).
		UpdateColumn("embedding", vectorutils.Float32SliceToBlob(vector))
	if write.Error != nil || write.RowsAffected != 1 {
		applogger.Error("daily memory vector backfill write failed", "memory_id", row.ID, "error", write.Error, "rows_affected", write.RowsAffected)
		return
	}
	applogger.Info("daily memory vector backfilled", "memory_id", row.ID, "person_id", row.PersonID, "local_date", row.LocalDate)
}

// backfillCognitiveVector regenerates description and impression vectors for
// cognitive memories. The impression text lives on the latest version, so a
// successful repair must not change the time of the subject's last review.
func backfillCognitiveVector(ctx context.Context, memoryID int64) {
	var row model.CognitiveMemory
	if err := database.DB.WithContext(ctx).Where("id = ?", memoryID).Take(&row).Error; err != nil {
		applogger.Error("cognitive memory vector backfill read failed", "memory_id", memoryID, "error", err)
		return
	}
	updates := map[string]interface{}{}
	if len(row.Embedding) == 0 {
		vector, err := embeddingSvc.EmbedSingle(ctx, row.Description)
		if err != nil {
			applogger.Error("cognitive description vector backfill failed", "memory_id", row.ID, "error", err)
		} else {
			updates["embedding"] = vectorutils.Float32SliceToBlob(vector)
		}
	}
	if len(row.ImpressionEmbedding) == 0 {
		var latest model.CognitiveMemoryVersion
		if err := database.DB.WithContext(ctx).Where("memory_id = ?", row.ID).Order("revision DESC").Take(&latest).Error; err != nil {
			applogger.Error("cognitive impression backfill could not read the latest version", "memory_id", row.ID, "error", err)
		} else {
			vector, embedErr := embeddingSvc.EmbedSingle(ctx, latest.Impression)
			if embedErr != nil {
				applogger.Error("cognitive impression vector backfill failed", "memory_id", row.ID, "error", embedErr)
			} else {
				updates["impression_embedding"] = vectorutils.Float32SliceToBlob(vector)
			}
		}
	}
	if len(updates) == 0 {
		return
	}
	write := database.DB.WithContext(ctx).Model(&model.CognitiveMemory{}).Where("id = ?", row.ID).UpdateColumns(updates)
	if write.Error != nil || write.RowsAffected != 1 {
		applogger.Error("cognitive memory vector backfill write failed", "memory_id", row.ID, "error", write.Error, "rows_affected", write.RowsAffected)
		return
	}
	applogger.Info("cognitive memory vector backfilled", "memory_id", row.ID, "person_id", row.PersonID,
		"description", updates["embedding"] != nil, "impression", updates["impression_embedding"] != nil)
}
