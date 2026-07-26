// Package llm provides LLM client wrappers for OpenAI-compatible chat completion APIs.
//
// This package implements the Go equivalent of Python's LLMService and TokenUsageLogger,
// providing utilities for creating chat model instances and tracking token usage/latency.
// All LLM calls automatically log token usage and latency in the same format as Python.
//
// All external types (Message, ToolCall, FunctionDefinition, ToolResponse) are package-level
// abstractions that decouple callers from the underlying OpenAI SDK. SDK types are confined
// to internal conversion functions within this package.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	openai "github.com/sashabaranov/go-openai"

	applogger "qingqiu-world-server/internal/logger"
)

// Temperature presets for different LLM service categories.
//
// These constants centralize all temperature values used across the application,
// replacing scattered magic numbers. Each preset corresponds to a specific
// trade-off between determinism and creativity:
//   - TemperatureDeterministic: structured output services that need consistent JSON
//   - TemperatureControlled: creative generation that should stay on-topic
//   - TemperatureCreative: general conversation and open-ended generation
const (
	TemperatureDeterministic float32 = 0.1
	TemperatureControlled    float32 = 0.3
	TemperatureCreative      float32 = 0.7
)

// Message represents a chat message in the LLM conversation.
// This is the universal message type used across the entire application,
// covering all OpenAI chat completion message roles:
//   - system: Role + Content
//   - user: Role + Content
//   - assistant: Role + Content + ToolCalls (optional)
//   - tool: Role + Content + ToolCallID
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall represents a single tool call returned by the LLM in an assistant message.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

// toolFunction represents the function details of a tool call.
type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// FunctionDefinition defines the schema for a tool that the LLM can call.
// This is the application-level abstraction over the OpenAI SDK's function definition type.
type FunctionDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolResponse represents the LLM response from a tool-calling chat completion.
// Decouples callers from the OpenAI SDK's ChatCompletionResponse type.
type ToolResponse struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls"`
	FinishReason string     `json:"finish_reason"`
}

// ChatModel wraps an OpenAI client with model configuration for chat completions.
// It supports multiple call modes: plain chat, streaming, tool-calling, and JSON schema output.
// Temperature is only sent to the API when explicitly set (> 0), matching LangChain's behavior
// where unset temperature defaults to 0.7 on the server side.
type ChatModel struct {
	client      *openai.Client
	baseURL     string
	modelID     string
	temperature float32
}

// NewChatModel creates a ChatModel without explicit temperature.
// The API server's default temperature (typically 0.7) will be used.
func NewChatModel(baseURL, apiKey, modelID string) *ChatModel {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return &ChatModel{
		client:  openai.NewClientWithConfig(cfg),
		baseURL: baseURL,
		modelID: modelID,
	}
}

// NewChatModelWithTemperature creates a ChatModel with explicit temperature control.
// Use the predefined constants (TemperatureDeterministic, TemperatureControlled, TemperatureCreative)
// instead of raw float values for consistency and readability.
func NewChatModelWithTemperature(baseURL, apiKey, modelID string, temperature float32) *ChatModel {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return &ChatModel{
		client:      openai.NewClientWithConfig(cfg),
		baseURL:     baseURL,
		modelID:     modelID,
		temperature: temperature,
	}
}

// --- Internal conversion functions ---
// These confine all openai SDK types within this package.

// toOpenAIMessages converts Message slice to OpenAI ChatCompletionMessage slice.
func toOpenAIMessages(messages []Message) []openai.ChatCompletionMessage {
	result := make([]openai.ChatCompletionMessage, 0, len(messages))
	for _, m := range messages {
		msg := openai.ChatCompletionMessage{
			Role:    m.Role,
			Content: m.Content,
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = toOpenAIToolCalls(m.ToolCalls)
		}
		if m.ToolCallID != "" {
			msg.ToolCallID = m.ToolCallID
		}
		result = append(result, msg)
	}
	return result
}

