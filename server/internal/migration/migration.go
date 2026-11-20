// Package migration applies release upgrades to existing SQLite data.
//
// Migration strategy:
//   - An empty database is initialized directly at the current release.
//   - A supported older database advances one release at a time.
//   - A confirmed pre-0.1.0 database follows the established reset policy.
//
// To add a migration for a new release:
//  1. Add sql/<version>.sql and append its migration entry below.
//  2. Add a data migration function only if SQL cannot express the conversion.
//  3. Update config.AppVersion to match the new version.
package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

// minDataVersion is the minimum DB version that supports incremental migration.
// Databases at this version or above receive sequential migration scripts.
// Only a database with a valid lower version follows the reset policy.
const minDataVersion = "0.1.0"

// migrationFunc executes one version's data migration on the release transaction.
type migrationFunc func(*gorm.DB)

// migration defines a single version's incremental migration step.
type migration struct {
	version     string        // Semver tag, e.g. "0.1.1"
	description string        // Human-readable summary
	beforeSQL   migrationFunc // Optional inspection of columns removed by the script
	fn          migrationFunc // The migration logic
}

// migrations is the ordered list of all incremental migrations.
// Each entry corresponds to one release version. When the database's current
// version is behind config.AppVersion, all entries with version > current
// are executed in order.
var migrations = []migration{
	{
		version:     "0.1.1",
		description: "Add AgentState table and seed energy for existing AI persons",
	},
	{
		version:     "0.1.2",
		description: "Add persistent agent event buffers and sleep state",
	},
	{version: "0.1.3", description: "No schema or data changes"},
	{version: "0.1.4", description: "Remove message drafts and obsolete draft references"},
	{
		version:     "0.1.5",
		description: "Drop works.type column (WorkType removed)",
	},
	{version: "0.1.6", description: "Add agent biographies"},
	{
		version:     "0.1.7",
		description: "Rename agent_deliveries to jinshus (drop session/paths, add topic, rename remark→description)",
	},
	{version: "0.1.8", description: "Add Jinshu read state"},
	{
		version:     "0.1.9",
		description: "Migrate agent_configs.knowledge_base_ids bindings into kb_access grants, then drop the column",
		beforeSQL:   inspectLegacyKBBindings,
	},
	{version: "0.1.10", description: "Add knowledge base keyword ratio"},
	{version: "0.1.11", description: "No schema or data changes"},
	{version: "0.1.12", description: "No schema or data changes"},
	{
		version:     "0.1.13",
		description: "Copy legacy workspace and private-space data into Agent Owned Space",
		fn:          migrate_0_1_13,
	},
	{
		version:     "0.1.14",
		description: "Add canonical document revisions and content-node mappings for knowledge bases",
		fn:          migrate_0_1_14,
	},
	{
		version:     "0.1.15",
		description: "Add natural-language applicability notes to knowledge-base relations",
	},
	{version: "0.1.16", description: "Add document chunk structure context"},
	{
		version:     "0.1.17",
		description: "Add agent voice, TTS renderer, speech history, and message/alarm expression fields",
	},
	{version: "0.1.18", description: "Add decision history, action effects, and recall indexes"},
	{
		version:     "0.1.19",
		description: "Register historical Workspaces, remove redundant Interaction Session IDs, and compact retired Jinshu types",
		fn:          migrate_0_1_19,
	},
	{version: "0.1.20", description: "Add memos"},
	{version: "0.1.21", description: "Add cognitive memories and decision thoughts"},
}

