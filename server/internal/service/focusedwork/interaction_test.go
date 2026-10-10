package focusedwork

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestSessionlessWorkPersistsInteractions verifies that Work history does not
// depend on the presence of an originating conversation.
func TestSessionlessWorkPersistsInteractions(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "interactions.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Interaction{}); err != nil {
		t.Fatal(err)
	}
	loop := &FocusedLoop{sessionID: 0, workID: 12}
	loop.weakWriteInteraction(1, model.InteractionTypeRequest, map[string]interface{}{"prompt": "read the document"})
	var records []model.Interaction
	if err := db.Where("work_id = ?", 12).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].WorkID != 12 {
		t.Fatalf("sessionless Work lost its interaction: %+v", records)
	}
}
