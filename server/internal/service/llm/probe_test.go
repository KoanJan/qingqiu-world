package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const probeToolResponse = `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"compatibility_check","arguments":"{\"value\":\"ok\"}"}}]}}]}`

func TestProbeChecksBothProductionCalls(t *testing.T) {
	var mu sync.Mutex
	choices := make([]json.RawMessage, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ToolChoice      json.RawMessage `json:"tool_choice"`
			ReasoningEffort string          `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode: %v", err)
		}
		if request.ReasoningEffort != "" {
			t.Errorf("non-DeepSeek request has reasoning_effort: %q", request.ReasoningEffort)
		}
		mu.Lock()
		choices = append(choices, request.ToolChoice)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, probeToolResponse)
	}))
	defer server.Close()

	result := Probe(context.Background(), server.URL, "secret", "test-model")
	if !result.RequiredTools.OK || !result.StructuredOutput.OK {
		t.Fatalf("probe: %+v", result)
	}
	if len(choices) != 2 || string(choices[0]) != `"required"` || !strings.Contains(string(choices[1]), `"compatibility_check"`) {
		t.Fatalf("unexpected tool choices: %q", choices)
	}
}

func TestProbeReportsHTTP400ByCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ToolChoice json.RawMessage `json:"tool_choice"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if string(request.ToolChoice) == `"required"` {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"tool_choice required unsupported for secret"}}`)
			return
		}
		_, _ = io.WriteString(w, probeToolResponse)
	}))
	defer server.Close()
	result := Probe(context.Background(), server.URL, "secret", "test-model")
	if result.RequiredTools.OK || result.RequiredTools.Error == "" || !result.StructuredOutput.OK {
		t.Fatalf("probe: %+v", result)
	}
	if strings.Contains(result.RequiredTools.Error, "secret") {
		t.Fatal("probe error exposed API key")
	}
}

func TestProbeTimeoutAndCredentialRedaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, probeToolResponse)
	}))
	defer server.Close()
	result := Probe(ctx, server.URL, "secret", "test-model")
	if result.RequiredTools.OK || result.StructuredOutput.OK {
		t.Fatalf("probe: %+v", result)
	}
	if strings.Contains(result.RequiredTools.Error+result.StructuredOutput.Error, "secret") {
		t.Fatal("probe exposed API key")
	}
	if got := probeCapability(context.DeadlineExceeded, "secret"); got.OK || got.Error == "" {
		t.Fatalf("error result: %+v", got)
	}
}

type probeRoundTripper func(*http.Request) (*http.Response, error)

func (f probeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfficialDeepSeekForcedToolRequestsDisableThinking(t *testing.T) {
	var choices []json.RawMessage
	cfg := openai.DefaultConfig("secret")
	cfg.BaseURL = officialDeepSeekBaseURL
	cfg.HTTPClient = &http.Client{Transport: probeRoundTripper(func(r *http.Request) (*http.Response, error) {
		var request struct {
			ToolChoice      json.RawMessage `json:"tool_choice"`
			ReasoningEffort string          `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode: %v", err)
		}
		if request.ReasoningEffort != "none" {
			t.Errorf("reasoning_effort = %q", request.ReasoningEffort)
		}
		choices = append(choices, request.ToolChoice)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(probeToolResponse)), Request: r}, nil
	})}
	cm := NewChatModel(cfg.BaseURL, "secret", "deepseek-flash")
	cm.client = openai.NewClientWithConfig(cfg)
	tool := FunctionDefinition{Name: "compatibility_check", Parameters: probeParameters}
	if _, err := cm.ChatWithRequiredTools(context.Background(), probeMessages, []FunctionDefinition{tool}); err != nil {
		t.Fatal(err)
	}
	if _, err := cm.tryFunctionCallJSON(context.Background(), probeMessages, JSONSchemaDefinition{Name: tool.Name, Schema: json.RawMessage(`{"type":"object"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(choices) != 2 || string(choices[0]) != `"required"` || !strings.Contains(string(choices[1]), `"compatibility_check"`) {
		t.Fatalf("unexpected tool choices: %q", choices)
	}
}

func TestDeepSeekForcedToolModeIgnoresLookalikeHost(t *testing.T) {
	cm := NewChatModel(officialDeepSeekBaseURL+".example.com", "secret", "test-model")
	req := openai.ChatCompletionRequest{ToolChoice: "required"}
	cm.setForcedToolMode(&req)
	if req.ReasoningEffort != "" {
		t.Fatalf("lookalike host changed reasoning effort to %q", req.ReasoningEffort)
	}
}

func TestProbeRejectsUnexpectedArguments(t *testing.T) {
	for _, arguments := range []string{
		`{"value":"wrong"}`,
		`{"value":"ok","extra":true}`,
		`{"value":"ok"} {"value":"ok"}`,
	} {
		if err := checkProbeArguments(arguments); err == nil {
			t.Errorf("accepted invalid arguments: %s", arguments)
		}
	}
}

func TestNamedToolFallbackRejectsDifferentTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"different_tool","arguments":"{\"value\":\"ok\"}"}}]}}]}`)
	}))
	defer server.Close()
	model := NewChatModel(server.URL, "secret", "test-model")
	_, err := model.tryFunctionCallJSON(context.Background(), probeMessages, JSONSchemaDefinition{Name: "compatibility_check", Schema: json.RawMessage(`{"type":"object"}`)})
	if err == nil {
		t.Fatal("accepted a different named tool")
	}
}
