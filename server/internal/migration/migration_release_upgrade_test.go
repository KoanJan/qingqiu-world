package migration

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestUpgradeFrom0117PreservesObservationHistory checks the formal release
// path. Version 0.1.17 had no Decision table, and its read boundary does not
// prove that every earlier message entered comprehension.
func TestUpgradeFrom0117PreservesObservationHistory(t *testing.T) {
	previousDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "release-upgrade.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previousDB })
	if err := db.AutoMigrate(&model.DBVersion{}, &model.Message{}, &model.Event{}, &model.ParticipantSession{}, &model.AgentObservation{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.DBVersion{Version: "0.1.17"}).Error; err != nil {
		t.Fatal(err)
	}
	message := model.Message{SessionID: 10, PersonID: 2, Content: "possibly skipped"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1, LastReadMessageID: message.ID}).Error; err != nil {
		t.Fatal(err)
	}

	Run()
	Run()
	if got := getDBVersion(); got != "0.1.18" {
		t.Fatalf("release upgrade version = %q", got)
	}
	var versionCount int64
	if err := db.Model(&model.DBVersion{}).Where("version = ?", "0.1.18").Count(&versionCount).Error; err != nil || versionCount != 1 {
		t.Fatalf("version recorded %d times: %v", versionCount, err)
	}
	var observationCount int64
	if err := db.Model(&model.AgentObservation{}).Count(&observationCount).Error; err != nil || observationCount != 0 {
		t.Fatalf("unproven observations created: count=%d err=%v", observationCount, err)
	}
}
