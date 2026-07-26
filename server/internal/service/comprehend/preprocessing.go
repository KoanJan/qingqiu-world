package comprehend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// Query type constants for classification
const queryTypeClear = "clear"         // Query is complete and unambiguous
const queryTypeAmbiguous = "ambiguous" // Query contains references to previous context
const queryTypeVague = "vague"         // Query is too vague to understand intent
const queryTypeNoQuery = "no_query"    // Query doesn't need retrieval (greetings, etc.)

// routingPrompt is the LLM prompt template for query type classification
// and keyword extraction for history search.
// It takes two parameters: history (formatted conversation) and query (the person's message).
const routingPrompt = `Analyze the query type and process accordingly.

Conversation history:
%s

Current query: %s

Classify the query type and process:
1. "no_query" - No retrieval needed: greetings, chitchat, emotional expressions, simple responses, etc. that can be answered without retrieving historical information.
2. "clear" - Clear query: the query is complete and unambiguous, requiring relevant information to answer.
3. "ambiguous" - Ambiguous reference: the query contains pronouns (like "it", "that", "this") or references to previous content, requiring context to understand. For this type, you MUST rewrite the query into a complete, clear query that can be understood independently without relying on conversation history.
4. "vague" - Too vague: the query is too brief or ambiguous, making it difficult to determine intent even with context. For this type, explain the reason for vagueness.

In addition, extract a list of keywords from the query (or rewritten query if applicable) suitable for keyword-based search in conversation history. Keywords should capture the core topics, entities, and concepts mentioned.`

// clarifyPrompt is the LLM prompt template for generating clarification questions.
// It takes three parameters: history, query, and reason.
const clarifyPrompt = `The query is too vague and needs clarification.

Conversation history:
%s

Query: %s

Reason for vagueness: %s

Generate a clarification question. The question should be concise, specific, and provide possible options.

IMPORTANT: The clarification question MUST be in the SAME LANGUAGE as the original query.
- If the query is in Chinese, respond in Chinese.
- If the query is in English, respond in English.

Output only the clarification question, without any additional content.`

// QueryRoutingResult represents the structured output of query routing.
// Defines the expected format when the LLM classifies and processes a user query.
type QueryRoutingResult struct {
	Type           string   `json:"type" jsonschema:"description=Query type classification,enum=no_query,enum=clear,enum=ambiguous,enum=vague,required"`
	RewrittenQuery string   `json:"rewritten_query" jsonschema:"description=Rewritten query that is self-contained and clear (required for ambiguous type)"`
	Keywords       []string `json:"keywords" jsonschema:"description=Keywords extracted from the query for keyword-based history search,required"`
	Reason         string   `json:"reason" jsonschema:"description=Reason why the query is vague and needs clarification (required for vague type)"`
}

// PreprocessingResult represents the full output of query preprocessing,
// including the processed query, type classification, keywords for retrieval, and clarification if needed.
type PreprocessingResult struct {
	OriginalQuery      string   `json:"original_query"`
	ProcessedQuery     string   `json:"processed_query"`
	Keywords           []string `json:"keywords"`
	QueryType          string   `json:"query_type"`
	NeedsClarification bool     `json:"needs_clarification"`
	Clarification      string   `json:"clarification"`
	SkipRetrieval      bool     `json:"skip_retrieval"`
}

// formatHistoryForPreprocessing formats conversation history for preprocessing prompts.
// Limits to the most recent maxMessages if > 0.
// userName is the actual name of the other party, agentName is the agent's own name.
func formatHistoryForPreprocessing(history []llm.Message, maxMessages int, userName, agentName string) string {
	if len(history) == 0 {
		return "(No conversation history)"
	}

	recent := history
	if maxMessages > 0 && len(history) > maxMessages {
		recent = history[len(history)-maxMessages:]
	}

	userRole := userName

	var formatted []string
	for _, msg := range recent {
		role := userRole
		if msg.Role != "user" {
			role = agentName
		}
		formatted = append(formatted, fmt.Sprintf("%s: %s", role, msg.Content))
	}
	return strings.Join(formatted, "\n")
}

