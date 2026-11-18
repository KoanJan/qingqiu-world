package unreleased

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSecondDevelopmentMigrationPreservesRelationsAndAlarmInstant(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "staged")
	legacy := filepath.Join(root, "legacy-aos")
	for _, dir := range []string{stage, legacy} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "tz.txt"), []byte("Asia/Shanghai"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "note.txt"), []byte("historical"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_ROOT", filepath.Join(root, "active"))
	t.Setenv("AOS_ROOT", legacy)
	config.Init()
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "migration.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Action{}, &model.ActionEffect{}, &model.ScheduledEvent{}); err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("old+08", 8*60*60)
	trigger := time.Date(2030, 10, 18, 4, 45, 37, 123000000, zone)
	later := time.Date(2030, 10, 17, 21, 0, 0, 0, time.FixedZone("old-04", -4*60*60))
	for _, record := range []any{
		&model.Person{ID: 1, Name: "existing", Type: model.PersonTypeAI},
		&model.Action{ID: 11, DecisionID: 1, Type: 0},
		&model.Action{ID: 12, DecisionID: 1, Type: model.ActionTypeCancelAlarm},
		&model.ActionEffect{ID: 21, ActionID: 11, EffectType: model.ActionEffectScheduledEvent, EffectID: 31},
		&model.ScheduledEvent{ID: 31, PersonID: 1, TriggerAt: trigger},
		&model.ScheduledEvent{ID: 32, PersonID: 1, TriggerAt: later},
	} {
		if err := db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}
	for value := model.ActionTypeStartFocusedWork; value <= model.ActionTypeCancelAlarm; value++ {
		if err := db.Create(&model.Action{ID: 100 + int64(value), DecisionID: 1, Type: value}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := Apply(db, 2, stage); err != nil {
		t.Fatal(err)
	}
	var chat, other model.Action
	if err := db.First(&chat, 11).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&other, 12).Error; err != nil {
		t.Fatal(err)
	}
	if chat.Type != model.ActionTypeChat || other.Type != model.ActionTypeCancelAlarm {
		t.Fatalf("action types after migration: chat=%d other=%d", chat.Type, other.Type)
	}
	for value := model.ActionTypeStartFocusedWork; value <= model.ActionTypeCancelAlarm; value++ {
		var unchanged model.Action
		if err := db.First(&unchanged, 100+int64(value)).Error; err != nil || unchanged.Type != value {
			t.Fatalf("Action type %d after migration = %d, error = %v", value, unchanged.Type, err)
		}
	}
	var effect model.ActionEffect
	if err := db.First(&effect, 21).Error; err != nil || effect.ActionID != 11 || effect.EffectID != 31 {
		t.Fatalf("effect relationship after migration: %+v, %v", effect, err)
	}
	var alarm model.ScheduledEvent
	if err := db.First(&alarm, 31).Error; err != nil || !alarm.TriggerAt.Equal(trigger) {
		t.Fatalf("alarm instant after migration: %s, %v", alarm.TriggerAt, err)
	}
	var raw string
	if err := db.Raw("SELECT trigger_at FROM scheduled_events WHERE id = 31").Scan(&raw).Error; err != nil || !(strings.HasSuffix(raw, "Z") || strings.HasSuffix(raw, "+00:00")) {
		t.Fatalf("alarm storage is not UTC: %q, %v", raw, err)
	}
	var ordered []int64
	if err := db.Raw("SELECT id FROM scheduled_events ORDER BY trigger_at, id").Scan(&ordered).Error; err != nil || len(ordered) != 2 || ordered[0] != 31 || ordered[1] != 32 {
		t.Fatalf("alarm sort order = %v, error = %v", ordered, err)
	}
	for _, path := range []string{filepath.Join(stage, "aos", "note.txt"), filepath.Join(legacy, "note.txt")} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != "historical" {
			t.Fatalf("file %s = %q, %v", path, content, err)
		}
	}
}
