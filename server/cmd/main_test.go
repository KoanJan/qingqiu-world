package main

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestEmbeddingServiceIsOptionalAtStartup verifies that a fresh database can
// reach setup without an embedding provider, while a configured restart loads it.
func TestEmbeddingServiceIsOptionalAtStartup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "startup.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.EmbeddingConfig{}); err != nil {
		t.Fatal(err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previousDB })

	if service := getEmbeddingService(); service != nil {
		t.Fatal("fresh installation unexpectedly initialized an embedding service")
	}
	config := model.EmbeddingConfig{Name: "test", ModelID: "embedding-test", BaseURL: "https://example.invalid", APIKey: "test-key"}
	if err := db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	if service := getEmbeddingService(); service == nil {
		t.Fatal("configured installation did not initialize an embedding service")
	}
}
