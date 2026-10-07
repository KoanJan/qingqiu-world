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

A knowledge base is like a library of materials in Qingqiu World: a persistent collection of documents with its own name and description.

Authorized knowledge bases (the only knowledge bases you are allowed to search):
%s

Current message batch: %s

Decide whether this message batch needs knowledge-base retrieval. If it does, produce a self-contained search query and pick the IDs of the authorized knowledge bases worth searching. When retrieval is unnecessary, return an empty query and an empty ID list.

Extract keywords suitable for searching relevant conversation history. Return an empty keyword list when history search is unnecessary.`

// QueryPreprocessingOutput contains retrieval requests for a message batch.
type QueryPreprocessingOutput struct {
	KnowledgeBaseQuery    string   `json:"knowledge_base_query" jsonschema:"description=Self-contained query for knowledge-base vector search; empty when unnecessary,required"`
	KnowledgeBaseIDs      []int64  `json:"knowledge_base_ids" jsonschema:"description=IDs of the authorized knowledge bases to search; empty when unnecessary,required"`
	HistorySearchKeywords []string `json:"history_search_keywords" jsonschema:"description=Keywords for conversation history search; empty when unnecessary,required"`
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

// PreprocessQuery prepares retrieval requests for a message batch.
// authorizedKBs is the agent's granted KB inventory: it is injected into the
// decision prompt (the LLM may only pick from it) and doubles as the fail-open
// fallback set when the decision call itself fails.
func PreprocessQuery(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	query string,
	history []types.ConversationMessage,
	authorizedKBs []types.KBDescriptor,
	maxMessages int,
) *QueryPreprocessingOutput {
	output := preprocessQuery(ctx, llmConfig, query, history, authorizedKBs, maxMessages)
	applogger.Info("query preprocessing complete",
		"knowledge_base_query", output.KnowledgeBaseQuery[:min(50, len(output.KnowledgeBaseQuery))],
		"knowledge_base_ids", output.KnowledgeBaseIDs,
		"history_search_keywords", output.HistorySearchKeywords,
	)
	return output
}