// toOpenAIToolCalls converts ToolCall slice to OpenAI ToolCall slice.
func toOpenAIToolCalls(tcs []ToolCall) []openai.ToolCall {
	result := make([]openai.ToolCall, 0, len(tcs))
	for _, tc := range tcs {
		result = append(result, openai.ToolCall{
			ID:   tc.ID,
			Type: openai.ToolType(tc.Type),
			Function: openai.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return result
}

// toOpenAIToolDefs converts FunctionDefinition slice to OpenAI Tool slice.
func toOpenAIToolDefs(defs []FunctionDefinition) []openai.Tool {
	result := make([]openai.Tool, 0, len(defs))
	for _, d := range defs {
		d := d
		result = append(result, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Parameters,
			},
		})
	}
	return result
}

// fromOpenAIToolCalls converts OpenAI ToolCall slice to ToolCall slice.
func fromOpenAIToolCalls(tcs []openai.ToolCall) []ToolCall {
	result := make([]ToolCall, 0, len(tcs))
	for _, tc := range tcs {
		result = append(result, ToolCall{
			ID:   tc.ID,
			Type: string(tc.Type),
			Function: toolFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return result
}

// fromOpenAIResponse converts OpenAI ChatCompletionResponse to ToolResponse.
func fromOpenAIResponse(resp *openai.ChatCompletionResponse) ToolResponse {
	if len(resp.Choices) == 0 {
		return ToolResponse{}
	}
	choice := resp.Choices[0]
	return ToolResponse{
		Content:      choice.Message.Content,
		ToolCalls:    fromOpenAIToolCalls(choice.Message.ToolCalls),
		FinishReason: string(choice.FinishReason),
	}
}

// --- Logging helpers ---

// logMessages logs input messages at debug level for traceability.
func logMessages(messages []Message) {
	for i, m := range messages {
		applogger.Debug("llm input", "index", i, "role", m.Role, "content", m.Content, "tool_calls", len(m.ToolCalls), "tool_call_id", m.ToolCallID)
	}
}

// --- Request builders ---

// buildRequest constructs a ChatCompletionRequest from Message slice.
// Temperature is only included when explicitly set (> 0).
func (cm *ChatModel) buildRequest(messages []Message) openai.ChatCompletionRequest {
	req := openai.ChatCompletionRequest{
		Model:    cm.modelID,
		Messages: toOpenAIMessages(messages),
	}

	if cm.temperature > 0 {
		req.Temperature = cm.temperature
	}

	return req
}

// --- Public API methods ---

// Chat sends a non-streaming chat completion request and returns the response content.
// Used by services that need a single complete response: summary, narrative, person state, etc.
func (cm *ChatModel) Chat(ctx context.Context, messages []Message) (string, error) {
	logMessages(messages)

	req := cm.buildRequest(messages)

	start := time.Now()
	resp, err := cm.client.CreateChatCompletion(ctx, req)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		applogger.Error("llm call failed", "model", cm.modelID, "latency_ms", latencyMs, "error", err)
		return "", fmt.Errorf("chat completion failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response choices returned")
	}

	content := resp.Choices[0].Message.Content
	applogger.Debug("llm output", "model", cm.modelID, "content", content)

	logTokenUsage(latencyMs, resp.Usage, cm.modelID)

	return content, nil
}

// stream wraps an OpenAI streaming response, hiding the SDK type from callers.
// Must be consumed via ConsumeStream. Automatically closed when consumption completes.
type stream struct {
	inner *openai.ChatCompletionStream
}

// ChatStream initiates a streaming chat completion request.
// Returns a stream that must be consumed via ConsumeStream.
// Used for real-time SSE streaming of LLM responses to the frontend.
// streamOptions.IncludeUsage is set to true to capture token usage in the final chunk.
func (cm *ChatModel) ChatStream(ctx context.Context, messages []Message) (*stream, error) {
	logMessages(messages)

	req := cm.buildRequest(messages)
	req.Stream = true
	req.StreamOptions = &openai.StreamOptions{
		IncludeUsage: true,
	}

	start := time.Now()
	inner, err := cm.client.CreateChatCompletionStream(ctx, req)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		applogger.Error("llm stream call failed", "model", cm.modelID, "latency_ms", latencyMs, "error", err)
		return nil, fmt.Errorf("chat completion stream failed: %w", err)
	}

	applogger.Debug("llm stream started", "model", cm.modelID, "connect_latency_ms", latencyMs)

	return &stream{inner: inner}, nil
}

// ChatWithTools sends a non-streaming chat completion request with tool definitions.
// Used by the task loop's ReAct pattern where the LLM decides which tools to call.
// This is the Go equivalent of Python's TaskLLMClient.invoke(), using the OpenAI Tools API
// (not the deprecated Functions API) for proper tool_calls support.
func (cm *ChatModel) ChatWithTools(ctx context.Context, messages []Message, toolDefs []FunctionDefinition) (ToolResponse, error) {
	logMessages(messages)

	req := openai.ChatCompletionRequest{
		Model:    cm.modelID,
		Messages: toOpenAIMessages(messages),
		Tools:    toOpenAIToolDefs(toolDefs),
	}

	if cm.temperature > 0 {
		req.Temperature = cm.temperature
	}

	start := time.Now()
	resp, err := cm.client.CreateChatCompletion(ctx, req)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		applogger.Error("llm call with tools failed", "model", cm.modelID, "latency_ms", latencyMs, "error", err)
		return ToolResponse{}, fmt.Errorf("chat completion with tools failed: %w", err)
	}

	result := fromOpenAIResponse(&resp)

	applogger.Debug("llm output", "model", cm.modelID, "content", result.Content, "tool_calls", len(result.ToolCalls))
	for i, tc := range result.ToolCalls {
		applogger.Debug("llm output tool_call", "index", i, "id", tc.ID, "name", tc.Function.Name, "arguments", tc.Function.Arguments)
	}

	logTokenUsage(latencyMs, resp.Usage, cm.modelID)

	return result, nil
}

// JSONSchemaDefinition defines a JSON Schema for structured LLM output.
// Used with ChatWithJSONSchema to force the LLM to respond in a specific JSON format.
// This is the Go equivalent of Python's with_structured_output() / response_format.
type JSONSchemaDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"` // Standard JSON Schema bytes, generated by GenerateSchema
	Strict      bool            `json:"strict"`
}