// Run selects the empty, legacy-reset, or sequential release-upgrade path.
func Run() {
	if err := ValidateReleaseChain(); err != nil {
		panic(fmt.Sprintf("invalid release migration chain: %v", err))
	}
	currentVersion, empty, err := inspectDatabase()
	if err != nil {
		panic(fmt.Sprintf("inspect database before migration: %v", err))
	}
	if empty {
		if err := database.InitializeFresh(config.AppVersion); err != nil {
			panic(fmt.Sprintf("initialize fresh database: %v", err))
		}
		applogger.Info("Fresh database initialized", "version", config.AppVersion)
		return
	}

	current, err := parseVersion(currentVersion)
	if err != nil {
		panic(fmt.Sprintf("invalid database version %q: %v", currentVersion, err))
	}
	app, err := parseVersion(config.AppVersion)
	if err != nil {
		panic(fmt.Sprintf("invalid application version %q: %v", config.AppVersion, err))
	}
	if versionLess(app, current) {
		panic(fmt.Sprintf("database version %s is newer than application version %s", currentVersion, config.AppVersion))
	}
	minimum, _ := parseVersion(minDataVersion)
	if versionLess(current, minimum) {
		applogger.Warn("Resetting confirmed pre-0.1.0 database", "version", currentVersion)
		if err := database.ResetPre010(config.AppVersion); err != nil {
			panic(fmt.Sprintf("reset pre-0.1.0 database: %v", err))
		}
		return
	}

	for _, m := range migrations {
		target, err := parseVersion(m.version)
		if err != nil {
			panic(fmt.Sprintf("invalid registered migration version %q: %v", m.version, err))
		}
		if !versionLess(current, target) || versionLess(app, target) {
			continue
		}
		applogger.Info("Executing release migration", "from", currentVersion, "to", m.version, "description", m.description)
		func() {
			defer func() {
				if cause := recover(); cause != nil {
					panic(fmt.Sprintf("release migration %s failed: %v", m.version, cause))
				}
			}()
			if err := executeReleaseStep(m); err != nil {
				panic(err)
			}
		}()
		currentVersion = m.version
		current = target
	}
	if currentVersion != config.AppVersion {
		panic(fmt.Sprintf("no registered migration from %s to application version %s", currentVersion, config.AppVersion))
	}
	applogger.Info("Release migrations completed", "version", currentVersion)
}

// ValidateReleaseChain checks the registered release sequence before any
// developer baseline or active database may be created.
func ValidateReleaseChain() error {
	return validateMigrationChain(migrations, minDataVersion, config.AppVersion)
}

// validateMigrationChain checks the registered release sequence before any
// database initialization or upgrade can conceal a missing step.
func validateMigrationChain(chain []migration, minimumVersion, appVersion string) error {
	previous, err := parseVersion(minimumVersion)
	if err != nil {
		return fmt.Errorf("invalid minimum data version %q: %w", minimumVersion, err)
	}
	app, err := parseVersion(appVersion)
	if err != nil {
		return fmt.Errorf("invalid application version %q: %w", appVersion, err)
	}
	if versionLess(app, previous) {
		return fmt.Errorf("application version %s precedes minimum data version %s", appVersion, minimumVersion)
	}
	previousName := minimumVersion
	for _, step := range chain {
		version, err := parseVersion(step.version)
		if err != nil {
			return fmt.Errorf("invalid migration version %q: %w", step.version, err)
		}
		if !versionLess(previous, version) {
			return fmt.Errorf("migration %s is duplicated or out of order after %s", step.version, previousName)
		}
		if !nextReleaseVersion(previous, version) {
			return fmt.Errorf("missing migration between %s and %s", previousName, step.version)
		}
		if versionLess(app, version) {
			return fmt.Errorf("migration %s exceeds application version %s", step.version, appVersion)
		}
		previous = version
		previousName = step.version
	}
	if previousName != appVersion {
		return fmt.Errorf("migration chain ends at %s, application version is %s", previousName, appVersion)
	}
	return nil
}

// A release advances one patch, starts the next minor at patch zero, or
// starts the next major at minor and patch zero.
func nextReleaseVersion(previous, next [3]int) bool {
	return (next[0] == previous[0] && next[1] == previous[1] && next[2] == previous[2]+1) ||
		(next[0] == previous[0] && next[1] == previous[1]+1 && next[2] == 0) ||
		(next[0] == previous[0]+1 && next[1] == 0 && next[2] == 0)
}

// executeReleaseStep keeps structure, data, and the version marker atomic.
func executeReleaseStep(m migration) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if m.beforeSQL != nil {
			m.beforeSQL(tx)
		}
		if err := applyReleaseSQL(tx, m.version); err != nil {
			return err
		}
		if m.fn != nil {
			m.fn(tx)
		}
		return recordVersion(tx, m.version, m.description)
	})
}

// inspectDatabase distinguishes a truly empty schema from missing or unreadable
// version metadata. Neither failure mode is allowed to clear existing data.
func inspectDatabase() (version string, empty bool, err error) {
	var tableCount int64
	if err := database.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view', 'trigger') AND name NOT LIKE 'sqlite_%'").Scan(&tableCount).Error; err != nil {
		return "", false, fmt.Errorf("list database objects: %w", err)
	}
	if tableCount == 0 {
		hasData, err := hasDataOutsideEmptyDatabase()
		if err != nil {
			return "", false, err
		}
		if hasData {
			return "", false, fmt.Errorf("nonempty data directory has no versioned database")
		}
		return "", true, nil
	}
	var versionTableCount int64
	if err := database.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'db_versions'").Scan(&versionTableCount).Error; err != nil {
		return "", false, fmt.Errorf("inspect db_versions table: %w", err)
	}
	if versionTableCount == 0 {
		return "", false, fmt.Errorf("nonempty database has no db_versions table")
	}
	var records []versionRecord
	result := database.DB.Raw(`SELECT id, version FROM db_versions ORDER BY id`).Scan(&records)
	if result.Error != nil {
		return "", false, fmt.Errorf("read database version history: %w", result.Error)
	}
	version, err = validateVersionHistory(records)
	if err != nil {
		return "", false, err
	}
	return version, false, nil
}

