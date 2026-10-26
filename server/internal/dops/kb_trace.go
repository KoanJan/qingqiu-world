package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreateKBUsageTrace persists one workload-scoped KB tool usage record.
// The caller owns the semantic payload; dops only owns the atomic insert.
func CreateKBUsageTrace(trace *model.KBUsageTrace) error {
	if trace == nil {
		return fmt.Errorf("kb usage trace is required")
	}
	if err := database.DB.Create(trace).Error; err != nil {
		return fmt.Errorf("create kb usage trace: %w", err)
	}
	return nil
}

// CountKBUsageTracesByWorkID returns how many KB calls were recorded for one
// workload. It is used to avoid enqueueing empty relation-analysis jobs.
func CountKBUsageTracesByWorkID(workID int64) (int64, error) {
	var count int64
	if err := database.DB.Model(&model.KBUsageTrace{}).Where("work_id = ?", workID).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count kb usage traces by work id: %w", err)
	}
	return count, nil
}

// ListKBUsageTracesByWorkID loads workload-scoped KB calls in execution order.
func ListKBUsageTracesByWorkID(workID int64) ([]model.KBUsageTrace, error) {
	var traces []model.KBUsageTrace
	if err := database.DB.Where("work_id = ?", workID).Order("id ASC").Find(&traces).Error; err != nil {
		return nil, fmt.Errorf("list kb usage traces by work id: %w", err)
	}
	return traces, nil
}
