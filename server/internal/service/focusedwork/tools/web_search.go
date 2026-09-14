package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

// defaultSearchNumResults is the default number of web search results returned.
const defaultSearchNumResults = 5

// WebSearchTool searches the web for information using configured search providers.
// Currently supports Tavily as the search provider.
// Returns a list of search results with title, URL, and snippet.
type WebSearchTool struct {
	searchConfig  *model.SearchConfig // Search configuration containing provider and API key
	CycleDetector                     // Embedded: cycle detection on (args, result) pairs
}

// NewWebSearchTool creates a WebSearchTool with the given search configuration.
func NewWebSearchTool(searchConfig *model.SearchConfig) *WebSearchTool {
	return &WebSearchTool{searchConfig: searchConfig}
}

// Name returns the tool name.
func (w *WebSearchTool) Name() ToolName { return ToolNameWebSearch }

// Description returns a brief description of the tool.
func (w *WebSearchTool) Description() string { return "Search the web for information" }

// Schema returns the LLM function definition for the tool.
func (w *WebSearchTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        w.Name().String(),
		Description: "Search the web for information. Use this tool to find current information, documentation, or answers to questions.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query",
				},
				"num_results": map[string]interface{}{
					"type":        "integer",
					"description": fmt.Sprintf("Number of results to return (default: %d)", defaultSearchNumResults),
					"default":     defaultSearchNumResults,
				},
			},
			"required": []string{"query"},
		},
	}
}

// SearchResult represents a single web search result.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchResponse is the structured response for web search execution.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Error   string         `json:"error,omitempty"`
}

// Execute performs a web search and returns results as JSON.
// Returns error message if search config is not available or provider is unknown.
func (w *WebSearchTool) Execute(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	numResults := defaultSearchNumResults
	if n, ok := args["num_results"].(float64); ok {
		numResults = int(n)
	}

	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	if w.searchConfig == nil || !w.searchConfig.IsAvailable() {
		return "", fmt.Errorf("search engine not configured")
	}

	provider := w.searchConfig.Provider
	apiKey := w.searchConfig.APIKey

	var results []SearchResult
	var err error

	switch provider {
	case "tavily":
		results, err = w.searchTavily(apiKey, query, numResults)
	default:
		return "", fmt.Errorf("unknown search provider: %s", provider)
	}

	if err != nil {
		return "", fmt.Errorf("search failed: %s", err.Error())
	}

	resp, _ := json.Marshal(SearchResponse{Results: results})
	return string(resp), nil
}

// searchTavily performs a search using the Tavily API.
// Sends a POST request to the Tavily search endpoint and parses the response.
func (w *WebSearchTool) searchTavily(apiKey, query string, numResults int) ([]SearchResult, error) {
	payload := map[string]interface{}{
		"api_key":     apiKey,
		"query":       query,
		"max_results": numResults,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post("https://api.tavily.com/search", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var tavilyResp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &tavilyResp); err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(tavilyResp.Results))
	for _, item := range tavilyResp.Results {
		results = append(results, SearchResult{
			Title:   item.Title,
			URL:     item.URL,
			Snippet: item.Content,
		})
	}
	return results, nil
}
