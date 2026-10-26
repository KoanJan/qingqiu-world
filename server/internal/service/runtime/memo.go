package runtime

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/memo"

	"gorm.io/gorm"
)

// handleManageMemo commits a memo mutation, its causal ActionEffect, and the
// Action's terminal state atomically. A failed or no-op mutation has no effect.
func (r *agentRuntime) handleManageMemo(act action.Action) {
	if act.MemoPlan == nil {
		applogger.Error("manage_memo action has no memo plan", "action_id", act.ID)
		endActionLogged(act.ID, "manage_memo")
		return
	}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := dops.RequireActivePersonTx(tx, r.agentPersonID); err != nil {
			return err
		}
		var stored model.Action
		if err := tx.Table("actions AS a").Select("a.*").
			Joins("JOIN decisions AS d ON d.id = a.decision_id").
			Where("a.id = ? AND a.type = ? AND a.status = ? AND d.person_id = ?", act.ID, model.ActionTypeManageMemo, model.ActionStatusInProgress, r.agentPersonID).
			Take(&stored).Error; err != nil {
			return fmt.Errorf("manage_memo has no owned running Action %d: %w", act.ID, err)
		}
		record, changed, err := memo.Apply(tx, r.agentPersonID, act.MemoPlan)
		if err != nil {
			return err
		}
		if changed {
			if err := recordActionEffect(tx, act.ID, model.ActionEffectMemo, record.ID); err != nil {
				return fmt.Errorf("record memo effect: %w", err)
			}
		}
		result := tx.Model(&model.Action{}).
			Where("id = ? AND status = ?", act.ID, model.ActionStatusInProgress).
			Update("status", model.ActionStatusEnded)
		if result.Error != nil {
			return fmt.Errorf("end memo action: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("memo action %d ended before its result committed", act.ID)
		}
		return nil
	})
	if err != nil {
		applogger.Error("manage_memo failed", "person_id", r.agentPersonID, "action_id", act.ID, "error", err)
		endActionLogged(act.ID, "manage_memo")
	}
}
