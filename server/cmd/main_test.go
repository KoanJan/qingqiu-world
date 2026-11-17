package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/migration/dev/unreleased"
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

// TestReleaseOnlyModeRejectedForDevelopmentBuild proves that the promoter's
// environment switch cannot skip the developer lifecycle in a build that still
// carries development steps: release migrations must never run directly on
// developer data.
func TestReleaseOnlyModeRejectedForDevelopmentBuild(t *testing.T) {
	t.Setenv("RELEASE_MIGRATION_ONLY", "1")
	if len(unreleased.Registered()) == 0 {
		t.Skip("no development steps are registered in this build")
	}
	defer func() {
		cause := recover()
		if cause == nil || !strings.Contains(fmt.Sprint(cause), "reserved for released builds") {
			t.Fatalf("expected release-only mode to be refused, got %v", cause)
		}
	}()
	if releaseOnlyMode() {
		t.Fatal("release-only mode accepted a development build")
	}
}
