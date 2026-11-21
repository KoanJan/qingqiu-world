package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/config"
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

// TestTestModeSelectionKeepsExistingModes checks that only explicit test mode
// opens test state and that conflicting switches fail before data is created.
func TestTestModeSelectionKeepsExistingModes(t *testing.T) {
	for _, tc := range []struct {
		name, test, dev, release string
		wantTest, wantError      bool
	}{
		{"app", "0", "0", "0", false, false},
		{"dev", "0", "1", "0", false, false},
		{"release only", "0", "0", "1", false, false},
		{"test", "1", "0", "0", true, false},
		{"test and dev", "1", "1", "0", false, true},
		{"test and release", "1", "0", "1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runRoot := t.TempDir()
			root := filepath.Join(runRoot, "product")
			t.Setenv("DATA_ROOT", root)
			t.Setenv("TEST_DATA_MODE", tc.test)
			t.Setenv("DEV_DATA_MODE", tc.dev)
			t.Setenv("RELEASE_MIGRATION_ONLY", tc.release)
			t.Setenv("TEST_MIGRATION_STATE_PATH", filepath.Join(runRoot, "test-migrations.json"))
			config.Init()
			runner, err := prepareTestMigrations()
			if (err != nil) != tc.wantError || (runner != nil) != tc.wantTest {
				t.Fatalf("test selected = %v, error = %v", runner != nil, err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("mode selection created product data: %v", err)
			}
		})
	}
}

// TestUnreleasedNormalStartupStillRejectsUnmanagedData exercises the actual
// main entry before database initialization, without a test mode exemption.
func TestUnreleasedNormalStartupStillRejectsUnmanagedData(t *testing.T) {
	if len(unreleased.Registered()) == 0 {
		t.Skip("released build has no unreleased startup guard")
	}
	for _, tc := range []struct{ name, dev, message string }{
		{"app", "0", "requires explicit developer data mode"},
		{"dev", "1", "DEV_REPO_ROOT must identify the developer checkout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "product")
			t.Setenv("DATA_ROOT", root)
			t.Setenv("TEST_DATA_MODE", "0")
			t.Setenv("DEV_DATA_MODE", tc.dev)
			t.Setenv("DEV_REPO_ROOT", "")
			t.Setenv("RELEASE_MIGRATION_ONLY", "0")
			defer func() {
				if cause := recover(); cause == nil || !strings.Contains(fmt.Sprint(cause), tc.message) {
					t.Fatalf("startup guard = %v, want %q", cause, tc.message)
				}
				for _, path := range []string{root, root + ".developer-data"} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("rejected startup created %s: %v", path, err)
					}
				}
			}()
			main()
		})
	}
}