// routeQuery classifies the query type and rewrites if ambiguous.
// Uses JSON Schema structured output for deterministic classification.
// Uses TemperatureDeterministic for consistent, deterministic outputs.
func routeQuery(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	query string,
	history []llm.Message,
	maxMessages int,
	userName string,
	agentName string,
) *QueryRoutingResult {
	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic)

	historyText := formatHistoryForPreprocessing(history, maxMessages, userName, agentName)
	prompt := fmt.Sprintf(routingPrompt, historyText, query)

	result, err := chatModel.ChatWithJSONSchema(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.JSONSchemaDefinition{
		Name:        "QueryRoutingResult",
		Description: "Classify and process the incoming query",
		Strict:      true,
		Schema:      llm.GenerateSchema[QueryRoutingResult](),
	})

	if err != nil {
		applogger.Error("Query routing failed", "error", err)
		return &QueryRoutingResult{Type: queryTypeClear}
	}

	if result != "" {
		var routing QueryRoutingResult
		if err := json.Unmarshal([]byte(result), &routing); err == nil {
			applogger.Info("Query routing result", "type", routing.Type)
			if routing.Type == queryTypeAmbiguous && routing.RewrittenQuery != "" {
				applogger.Info("Query rewritten", "original", query[:min(50, len(query))], "rewritten", routing.RewrittenQuery[:min(50, len(routing.RewrittenQuery))])
			}
			return &routing
		}
	}

	return &QueryRoutingResult{Type: queryTypeClear}
}

// generateClarification generates a clarification question for vague queries.
// If characterSettings is non-empty, it is prepended to the prompt for personality alignment.
// Uses TemperatureDeterministic for consistent outputs.
func generateClarification(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	query string,
	history []llm.Message,
	reason string,
	characterSettings string,
	maxMessages int,
	userName string,
	agentName string,
) string {
	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic)

	historyText := formatHistoryForPreprocessing(history, maxMessages, userName, agentName)
	prompt := fmt.Sprintf(clarifyPrompt, historyText, query, reason)

	if characterSettings != "" {
		prompt = fmt.Sprintf("[Your Character]\n%s\n\n%s", characterSettings, prompt)
	}

	result, err := chatModel.Chat(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		applogger.Error("Clarification generation failed", "error", err)
		return "Your question is a bit vague. Could you please provide more details about your needs?"
	}

	applogger.Info("Generated clarification for query", "query", query[:min(50, len(query))])
	return result
}

// PreprocessQuery is the main entry point for query preprocessing.
// It classifies the query type and applies the appropriate transformation:
//   - no_query: skip retrieval, use original query
//   - clear: use original query with retrieval
//   - ambiguous: rewrite query with context for retrieval
//   - vague: generate clarification question, mark as needs_clarification
func PreprocessQuery(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	query string,
	history []llm.Message,
	characterSettings string,
	maxMessages int,
	userName string,
	agentName string,
) *PreprocessingResult {
	result := &PreprocessingResult{
		OriginalQuery:  query,
		ProcessedQuery: query,
		QueryType:      queryTypeClear,
	}

	routing := routeQuery(ctx, llmConfig, query, history, maxMessages, userName, agentName)
	result.Keywords = routing.Keywords
	queryType := routing.Type
	result.QueryType = queryType

	switch queryType {
	case queryTypeNoQuery:
		result.ProcessedQuery = query
		result.SkipRetrieval = true

	case queryTypeClear:
		result.ProcessedQuery = query
		result.SkipRetrieval = false

	case queryTypeAmbiguous:
		if routing.RewrittenQuery != "" {
			result.ProcessedQuery = routing.RewrittenQuery
		} else {
			result.ProcessedQuery = query
		}

	case queryTypeVague:
		reason := "Query is too vague"
		if routing.Reason != "" {
			reason = routing.Reason
		}
		clarification := generateClarification(ctx, llmConfig, query, history, reason, characterSettings, maxMessages, userName, agentName)
		result.NeedsClarification = true
		result.Clarification = clarification
	}

	applogger.Info("Query preprocessing complete",
		"type", queryType,
		"processed", result.ProcessedQuery[:min(50, len(result.ProcessedQuery))],
		"keywords", result.Keywords,
	)
	return result
}
