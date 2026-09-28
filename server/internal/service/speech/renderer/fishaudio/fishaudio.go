// Package fishaudio implements the Fish Audio rendering adapter.
package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ugorji/go/codec"

	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/speech/renderer"
)

const fishAudioEndpoint = "https://api.fish.audio/v1/tts"

// modelSpec is the Fish Audio model capability boundary. Only models that
// support S2 free-form square-bracket instructions can satisfy the application's
// provider-neutral expression contract. New model families must declare and
// implement their own translation before becoming selectable.
type modelSpec struct {
	ID string
}

var supportedModelSpecs = []modelSpec{
	{ID: "s2.1-pro"},
	{ID: "s2.1-pro-free"},
	{ID: "s2-pro"},
}

var supportedOutputFormats = []string{"mp3", "wav", "opus"}
var supportedLatencyModes = []string{"normal", "balanced", "low"}

// fishAudioConnectionConfig is the Provider-owned shape stored in Renderer JSON.
type fishAudioConnectionConfig struct {
	APIKey       string   `json:"api_key"`
	Model        string   `json:"model"`
	OutputFormat string   `json:"output_format"`
	Latency      string   `json:"latency"`
	Temperature  *float64 `json:"temperature"`
	TopP         *float64 `json:"top_p"`
}

// fishAudioRequestSnapshot records the non-secret, semantically relevant
// request material used for a successful synthesis. Reference audio bytes are
// represented by their immutable SHA-256 instead of being duplicated.
type fishAudioRequestSnapshot struct {
	Provider              model.TTSProvider `json:"provider"`
	Endpoint              string            `json:"endpoint"`
	Model                 string            `json:"model"`
	ContentType           string            `json:"content_type"`
	Text                  string            `json:"text"`
	ExpressionInstruction string            `json:"expression_instruction"`
	ExpressionCue         string            `json:"expression_cue"`
	SampleAudioSHA256     string            `json:"sample_audio_sha256"`
	Format                string            `json:"format"`
	Latency               string            `json:"latency"`
	Normalize             bool              `json:"normalize"`
	Temperature           *float64          `json:"temperature,omitempty"`
	TopP                  *float64          `json:"top_p,omitempty"`
}

// Adapter sends the immutable AgentVoice sample with every synthesis
// request using Fish Audio's instant-clone mode: the canonical sample travels
// inline in the `references` field of each MessagePack request, and no
// provider-owned voice resource is ever registered.
//
// Decision recorded against the official documentation: Fish Audio also offers a
// persistent model (voices.create returning a reusable reference_id) and its
// docs recommend that mode "to reuse a voice across many requests". Instant
// clone is kept so the sample remains one locally owned canonical resource with
// no provider-side lifecycle to create, poll (created -> trained), invalidate on
// connection changes, or clean up. The cost is resending the sample on every
// synthesis, bounded by the 32 MiB sample limit. Consequence: RendererVoiceRef
// stays empty for this adapter.
//
// The official SDK github.com/fishaudio/fish-audio-go is deliberately not used.
// Its HTTP synthesis path (TTS.Convert / TTS.Stream) marshals the request with
// encoding/json, so ReferenceAudio.Audio is base64-encoded into the body; it
// only speaks MessagePack on the WebSocket streaming path, which does not fit
// whole-utterance rendering. That would trade the documented binary attachment
// for base64 overhead on every request. The SDK also forces float64 for
// temperature/top_p (so an explicit 0 cannot be expressed) and hides the
// upstream response headers this adapter validates.
type adapter struct{}

var _ renderer.Adapter = (*adapter)(nil)

// New returns the Fish Audio implementation behind the provider-neutral
// renderer contract; callers cannot depend on provider implementation details.
func New() renderer.Adapter { return &adapter{} }

func (a *adapter) Provider() model.TTSProvider { return model.TTSProviderFishAudio }

func (a *adapter) Definition() model.TTSProviderDefinition {
	return model.TTSProviderDefinition{
		Provider:                   model.TTSProviderFishAudio,
		Name:                       "Fish Audio",
		ConnectionConfigJSONSchema: fishAudioConnectionConfigSchema(),
		TermsURL:                   "https://fish.audio/terms/",
		Description:                "Fish Audio zero-shot voice cloning with direct reference-sample delivery",
	}
}

