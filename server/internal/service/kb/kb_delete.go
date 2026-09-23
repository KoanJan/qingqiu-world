package kb

import (
	"fmt"
	"os"
	"path/filepath"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
)

// DeleteKnowledgeBase deletes a knowledge base and all KB-owned data. Database
// rows are removed by dops in one transaction; service owns in-memory index and
// filesystem cleanup because those resources are outside DB atomicity.
func DeleteKnowledgeBase(kbID int64) error {
	releaseindexManager(kbID)
	releaseBM25Index(kbID)
	applogger.Debug("KB delete: released in-memory indexes", "kb_id", kbID)

	summary, err := dops.DeleteKnowledgeBaseRows(kbID)
	if err != nil {
		applogger.Error("failed to delete KB database rows", "kb_id", kbID, "error", err)
		return err
	}

	kbDir := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID))
	if err := os.RemoveAll(kbDir); err != nil {
		applogger.Error("failed to remove KB storage directory", "kb_id", kbID, "path", kbDir, "error", err)
		return fmt.Errorf("remove KB storage directory: %w", err)
	}

	applogger.Info("Deleted knowledge base and cascaded KB-owned data",
		"kb_id", kbID,
		"documents", summary.Documents,
		"revisions", summary.Revisions,
		"content_nodes", summary.ContentNodes,
		"chunk_nodes", summary.ChunkNodes,
		"chunks", summary.Chunks,
		"relation_jobs", summary.RelationJobs,
		"relations", summary.Relations,
		"relation_evidence", summary.RelationEvidence,
		"entities", summary.Entities,
		"usage_traces", summary.UsageTraces,
		"access_grants", summary.AccessGrants,
	)

	return nil
}
