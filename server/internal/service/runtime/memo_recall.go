package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/memo"
)

// memoRecallArgs is deliberately separate from cross-source recall arguments:
// memos are current owned resources, not observed Events or indexed history.
type memoRecallArgs struct {
	MemoID      int64            `json:"memo_id"`
	Status      model.MemoStatus `json:"status"`
	Page        int              `json:"page"`
	PageSize    int              `json:"page_size"`
	HistoryPage int              `json:"history_page"`
}

type memoPreview struct {
	MemoID    int64     `json:"memo_id"`
	Preview   string    `json:"preview"`
	Status    string    `json:"status"`
	Pinned    bool      `json:"pinned"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type memoChange struct {
	ActionID int64  `json:"action_id"`
	EventID  int64  `json:"event_id,omitempty"`
	Trigger  string `json:"trigger,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// executeMemoRecall exposes complete current text and a bounded route back to
// the Actions that actually changed it. All queries enforce PersonID ownership.
func executeMemoRecall(personID int64, arguments string) (string, error) {
	var args memoRecallArgs
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", fmt.Errorf("invalid memo recall arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("memo recall has trailing JSON")
	}
	if args.MemoID < 0 || args.Page < 0 || args.PageSize < 0 || args.HistoryPage < 0 {
		return "", errors.New("memo recall has a negative ID or page")
	}
	if args.MemoID > 0 {
		if args.Status != 0 || args.Page != 0 || args.PageSize != 0 {
			return "", errors.New("memo detail only accepts memo_id and history_page")
		}
		return readMemoRecall(personID, args.MemoID, args.HistoryPage)
	}
	if args.HistoryPage != 0 {
		return "", errors.New("memo browsing does not accept history_page")
	}
	page, limit := args.Page, args.PageSize
	if page == 0 {
		page = 1
	}
	if limit == 0 {
		limit = 5
	}
	records, more, err := memo.List(personID, args.Status, page, limit)
	if err != nil {
		return "", err
	}
	items := make([]memoPreview, 0, len(records))
	for _, record := range records {
		items = append(items, memoPreview{MemoID: record.ID, Preview: shortContextText(record.Content, 160),
			Status: memoStatusName(record.Status), Pinned: record.Pinned, Revision: record.Revision,
			CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt})
	}
	output := struct {
		Items    []memoPreview `json:"items"`
		HasMore  bool          `json:"has_more"`
		NextPage int           `json:"next_page,omitempty"`
	}{Items: items, HasMore: more}
	if more {
		output.NextPage = page + 1
	}
	encoded, err := json.Marshal(output)
	return string(encoded), err
}

func readMemoRecall(personID, memoID int64, historyPage int) (string, error) {
	if historyPage == 0 {
		historyPage = 1
	}
	if historyPage > 10000 {
		return "", errors.New("memo history page is out of range")
	}
	record, err := memo.Read(personID, memoID)
	if err != nil {
		return "", err
	}
	const historyLimit = 3
	var changes []struct {
		ActionID int64  `gorm:"column:action_id"`
		EventID  int64  `gorm:"column:event_id"`
		Reason   string `gorm:"column:reason"`
	}
	if err := database.DB.Table("action_effects AS ae").
		Select("ae.action_id, d.event_id, a.reason").
		Joins("JOIN actions AS a ON a.id = ae.action_id").
		Joins("JOIN decisions AS d ON d.id = a.decision_id").
		Where("ae.effect_type = ? AND ae.effect_id = ? AND d.person_id = ?", model.ActionEffectMemo, memoID, personID).
		Order("ae.id DESC").Offset((historyPage - 1) * historyLimit).Limit(historyLimit + 1).Scan(&changes).Error; err != nil {
		return "", fmt.Errorf("read memo change sources: %w", err)
	}
	more := len(changes) > historyLimit
	if more {
		changes = changes[:historyLimit]
	}
	if historyPage == 1 && len(changes) == 0 {
		return "", fmt.Errorf("memo %d has no recorded source Action", memoID)
	}
	presented := make([]memoChange, 0, len(changes))
	for _, change := range changes {
		entry := memoChange{ActionID: change.ActionID, EventID: change.EventID, Reason: shortContextText(change.Reason, 160)}
		if change.EventID == 0 {
			entry.Trigger = "heartbeat"
		}
		presented = append(presented, entry)
	}
	output := struct {
		MemoID          int64        `json:"memo_id"`
		Content         string       `json:"content"`
		Status          string       `json:"status"`
		Pinned          bool         `json:"pinned"`
		Revision        int64        `json:"revision"`
		CreatedAt       time.Time    `json:"created_at"`
		UpdatedAt       time.Time    `json:"updated_at"`
		Changes         []memoChange `json:"recorded_changes"`
		HasMoreHistory  bool         `json:"has_more_history"`
		NextHistoryPage int          `json:"next_history_page,omitempty"`
	}{MemoID: record.ID, Content: record.Content, Status: memoStatusName(record.Status), Pinned: record.Pinned,
		Revision: record.Revision, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, Changes: presented, HasMoreHistory: more}
	if more {
		output.NextHistoryPage = historyPage + 1
	}
	encoded, err := json.Marshal(output)
	return string(encoded), err
}

func memoStatusName(status model.MemoStatus) string {
	switch status {
	case model.MemoStatusActive:
		return "active"
	case model.MemoStatusArchived:
		return "archived"
	default:
		return "unknown"
	}
}
