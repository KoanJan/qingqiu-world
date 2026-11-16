package migration

import (
	"fmt"
	"strings"
	"testing"

	"qingqiu-world-server/internal/config"
)

func TestValidateMigrationChain(t *testing.T) {
	if err := validateMigrationChain(migrations, minDataVersion, config.AppVersion); err != nil {
		t.Fatalf("registered release chain is invalid: %v", err)
	}
	for _, tc := range []struct {
		name     string
		versions []string
		app      string
		want     string
	}{
		{"missing first", []string{"0.1.2"}, "0.1.2", "missing migration"},
		{"missing middle", []string{"0.1.1", "0.1.3"}, "0.1.3", "missing migration"},
		{"duplicate", []string{"0.1.1", "0.1.1"}, "0.1.1", "duplicated or out of order"},
		{"out of order", []string{"0.1.1", "0.1.2", "0.1.1"}, "0.1.2", "duplicated or out of order"},
		{"missing last", []string{"0.1.1"}, "0.1.2", "chain ends"},
		{"beyond app", []string{"0.1.1", "0.1.2"}, "0.1.1", "exceeds application version"},
		{"invalid version", []string{"0.1.bad"}, "0.1.1", "invalid migration version"},
		{"next minor", []string{"0.1.1", "0.2.0"}, "0.2.0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := make([]migration, 0, len(tc.versions))
			for _, version := range tc.versions {
				chain = append(chain, migration{version: version})
			}
			err := validateMigrationChain(chain, minDataVersion, tc.app)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunRejectsMissingReleaseBeforeFreshInitialization(t *testing.T) {
	db := startupTestDB(t)
	original := migrations
	migrations = append(append([]migration{}, original[:2]...), original[3:]...)
	t.Cleanup(func() { migrations = original })
	defer func() {
		if cause := recover(); cause == nil || !strings.Contains(fmt.Sprint(cause), "missing migration") {
			t.Fatalf("expected missing release panic, got %v", cause)
		}
		if db.Migrator().HasTable("db_versions") {
			t.Fatal("fresh database was initialized with an invalid release chain")
		}
	}()
	Run()
}
