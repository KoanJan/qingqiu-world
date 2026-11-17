package migration

import (
	"embed"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

//go:embed sql/*.sql
var releaseSQL embed.FS

// applyReleaseSQL executes the fixed SQL for one release on the caller's
// transaction. The caller also records the release version on that transaction.
func applyReleaseSQL(tx *gorm.DB, version string) error {
	script, err := releaseSQL.ReadFile("sql/" + version + ".sql")
	if err != nil {
		return fmt.Errorf("read migration SQL for %s: %w", version, err)
	}
	content := strings.TrimSpace(string(script))
	if content == "" {
		return nil
	}
	if !strings.HasSuffix(content, ";") {
		return fmt.Errorf("migration SQL for %s must end with a semicolon", version)
	}
	if err := tx.Exec(content).Error; err != nil {
		return fmt.Errorf("execute migration SQL for %s: %w", version, err)
	}
	return nil
}
