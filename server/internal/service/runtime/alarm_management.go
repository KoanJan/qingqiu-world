package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"

	"gorm.io/gorm"
)

const (
	pendingAlarmDefaultPageSize = 5
	pendingAlarmMaxPageSize     = 20
)

// pendingAlarmListArgs belongs to a current resource read, not historical
// recall. Only pending alarms are exposed, so cancelled ones disappear here.
type pendingAlarmListArgs struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

type pendingAlarmView struct {
	AlarmID               int64     `json:"alarm_id"`
	TriggerAt             time.Time `json:"trigger_at"`
	Message               string    `json:"message"`
	Handling              string    `json:"handling"`
	ActionContent         string    `json:"action_content,omitempty"`
	ExpressionInstruction string    `json:"expression_instruction,omitempty"`
	SessionID             int64     `json:"session_id,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// executePendingAlarmList reads only the caller's still-pending alarms. It
// never infers equivalence between alarm messages or changes their state.
func executePendingAlarmList(personID int64, arguments string) (string, error) {
	if personID <= 0 {
		return "", errors.New("pending alarms require a positive owner")
	}
	var args pendingAlarmListArgs
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", fmt.Errorf("invalid pending-alarm arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("pending-alarm arguments contain trailing JSON")
	}
	if args.Page < 0 || args.PageSize < 0 || args.PageSize > pendingAlarmMaxPageSize {
		return "", fmt.Errorf("pending-alarm page must be nonnegative and page_size at most %d", pendingAlarmMaxPageSize)
	}
	page, pageSize := args.Page, args.PageSize
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = pendingAlarmDefaultPageSize
	}
	if page > int(^uint(0)>>1)/pageSize {
		return "", errors.New("pending-alarm page is too large")
	}
	var rows []model.ScheduledEvent
	if err := database.DB.Where("person_id = ? AND status = ?", personID, model.ScheduledEventStatusPending).
		Order("trigger_at, id").Offset((page - 1) * pageSize).Limit(pageSize + 1).Find(&rows).Error; err != nil {
		return "", err
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]pendingAlarmView, 0, len(rows))
	for _, row := range rows {
		handling := "When this alarm fires, you will decide what to do from its note."
		if row.Action == model.ScheduledEventActionSendMessage {
			handling = "When this alarm fires, the prepared message will be sent directly."
		}
		items = append(items, pendingAlarmView{AlarmID: row.ID, TriggerAt: row.TriggerAt.In(energy.Timezone()),
			Message: row.Message, Handling: handling, ActionContent: row.ActionContent,
			ExpressionInstruction: row.ExpressionInstruction, SessionID: row.SessionID,
			CreatedAt: row.CreatedAt.In(energy.Timezone())})
	}
	returnJSON := struct {
		Items   []pendingAlarmView `json:"items"`
		Page    int                `json:"page"`
		HasMore bool               `json:"has_more"`
	}{Items: items, Page: page, HasMore: hasMore}
	encoded, err := json.Marshal(returnJSON)
	return string(encoded), err
}

// cancelScheduledAlarm commits a cancellation and its causal ActionEffect in
// one transaction. The conditional pending update decides a race with firing;
// a losing cancellation records no effect. Only after commit is the local
// timer cancelled, because the database state controls actual delivery.
func cancelScheduledAlarm(personID, actionID, alarmID int64) error {
	if personID <= 0 || actionID <= 0 || alarmID <= 0 {
		return errors.New("cancelling an alarm requires positive owner, action, and alarm IDs")
	}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := dops.RequireActivePersonTx(tx, personID); err != nil {
			return err
		}
		var source model.Action
		if err := tx.Table("actions AS a").Select("a.*").
			Joins("JOIN decisions AS d ON d.id = a.decision_id").
			Where("a.id = ? AND a.type = ? AND a.status = ? AND d.person_id = ?", actionID, model.ActionTypeCancelAlarm, model.ActionStatusInProgress, personID).
			Take(&source).Error; err != nil {
			return fmt.Errorf("no owned running cancel-alarm action %d: %w", actionID, err)
		}
		changed := tx.Model(&model.ScheduledEvent{}).
			Where("id = ? AND person_id = ? AND status = ?", alarmID, personID, model.ScheduledEventStatusPending).
			Update("status", model.ScheduledEventStatusCancelled)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return fmt.Errorf("alarm %d is not owned and pending", alarmID)
		}
		if err := recordActionEffect(tx, actionID, model.ActionEffectScheduledEventCancellation, alarmID); err != nil {
			return err
		}
		ended := tx.Model(&model.Action{}).Where("id = ? AND status = ?", actionID, model.ActionStatusInProgress).
			Update("status", model.ActionStatusEnded)
		if ended.Error != nil {
			return ended.Error
		}
		if ended.RowsAffected != 1 {
			return fmt.Errorf("cancel-alarm action %d ended before the cancellation committed", actionID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	alarmRegistry.cancelOne(alarmID)
	applogger.Info("scheduled alarm cancelled", "person_id", personID, "action_id", actionID, "alarm_id", alarmID)
	return nil
}
