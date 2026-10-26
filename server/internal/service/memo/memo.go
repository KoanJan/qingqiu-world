// Package memo owns the state and owner-scoped queries for private memos.
// Runtime supplies the accepted Action and commits its ActionEffect together
// with an Apply change; Focus and Private Space have no direct write path.
package memo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"

	"gorm.io/gorm"
)

// MaxContentBytes bounds one memo's complete UTF-8 text, not its preview.
const MaxContentBytes = 8 * 1024

// ValidatePlan checks the fields needed for the chosen mutation before an
// Action is accepted. Apply validates again at the persistence boundary.
func ValidatePlan(plan *action.MemoPlan) error {
	if plan == nil {
		return errors.New("memo plan is missing")
	}
	switch plan.Operation {
	case model.MemoOperationCreate:
		if plan.MemoID != 0 || plan.Revision != 0 {
			return errors.New("creating a memo cannot name an existing memo or revision")
		}
		return validateContent(plan.Content)
	case model.MemoOperationRevise:
		if plan.MemoID <= 0 || plan.Revision <= 0 {
			return errors.New("revising a memo needs its ID and current revision")
		}
		if plan.Pinned != nil {
			return errors.New("revise cannot also change pinned state")
		}
		return validateContent(plan.Content)
	case model.MemoOperationSetPinned, model.MemoOperationArchive, model.MemoOperationRestore:
		if plan.MemoID <= 0 || plan.Revision <= 0 {
			return errors.New("changing a memo needs its ID and current revision")
		}
		if plan.Content != "" || (plan.Operation != model.MemoOperationSetPinned && plan.Pinned != nil) {
			return errors.New("memo operation contains unrelated content or pinned state")
		}
		if plan.Operation == model.MemoOperationSetPinned && plan.Pinned == nil {
			return errors.New("set pinned requires an explicit boolean")
		}
		return nil
	default:
		return fmt.Errorf("unknown memo operation %d", plan.Operation)
	}
}

func validateContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("memo content is empty")
	}
	if len(content) > MaxContentBytes {
		return fmt.Errorf("memo content exceeds %d UTF-8 bytes", MaxContentBytes)
	}
	return nil
}

// Apply changes current memo state inside the caller's transaction. The caller
// must record the resulting ActionEffect and end the Action before committing.
// A no-op returns changed=false, so it must not be recorded as a new effect.
func Apply(tx *gorm.DB, personID int64, plan *action.MemoPlan) (model.Memo, bool, error) {
	if tx == nil || personID <= 0 {
		return model.Memo{}, false, errors.New("memo write has no transaction or owner")
	}
	if err := ValidatePlan(plan); err != nil {
		return model.Memo{}, false, err
	}
	if plan.Operation == model.MemoOperationCreate {
		record := model.Memo{PersonID: personID, Content: plan.Content, Status: model.MemoStatusActive, Pinned: plan.Pinned != nil && *plan.Pinned, Revision: 1}
		if err := tx.Create(&record).Error; err != nil {
			return model.Memo{}, false, fmt.Errorf("create memo: %w", err)
		}
		return record, true, nil
	}

	var current model.Memo
	if err := tx.Where("id = ? AND person_id = ?", plan.MemoID, personID).Take(&current).Error; err != nil {
		return model.Memo{}, false, fmt.Errorf("read owned memo %d: %w", plan.MemoID, err)
	}
	if current.Revision != plan.Revision {
		return model.Memo{}, false, fmt.Errorf("memo revision conflict: expected %d, current %d", plan.Revision, current.Revision)
	}
	if current.Status != model.MemoStatusActive && current.Status != model.MemoStatusArchived {
		return model.Memo{}, false, fmt.Errorf("memo %d has invalid status %d", current.ID, current.Status)
	}

	updates := map[string]any{}
	switch plan.Operation {
	case model.MemoOperationRevise:
		if current.Content != plan.Content {
			updates["content"] = plan.Content
		}
	case model.MemoOperationSetPinned:
		if current.Status != model.MemoStatusActive && *plan.Pinned {
			return model.Memo{}, false, errors.New("archived memo cannot be pinned")
		}
		if current.Pinned != *plan.Pinned {
			updates["pinned"] = *plan.Pinned
		}
	case model.MemoOperationArchive:
		if current.Status != model.MemoStatusArchived || current.Pinned {
			updates["status"] = model.MemoStatusArchived
			updates["pinned"] = false
		}
	case model.MemoOperationRestore:
		if current.Status == model.MemoStatusArchived {
			updates["status"] = model.MemoStatusActive
			updates["pinned"] = false
		}
	}
	if len(updates) == 0 {
		return current, false, nil
	}
	updates["revision"] = gorm.Expr("revision + 1")
	updates["updated_at"] = time.Now()
	result := tx.Model(&model.Memo{}).Where("id = ? AND person_id = ? AND revision = ?", current.ID, personID, plan.Revision).Updates(updates)
	if result.Error != nil {
		return model.Memo{}, false, fmt.Errorf("update memo %d: %w", current.ID, result.Error)
	}
	if result.RowsAffected != 1 {
		var latest model.Memo
		if err := tx.Select("revision").Where("id = ? AND person_id = ?", current.ID, personID).Take(&latest).Error; err == nil {
			return model.Memo{}, false, fmt.Errorf("memo revision conflict: expected %d, current %d", plan.Revision, latest.Revision)
		}
		return model.Memo{}, false, fmt.Errorf("memo %d changed or became unavailable before update", current.ID)
	}
	if err := tx.Where("id = ? AND person_id = ?", current.ID, personID).Take(&current).Error; err != nil {
		return model.Memo{}, false, fmt.Errorf("read changed memo %d: %w", plan.MemoID, err)
	}
	return current, true, nil
}

