package kb

import (
	"os"
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestCreateKnowledgeBaseLeavesNoRowOnStorageFailure checks that a failed
// filesystem setup cannot publish a KB record without usable storage.
func TestCreateKnowledgeBaseLeavesNoRowOnStorageFailure(t *testing.T) {
	dataRoot := t.TempDir()
	settings := config.Get()
	previousRoot := settings.DataRoot
	settings.DataRoot = dataRoot
	t.Cleanup(func() { settings.DataRoot = previousRoot })

	db, err := gorm.Open(sqlite.Open(filepath.Join(dataRoot, "create.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.KnowledgeBase{}); err != nil {
		t.Fatalf("migrate KB table: %v", err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = previousDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	blocker := filepath.Join(dataRoot, "kb")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("block KB directory: %v", err)
	}
	if err := CreateKnowledgeBase(&model.KnowledgeBase{Name: "storage failure"}); err == nil {
		t.Fatal("creating KB with blocked storage succeeded")
	}
	var count int64
	if err := db.Model(&model.KnowledgeBase{}).Count(&count).Error; err != nil {
		t.Fatalf("count KB rows after failure: %v", err)
	}
	if count != 0 {
		t.Fatalf("storage failure left %d KB records", count)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatalf("remove storage blocker: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "storage failure"}
	if err := CreateKnowledgeBase(kb); err != nil {
		t.Fatalf("create KB after fixing storage: %v", err)
	}
	if err := db.Model(&model.KnowledgeBase{}).Count(&count).Error; err != nil {
		t.Fatalf("count KB rows after retry: %v", err)
	}
	if count != 1 {
		t.Fatalf("retry left %d KB records, want one", count)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(kb.IndexFilePath), "vectors.db")); err != nil {
		t.Fatalf("created KB has no vector storage: %v", err)
	}
}
