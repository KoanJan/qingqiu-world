package speech

import (
	"bytes"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
)

func TestSaveAgentVoiceCreatesImmutableVersionsAndReusesIdenticalConfiguration(t *testing.T) {
	db := installSpeechTestDB(t)
	if err := db.AutoMigrate(&model.AgentVoice{}); err != nil {
		t.Fatalf("migrate AgentVoice: %v", err)
	}
	settings := config.Get()
	oldDataRoot := settings.DataRoot
	settings.DataRoot = t.TempDir()
	t.Cleanup(func() { settings.DataRoot = oldDataRoot })
	agent := model.Person{Name: "versioned-voice-agent", Type: model.PersonTypeAI}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create Agent: %v", err)
	}
	initial := model.AgentVoice{PersonID: agent.ID, RendererVoiceRef: "provider-cache"}
	if err := db.Create(&initial).Error; err != nil {
		t.Fatalf("create initial renderer-only voice: %v", err)
	}

	firstUpdate := VoiceBindingUpdate{
		RendererID:       0,
		SampleTranscript: "first transcript",
		SampleLocale:     "en-US",
		SampleAudio:      testVoiceSample("same audio"),
	}
	first, err := SaveAgentVoice(t.Context(), agent.ID, firstUpdate)
	if err != nil {
		t.Fatalf("save first voice: %v", err)
	}
	if first.RendererVoiceRef != "" {
		t.Fatalf("new voice inherited provider-derived ref %q", first.RendererVoiceRef)
	}
	identical, err := SaveAgentVoice(t.Context(), agent.ID, VoiceBindingUpdate{
		RendererID:       0,
		SampleTranscript: "first transcript",
		SampleLocale:     "en-US",
		SampleAudio:      testVoiceSample("same audio"),
	})
	if err != nil {
		t.Fatalf("save identical voice: %v", err)
	}
	if identical.ID != first.ID {
		t.Fatalf("identical save created version %d, want existing %d", identical.ID, first.ID)
	}

	second, err := SaveAgentVoice(t.Context(), agent.ID, VoiceBindingUpdate{
		RendererID:       0,
		SampleTranscript: "revised transcript",
		SampleLocale:     "en-US",
	})
	if err != nil {
		t.Fatalf("save changed voice: %v", err)
	}
	if second.ID <= first.ID {
		t.Fatalf("new voice ID = %d, want greater than %d", second.ID, first.ID)
	}
	var oldVersion model.AgentVoice
	if err := db.First(&oldVersion, first.ID).Error; err != nil {
		t.Fatalf("reload first voice: %v", err)
	}
	if oldVersion.SampleAudioSHA256 != first.SampleAudioSHA256 || oldVersion.SampleTranscript != "first transcript" {
		t.Fatal("inserting a new voice mutated the previous version")
	}
	localized, err := SaveAgentVoice(t.Context(), agent.ID, VoiceBindingUpdate{
		RendererID:       0,
		SampleTranscript: second.SampleTranscript,
		SampleLocale:     "ja-JP",
	})
	if err != nil {
		t.Fatalf("save locale-only voice change: %v", err)
	}
	if localized.SampleAudioSHA256 != second.SampleAudioSHA256 || localized.SampleAudioRelativePath != second.SampleAudioRelativePath {
		t.Fatal("metadata-only save did not preserve the current audio")
	}
	if localized.SampleLocale != "ja-JP" {
		t.Fatalf("locale-only save locale = %q, want ja-JP", localized.SampleLocale)
	}
	latest, err := dops.GetAgentVoiceByPersonID(agent.ID)
	if err != nil {
		t.Fatalf("load latest voice: %v", err)
	}
	if latest.ID != localized.ID {
		t.Fatalf("latest voice ID = %d, want %d", latest.ID, localized.ID)
	}
	var count int64
	if err := db.Model(&model.AgentVoice{}).Where("person_id = ?", agent.ID).Count(&count).Error; err != nil {
		t.Fatalf("count voice versions: %v", err)
	}
	if count != 4 {
		t.Fatalf("voice version count = %d, want 4", count)
	}
}

func TestVoiceVersionSeparatesConfigurationFromDerivedState(t *testing.T) {
	current := model.AgentVoice{
		PersonID:                42,
		TTSRendererID:           9,
		RendererVoiceRef:        "provider-cache",
		SampleAudioSHA256:       "old-sha256",
		SampleTranscript:        "old transcript",
		SampleAudioExtension:    ".wav",
		SampleAudioRelativePath: "samples/42/old.wav",
		SampleLocale:            "en-US",
	}
	sameConfig := current
	sameConfig.RendererVoiceRef = "another-provider-cache"
	if !sameVoiceConfiguration(&current, &sameConfig) {
		t.Fatal("provider-derived runtime state changed the voice configuration identity")
	}
	next := current
	next.RendererVoiceRef = ""
	next.SampleAudioSHA256 = "new-sha256"
	next.SampleTranscript = "new transcript"
	next.SampleAudioRelativePath = "samples/42/new.wav"
	if next.TTSRendererID != current.TTSRendererID {
		t.Fatal("new voice did not preserve an unchanged renderer binding")
	}
	if sameVoiceConfiguration(&current, &next) {
		t.Fatal("changed sample was treated as the same voice configuration")
	}
}

func testVoiceSample(content string) *VoiceSampleUpload {
	audio := append([]byte("RIFF\x04\x00\x00\x00WAVE"), []byte(content)...)
	return &VoiceSampleUpload{
		Filename: "sample.wav",
		Audio:    bytes.NewReader(audio),
	}
}