// ChatWithJSONSchema sends a chat completion request with JSON Schema response format.
// Forces the LLM to output valid JSON conforming to the provided schema.
//
// Uses a cache-driven two-tier fallback strategy:
//  1. Checks capability cache (persisted in DB) for this model's json_schema support
//  2. If supported: uses native json_schema response format
//  3. If not supported: emulates structured output via function call with tool_choice
//  4. If untested: tries json_schema first, caches the result for future calls
func (cm *ChatModel) ChatWithJSONSchema(ctx context.Context, messages []Message, schemaDef JSONSchemaDefinition) (string, error) {
	// Check capability cache first
	cap, found := cm.lookupCapability()
	if found {
		if cap.SupportsJSONSchema == 1 {
			return cm.tryJSONSchema(ctx, messages, schemaDef)
		}
		applogger.Debug("json_schema not supported by model, using function_call fallback",
			"model", cm.modelID, "base_url", cm.baseURL)
		return cm.tryFunctionCallJSON(ctx, messages, schemaDef)
	}

	// First use of this model: try json_schema and record the result
	result, err := cm.tryJSONSchema(ctx, messages, schemaDef)
	if err == nil {
		cm.saveCapability(1)
		return result, nil
	}

	if isResponseFormatError(err) {
		applogger.Error("json_schema not supported, caching and falling back to function_call",
			"model", cm.modelID, "base_url", cm.baseURL)
		cm.saveCapability(0)
		return cm.tryFunctionCallJSON(ctx, messages, schemaDef)
	}

	// Real error (network, auth, etc.), don't cache
	return "", err
}

// tryJSONSchema attempts a chat completion with native json_schema response format.
func (cm *ChatModel) tryJSONSchema(ctx context.Context, messages []Message, schemaDef JSONSchemaDefinition) (string, error) {
	logMessages(messages)

	req := cm.buildRequest(messages)

	req.ResponseFormat = &openai.ChatCompletionResponseFormat{
		Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
		JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
			Name:        schemaDef.Name,
			Description: schemaDef.Description,
			Schema:      schemaDef.Schema, // json.RawMessage implements json.Marshaler
			Strict:      schemaDef.Strict,
		},
	}

	start := time.Now()
	resp, err := cm.client.CreateChatCompletion(ctx, req)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		applogger.Error("llm call with json_schema failed", "model", cm.modelID, "latency_ms", latencyMs, "error", err)
		return "", fmt.Errorf("chat completion with json_schema failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response choices returned")
	}

	content := resp.Choices[0].Message.Content
	applogger.Debug("llm output", "model", cm.modelID, "schema", schemaDef.Name, "content", content)

	logTokenUsage(latencyMs, resp.Usage, cm.modelID)

	return content, nil
}

