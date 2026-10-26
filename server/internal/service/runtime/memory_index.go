package runtime

import (
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/memory"
)

// refreshMemorySource maintains a rebuildable index after the original write
// commits. An index failure never changes whether the world action occurred.
func refreshMemorySource(kind model.MemorySourceKind, id int64) {
	if id <= 0 {
		return
	}
	if err := memory.RefreshSource(kind, id); err != nil {
		applogger.Error("memory source index refresh failed", "source_kind", kind, "source_id", id, "error", err)
	}
}
