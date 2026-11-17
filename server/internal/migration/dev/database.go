package dev

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/glebarez/go-sqlite"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openDataDB(root string) (*gorm.DB, error) {
	path := filepath.Join(root, "db", "database.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open staged database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	return db, nil
}

func closeDataDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// inspectDataDB opens a released or staged SQLite database without creating it.
func inspectDataDB(root string) (string, error) {
	return inspectDataDBWithMode(root, false)
}

func inspectImmutableDataDB(root string) (string, error) {
	return inspectDataDBWithMode(root, true)
}

func inspectDataDBWithMode(root string, immutable bool) (string, error) {
	path := filepath.Join(root, "db", "database.db")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("database file %s: %w", path, err)
	}
	query := "mode=ro"
	if immutable {
		query += "&immutable=1"
	}
	// SQLite file URIs require slash-separated, absolute paths. On Windows a
	// drive path such as C:\data\db\database.db must become file:///C:/data/...,
	// so the volume prefix needs one extra leading slash.
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil {
		return "", fmt.Errorf("check database integrity: %w", err)
	}
	if integrity != "ok" {
		return "", fmt.Errorf("database integrity check failed: %s", integrity)
	}
	var version string
	if err := db.QueryRow("SELECT version FROM db_versions ORDER BY id DESC LIMIT 1").Scan(&version); err != nil {
		return "", fmt.Errorf("read database release version: %w", err)
	}
	return version, nil
}

func normalizeSQLiteWAL(root string) error {
	db, err := openDataDB(root)
	if err != nil {
		return err
	}
	var busy, logFrames, checkpointed int
	result := db.Raw("PRAGMA wal_checkpoint(TRUNCATE)").Row().Scan(&busy, &logFrames, &checkpointed)
	closeErr := closeDataDB(db)
	if result != nil {
		return fmt.Errorf("checkpoint staged SQLite WAL: %w", result)
	}
	if closeErr != nil {
		return closeErr
	}
	if busy != 0 {
		return fmt.Errorf("staged SQLite WAL still has active readers or writers")
	}
	if logFrames > checkpointed {
		return fmt.Errorf("staged SQLite WAL checkpointed %d of %d frames", checkpointed, logFrames)
	}
	wal := filepath.Join(root, "db", "database.db-wal")
	if info, err := os.Stat(wal); err == nil && info.Size() != 0 {
		return fmt.Errorf("staged SQLite WAL has %d uncheckpointed bytes", info.Size())
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
