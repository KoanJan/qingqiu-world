package renderer

import (
	"context"
	"testing"

	"qingqiu-world-server/internal/model"
)

type definitionTestAdapter struct {
	definition model.TTSProviderDefinition
}

func (a definitionTestAdapter) Provider() model.TTSProvider                       { return model.TTSProviderFishAudio }
func (a definitionTestAdapter) Definition() model.TTSProviderDefinition           { return a.definition }
func (a definitionTestAdapter) Validate(context.Context, model.TTSRenderer) error { return nil }
func (a definitionTestAdapter) PrepareVoice(context.Context, model.TTSRenderer, VoiceSample) (string, error) {
	return "", nil
}
func (a definitionTestAdapter) Synthesize(context.Context, Request) (Result, error) {
	return Result{}, nil
}

// TestProviderDefinitionRequiresAdapterOwnedTermsURL prevents a future
// adapter from reintroducing a user-entered or absent terms-link flow.
func TestProviderDefinitionRequiresAdapterOwnedTermsURL(t *testing.T) {
	definition := model.TTSProviderDefinition{
		Provider:                   model.TTSProviderFishAudio,
		Name:                       "Test provider",
		ConnectionConfigJSONSchema: `{"type":"object","properties":{"api_key":{"type":"string","x-ui":{"label":{"en":"API key"}}}}}`,
		TermsURL:                   "https://example.com/terms",
	}
	adapter := definitionTestAdapter{definition: definition}
	if err := validateProviderDefinition(adapter, definition); err != nil {
		t.Fatalf("expected Fish Audio provider definition to be valid: %v", err)
	}

	definition.TermsURL = ""
	if err := validateProviderDefinition(adapter, definition); err == nil {
		t.Fatal("expected provider definition without terms URL to fail")
	}

	definition = adapter.Definition()
	definition.Provider = model.TTSProviderUnknown
	if err := validateProviderDefinition(adapter, definition); err == nil {
		t.Fatal("expected mismatched provider definition to fail")
	}

	definition = adapter.Definition()
	definition.ConnectionConfigJSONSchema = `{"type":"object","properties":{"top_p":{"type":"number"}}}`
	if err := validateProviderDefinition(adapter, definition); err == nil {
		t.Fatal("expected schema without localized field label to fail")
	}
}