func (a *adapter) Validate(_ context.Context, configuredRenderer model.TTSRenderer) error {
	if configuredRenderer.Provider != model.TTSProviderFishAudio {
		return fmt.Errorf("Fish Audio adapter received provider %d", configuredRenderer.Provider)
	}
	cfg, err := decodeFishAudioConfig(configuredRenderer)
	if err != nil {
		return err
	}
	if cfg.APIKey == "" || cfg.Model == "" {
		return fmt.Errorf("Fish Audio api_key and model are required")
	}
	if !isFishAudioModel(cfg.Model) {
		return fmt.Errorf("unsupported Fish Audio model %q", cfg.Model)
	}
	if cfg.OutputFormat != "" && !isFishAudioOutputFormat(cfg.OutputFormat) {
		return fmt.Errorf("unsupported Fish Audio output_format %q", cfg.OutputFormat)
	}
	if cfg.Latency != "" && !contains(supportedLatencyModes, cfg.Latency) {
		return fmt.Errorf("unsupported Fish Audio latency %q", cfg.Latency)
	}
	if cfg.Temperature != nil && (*cfg.Temperature < 0 || *cfg.Temperature > 1) {
		return fmt.Errorf("Fish Audio temperature must be between 0 and 1")
	}
	if cfg.TopP != nil && (*cfg.TopP < 0 || *cfg.TopP > 1) {
		return fmt.Errorf("Fish Audio top_p must be between 0 and 1")
	}
	return nil
}

// PrepareVoice validates the direct-reference mode and returns no provider
// reference: the canonical sample is supplied to Synthesize on every request by
// design (see the adapter comment), so there is nothing to cache here.
func (a *adapter) PrepareVoice(ctx context.Context, configuredRenderer model.TTSRenderer, _ renderer.VoiceSample) (string, error) {
	if err := a.Validate(ctx, configuredRenderer); err != nil {
		return "", err
	}
	return "", nil
}

