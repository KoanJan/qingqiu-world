package memory

import (
	"testing"

	"qingqiu-world-server/internal/model"
)

// TestCreateObservationTxFollowsSourceCommit verifies that a sender's
// observation cannot outlive a rolled-back message transaction.
func TestCreateObservationTxFollowsSourceCommit(t *testing.T) {
	db := recallTestDB(t)
	event := model.Event{EventType: model.EventTypeMessage, RefID: 1}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	firstID, err := CreateObservationTx(tx, 1, event.ID)
	if err != nil || firstID <= 0 {
		t.Fatalf("transactional observation insert failed: id=%d err=%v", firstID, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.AgentObservation{}).Where("person_id = ? AND event_id = ?", 1, event.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rolled-back observation survived: count=%d err=%v", count, err)
	}
	tx = db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	committedID, err := CreateObservationTx(tx, 1, event.ID)
	if err != nil || committedID <= 0 {
		t.Fatalf("second transactional observation failed: id=%d err=%v", committedID, err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	tx = db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	duplicateID, err := CreateObservationTx(tx, 1, event.ID)
	if err != nil || duplicateID != 0 {
		t.Fatalf("duplicate observation claimed a new ID: id=%d err=%v", duplicateID, err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AgentObservation{}).Where("person_id = ? AND event_id = ?", 1, event.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("committed observation count changed: count=%d err=%v", count, err)
	}
}
