package speech

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	renderercore "qingqiu-world-server/internal/service/speech/renderer"
)

var registerRenderTestAdapter sync.Once
var sharedRenderTestAdapter = &renderTestAdapter{}

type renderTestAdapter struct {
	onSynthesize func(renderercore.Request)
}

func (a *renderTestAdapter) Provider() model.TTSProvider { return model.TTSProviderCosyVoice }

func (a *renderTestAdapter) Definition() model.TTSProviderDefinition {
	return model.TTSProviderDefinition{Provider: model.TTSProviderCosyVoice}
}

func (a *renderTestAdapter) Validate(context.Context, model.TTSRenderer) error { return nil }

func (a *renderTestAdapter) PrepareVoice(context.Context, model.TTSRenderer, renderercore.VoiceSample) (string, error) {
	return "", nil
}

func (a *renderTestAdapter) Synthesize(_ context.Context, request renderercore.Request) (renderercore.Result, error) {
	if a.onSynthesize != nil {
		a.onSynthesize(request)
	}
	return renderercore.Result{
		Bytes:                  []byte("rendered-audio"),
		MIMEType:               "audio/mpeg",
		Format:                 "mp3",
		AdapterRequestSnapshot: `{"text":"rendered message","sample_audio_sha256":"first-sha256"}`,
	}, nil
}

func TestValidateAdapterRequestSnapshotRejectsSecrets(t *testing.T) {
	valid, err := renderercore.ValidateRequestSnapshot(`{"text":"[gentle] hello","temperature":0.4}`)
	if err != nil {
		t.Fatalf("valid request snapshot failed: %v", err)
	}
	if !strings.Contains(valid, `"text":"[gentle] hello"`) {
		t.Fatalf("normalized request snapshot lost text: %s", valid)
	}
	if _, err := renderercore.ValidateRequestSnapshot(`{"text":"hello","api_key":"secret"}`); err == nil {
		t.Fatal("request snapshot containing api_key was accepted")
	}
	if _, err := renderercore.ValidateRequestSnapshot(`{}`); err == nil {
		t.Fatal("empty request snapshot was accepted")
	}
}

// TestEnsureRenderPreservesSuccessAndRequeuesFailure verifies the durable
// message-level delivery contract without substituting any storage component.
func TestEnsureRenderPreservesSuccessAndRequeuesFailure(t *testing.T) {
	db := installSpeechTestDB(t)
	agent := model.Person{Name: "speech-test-agent", Type: model.PersonTypeAI}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	message := model.Message{SessionID: 1, PersonID: agent.ID, Content: "A durable response."}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	pending, err := EnsureRender(message.ID)
	if err != nil {
		t.Fatalf("create pending render: %v", err)
	}
	if pending.Status != model.SpeechRenderStatusPending {
		t.Fatalf("pending status = %d, want %d", pending.Status, model.SpeechRenderStatusPending)
	}

	if err := db.Model(&model.SpeechRenderHistory{}).Where("id = ?", pending.ID).Updates(map[string]interface{}{
		"status":                   model.SpeechRenderStatusSuccess,
		"relative_path":            "messages/1/1.mp3",
		"adapter_request_snapshot": `{"text":"[calm] A durable response."}`,
	}).Error; err != nil {
		t.Fatalf("mark render success: %v", err)
	}
	success, err := EnsureRender(message.ID)
	if err != nil {
		t.Fatalf("reuse successful render: %v", err)
	}
	if success.ID != pending.ID || success.Status != model.SpeechRenderStatusSuccess {
		t.Fatalf("success was not preserved: id=%d status=%d", success.ID, success.Status)
	}
	if success.AdapterRequestSnapshot != `{"text":"[calm] A durable response."}` {
		t.Fatalf("success request snapshot = %q", success.AdapterRequestSnapshot)
	}

	failedMessage := model.Message{SessionID: 1, PersonID: agent.ID, Content: "A retryable response."}
	if err := db.Create(&failedMessage).Error; err != nil {
		t.Fatalf("create failed-message fixture: %v", err)
	}
	failed, err := EnsureRender(failedMessage.ID)
	if err != nil {
		t.Fatalf("create retryable render: %v", err)
	}
	if err := db.Model(&model.SpeechRenderHistory{}).Where("id = ?", failed.ID).Updates(map[string]interface{}{
		"status":        model.SpeechRenderStatusFailed,
		"error_message": "temporary provider failure",
	}).Error; err != nil {
		t.Fatalf("mark render failed: %v", err)
	}
	requeued, err := EnsureRender(failedMessage.ID)
	if err != nil {
		t.Fatalf("requeue failed render: %v", err)
	}
	if requeued.ID != failed.ID || requeued.Status != model.SpeechRenderStatusPending || requeued.ErrorMessage != "" {
		t.Fatalf("failed history was not requeued cleanly: %+v", requeued)
	}

	var count int64
	if err := db.Model(&model.SpeechRenderHistory{}).Where("message_id = ?", failedMessage.ID).Count(&count).Error; err != nil {
		t.Fatalf("count render histories: %v", err)
	}
	if count != 1 {
		t.Fatalf("render history count = %d, want 1", count)
	}
}

// TestEnsureRenderRejectsHumanMessage keeps the speech API scoped to Agent messages.
func TestEnsureRenderRejectsHumanMessage(t *testing.T) {
	db := installSpeechTestDB(t)
	human := model.Person{Name: "speech-test-human", Type: model.PersonTypeHuman}
	if err := db.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}
	message := model.Message{SessionID: 1, PersonID: human.ID, Content: "Human text."}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	if _, err := EnsureRender(message.ID); err == nil {
		t.Fatal("human message unexpectedly accepted for speech rendering")
	}
}

