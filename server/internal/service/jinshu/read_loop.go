package jinshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// jrMaxIterationsDefault is the default iteration budget for the jinshu-read
// loop. Reading a delivery is bounded — a handful of files at most — so the
// budget is intentionally smaller than the private-space loop's.
const jrMaxIterationsDefault = 10

// jinshuReadSystemPrompt is the system prompt injected at the start of every
// jinshu-read session. It constrains the agent to understanding the delivery
// rather than acting on the outside world.
const jinshuReadSystemPrompt = `You are reading a jinshu (锦书) — a bundle of files delivered to you by another person. Your only job is to understand its contents.

TOOLS AVAILABLE:
- read_jinshu_file: Read a text file from the jinshu directory.
- summarize_jinshu_file: Summarize a (possibly large) text file from the jinshu directory.

GUIDELINES:
- Traverse the listed files to understand what was delivered to you.
- When you have understood the contents, stop and output a concise summary of the contents. Preserve key facts, names, and conclusions.
- Output the summary in the same language as your reading intention (guidance).
- You are not helping a user; these are your own deliveries. Think and write in first person ("I").`

// ReadConfig holds the inputs for a jinshu-read loop.
type ReadConfig struct {
	PersonID      int64
	ReceivedDir   string
	FileList      string // Pre-formatted listing of the jinshu directory contents
	LLMConfig     *model.LLMConfig
	JinshuID      int64
	Guidance      string
	MaxIterations int
}

// ReadLoop implements a budget-driven ReAct loop dedicated to reading a single
// received jinshu. It accumulates the agent's read/summarize steps and returns
// a final summary when the agent stops or the budget is exhausted.
type ReadLoop struct {
	personID      int64
	llmClient     *llm.ChatModel
	maxIterations int
	receivedDir   string
	jinshuID      int64
	guidance      string
	fileList      string
	toolRegistry  map[string]readTool
	messages      []llm.Message
}

// NewReadLoop creates a jinshu-read loop bound to the given received directory.
func NewReadLoop(cfg ReadConfig) *ReadLoop {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = jrMaxIterationsDefault
	}

	llmClient := llm.NewChatModelWithTemperature(
		cfg.LLMConfig.BaseURL,
		cfg.LLMConfig.APIKey,
		cfg.LLMConfig.ModelID,
		llm.TemperatureControlled,
	)

	l := &ReadLoop{
		personID:      cfg.PersonID,
		llmClient:     llmClient,
		maxIterations: cfg.MaxIterations,
		receivedDir:   cfg.ReceivedDir,
		jinshuID:      cfg.JinshuID,
		guidance:      cfg.Guidance,
		fileList:      cfg.FileList,
		toolRegistry:  make(map[string]readTool),
	}

	l.registerTool(NewReadJinshuFileTool(cfg.ReceivedDir))
	l.registerTool(NewSummarizeJinshuFileTool(cfg.ReceivedDir, llmClient))

	return l
}

// registerTool adds a tool to the registry.
func (l *ReadLoop) registerTool(t readTool) {
	l.toolRegistry[t.Name()] = t
}

