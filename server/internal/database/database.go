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

// Init initializes the SQLite database connection, creates tables, and seeds defaults.
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

	// Create all tables using GORM AutoMigrate.
	models := allModels()
	for _, m := range models {
		if err := DB.AutoMigrate(m); err != nil {
			panic(fmt.Sprintf("Failed to auto-migrate %T: %v", m, err))
		}
	}
	// Heartbeats use event_id=0 repeatedly; external events have one accepted
	// decision per person and durable event, including zero-action decisions.
	if err := DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_external_event ON decisions(person_id, event_id) WHERE event_id > 0").Error; err != nil {
		panic(fmt.Sprintf("Failed to create decision event index: %v", err))
	}
	if err := EnsureRecallIndexes(); err != nil {
		panic(fmt.Sprintf("Failed to create recall indexes: %v", err))
	}

	ensureSearchConfig()

	applogger.Info("Database schema migration completed")
}

// EnsureRecallIndexes creates the source-specific access paths used by bounded
// memory recall. It is idempotent and also used by isolated database tests.
func EnsureRecallIndexes() error {
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
		if err := DB.Exec(indexSQL).Error; err != nil {
			return err
		}
	}
	return nil
}

// clearAndInit truncates all data and re-seeds defaults.
// Exported for use by the migration package when DB version is below threshold.
func ClearAndInit() {
	models := allModels()
	for _, m := range models {
		if err := DB.Where("1 = 1").Delete(m).Error; err != nil {
			tableName := ""
			if tabler, ok := m.(interface{ TableName() string }); ok {
				tableName = tabler.TableName()
			}
			applogger.Error("clearAndInit: failed to clear table", "table", tableName, "error", err)
		}
	}
	ensureSearchConfig()
}

// allModels returns the full list of model structs for table operations.
// Exported so that the migration package can access the model list for clearAndInit.
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
		&model.AgentObservation{},
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

// ensureSearchConfig creates the default search config record if it doesn't exist.
// Exported so that the migration package can call it during clearAndInit.
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