func (a *adapter) Synthesize(ctx context.Context, request renderer.Request) (renderer.Result, error) {
	cfg, err := decodeFishAudioConfig(request.Renderer)
	if err != nil {
		return renderer.Result{}, err
	}
	if err := a.Validate(ctx, request.Renderer); err != nil {
		return renderer.Result{}, err
	}
	reference, err := loadFishAudioReference(request.Sample)
	if err != nil {
		return renderer.Result{}, err
	}
	body, requestSnapshot, err := buildFishAudioRequest(cfg, request, reference)
	if err != nil {
		return renderer.Result{}, err
	}
	requestSnapshot, err = renderer.ValidateRequestSnapshot(requestSnapshot)
	if err != nil {
		return renderer.Result{}, fmt.Errorf("validate Fish Audio request snapshot: %w", err)
	}
	payload, err := encodeFishAudioRequest(body)
	if err != nil {
		return renderer.Result{}, err
	}
	format := fishAudioOutputFormat(cfg.OutputFormat)
	requestContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, fishAudioEndpoint, bytes.NewReader(payload))
	if err != nil {
		return renderer.Result{}, fmt.Errorf("build Fish Audio request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	httpRequest.Header.Set("Content-Type", "application/msgpack")
	httpRequest.Header.Set("model", cfg.Model)
	response, err := (&http.Client{}).Do(httpRequest)
	if err != nil {
		return renderer.Result{}, fmt.Errorf("send Fish Audio request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		errorBody, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if readErr != nil {
			return renderer.Result{}, fmt.Errorf("read Fish Audio error response: %w", readErr)
		}
		return renderer.Result{}, fmt.Errorf("Fish Audio status %d: %s", response.StatusCode, strings.TrimSpace(string(errorBody)))
	}
	audio, err := io.ReadAll(io.LimitReader(response.Body, renderer.MaxAudioBytes+1))
	if err != nil {
		return renderer.Result{}, fmt.Errorf("read Fish Audio audio: %w", err)
	}
	if len(audio) == 0 {
		return renderer.Result{}, fmt.Errorf("Fish Audio returned empty audio")
	}
	if len(audio) > renderer.MaxAudioBytes {
		return renderer.Result{}, fmt.Errorf("Fish Audio returned audio larger than 128 MiB")
	}
	mimeType := response.Header.Get("Content-Type")
	if mimeType != "" {
		mediaType, _, parseErr := mime.ParseMediaType(mimeType)
		if parseErr != nil || (!strings.HasPrefix(mediaType, "audio/") && mediaType != "application/octet-stream") {
			return renderer.Result{}, fmt.Errorf("Fish Audio returned unexpected content type %q", mimeType)
		}
	}
	return renderer.Result{
		Bytes:                  audio,
		MIMEType:               mimeType,
		Format:                 format,
		AdapterRequestSnapshot: requestSnapshot,
	}, nil
}

func buildFishAudioRequest(cfg fishAudioConnectionConfig, request renderer.Request, reference fishAudioReference) (map[string]interface{}, string, error) {
	expressionCue := fishAudioExpressionCue(request.ExpressionInstruction)
	renderText := fishAudioRenderText(request.Content, expressionCue)
	format := fishAudioOutputFormat(cfg.OutputFormat)
	latency := fishAudioLatency(cfg.Latency)
	body := map[string]interface{}{
		"text":       renderText,
		"references": []fishAudioReference{reference},
		"format":     format,
		"latency":    latency,
		"normalize":  true,
	}
	// Sampling parameters are sent only when the user explicitly configured
	// them: Fish Audio documents its own defaults as well-tuned, so an absent
	// field lets the provider keep tuning them instead of pinning our guess.
	if cfg.Temperature != nil {
		body["temperature"] = *cfg.Temperature
	}
	if cfg.TopP != nil {
		body["top_p"] = *cfg.TopP
	}
	requestSnapshot, err := json.Marshal(fishAudioRequestSnapshot{
		Provider:              model.TTSProviderFishAudio,
		Endpoint:              fishAudioEndpoint,
		Model:                 cfg.Model,
		ContentType:           "application/msgpack",
		Text:                  renderText,
		ExpressionInstruction: request.ExpressionInstruction,
		ExpressionCue:         expressionCue,
		SampleAudioSHA256:     request.Sample.AudioSHA256,
		Format:                format,
		Latency:               latency,
		Normalize:             true,
		Temperature:           cfg.Temperature,
		TopP:                  cfg.TopP,
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode Fish Audio request snapshot: %w", err)
	}
	return body, string(requestSnapshot), nil
}

type fishAudioReference struct {
	Audio []byte `codec:"audio"`
	Text  string `codec:"text"`
}

func loadFishAudioReference(sample renderer.VoiceSample) (fishAudioReference, error) {
	extension := strings.ToLower(sample.Extension)
	if extension != ".wav" && extension != ".mp3" && extension != ".flac" {
		return fishAudioReference{}, fmt.Errorf("Fish Audio does not accept reference format %q", extension)
	}
	if len(sample.Audio) == 0 || len(sample.Audio) > 32<<20 {
		return fishAudioReference{}, fmt.Errorf("Fish Audio reference audio must be between 1 byte and 32 MiB")
	}
	return fishAudioReference{Audio: sample.Audio, Text: sample.Transcript}, nil
}

func encodeFishAudioRequest(value interface{}) ([]byte, error) {
	var payload bytes.Buffer
	handle := codec.MsgpackHandle{}
	if err := codec.NewEncoder(&payload, &handle).Encode(value); err != nil {
		return nil, fmt.Errorf("encode Fish Audio MessagePack request: %w", err)
	}
	return payload.Bytes(), nil
}

// fishAudioExpressionCueEscaper removes characters that cannot travel inside a
// single-line bracket cue: brackets delimit the cue itself and line breaks would
// split it in two.
var fishAudioExpressionCueEscaper = strings.NewReplacer("[", " ", "]", " ")

// fishAudioRenderText prefixes the spoken content with the Message's
// provider-neutral expression instruction.
//
// Fish Audio's S2 family accepts free-form natural-language bracket cues and
// documents them as not limited to a fixed set of tags, so the instruction is
// used directly as the provider's own control language. Compiling it through an
// LLM would only be required for a provider that cannot accept free-form text;
// keeping the instruction provider-neutral is what stops TTS concepts from
// leaking into the Message layer.
func fishAudioExpressionCue(instruction string) string {
	cue := strings.Join(strings.Fields(fishAudioExpressionCueEscaper.Replace(instruction)), " ")
	if cue != strings.TrimSpace(instruction) {
		raw := instruction
		if len(raw) > 120 {
			raw = raw[:120] + "..."
		}
		applogger.Debug("sanitized Fish Audio expression cue", "raw", raw, "cue", cue)
	}
	return cue
}

func fishAudioRenderText(content, expressionCue string) string {
	if expressionCue == "" {
		return content
	}
	return "[" + expressionCue + "] " + content
}

func decodeFishAudioConfig(renderer model.TTSRenderer) (fishAudioConnectionConfig, error) {
	var cfg fishAudioConnectionConfig
	decoder := json.NewDecoder(strings.NewReader(renderer.ConnectionConfigJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode Fish Audio connection config: %w", err)
	}
	if decoder.More() {
		return cfg, fmt.Errorf("decode Fish Audio connection config: multiple JSON values")
	}
	return cfg, nil
}

func isFishAudioModel(modelID string) bool {
	for _, spec := range supportedModelSpecs {
		if spec.ID == modelID {
			return true
		}
	}
	return false
}

func isFishAudioOutputFormat(format string) bool {
	return contains(supportedOutputFormats, format)
}

func fishAudioOutputFormat(format string) string {
	if isFishAudioOutputFormat(format) {
		return format
	}
	return "mp3"
}

// fishAudioLatency falls back to the current provider default. Normal favors
// quality; balanced and low trade quality for lower latency.
func fishAudioLatency(latency string) string {
	if contains(supportedLatencyModes, latency) {
		return latency
	}
	return "normal"
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func modelIDs() []string {
	ids := make([]string, 0, len(supportedModelSpecs))
	for _, spec := range supportedModelSpecs {
		ids = append(ids, spec.ID)
	}
	return ids
}

// fishAudioConnectionConfigSchema describes only verified /v1/tts inputs used
// by this adapter. Optional sampling fields are omitted from requests when the
// user leaves them unset, preserving Fish Audio's current defaults.
func fishAudioConnectionConfigSchema() string {
	modelsJSON, err := json.Marshal(modelIDs())
	if err != nil {
		panic(fmt.Sprintf("encode Fish Audio model schema: %v", err))
	}
	formatsJSON, err := json.Marshal(supportedOutputFormats)
	if err != nil {
		panic(fmt.Sprintf("encode Fish Audio format schema: %v", err))
	}
	latenciesJSON, err := json.Marshal(supportedLatencyModes)
	if err != nil {
		panic(fmt.Sprintf("encode Fish Audio latency schema: %v", err))
	}
	return fmt.Sprintf(`{
  "type": "object",
  "required": ["api_key", "model"],
  "properties": {
    "api_key": {"type": "string", "format": "password", "x-ui": {"label": {"en": "API Key", "zh-CN": "API 密钥"}, "description": {"en": "Your Fish Audio API key", "zh-CN": "Fish Audio 的 API 密钥"}}},
    "model": {"type": "string", "enum": %s, "default": "s2.1-pro", "x-ui": {"label": {"en": "Model", "zh-CN": "模型"}, "description": {"en": "S2 model compatible with free-form expression instructions", "zh-CN": "支持自由文本表达指令的 S2 模型"}}},
    "output_format": {"type": "string", "enum": %s, "default": "mp3", "x-ui": {"label": {"en": "Output Format", "zh-CN": "输出格式"}, "description": {"en": "Provider output format supported by browser playback", "zh-CN": "供应商支持且可由浏览器播放的输出格式"}}},
    "latency": {"type": "string", "enum": %s, "default": "normal", "x-ui": {"label": {"en": "Latency Mode", "zh-CN": "延迟模式"}, "description": {"en": "Normal gives best quality; balanced and low reduce latency", "zh-CN": "普通模式质量最佳；均衡和低延迟模式会降低等待时间"}}},
    "temperature": {"type": "number", "minimum": 0, "maximum": 1, "x-ui": {"label": {"en": "Expressiveness Variation", "zh-CN": "表现力变化"}, "description": {"en": "Optional expressiveness control; empty uses Fish Audio's default 0.7", "zh-CN": "可选的表现力控制；留空使用 Fish Audio 默认值 0.7"}}},
    "top_p": {"type": "number", "minimum": 0, "maximum": 1, "x-ui": {"label": {"en": "Sampling Diversity", "zh-CN": "采样多样性"}, "description": {"en": "Optional nucleus-sampling diversity; empty uses Fish Audio's default 0.7", "zh-CN": "可选的核采样多样性；留空使用 Fish Audio 默认值 0.7"}}}
  }
}`, modelsJSON, formatsJSON, latenciesJSON)
}
