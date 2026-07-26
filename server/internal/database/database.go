// Package database provides SQLite database initialization and migration.
//
// This package handles:
//   - Database connection setup with WAL mode for concurrent access
//   - Auto-migration of all model tables
//   - Default data seeding (search config, DB version)
//
// SQLite configuration:
//   - WAL journal mode for better concurrent read performance
//   - 5-second busy timeout for write contention
//   - Immediate transaction locking to prevent deadlocks
//   - Single connection pool (SQLite limitation)
package database

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"qingqiu-world-server/internal/config"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DB is the global database connection instance.
//
// NOTE: *gorm.DB exposes full database capabilities including dangerous operations
// (Raw, Exec, Migrator, DB, Callback, etc.) that violate the principle of least
// privilege. For an internal application this risk is acceptable, but if stricter
// access control is needed in the future, consider encapsulating *gorm.DB within
// this package and exposing only business-semantic functions (e.g. FindByID,
// CreateEntity, UpdateEntity) so that *gorm.DB never leaks outside this package.
var DB *gorm.DB

// Init initializes the SQLite database connection.
// Creates the database directory if it doesn't exist.
// Configures WAL mode, busy timeout, and immediate transaction locking.
func Init() {
	settings := config.Get()

	dbDir := filepath.Join(settings.DataRoot, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		panic(fmt.Sprintf("Failed to create database directory: %v", err))
	}

	dbPath := settings.DatabaseURL()
	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DefaultContextTimeout: 30 * time.Second,
		Logger:                gormlogger.Default.LogMode(gormlogger.Silent),
		NowFunc: func() time.Time {
			return time.Now().Local()
		},
	})
	if err != nil {
		panic(fmt.Sprintf("Failed to connect to database: %v", err))
	}

	sqlDB, err := db.DB()
	if err != nil {
		panic(fmt.Sprintf("Failed to get underlying sql.DB: %v", err))
	}
	sqlDB.SetMaxOpenConns(1)

	DB = db
	applogger.Info("Database initialized", "path", dbPath)
}

// AutoMigrate creates all database tables using GORM AutoMigrate.
// No data migration is performed — this version series does not support
// compatibility with previous schema versions. Use manual migration scripts
// for development data.
func AutoMigrate() {
	models := []interface{}{
		&model.Person{},
		&model.LLMConfig{},
		&model.EmbeddingConfig{},
		&model.AgentConfig{},
		&model.Session{},
		&model.Message{},
		&model.Interaction{},
		&model.Summary{},
		&model.AgentNarrative{},
		&model.SearchConfig{},
		&model.DBVersion{},
		&model.KnowledgeBase{},
		&model.Document{},
		&model.DocumentChunk{},
		&model.Work{},
		&model.MessageDraft{},
		&model.ParticipantSession{},
		&model.ScheduledEvent{},
		&model.Event{},
		&model.AgentObservation{},
		&model.EventVector{},
		&model.EntityProfile{},
		&model.ModelCapability{},
		&model.AgentExperience{},
		&model.AgentExperienceVector{},
		&model.PublicExperience{},
		&model.PublicExperienceVector{},
		&model.SystemLLMConfig{},
		&model.UploadedSkill{},
		&model.AgentDelivery{},
	}

	for _, m := range models {
		if err := DB.AutoMigrate(m); err != nil {
			panic(fmt.Sprintf("Failed to auto-migrate %T: %v", m, err))
		}
	}

	ensureSearchConfig()
	ensureDBVersion()

	applogger.Info("Database migration completed")
}

// ensureSearchConfig creates the default search config record if it doesn't exist.
func ensureSearchConfig() {
	var count int64
	DB.Model(&model.SearchConfig{}).Where("id = ?", 1).Count(&count)
	if count == 0 {
		DB.Create(&model.SearchConfig{
			Provider:    "tavily",
			APIKey:      "",
			Description: "",
			IsActive:    false,
		})
	}
}

// ensureDBVersion creates the initial DB version record if the table is empty.
func ensureDBVersion() {
	var count int64
	DB.Model(&model.DBVersion{}).Count(&count)
	if count == 0 {
		DB.Create(&model.DBVersion{
			Version:     config.AppVersion,
			Description: "Initial SQLite schema after MySQL migration",
		})
	}
}
