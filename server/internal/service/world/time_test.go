package world

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	applogger "qingqiu-world-server/internal/logger"
)

func timezoneTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "world.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE persons (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestWorldTimezoneUsesSystemThenPersistedFile(t *testing.T) {
	db := timezoneTestDB(t)
	root := t.TempDir()
	t.Setenv("TZ", "Pacific/Honolulu")
	if err := InitTime(db, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "tz.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if string(data) != "UTC" {
			t.Fatalf("Windows timezone = %q, want UTC", data)
		}
	} else {
		name, err := systemTimezoneName()
		if err != nil || string(data) != name {
			t.Fatalf("system timezone = %q, persisted = %q, error = %v", name, data, err)
		}
	}
	if err := db.Exec("INSERT INTO persons (id) VALUES (1)").Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tz.txt"), []byte("Asia/Tokyo"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "Europe/London")
	if err := InitTime(db, root); err != nil {
		t.Fatal(err)
	}
	if Timezone().String() != "Asia/Tokyo" || Now().Location().String() != "Asia/Tokyo" || Today() != time.Now().In(Timezone()).Format("2006-01-02") {
		t.Fatalf("persisted world timezone was not used: %s", Timezone())
	}
	if got := FormatTime(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)); got != "2026-10-18T09:00:00+09:00" {
		t.Fatalf("world time display = %q", got)
	}
}

func TestExistingWorldRejectsMissingOrInvalidTimezone(t *testing.T) {
	db := timezoneTestDB(t)
	if err := db.Exec("INSERT INTO persons (id) VALUES (1)").Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := InitTime(db, root); err == nil || !strings.Contains(err.Error(), "missing tz.txt") {
		t.Fatalf("missing timezone error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tz.txt"), []byte("invalid-zone"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := InitTime(db, root); err == nil || !strings.Contains(err.Error(), "invalid tz.txt") {
		t.Fatalf("invalid timezone error = %v", err)
	}
}

func TestEmptyWorldRepairsInvalidTimezone(t *testing.T) {
	db := timezoneTestDB(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tz.txt"), []byte("invalid-zone"), 0644); err != nil {
		t.Fatal(err)
	}
	previousWarn := applogger.Warn
	var warned bool
	applogger.Warn = func(message string, args ...any) {
		if strings.Contains(message, "invalid timezone file") {
			warned = true
		}
	}
	t.Cleanup(func() { applogger.Warn = previousWarn })
	if err := InitTime(db, root); err != nil {
		t.Fatal(err)
	}
	if !warned {
		t.Fatal("invalid timezone repair did not emit a warning")
	}
	if _, err := loadTimezone(Timezone().String()); err != nil {
		t.Fatal(err)
	}
}
