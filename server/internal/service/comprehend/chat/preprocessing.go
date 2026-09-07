package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// routingPrompt is the LLM prompt template for retrieval planning.
// It takes three parameters: history (formatted conversation), the authorized
// KB inventory, and the query (the person's message batch). The LLM itself
// decides whether a knowledge-base search is needed and which authorized KBs
// to target (agentic retrieval decision).
const routingPrompt = `Analyze the query type and process accordingly.

Conversation history:
%s

Authorized knowledge bases (the only knowledge bases you are allowed to search):
%s

Current message batch: %s

Decide whether this message batch needs knowledge-base retrieval. If it does, produce a self-contained search query and pick the IDs of the authorized knowledge bases worth searching. When retrieval is unnecessary, return an empty query and an empty ID list.

Extract keywords suitable for searching relevant conversation history. Return an empty keyword list when history search is unnecessary.

If the batch is too vague to act on even with the conversation history, set needs_clarification to true and explain why in clarification_reason.`

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

// QueryPreprocessingOutput contains retrieval and clarification instructions for a message batch.
type QueryPreprocessingOutput struct {
	KnowledgeBaseQuery    string   `json:"knowledge_base_query" jsonschema:"description=Self-contained query for knowledge-base vector search; empty when unnecessary,required"`
	KnowledgeBaseIDs      []int64  `json:"knowledge_base_ids" jsonschema:"description=IDs of the authorized knowledge bases to search; empty when unnecessary,required"`
	HistorySearchKeywords []string `json:"history_search_keywords" jsonschema:"description=Keywords for conversation history search; empty when unnecessary,required"`
	NeedsClarification    bool     `json:"needs_clarification" jsonschema:"description=Whether the message batch needs clarification,required"`
	ClarificationReason   string   `json:"clarification_reason" jsonschema:"description=Reason the message batch needs clarification"`
	Clarification         string   `json:"clarification" jsonschema:"description=Clarification question when needed"`
}

// formatAuthorizedKBs renders the authorized KB inventory for the routing
// prompt so the LLM can decide which (if any) KBs to search. Returns a
// placeholder when the agent holds no grants.
func formatAuthorizedKBs(kbs []types.KBDescriptor) string {
	if len(kbs) == 0 {
		return "(none)"
	}
	lines := make([]string, 0, len(kbs))
	for _, item := range kbs {
		lines = append(lines, fmt.Sprintf("- ID %d: %s — %s", item.ID, item.Name, item.Description))
	}
	return strings.Join(lines, "\n")
}

// allKBsFallback builds the fail-open preprocessing output: when the routing
// call fails or yields unusable output, fall back to searching the full
// authorized KB inventory with the raw query instead of silently skipping
// retrieval.
func allKBsFallback(query string, authorizedKBs []types.KBDescriptor) *QueryPreprocessingOutput {
	ids := make([]int64, 0, len(authorizedKBs))
	for _, item := range authorizedKBs {
		ids = append(ids, item.ID)
	}
	return &QueryPreprocessingOutput{
		KnowledgeBaseQuery: query,
		KnowledgeBaseIDs:   ids,
	}
}

// formatHistoryForPreprocessing formats conversation history for preprocessing prompts.
// Limits to the most recent maxMessages if > 0.
func formatHistoryForPreprocessing(history []types.ConversationMessage, maxMessages int) string {
	if len(history) == 0 {
		return "(No conversation history)"
	}

	recent := history
	if maxMessages > 0 && len(history) > maxMessages {
		recent = history[len(history)-maxMessages:]
	}

	var formatted []string
	for _, msg := range recent {
		formatted = append(formatted, fmt.Sprintf("%s [%s]: %s", msg.PersonName, msg.CreatedAt.Format("2006-01-02 15:04:05"), msg.Content))
	}
	return strings.Join(formatted, "\n")
}

// preprocessQuery runs the routing decision for a message batch. authorizedKBs
// is injected into the prompt so the LLM may only pick from the granted set;
// on any failure the output degrades to allKBsFallback (fail-open, logged).
func preprocessQuery(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	query string,
	history []types.ConversationMessage,
	authorizedKBs []types.KBDescriptor,
	maxMessages int,
) *QueryPreprocessingOutput {
	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic)

	historyText := formatHistoryForPreprocessing(history, maxMessages)
	prompt := fmt.Sprintf(routingPrompt, historyText, formatAuthorizedKBs(authorizedKBs), query)

	result, err := chatModel.ChatWithJSONSchema(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.JSONSchemaDefinition{
		Name:        "QueryPreprocessingOutput",
		Description: "Prepare retrieval requests for the incoming message batch",
		Strict:      true,
		Schema:      llm.GenerateSchema[QueryPreprocessingOutput](),
	})

	if err != nil {
		applogger.Error("query preprocessing failed, failing open to search all authorized KBs", "error", err)
		return allKBsFallback(query, authorizedKBs)
	}

	if result != "" {
		var output QueryPreprocessingOutput
		if err := json.Unmarshal([]byte(result), &output); err == nil {
			return &output
		}
		applogger.Error("query preprocessing returned invalid JSON, failing open to search all authorized KBs",
			"raw", result[:min(200, len(result))])
	} else {
		applogger.Warn("query preprocessing returned empty output, failing open to search all authorized KBs")
	}

	return allKBsFallback(query, authorizedKBs)
}

// generateClarification generates a clarification question for vague queries.
// If characterSettings is non-empty, it is prepended to the prompt for personality alignment.
// Uses TemperatureDeterministic for consistent outputs.
func generateClarification(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	query string,
	history []types.ConversationMessage,
	reason string,
	characterSettings string,
	maxMessages int,
) string {
	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic)

	historyText := formatHistoryForPreprocessing(history, maxMessages)
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

// PreprocessQuery prepares retrieval requests and clarification output for a message batch.
// authorizedKBs is the agent's granted KB inventory: it is injected into the
// decision prompt (the LLM may only pick from it) and doubles as the fail-open
// fallback set when the decision call itself fails.
func PreprocessQuery(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	query string,
	history []types.ConversationMessage,
	characterSettings string,
	authorizedKBs []types.KBDescriptor,
	maxMessages int,
) *QueryPreprocessingOutput {
	output := preprocessQuery(ctx, llmConfig, query, history, authorizedKBs, maxMessages)
	if output.NeedsClarification {
		reason := "Query is too vague"
		if output.ClarificationReason != "" {
			reason = output.ClarificationReason
		}
		output.Clarification = generateClarification(ctx, llmConfig, query, history, reason, characterSettings, maxMessages)
	}

	applogger.Info("query preprocessing complete",
		"knowledge_base_query", output.KnowledgeBaseQuery[:min(50, len(output.KnowledgeBaseQuery))],
		"knowledge_base_ids", output.KnowledgeBaseIDs,
		"history_search_keywords", output.HistorySearchKeywords,
	)
	return output
}
