// Package database provides the SQLite connection and current-release initialization.
//
// This package handles:
//   - Database connection setup with WAL mode for concurrent access
//   - Schema and default data creation for an empty database
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
	"strings"
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

// Init initializes only the SQLite database connection.
// Creates the database directory if it doesn't exist.
// Configures WAL mode, busy timeout, and immediate transaction locking.
func Init() {
	settings := config.Get()

	dbDir := filepath.Join(settings.DataRoot, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		panic(fmt.Sprintf("Failed to create database directory: %v", err))
	}

	dbPath := settings.DatabaseURL()
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"

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

// InitializeFresh creates the current release directly in a confirmed empty DB.
// Schema, indexes, defaults, and the version marker commit as one SQLite unit.
func InitializeFresh(version string) error {
	return InitializeFreshAt(DB, version)
}

// InitializeFreshAt initializes a separate, already-opened database. Developer
// baseline creation uses this so it cannot mutate the active database.
func InitializeFreshAt(db *gorm.DB, version string) error {
	return db.Transaction(func(tx *gorm.DB) error { return initializeCurrent(tx, version) })
}

// ResetPre010 applies the established reset policy only after a valid version
// below 0.1.0 has been read. It replaces that schema with the current release.
func ResetPre010(version string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var tables []string
		if err := tx.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&tables).Error; err != nil {
			return fmt.Errorf("list pre-0.1.0 tables: %w", err)
		}
		for _, table := range tables {
			quoted := strings.ReplaceAll(table, `"`, `""`)
			if err := tx.Exec(`DROP TABLE "` + quoted + `"`).Error; err != nil {
				return fmt.Errorf("drop pre-0.1.0 table %s: %w", table, err)
			}
		}
		return initializeCurrent(tx, version)
	})
}

func initializeCurrent(tx *gorm.DB, version string) error {
	models := allModels()
	if version == "0.1.21" {
		for i, entry := range models {
			if _, ok := entry.(*model.LLMConfig); ok {
				models[i] = &releasedLLMConfig{}
				break
			}
		}
	}
	if err := tx.Migrator().CreateTable(models...); err != nil {
		return fmt.Errorf("create current-release tables: %w", err)
	}
	if err := EnsureDecisionEventIndex(tx); err != nil {
		return err
	}
	if err := ensureRecallIndexes(tx); err != nil {
		return err
	}
	if err := tx.Create(&model.SearchConfig{Provider: "tavily", APIKey: "", Description: "", IsActive: false}).Error; err != nil {
		return fmt.Errorf("seed search config: %w", err)
	}
	if err := tx.Create(&model.DBVersion{Version: version, Description: "Fresh installation"}).Error; err != nil {
		return fmt.Errorf("record fresh database version %s: %w", version, err)
	}
	return nil
}

// EnsureDecisionEventIndex creates the partial uniqueness rule introduced in 0.1.18.
func EnsureDecisionEventIndex(db *gorm.DB) error {
	return db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_external_event ON decisions(person_id, event_id) WHERE event_id > 0").Error
}

// EnsureRecallIndexes creates the source-specific access paths used by bounded
// memory recall. It is idempotent and also used by isolated database tests.
func EnsureRecallIndexes() error {
	return ensureRecallIndexes(DB)
}

func ensureRecallIndexes(db *gorm.DB) error {
	for _, indexSQL := range []string{
		"CREATE INDEX IF NOT EXISTS idx_recall_events_type_ref ON events(event_type, ref_id)",
		"CREATE INDEX IF NOT EXISTS idx_recall_events_self_held_time ON events(ref_id, created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recall_messages_time ON messages(created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recall_messages_session_time ON messages(session_id, created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recall_participants_session_person ON participant_sessions(session_id, participant_id)",
		"CREATE INDEX IF NOT EXISTS idx_recall_actions_time ON actions(created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recall_action_effect_source ON action_effects(action_id, effect_type, effect_id)",
		"CREATE INDEX IF NOT EXISTS idx_recall_works_person_time ON works(person_id, created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recall_handoffs_person_time ON focus_handoffs(person_id, created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recall_handoffs_work_person_time ON focus_handoffs(work_id, person_id, created_at DESC, id DESC)",
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_recall_action_work_target ON actions(CASE WHEN json_valid(plan_json) THEN CAST(json_extract(plan_json, '$.target_work_id') AS INTEGER) ELSE 0 END, created_at DESC, id DESC) WHERE type IN (%d, %d)", model.ActionTypeRouteFocusedWork, model.ActionTypeCancelFocusedWork),
	} {
		if err := db.Exec(indexSQL).Error; err != nil {
			return err
		}
	}
	return nil
}

// allModels returns the full list of model structs for table operations.
func allModels() []any {
	return []any{
		&model.Person{},
		&model.LLMConfig{},
		&model.EmbeddingConfig{},
		&model.AgentConfig{},
		&model.Session{},
		&model.Message{},
		&model.TTSProviderDefinition{},
		&model.TTSRenderer{},
		&model.AgentVoice{},
		&model.SpeechRenderHistory{},
		&model.Interaction{},
		&model.Summary{},
		&model.AgentNarrative{},
		&model.SearchConfig{},
		&model.DBVersion{},
		&model.KnowledgeBase{},
		&model.KBAccess{},
		&model.Document{},
		&model.DocumentRevision{},
		&model.ContentNode{},
		&model.DocumentChunk{},
		&model.DocumentChunkNode{},
		&model.KBUsageTrace{},
		&model.KBEntity{},
		&model.KBRelation{},
		&model.KBRelationEvidence{},
		&model.KBRelationJob{},
		&model.Work{},
		&model.Workspace{},
		&model.WorkspaceUse{},
		&model.FocusHandoff{},
		&model.ParticipantSession{},
		&model.ScheduledEvent{},
		&model.Event{},
		&model.Decision{},
		&model.Action{},
		&model.ActionEffect{},
		&model.Memo{},
		&model.AgentObservation{},
		&model.DailyMemory{},
		&model.CognitiveMemory{},
		&model.CognitiveMemoryVersion{},
		&model.CognitiveProcess{},
		&model.EventVector{},
		&model.MemoryTerm{},
		&model.EntityProfile{},
		&model.ModelCapability{},
		&model.AgentExperience{},
		&model.AgentExperienceVector{},
		&model.PublicExperience{},
		&model.PublicExperienceVector{},
		&model.SystemLLMConfig{},
		&model.UploadedSkill{},
		&model.Jinshu{},
		&model.AgentState{},
		&model.AgentEventBuffer{},
		&model.AgentBiography{},
		&model.PSDigest{},
	}
}
