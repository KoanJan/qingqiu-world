// Package migration handles incremental and full-init database data migrations.
//
// Migration strategy:
//   - DB version >= minDataVersion ("0.1.0"): execute incremental migration
//     scripts in semver order, each updating the db_versions table on success.
//   - DB version < minDataVersion or no version record: clear all data and
//     re-initialize from scratch.
//
// To add a migration for a new release:
//  1. Append a migration entry with the new version string and function to the
//     migrations list below.
//  2. Update config.AppVersion to match the new version.
package migration

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// minDataVersion is the minimum DB version that supports incremental migration.
// Databases at this version or above receive sequential migration scripts.
// Databases below this version (or with no version record) are cleared and
// re-initialized from scratch.
const minDataVersion = "0.1.0"

// migrationFunc executes one version's incremental migration.
// It has access to the global database.DB instance.
type migrationFunc func()

// migration defines a single version's incremental migration step.
type migration struct {
	version     string        // Semver tag, e.g. "0.1.1"
	description string        // Human-readable summary
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
		fn:          migrate_0_1_1,
	},
	{
		version:     "0.1.2",
		description: "Add persistent agent event buffers and sleep state",
		fn:          migrate_0_1_2,
	},
}

// Run executes incremental migration scripts based on the current database
// version. Called from main.go after database.AutoMigrate.
//
// Logic:
//  1. Read the latest version from db_versions table.
//  2. If no version record exists OR version < minDataVersion:
//     → Clear all data and re-initialize (fresh install).
//  3. If version >= minDataVersion:
//     → Execute all migration entries where version > current, in order.
//  4. Record the final version in db_versions.
func Run() {
	currentVersion := getDBVersion()

	if currentVersion == "" || semverLess(currentVersion, minDataVersion) {
		applogger.Info("DB version below minimum for incremental migration, performing full init",
			"current", currentVersion, "minimum", minDataVersion)
		database.ClearAndInit()
		recordVersion(config.AppVersion, "Full init (version below migration threshold)")
		return
	}

	// Incremental migration: run all scripts with version > currentVersion
	var pending []migration
	for _, m := range migrations {
		if semverLess(currentVersion, m.version) {
			pending = append(pending, m)
		}
	}

	if len(pending) == 0 {
		// No migration scripts to run, but the DB version may still be behind
		// the app version (e.g., a release with no data changes). Advance the
		// recorded version so future migrations start from the right point.
		if semverLess(currentVersion, config.AppVersion) {
			applogger.Info("No data migration needed, advancing DB version",
				"from", currentVersion, "to", config.AppVersion)
			recordVersion(config.AppVersion, "No-op version bump (no data changes)")
		} else {
			applogger.Info("DB schema is up to date", "version", currentVersion)
		}
		return
	}

	applogger.Info("Running incremental migrations",
		"from", currentVersion, "count", len(pending))

	for _, m := range pending {
		applogger.Info("Executing migration", "version", m.version, "description", m.description)
		m.fn()
		recordVersion(m.version, m.description)
	}

	applogger.Info("All migrations completed", "version", config.AppVersion)
}

// getDBVersion returns the latest version string from db_versions, or "" if empty.
func getDBVersion() string {
	var record model.DBVersion
	err := database.DB.Order("id DESC").First(&record).Error
	if err != nil {
		return ""
	}
	return record.Version
}

// recordVersion inserts a new db_versions record.
func recordVersion(version, description string) {
	if err := database.DB.Create(&model.DBVersion{
		Version:     version,
		Description: description,
	}).Error; err != nil {
		applogger.Error("failed to record DB version", "version", version, "error", err)
	}
}

// semverLess returns true if a < b in simplified semver comparison.
// Supports "X.Y.Z" format with numeric comparison at each level.
func semverLess(a, b string) bool {
	aparts := parseSemver(a)
	bparts := parseSemver(b)
	for i := 0; i < 3; i++ {
		if aparts[i] < bparts[i] {
			return true
		}
		if aparts[i] > bparts[i] {
			return false
		}
	}
	return false // equal
}

// parseSemver splits "X.Y.Z" into [X, Y, Z] integers.
// Missing or non-numeric parts default to 0.
func parseSemver(v string) [3]int {
	var result [3]int
	parts := strings.SplitN(v, ".", 3)
	for i, p := range parts {
		var n int
		fmt.Sscanf(p, "%d", &n)
		result[i] = n
	}
	return result
}
