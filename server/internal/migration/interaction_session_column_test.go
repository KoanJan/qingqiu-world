package migration

import (
	"path/filepath"
	"testing"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// legacyInteractionRow models the released schema for upgrade testing only.
type legacyInteractionRow struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	SessionID int64  `gorm:"not null;index"`
	WorkID    int64  `gorm:"not null;index"`
	Iteration int    `gorm:"not null"`
	Type      int    `gorm:"not null"`
	Data      string `gorm:"type:text;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyInteractionRow) TableName() string { return "interactions" }

// TestInteractionSessionColumnUpgrade preserves Work-owned history while
// retiring the released 0.1.18 Interaction.SessionID column.
func TestInteractionSessionColumnUpgrade(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upgrade.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.Work{}, &model.Workspace{},
		&model.WorkspaceUse{}, &model.Action{}, &model.AgentEventBuffer{}, &model.Event{}, &model.DBVersion{}, &legacyInteractionRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE persons DROP COLUMN status; ALTER TABLE sessions DROP COLUMN status;").Error; err != nil {
		t.Fatal(err)
	}
	work := model.Work{PersonID: 9, SessionID: 12, Description: "Prior Focus"}
	if err := db.Create(&work).Error; err != nil {
		t.Fatal(err)
	}
	old := legacyInteractionRow{SessionID: 12, WorkID: work.ID, Iteration: 2,
		Type: model.InteractionTypeResponse, Data: `{"content":"historical result"}`}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	sessionlessWork := model.Work{PersonID: 9, SessionID: 0, Description: "Jinshu Focus"}
	if err := db.Create(&sessionlessWork).Error; err != nil {
		t.Fatal(err)
	}
	sessionless := legacyInteractionRow{SessionID: 0, WorkID: sessionlessWork.ID, Iteration: 1,
		Type: model.InteractionTypeResponse, Data: `{"content":"Jinshu result"}`}
	if err := db.Create(&sessionless).Error; err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.Interaction{}, "session_id") {
		t.Fatal("test setup has no legacy session column")
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyReleaseSQL(tx, "0.1.19") }); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn(&model.Interaction{}, "session_id") {
		t.Fatal("obsolete Interaction Session column survived upgrade")
	}
	if !db.Migrator().HasIndex(&model.Interaction{}, "idx_interactions_work_id") {
		t.Fatal("Interaction Work lookup index was lost during upgrade")
	}
	var remaining model.Interaction
	if err := db.First(&remaining, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if remaining.WorkID != work.ID || remaining.Iteration != old.Iteration || remaining.Data != old.Data {
		t.Fatalf("historical Work interaction changed: %+v", remaining)
	}
	var sessionlessRemaining model.Interaction
	if err := db.First(&sessionlessRemaining, sessionless.ID).Error; err != nil || sessionlessRemaining.WorkID != sessionlessWork.ID {
		t.Fatalf("sessionless Work interaction changed: %+v err=%v", sessionlessRemaining, err)
	}
	var count int64
	if err := db.Table("interactions AS i").Joins("JOIN works AS w ON w.id = i.work_id").
		Where("w.session_id = ?", 12).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("session Activity lost historical Work interaction: count=%d err=%v", count, err)
	}
}
