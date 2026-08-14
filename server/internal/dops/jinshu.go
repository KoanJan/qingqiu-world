package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreateJinshu inserts a jinshu record and returns it with its auto-increment
// id. The id doubles as the directory name under both the sender's sent/ and
// the recipient's received/ jinshu directories.
func CreateJinshu(fromPersonID, toPersonID int64, topic, description string) (*model.Jinshu, error) {
	record := model.Jinshu{
		FromPersonID: fromPersonID,
		ToPersonID:   toPersonID,
		Topic:        topic,
		Description:  description,
	}
	if err := database.DB.Create(&record).Error; err != nil {
		return nil, fmt.Errorf("create jinshu: %w", err)
	}
	return &record, nil
}

// GetJinshu retrieves a jinshu record by id.
func GetJinshu(jinshuID int64) (*model.Jinshu, error) {
	return Get[model.Jinshu](jinshuID)
}

// ListReceivedJinshu returns jinshu received by personID, newest first.
// offset skips the first N rows (only applied when limit > 0); limit <= 0
// means no limit.
func ListReceivedJinshu(personID int64, offset, limit int) ([]model.Jinshu, error) {
	return SearchReceivedJinshu(personID, "", offset, limit)
}

// SearchReceivedJinshu returns jinshu received by personID, newest first,
// optionally filtered by a keyword matched against topic or description.
// offset skips the first N rows (only applied when limit > 0); limit <= 0
// means no limit.
func SearchReceivedJinshu(personID int64, query string, offset, limit int) ([]model.Jinshu, error) {
	var records []model.Jinshu
	q := database.DB.Where("to_person_id = ?", personID)
	if query != "" {
		like := "%" + query + "%"
		q = q.Where("topic LIKE ? OR description LIKE ?", like, like)
	}
	q = q.Order("id DESC")
	if limit > 0 {
		if offset > 0 {
			q = q.Offset(offset)
		}
		q = q.Limit(limit)
	}
	if err := q.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("search received jinshu: %w", err)
	}
	return records, nil
}

// ListSentJinshu returns jinshu sent by personID, newest first.
// offset skips the first N rows (only applied when limit > 0); limit <= 0
// means no limit.
func ListSentJinshu(personID int64, offset, limit int) ([]model.Jinshu, error) {
	return SearchSentJinshu(personID, "", offset, limit)
}

// SearchSentJinshu returns jinshu sent by personID, newest first, optionally
// filtered by a keyword matched against topic or description.
// offset skips the first N rows (only applied when limit > 0); limit <= 0
// means no limit.
func SearchSentJinshu(personID int64, query string, offset, limit int) ([]model.Jinshu, error) {
	var records []model.Jinshu
	q := database.DB.Where("from_person_id = ?", personID)
	if query != "" {
		like := "%" + query + "%"
		q = q.Where("topic LIKE ? OR description LIKE ?", like, like)
	}
	q = q.Order("id DESC")
	if limit > 0 {
		if offset > 0 {
			q = q.Offset(offset)
		}
		q = q.Limit(limit)
	}
	if err := q.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("search sent jinshu: %w", err)
	}
	return records, nil
}

// MarkJinshuRead sets the receiver-only read flag on a jinshu. It is
// idempotent: marking an already-read record is a no-op.
func MarkJinshuRead(jinshuID int64) error {
	res := database.DB.Model(&model.Jinshu{}).
		Where("id = ? AND is_read = ?", jinshuID, false).
		Update("is_read", true)
	if res.Error != nil {
		return fmt.Errorf("mark jinshu %d read: %w", jinshuID, res.Error)
	}
	return nil
}
