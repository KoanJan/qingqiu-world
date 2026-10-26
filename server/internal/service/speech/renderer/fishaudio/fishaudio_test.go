package fishaudio

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ugorji/go/codec"

	"qingqiu-world-server/internal/model"
	renderercore "qingqiu-world-server/internal/service/speech/renderer"
)

func TestFishAudioConnectionSchemaMatchesSupportedRequestContract(t *testing.T) {
	type schemaField struct {
		Enum    []string `json:"enum"`
		Default string   `json:"default"`
		Minimum float64  `json:"minimum"`
		Maximum float64  `json:"maximum"`
	}
	var schema struct {
		Properties map[string]schemaField `json:"properties"`
	}
	if err := json.Unmarshal([]byte(fishAudioConnectionConfigSchema()), &schema); err != nil {
		t.Fatalf("decode Fish Audio schema: %v", err)
	}
	if got := schema.Properties["model"].Enum; !reflect.DeepEqual(got, modelIDs()) {
		t.Fatalf("model enum = %v, want %v", got, modelIDs())
	}
	if schema.Properties["latency"].Default != "normal" {
		t.Fatalf("latency default = %q, want normal", schema.Properties["latency"].Default)
	}
	for _, name := range []string{"temperature", "top_p"} {
		field := schema.Properties[name]
		if field.Minimum != 0 || field.Maximum != 1 {
			t.Fatalf("%s range = [%v,%v], want [0,1]", name, field.Minimum, field.Maximum)
		}
	}
	if isFishAudioModel("s1") || isFishAudioModel("drama-3-preview") {
		t.Fatal("models without verified S2 free-form expression semantics must not be selectable")
	}
}

// TestFishAudioDirectReferenceEncoding verifies that the direct-sample Adapter
// produces the binary reference shape required by the Fish Audio API.
func TestFishAudioDirectReferenceEncoding(t *testing.T) {
	adapter := New()
	renderer := model.TTSRenderer{
		Provider:             model.TTSProviderFishAudio,
		ConnectionConfigJSON: `{"api_key":"test-key","model":"s2.1-pro","output_format":"mp3"}`,
	}
	if err := adapter.Validate(t.Context(), renderer); err != nil {
		t.Fatalf("validate direct-reference renderer: %v", err)
	}
	ref, err := adapter.PrepareVoice(t.Context(), renderer, renderercore.VoiceSample{})
	if err != nil {
		t.Fatalf("prepare direct reference: %v", err)
	}
	if ref != "" {
		t.Fatalf("direct-reference adapter returned renderer voice ref %q", ref)
	}

	payload, err := encodeFishAudioRequest(map[string]interface{}{
		"text": "hello",
		"references": []fishAudioReference{{
			Audio: []byte{1, 2, 3},
			Text:  "sample transcript",
		}},
	})
	if err != nil {
		t.Fatalf("encode MessagePack payload: %v", err)
	}
	var decoded struct {
		Text       string               `codec:"text"`
		References []fishAudioReference `codec:"references"`
	}
	handle := codec.MsgpackHandle{}
	if err := codec.NewDecoder(bytes.NewReader(payload), &handle).Decode(&decoded); err != nil {
		t.Fatalf("decode MessagePack payload: %v", err)
	}
	if decoded.Text != "hello" {
		t.Fatalf("text = %q, want hello", decoded.Text)
	}
	if len(decoded.References) != 1 {
		t.Fatalf("references = %#v, want one sample", decoded.References)
	}
	sample := decoded.References[0]
	if !bytes.Equal(sample.Audio, []byte{1, 2, 3}) || sample.Text != "sample transcript" {
		t.Fatalf("decoded sample = %#v", sample)
	}
}

func TestFishAudioRequestSnapshotMatchesSentExpressionAndParameters(t *testing.T) {
	temperature := 0.35
	topP := 0.8
	cfg := fishAudioConnectionConfig{
		APIKey:       "must-not-be-persisted",
		Model:        "s2.1-pro",
		OutputFormat: "mp3",
		Latency:      "balanced",
		Temperature:  &temperature,
		TopP:         &topP,
	}
	request := renderercore.Request{
		Content:               "Everything will be okay.",
		ExpressionInstruction: " [gentle]\n and reassuring ",
		Sample:                renderercore.VoiceSample{AudioSHA256: "voice-audio-sha256"},
	}
	body, snapshotJSON, err := buildFishAudioRequest(cfg, request, fishAudioReference{
		Audio: []byte{1, 2, 3},
		Text:  "reference transcript",
	})
	if err != nil {
		t.Fatalf("build Fish Audio request: %v", err)
	}
	if body["text"] != "[gentle and reassuring] Everything will be okay." {
		t.Fatalf("sent text = %q", body["text"])
	}

	var snapshot fishAudioRequestSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		t.Fatalf("decode request snapshot: %v", err)
	}
	if snapshot.Text != body["text"] || snapshot.ExpressionCue != "gentle and reassuring" {
		t.Fatalf("snapshot does not match sent expression: %+v", snapshot)
	}
	if snapshot.Model != cfg.Model || snapshot.Format != body["format"] || snapshot.Latency != body["latency"] {
		t.Fatalf("snapshot does not match sent parameters: %+v", snapshot)
	}
	if snapshot.Provider != model.TTSProviderFishAudio || snapshot.SampleAudioSHA256 != "voice-audio-sha256" {
		t.Fatalf("snapshot lost provider or sample identity: %+v", snapshot)
	}
	if snapshot.Temperature == nil || *snapshot.Temperature != temperature || snapshot.TopP == nil || *snapshot.TopP != topP {
		t.Fatalf("snapshot lost sampling parameters: %+v", snapshot)
	}
	if bytes.Contains([]byte(snapshotJSON), []byte(cfg.APIKey)) {
		t.Fatal("request snapshot contains API key")
	}
}

func TestFishAudioRequestUsesProviderSamplingDefaultsWhenUnset(t *testing.T) {
	body, _, err := buildFishAudioRequest(fishAudioConnectionConfig{
		Model: "s2.1-pro",
	}, renderercore.Request{Content: "Hello"}, fishAudioReference{})
	if err != nil {
		t.Fatalf("build Fish Audio request: %v", err)
	}
	if _, exists := body["temperature"]; exists {
		t.Fatal("temperature should be omitted when it was not configured")
	}
	if _, exists := body["top_p"]; exists {
		t.Fatal("top_p should be omitted when it was not configured")
	}
	if body["latency"] != "normal" {
		t.Fatalf("latency = %v, want provider default normal", body["latency"])
	}
}