// Read returns the owner's complete current memo, including archived entries.
func Read(personID, memoID int64) (model.Memo, error) {
	if personID <= 0 || memoID <= 0 {
		return model.Memo{}, errors.New("memo read needs a valid owner and ID")
	}
	var record model.Memo
	if err := database.DB.Where("id = ? AND person_id = ?", memoID, personID).Take(&record).Error; err != nil {
		return model.Memo{}, fmt.Errorf("read owned memo %d: %w", memoID, err)
	}
	if record.Status != model.MemoStatusActive && record.Status != model.MemoStatusArchived {
		return model.Memo{}, fmt.Errorf("memo %d has invalid status %d", memoID, record.Status)
	}
	return record, nil
}

// List browses one owner's current resources in a deterministic order. Status zero
// selects both states; callers bound page and limit before invoking it.
func List(personID int64, status model.MemoStatus, page, limit int) ([]model.Memo, bool, error) {
	if personID <= 0 || page < 1 || page > 10000 || limit < 1 || limit > 20 ||
		(status != 0 && status != model.MemoStatusActive && status != model.MemoStatusArchived) {
		return nil, false, errors.New("invalid memo list range")
	}
	query := database.DB.Where("person_id = ?", personID)
	if status != 0 {
		query = query.Where("status = ?", status)
	}
	var records []model.Memo
	if err := query.Order("updated_at DESC, id DESC").Offset((page - 1) * limit).Limit(limit + 1).Find(&records).Error; err != nil {
		return nil, false, fmt.Errorf("list owned memos: %w", err)
	}
	more := len(records) > limit
	if more {
		records = records[:limit]
	}
	for _, record := range records {
		if record.Status != model.MemoStatusActive && record.Status != model.MemoStatusArchived {
			return nil, false, fmt.Errorf("memo %d has invalid status %d", record.ID, record.Status)
		}
	}
	return records, more, nil
}

// PinnedOverview reads only a few active, self-selected memos for Subject.
func PinnedOverview(personID int64, limit int) ([]model.Memo, error) {
	if personID <= 0 || limit < 1 || limit > 10 {
		return nil, errors.New("invalid memo overview range")
	}
	var records []model.Memo
	if err := database.DB.Where("person_id = ? AND status = ? AND pinned = ?", personID, model.MemoStatusActive, true).
		Order("updated_at DESC, id DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("load pinned memos: %w", err)
	}
	return records, nil
}
