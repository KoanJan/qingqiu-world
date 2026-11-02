package dops

import (
	"errors"
	"testing"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAgentAssignmentRequiresCurrentPassingProbe(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/probe.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close(); database.DB = previous })
	if err := db.AutoMigrate(&model.LLMConfig{}, &model.Person{}, &model.AgentConfig{}); err != nil {
		t.Fatal(err)
	}

	config := model.LLMConfig{Name: "working", ModelID: "model", BaseURL: "http://example.test", APIKey: "key", ProbeStatus: model.ProbeStatusUnverified}
	config.ProbeConfigHash = config.ProbeFingerprint()
	if err := db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateAIPerson("blocked", "", "", config.ID, ""); !errors.Is(err, ErrLLMProbeRequired) {
		t.Fatalf("unverified assignment: %v", err)
	}
	if err := db.Model(&config).Update("probe_status", model.ProbeStatusPassed).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateAIPerson("incomplete", "", "", config.ID, ""); !errors.Is(err, ErrLLMProbeRequired) {
		t.Fatalf("passing status without capability evidence: %v", err)
	}
	if err := db.Model(&config).Updates(map[string]interface{}{
		"probe_status": model.ProbeStatusPassed, "probe_required_tools_ok": true,
		"probe_structured_output_ok": true, "probe_checked_at": time.Now().UnixMilli(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateAIPerson("allowed", "", "", config.ID, ""); err != nil {
		t.Fatalf("passing assignment: %v", err)
	}
	person, err := GetPersonByName("allowed")
	if err != nil {
		t.Fatal(err)
	}

	other := model.LLMConfig{Name: "other", ModelID: "model", BaseURL: "http://example.test", APIKey: "key", ProbeStatus: model.ProbeStatusFailed}
	other.ProbeConfigHash = other.ProbeFingerprint()
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := UpdateAIPerson(&AIPersonUpdates{PersonID: person.ID, LLMConfigID: &other.ID}); !errors.Is(err, ErrLLMProbeRequired) {
		t.Fatalf("failed probe assignment: %v", err)
	}
	if err := db.Model(&config).Update("api_key", "changed").Error; err != nil {
		t.Fatal(err)
	}
	if err := RequirePassingLLMProbe(config.ID); !errors.Is(err, ErrLLMProbeRequired) {
		t.Fatalf("stale probe: %v", err)
	}
	if err := UpdateAIPerson(&AIPersonUpdates{PersonID: person.ID, LLMConfigID: &config.ID}); err != nil {
		t.Fatalf("existing assignment should remain: %v", err)
	}
	if err := db.Model(&other).Updates(map[string]interface{}{
		"probe_status": model.ProbeStatusPassed, "probe_config_hash": other.ProbeFingerprint(),
		"probe_required_tools_ok": true, "probe_structured_output_ok": true,
		"probe_checked_at": time.Now().UnixMilli(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := UpdateAIPerson(&AIPersonUpdates{PersonID: person.ID, LLMConfigID: &other.ID}); err != nil {
		t.Fatalf("retested assignment: %v", err)
	}
}

func TestExistingLLMConfigStartsUnverifiedAfterSchemaUpgrade(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/upgrade.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(`CREATE TABLE llm_configs (id INTEGER PRIMARY KEY, name TEXT NOT NULL,
		model_id TEXT NOT NULL, base_url TEXT NOT NULL, api_key TEXT NOT NULL,
		description TEXT NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO llm_configs VALUES (1, 'existing', 'model', 'https://example.test',
		'key', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.LLMConfig{}); err != nil {
		t.Fatal(err)
	}
	var config model.LLMConfig
	if err := db.First(&config, 1).Error; err != nil {
		t.Fatal(err)
	}
	if config.ProbeStatus != model.ProbeStatusUnverified || config.ProbeCheckedAt != 0 || config.ProbePassed() {
		t.Fatalf("historical config was treated as verified: %+v", config)
	}
	var columns []struct {
		Name    string `gorm:"column:name"`
		NotNull int    `gorm:"column:notnull"`
	}
	if err := db.Raw("PRAGMA table_info(llm_configs)").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"probe_status", "probe_checked_at"} {
		found := false
		for _, column := range columns {
			if column.Name == name {
				found = true
				if column.NotNull != 1 {
					t.Errorf("%s must be NOT NULL", name)
				}
			}
		}
		if !found {
			t.Errorf("missing %s column", name)
		}
	}
}
