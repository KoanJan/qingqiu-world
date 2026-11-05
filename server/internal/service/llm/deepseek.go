package llm

import (
	"net/url"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

const (
	officialDeepSeekHost    = "api.deepseek.com"
	officialDeepSeekBaseURL = "https://" + officialDeepSeekHost
)

// DeepSeek's official API rejects forced tool choices in its default thinking mode.
func (cm *ChatModel) setForcedToolMode(req *openai.ChatCompletionRequest) {
	u, err := url.Parse(cm.baseURL)
	if err != nil || !strings.EqualFold(u.Hostname(), officialDeepSeekHost) {
		return
	}
	if req.ToolChoice != nil && req.ToolChoice != "auto" && req.ToolChoice != "none" {
		req.ReasoningEffort = "none"
	}
}