func TestRenderPersistsAttemptVoiceWhenANewerVersionAppears(t *testing.T) {
	db := installSpeechTestDB(t)
	if err := db.AutoMigrate(&model.TTSRenderer{}, &model.AgentVoice{}); err != nil {
		t.Fatalf("migrate speech configuration: %v", err)
	}
	settings := config.Get()
	oldDataRoot := settings.DataRoot
	settings.DataRoot = t.TempDir()
	t.Cleanup(func() { settings.DataRoot = oldDataRoot })

	adapter := sharedRenderTestAdapter
	registerRenderTestAdapter.Do(func() { renderercore.Register(adapter) })
	agent := model.Person{Name: "attempt-voice-agent", Type: model.PersonTypeAI}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create Agent: %v", err)
	}
	renderer := model.TTSRenderer{Name: "test renderer", Provider: model.TTSProviderCosyVoice, ConnectionConfigJSON: `{}`}
	if err := db.Create(&renderer).Error; err != nil {
		t.Fatalf("create renderer: %v", err)
	}
	sampleAudio := []byte("immutable-sample")
	audioSHA256 := voiceSampleAudioSHA256(sampleAudio)
	relativePath := voiceSampleRelativePath(agent.ID, ".wav", audioSHA256)
	if err := writeVoiceSampleAudio(relativePath, sampleAudio); err != nil {
		t.Fatalf("write voice sample: %v", err)
	}
	firstVoice := model.AgentVoice{
		PersonID:                agent.ID,
		TTSRendererID:           renderer.ID,
		SampleAudioSHA256:       audioSHA256,
		SampleTranscript:        "sample words",
		SampleAudioExtension:    ".wav",
		SampleAudioRelativePath: relativePath,
		SampleLocale:            "en-US",
	}
	if err := db.Create(&firstVoice).Error; err != nil {
		t.Fatalf("create first voice: %v", err)
	}
	message := model.Message{SessionID: 1, PersonID: agent.ID, Content: "rendered message"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	history := model.SpeechRenderHistory{
		MessageID: message.ID,
		Status:    model.SpeechRenderStatusRendering,
	}
	if err := db.Create(&history).Error; err != nil {
		t.Fatalf("create render history: %v", err)
	}
	var secondVoice model.AgentVoice
	adapter.onSynthesize = func(request renderercore.Request) {
		if request.Sample.AudioSHA256 != firstVoice.SampleAudioSHA256 {
			t.Errorf("attempt sample SHA-256 = %q, want %q", request.Sample.AudioSHA256, firstVoice.SampleAudioSHA256)
		}
		secondVoice = firstVoice
		secondVoice.ID = 0
		secondVoice.SampleTranscript = "new version created during synthesis"
		secondVoice.SampleAudioSHA256 = "second-sha256"
		secondVoice.SampleAudioRelativePath = "samples/new-version.wav"
		if err := db.Create(&secondVoice).Error; err != nil {
			t.Errorf("create newer voice during synthesis: %v", err)
		}
	}
	t.Cleanup(func() { adapter.onSynthesize = nil })

	if err := render(t.Context(), &history); err != nil {
		t.Fatalf("render with concurrent voice version: %v", err)
	}
	var saved model.SpeechRenderHistory
	if err := db.First(&saved, history.ID).Error; err != nil {
		t.Fatalf("reload render history: %v", err)
	}
	if saved.Status != model.SpeechRenderStatusSuccess {
		t.Fatalf("render status = %d, want success", saved.Status)
	}
	if saved.AgentVoiceID != firstVoice.ID {
		t.Fatalf("history voice ID = %d, want attempt voice %d", saved.AgentVoiceID, firstVoice.ID)
	}
	latest, err := dops.GetAgentVoiceByPersonID(agent.ID)
	if err != nil {
		t.Fatalf("load latest voice: %v", err)
	}
	if latest.ID != secondVoice.ID {
		t.Fatalf("latest voice ID = %d, want newer version %d", latest.ID, secondVoice.ID)
	}
}

func TestRecoverInterruptedRendersRequeuesOnlyRenderingWork(t *testing.T) {
	db := installSpeechTestDB(t)
	histories := []model.SpeechRenderHistory{
		{MessageID: 1, Status: model.SpeechRenderStatusPending},
		{MessageID: 2, Status: model.SpeechRenderStatusRendering, AttemptCount: 3},
		{MessageID: 3, Status: model.SpeechRenderStatusSuccess, AgentVoiceID: 8, RelativePath: "messages/3/3.mp3"},
	}
	if err := db.Create(&histories).Error; err != nil {
		t.Fatalf("create recovery histories: %v", err)
	}
	if err := recoverInterruptedRenders(); err != nil {
		t.Fatalf("recover interrupted renders: %v", err)
	}
	var recovered []model.SpeechRenderHistory
	if err := db.Order("message_id ASC").Find(&recovered).Error; err != nil {
		t.Fatalf("reload recovery histories: %v", err)
	}
	if recovered[0].Status != model.SpeechRenderStatusPending {
		t.Fatalf("pending history status = %d", recovered[0].Status)
	}
	if recovered[1].Status != model.SpeechRenderStatusPending || recovered[1].AttemptCount != 3 {
		t.Fatalf("interrupted history was not requeued cleanly: %+v", recovered[1])
	}
	if recovered[2].Status != model.SpeechRenderStatusSuccess {
		t.Fatalf("successful history status = %d", recovered[2].Status)
	}
}

func installSpeechTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		database.DB = oldDB
	})
	if err := db.AutoMigrate(&model.Person{}, &model.Message{}, &model.SpeechRenderHistory{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}
