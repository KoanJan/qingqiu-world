package dops

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

func TestDeleteTTSRendererChecksOnlyCurrentAgentVoiceVersions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tts-delete.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previousDB })
	if err := db.AutoMigrate(&model.TTSRenderer{}, &model.AgentVoice{}); err != nil {
		t.Fatalf("migrate speech models: %v", err)
	}

	historicalRenderer := model.TTSRenderer{Name: "historical", Provider: model.TTSProviderFishAudio, ConnectionConfigJSON: `{}`}
	currentRenderer := model.TTSRenderer{Name: "current", Provider: model.TTSProviderFishAudio, ConnectionConfigJSON: `{}`}
	if err := db.Create(&historicalRenderer).Error; err != nil {
		t.Fatalf("create historical renderer: %v", err)
	}
	if err := db.Create(&currentRenderer).Error; err != nil {
		t.Fatalf("create current renderer: %v", err)
	}
	for _, voice := range []model.AgentVoice{
		{PersonID: 7, TTSRendererID: historicalRenderer.ID},
		{PersonID: 7, TTSRendererID: currentRenderer.ID},
	} {
		if err := db.Create(&voice).Error; err != nil {
			t.Fatalf("create Agent voice version: %v", err)
		}
	}

	bound, err := DeleteTTSRendererIfUnbound(historicalRenderer.ID)
	if err != nil || bound != 0 {
		t.Fatalf("delete historically referenced renderer: bound=%d err=%v", bound, err)
	}
	if err := db.First(&model.TTSRenderer{}, historicalRenderer.ID).Error; err == nil {
		t.Fatal("historically referenced renderer was not deleted")
	}
	bound, err = DeleteTTSRendererIfUnbound(currentRenderer.ID)
	if err != nil {
		t.Fatalf("check current renderer binding: %v", err)
	}
	if bound != 1 {
		t.Fatalf("current renderer binding count = %d, want 1", bound)
	}
	if err := db.First(&model.TTSRenderer{}, currentRenderer.ID).Error; err != nil {
		t.Fatalf("bound current renderer was deleted: %v", err)
	}
}
