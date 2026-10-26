// Package renderer defines the provider-neutral speech rendering boundary.
package renderer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// VoiceSample is the provider-neutral view of one verified immutable voice
// sample. Storage is owned by the speech service; adapters receive bytes and
// metadata so they never depend on application file layout.
type VoiceSample struct {
	Audio       []byte
	Extension   string
	AudioSHA256 string
	Transcript  string
	Locale      string
}

// Result is the output contract returned by one provider adapter. Along with
// audio, it carries a secret-free snapshot of the request that produced it.
// Registration-based adapters include the exact provider voice reference used
// in that snapshot because the derived cache may be repaired independently.
type Result struct {
	Bytes                  []byte
	MIMEType               string
	Format                 string
	AdapterRequestSnapshot string
}

// Request carries only stable domain inputs into a provider adapter.
type Request struct {
	Content               string
	ExpressionInstruction string
	Sample                VoiceSample
	RendererVoiceRef      string
	Renderer              model.TTSRenderer
}

// Adapter isolates one provider's protocol from speech delivery orchestration.
type Adapter interface {
	Provider() model.TTSProvider
	Definition() model.TTSProviderDefinition
	Validate(ctx context.Context, renderer model.TTSRenderer) error
	PrepareVoice(ctx context.Context, renderer model.TTSRenderer, sample VoiceSample) (string, error)
	Synthesize(ctx context.Context, request Request) (Result, error)
}

var registry = struct {
	sync.RWMutex
	adapters map[model.TTSProvider]Adapter
}{adapters: make(map[model.TTSProvider]Adapter)}

// Register registers one unique provider implementation at startup.
func Register(adapter Adapter) {
	registry.Lock()
	defer registry.Unlock()
	provider := adapter.Provider()
	if _, exists := registry.adapters[provider]; exists {
		panic(fmt.Sprintf("speech adapter already registered for provider %d", provider))
	}
	registry.adapters[provider] = adapter
}

// Get resolves the only adapter for a registered provider.
func Get(provider model.TTSProvider) (Adapter, error) {
	registry.RLock()
	adapter := registry.adapters[provider]
	registry.RUnlock()
	if adapter == nil {
		return nil, fmt.Errorf("no speech adapter registered for provider %d", provider)
	}
	return adapter, nil
}

// Validate validates one persisted renderer using its provider adapter.
func Validate(ctx context.Context, configuredRenderer model.TTSRenderer) error {
	adapter, err := Get(configuredRenderer.Provider)
	if err != nil {
		return err
	}
	return adapter.Validate(ctx, configuredRenderer)
}

// SyncProviderDefinitions synchronizes registered adapter contracts into the
// provider directory after the application composition root has registered all
// concrete adapters.
func SyncProviderDefinitions() {
	registry.RLock()
	adapters := make([]Adapter, 0, len(registry.adapters))
	for _, adapter := range registry.adapters {
		adapters = append(adapters, adapter)
	}
	registry.RUnlock()
	providers := make([]model.TTSProvider, 0, len(adapters))
	for _, adapter := range adapters {
		providers = append(providers, adapter.Provider())
	}
	if err := dops.DeleteUnregisteredTTSProviderDefinitions(providers); err != nil {
		applogger.Error("failed to delete stale TTS provider definitions", "error", err)
		panic(err)
	}
	for _, adapter := range adapters {
		definition := adapter.Definition()
		if err := validateProviderDefinition(adapter, definition); err != nil {
			applogger.Error("invalid TTS provider definition", "provider", adapter.Provider(), "error", err)
			panic(err)
		}
		if err := dops.UpsertTTSProviderDefinition(&definition); err != nil {
			applogger.Error("failed to synchronize TTS provider definition", "provider", definition.Provider, "error", err)
			panic(err)
		}
	}
}

// validateProviderDefinition ensures every selectable adapter supplies the
// fixed terms link required before the application sends voice references.
func validateProviderDefinition(adapter Adapter, definition model.TTSProviderDefinition) error {
	if definition.Provider != adapter.Provider() || definition.Provider == model.TTSProviderUnknown {
		return fmt.Errorf("provider definition does not match registered adapter")
	}
	if definition.Name == "" || definition.ConnectionConfigJSONSchema == "" || definition.TermsURL == "" {
		return fmt.Errorf("provider definition requires name, configuration schema, and terms URL")
	}
	if err := validateConnectionConfigSchema(definition.ConnectionConfigJSONSchema); err != nil {
		return fmt.Errorf("provider definition configuration schema: %w", err)
	}
	termsURL, err := url.ParseRequestURI(definition.TermsURL)
	if err != nil || (termsURL.Scheme != "https" && termsURL.Scheme != "http") || termsURL.Host == "" {
		return fmt.Errorf("provider definition terms URL must be an absolute HTTP(S) URL")
	}
	return nil
}

// providerConfigSchema contains the subset of JSON Schema required to ensure
// that protocol parameter keys never become UI labels. x-ui is a presentation
// extension whose localized labels are owned by each provider adapter.
type providerConfigSchema struct {
	Type       string                               `json:"type"`
	Properties map[string]providerConfigSchemaField `json:"properties"`
}