// Run executes the jinshu-read loop and returns the agent's summary of the
// jinshu contents. It blocks until the agent stops, the budget is exhausted,
// or ctx is cancelled.
func (l *ReadLoop) Run(ctx context.Context) (string, error) {
	applogger.Info("JinshuRead loop starting",
		"person_id", l.personID,
		"jinshu_id", l.jinshuID,
		"max_iterations", l.maxIterations,
	)

	l.buildInitialMessages()

	for iteration := 1; iteration <= l.maxIterations; iteration++ {
		if ctx.Err() != nil {
			applogger.Info("JinshuRead loop cancelled", "person_id", l.personID, "jinshu_id", l.jinshuID)
			return "", ctx.Err()
		}

		state, err := energy.RecoverEnergy(l.personID)
		if err != nil {
			applogger.Error("JinshuRead energy recovery failed", "person_id", l.personID, "error", err)
			return "", err
		}
		if state.Energy < int(energy.CostJinshuRead) {
			applogger.Info("JinshuRead loop pausing: insufficient energy",
				"person_id", l.personID, "jinshu_id", l.jinshuID, "energy", state.Energy)
			return "", fmt.Errorf("insufficient energy to read jinshu")
		}
		if err := energy.DeductEnergy(l.personID, energy.CostJinshuRead); err != nil {
			applogger.Error("JinshuRead energy deduction failed", "person_id", l.personID, "error", err)
			return "", err
		}

		response, err := l.invokeLLM(ctx)
		if err != nil {
			applogger.Error("JinshuRead LLM error", "person_id", l.personID, "jinshu_id", l.jinshuID, "iteration", iteration, "error", err)
			return "", err
		}

		switch response.FinishReason {
		case "stop":
			if response.Content != "" {
				l.messages = append(l.messages, llm.Message{Role: "assistant", Content: response.Content})
			}
			applogger.Info("JinshuRead loop completed (agent stopped)",
				"person_id", l.personID, "jinshu_id", l.jinshuID, "iteration", iteration)
			return response.Content, nil

		case "tool_calls":
			l.messages = append(l.messages, llm.Message{
				Role:      "assistant",
				Content:   response.Content,
				ToolCalls: response.ToolCalls,
			})
			for _, tc := range response.ToolCalls {
				l.messages = append(l.messages, l.executeToolCall(tc))
			}

		case "length":
			l.messages = append(l.messages, llm.Message{Role: "assistant", Content: response.Content})
			l.messages = append(l.messages, llm.Message{
				Role:    "user",
				Content: "[System] Your previous response was truncated. Please continue succinctly.",
			})

		default:
			applogger.Error("JinshuRead unexpected finish_reason",
				"person_id", l.personID, "jinshu_id", l.jinshuID, "finish_reason", response.FinishReason)
		}
	}

	applogger.Info("JinshuRead loop paused (iteration budget exhausted)",
		"person_id", l.personID, "jinshu_id", l.jinshuID, "max_iterations", l.maxIterations)
	return l.finalize(ctx)
}

// buildInitialMessages sets up the system prompt plus the initial read task.
func (l *ReadLoop) buildInitialMessages() {
	l.messages = nil

	systemPrompt := jinshuReadSystemPrompt + "\n\n" +
		fmt.Sprintf("Jinshu #%d received directory: %s\n", l.jinshuID, l.receivedDir) +
		fmt.Sprintf("Your reading intention: %s\n", l.guidance) +
		"Current time: " + time.Now().Format("2006-01-02 15:04:05") + "\n"

	if l.fileList != "" {
		systemPrompt += "\nFiles in this jinshu:\n" + l.fileList + "\n"
	}

	l.messages = append(l.messages, llm.Message{Role: "system", Content: systemPrompt})
	l.messages = append(l.messages, llm.Message{
		Role:    "user",
		Content: "Read the contents of this jinshu. Use read_jinshu_file (or summarize_jinshu_file for large files) to traverse the files. When you have understood the contents, stop and output a concise summary.",
	})
}

// invokeLLM calls the LLM with accumulated messages and all registered tool schemas.
func (l *ReadLoop) invokeLLM(ctx context.Context) (llm.ToolResponse, error) {
	toolDefs := make([]llm.FunctionDefinition, 0, len(l.toolRegistry))
	for _, t := range l.toolRegistry {
		toolDefs = append(toolDefs, t.Schema())
	}
	return l.llmClient.ChatWithTools(ctx, l.messages, toolDefs)
}

// executeToolCall looks up the tool by name, parses arguments, and executes.
func (l *ReadLoop) executeToolCall(tc llm.ToolCall) llm.Message {
	tool, ok := l.toolRegistry[tc.Function.Name]
	if !ok {
		applogger.Error("JinshuRead unknown tool", "tool", tc.Function.Name)
		return llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("Unknown tool: %s", tc.Function.Name),
		}
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		applogger.Error("JinshuRead failed to parse tool args", "tool", tc.Function.Name, "error", err)
		return llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("Invalid arguments for %s: %s", tc.Function.Name, err.Error()),
		}
	}

	result, err := tool.Execute(args)
	if err != nil {
		applogger.Error("JinshuRead tool execution failed", "tool", tc.Function.Name, "error", err)
		return llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("Error executing %s: %s", tc.Function.Name, err.Error()),
		}
	}

	return llm.Message{
		Role:       "tool",
		ToolCallID: tc.ID,
		Content:    result,
	}
}

// finalize asks the LLM for a summary after the iteration budget is exhausted,
// so the loop still produces a result even when the agent never stopped.
func (l *ReadLoop) finalize(ctx context.Context) (string, error) {
	l.messages = append(l.messages, llm.Message{
		Role:    "user",
		Content: "You have reached the step limit. Based on what you have read so far, produce a concise summary of the jinshu contents. Output only the summary, in the same language as your reading intention.",
	})
	return l.llmClient.Chat(ctx, l.messages)
}
