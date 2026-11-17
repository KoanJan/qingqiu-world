package database

import (
	"testing"

	"qingqiu-world-server/internal/config"
)

func TestInitConfiguresSQLiteJournalAndBusyTimeout(t *testing.T) {
	t.Setenv("DATA_ROOT", t.TempDir())
	config.Init()
	previous := DB
	Init()
	t.Cleanup(func() {
		if sqlDB, err := DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB = previous
	})
	var journal string
	if err := DB.Raw("PRAGMA journal_mode").Scan(&journal).Error; err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}
	var timeout int
	if err := DB.Raw("PRAGMA busy_timeout").Scan(&timeout).Error; err != nil {
		t.Fatal(err)
	}
	if timeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", timeout)
	}
}