// tryFunctionCallJSON emulates structured output by wrapping the schema as a single tool definition.
// The LLM is expected to call the tool with the structured data as arguments.
// ToolChoice is not forced to maintain compatibility with models that restrict it
// (e.g., DeepSeek thinking mode), relying on the model's natural behavior to use the only available tool.
// This is the fallback for models that do not support json_schema response format.
func (cm *ChatModel) tryFunctionCallJSON(ctx context.Context, messages []Message, schemaDef JSONSchemaDefinition) (string, error) {
	logMessages(messages)

	params := rawMessageToParams(schemaDef.Schema)

	toolDef := FunctionDefinition{
		Name:        schemaDef.Name,
		Description: schemaDef.Description,
		Parameters:  params,
	}

	req := openai.ChatCompletionRequest{
		Model:       cm.modelID,
		Messages:    toOpenAIMessages(messages),
		Tools:       toOpenAIToolDefs([]FunctionDefinition{toolDef}),
		Temperature: 0,
	}

	start := time.Now()
	resp, err := cm.client.CreateChatCompletion(ctx, req)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		applogger.Error("llm call with function_call emulation failed", "model", cm.modelID, "latency_ms", latencyMs, "error", err)
		return "", fmt.Errorf("function_call emulation failed: %w", err)
	}

	if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
		return "", fmt.Errorf("function_call emulation: no tool call in response")
	}

	args := resp.Choices[0].Message.ToolCalls[0].Function.Arguments
	applogger.Debug("llm output", "model", cm.modelID, "schema", schemaDef.Name, "function_call_arguments", args)

	logTokenUsage(latencyMs, resp.Usage, cm.modelID)

	return args, nil
}

// rawMessageToParams converts json.RawMessage (standard JSON Schema) to map[string]interface{}
// for use as a function call parameter definition.
func rawMessageToParams(data json.RawMessage) map[string]interface{} {
	if len(data) == 0 {
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}
	var params map[string]interface{}
	if err := json.Unmarshal(data, &params); err != nil {
		applogger.Error("failed to unmarshal schema for function_call", "error", err)
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}
	return params
}

// streamHandler is a callback function invoked for each chunk received from a streaming response.
type streamHandler func(chunk string) error

// logTokenUsage logs LLM token usage in the same format as Python's TokenUsageLogger:
// "llm usage | latency=XXXms | prompt_tokens: XXX | completion_tokens: XXX | total_tokens: XXX | model=XXX"
func logTokenUsage(latencyMs float64, usage openai.Usage, model string) {
	args := []interface{}{
		"latency_ms", latencyMs,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"total_tokens", usage.TotalTokens,
	}
	if model != "" {
		args = append(args, "model", model)
	}
	applogger.Debug("llm usage", args...)
}

// ConsumeStream reads all chunks from a streaming response, invoking the handler for each chunk.
// Returns the full accumulated content when the stream completes.
// Token usage is logged if available in the final stream chunk (via streamOptions.IncludeUsage).
func (cm *ChatModel) ConsumeStream(stream *stream, handler streamHandler) (string, error) {
	defer stream.inner.Close()

	start := time.Now()
	var fullContent string
	var streamUsage openai.Usage
	hasUsage := false

	for {
		resp, err := stream.inner.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fullContent, fmt.Errorf("stream recv error: %w", err)
		}

		if resp.Usage != nil {
			streamUsage = *resp.Usage
			hasUsage = true
		}

		if len(resp.Choices) > 0 {
			delta := resp.Choices[0].Delta.Content
			if delta != "" {
				fullContent += delta
				if handler != nil {
					if err := handler(delta); err != nil {
						return fullContent, err
					}
				}
			}
		}
	}

	latencyMs := float64(time.Since(start).Milliseconds())
	applogger.Debug("llm output", "model", cm.modelID, "stream", true, "content", fullContent)

	if hasUsage {
		logTokenUsage(latencyMs, streamUsage, cm.modelID)
	} else {
		applogger.Debug("llm stream completed", "model", cm.modelID, "latency_ms", latencyMs)
	}

	return fullContent, nil
}