// hasDataOutsideEmptyDatabase excludes SQLite's own files, the timezone file
// written before migration starts, the configured log directory, and known
// operating-system metadata files.
func hasDataOutsideEmptyDatabase() (bool, error) {
	settings := config.Get()
	entries, err := os.ReadDir(settings.DataRoot)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect data directory: %w", err)
	}
	logDir, err := filepath.Rel(settings.DataRoot, settings.LogDir)
	if err != nil || logDir == "." || strings.HasPrefix(logDir, "..") || strings.Contains(logDir, string(filepath.Separator)) {
		logDir = ""
	}
	for _, entry := range entries {
		if entry.Name() == logDir || isSystemMetadata(entry) {
			continue
		}
		if entry.Name() == "tz.txt" && entry.Type().IsRegular() {
			continue
		}
		if entry.Name() != "db" || !entry.IsDir() {
			return true, nil
		}
		dbEntries, err := os.ReadDir(filepath.Join(settings.DataRoot, "db"))
		if err != nil {
			return false, fmt.Errorf("inspect database directory: %w", err)
		}
		for _, file := range dbEntries {
			if isSystemMetadata(file) {
				continue
			}
			switch file.Name() {
			case "database.db", "database.db-wal", "database.db-shm":
			default:
				return true, nil
			}
		}
	}
	return false, nil
}

// isSystemMetadata accepts only regular files with names written by Finder or
// Windows Explorer. Directories and symlinks still block fresh initialization.
func isSystemMetadata(entry os.DirEntry) bool {
	if !entry.Type().IsRegular() {
		return false
	}
	switch entry.Name() {
	case ".DS_Store", ".localized", "Thumbs.db", "desktop.ini":
		return true
	}
	return strings.HasPrefix(entry.Name(), "._")
}

// getDBVersion reads a known initialized database's latest release marker.
func getDBVersion() string {
	version, _, err := inspectDatabase()
	if err != nil {
		panic(err)
	}
	return version
}

func recordVersion(tx *gorm.DB, version, description string) error {
	if err := tx.Exec(`INSERT INTO db_versions (version, description, applied_at) VALUES (?, ?, ?)`,
		version, description, time.Now()).Error; err != nil {
		return fmt.Errorf("record database version %s: %w", version, err)
	}
	return nil
}

// versionRecord is one db_versions row in insertion order.
type versionRecord struct {
	ID      int64
	Version string
}

// validateVersionHistory returns the current release only when the recorded
// history can come from sequential upgrades. The newest row is authoritative
// only if no older release was recorded after it; otherwise the database is
// ambiguous and must stop before any migration runs. Repeated adjacent versions
// are valid because 0.1.19 writes additional markers for its enum compactions.
func validateVersionHistory(records []versionRecord) (string, error) {
	if len(records) == 0 {
		return "", fmt.Errorf("nonempty database has no version record")
	}
	var previous [3]int
	for i, record := range records {
		version, err := parseVersion(record.Version)
		if err != nil {
			return "", fmt.Errorf("invalid database version record %d (%q): %w", record.ID, record.Version, err)
		}
		if i > 0 && versionLess(version, previous) {
			return "", fmt.Errorf("database version history is out of order: %s follows %s", record.Version, records[i-1].Version)
		}
		previous = version
	}
	return records[len(records)-1].Version, nil
}

// parseVersion accepts only the release version stored in db_versions.
func parseVersion(version string) ([3]int, error) {
	var parts [3]int
	segments := strings.Split(version, ".")
	if len(segments) != len(parts) {
		return parts, fmt.Errorf("expected major.minor.patch")
	}
	for i, segment := range segments {
		if segment == "" {
			return parts, fmt.Errorf("empty version component")
		}
		n, err := strconv.Atoi(segment)
		if err != nil || n < 0 {
			return parts, fmt.Errorf("invalid version component %q", segment)
		}
		parts[i] = n
	}
	return parts, nil
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