type providerConfigSchemaField struct {
	Type       string                               `json:"type"`
	Format     string                               `json:"format"`
	Properties map[string]providerConfigSchemaField `json:"properties"`
	UI         providerConfigFieldUI                `json:"x-ui"`
}

type providerConfigFieldUI struct {
	Label map[string]string `json:"label"`
}

// validateConnectionConfigSchema rejects provider schemas that would make the
// frontend expose raw API parameter names such as top_p to end users.
func validateConnectionConfigSchema(encoded string) error {
	var schema providerConfigSchema
	if err := json.Unmarshal([]byte(encoded), &schema); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if schema.Type != "object" || len(schema.Properties) == 0 {
		return fmt.Errorf("root must be an object with properties")
	}
	return validateConnectionConfigFields(schema.Properties, "")
}

func validateConnectionConfigFields(fields map[string]providerConfigSchemaField, parent string) error {
	for key, field := range fields {
		path := key
		if parent != "" {
			path = parent + "." + key
		}
		if strings.TrimSpace(field.Type) == "" {
			return fmt.Errorf("field %q is missing a type", path)
		}
		if strings.TrimSpace(field.UI.Label["en"]) == "" {
			return fmt.Errorf("field %q requires x-ui.label.en", path)
		}
		if field.Type == "object" {
			if len(field.Properties) == 0 {
				return fmt.Errorf("object field %q requires properties", path)
			}
			if err := validateConnectionConfigFields(field.Properties, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// PublicConnectionConfig removes every provider-declared password field while
// reporting which write-only paths already have a value. The API can therefore
// support edits without disclosing or inventing credential placeholders.
func PublicConnectionConfig(definition model.TTSProviderDefinition, encoded string) (map[string]interface{}, []string, error) {
	schema, err := decodeProviderConfigSchema(definition.ConnectionConfigJSONSchema)
	if err != nil {
		return nil, nil, err
	}
	config, err := decodeConfigObject(encoded)
	if err != nil {
		return nil, nil, err
	}
	configuredSecrets := make([]string, 0)
	removeSecretFields(config, schema.Properties, nil, &configuredSecrets)
	sort.Strings(configuredSecrets)
	return config, configuredSecrets, nil
}

// MergeConnectionConfig applies an edit payload to the current configuration.
// Missing or empty password fields preserve the stored value; every other
// field comes from the submitted declarative configuration.
func MergeConnectionConfig(definition model.TTSProviderDefinition, currentEncoded, updateEncoded string) (string, error) {
	schema, err := decodeProviderConfigSchema(definition.ConnectionConfigJSONSchema)
	if err != nil {
		return "", err
	}
	current, err := decodeConfigObject(currentEncoded)
	if err != nil {
		return "", fmt.Errorf("decode current connection config: %w", err)
	}
	update, err := decodeConfigObject(updateEncoded)
	if err != nil {
		return "", fmt.Errorf("decode updated connection config: %w", err)
	}
	preserveSecretFields(update, current, schema.Properties)
	encoded, err := json.Marshal(update)
	if err != nil {
		return "", fmt.Errorf("encode updated connection config: %w", err)
	}
	return string(encoded), nil
}

// decodeProviderConfigSchema reads the provider-owned schema subset used for
// secret handling.
func decodeProviderConfigSchema(encoded string) (providerConfigSchema, error) {
	var schema providerConfigSchema
	if err := json.Unmarshal([]byte(encoded), &schema); err != nil {
		return schema, fmt.Errorf("decode provider configuration schema: %w", err)
	}
	return schema, nil
}

// decodeConfigObject preserves JSON number precision while enforcing an object
// root for renderer configuration.
func decodeConfigObject(encoded string) (map[string]interface{}, error) {
	var config map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil || config == nil {
		if err == nil {
			err = fmt.Errorf("root is not an object")
		}
		return nil, err
	}
	return config, nil
}

// removeSecretFields mutates a public copy and records only configured paths.
func removeSecretFields(config map[string]interface{}, fields map[string]providerConfigSchemaField, parent []string, configured *[]string) {
	for key, field := range fields {
		path := append(append([]string(nil), parent...), key)
		if field.Format == "password" {
			if value, exists := config[key]; exists && fmt.Sprint(value) != "" {
				*configured = append(*configured, strings.Join(path, "."))
			}
			delete(config, key)
			continue
		}
		if field.Type == "object" {
			if nested, ok := config[key].(map[string]interface{}); ok {
				removeSecretFields(nested, field.Properties, path, configured)
			}
		}
	}
}

// preserveSecretFields carries current credentials into an edit payload only
// when the matching password input is absent or empty.
func preserveSecretFields(update, current map[string]interface{}, fields map[string]providerConfigSchemaField) {
	for key, field := range fields {
		if field.Format == "password" {
			value, exists := update[key]
			if (!exists || value == "") && current[key] != nil {
				update[key] = current[key]
			}
			continue
		}
		if field.Type != "object" {
			continue
		}
		updateNested, updateOK := update[key].(map[string]interface{})
		currentNested, currentOK := current[key].(map[string]interface{})
		if !updateOK {
			updateNested = make(map[string]interface{})
			update[key] = updateNested
		}
		if !currentOK {
			currentNested = make(map[string]interface{})
		}
		preserveSecretFields(updateNested, currentNested, field.Properties)
	}
}
