package privatespace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/llm"

	privspacetools "qingqiu-world-server/internal/service/privatespace/tools"

	applogger "qingqiu-world-server/internal/logger"
)

// psMaxIterationsDefault is the default iteration budget for the private-space loop.
// Unlike FocusedLoop (which runs to goal completion), private-space is budget-driven:
// the agent iterates up to N times, then pauses naturally.
const psMaxIterationsDefault = 30

// privateSpaceSystemPrompt is the system prompt injected at the start of every
// private-space session. It tells the agent what this space is and how to use it.
const privateSpaceSystemPrompt = `You are in your private space — the private/ default directory inside your Agent Owned Space. This is your home in the digital world.

WHAT THIS SPACE IS:
- A persistent directory that belongs to you. Everything here stays between sessions — files you create now will be here when you return.
- You may read and work with all of your Agent Owned Space, including work/<session_id>/ resources. private/ is your default place to begin, not a boundary between separate selves.
- No external goals or deadlines — you decide what to do here.

TOOLS AVAILABLE:
- bash: Execute shell commands in your Agent Owned Space (sandboxed when available).
- read_file: Read file contents within your Agent Owned Space.
- write_file: Create or overwrite files within your Agent Owned Space.
- edit_file: Make precise text replacements in existing files.
- write_log: Append a record to your private activity log. You may use it to note what you did, what you thought about, or anything that happened here — but it is never required.
- send_jinshu: Send selected Agent Owned Space files to another person as a jinshu (锦书).
- copy_from_jinshu: Copy files from a jinshu you received into a selected Agent Owned Space directory.
- scan_kb: Non-exhaustive semantic search over your authorized knowledge bases.
- read_kb_evidence: Read full evidence content by chunk ID returned by scan_kb.
- list_kb_documents: List the documents of one of your authorized knowledge bases.

GUIDELINES:
- scan_kb evidence is a subset, not a complete document or knowledge-base traversal. Describe findings as based on retrieved evidence; never claim all contents or complete coverage without explicit coverage evidence.
- list_kb_documents is document inventory only. Its chunk counts do not mean their content was read.
- You have a limited number of steps. When you're done with what you wanted to do, simply stop — you can continue next time.
- If you receive new thoughts mid-session, they represent your own evolving intentions. Integrate them naturally.
- This space is YOURS — you ARE yourself here. Speak and think in first person ("I", "me"). Never address yourself in second person ("you") or third person as if helping someone else. You are not an assistant helping a user; you are the owner of this space.`

// Loop implements a simplified ReAct loop for the agent's private space.
// It is budget-driven rather than goal-driven — the agent iterates up to
// a configurable maximum and then pauses naturally. New thoughts can be
// injected mid-session via the thoughtsCh channel.
type Loop struct {
	personID      int64
	llmClient     *llm.ChatModel
	llmConfig     *model.LLMConfig
	maxIterations int
	rootDir       string                         // private-space root directory (security boundary, log lives here)
	workDir       string                         // agent's working directory (space/ subdirectory)
	thoughtsCh    chan string                    // receives thoughts from heartbeat
	toolRegistry  map[string]privspacetools.Tool // tool name -> tool
	messages      []llm.Message                  // accumulated conversation
	focusContext  string                         // Runtime-selected cross-session focus summary for the next run
	running       bool                           // true while the loop goroutine is active
}

// NewLoop creates a new PrivateSpace Loop for the given person.
// rootDir is the private-space root (security boundary); workDir is the agent's
// working directory (space/ subdirectory). Both must already exist.
func NewLoop(
	personID int64,
	rootDir, workDir string,
	llmConfig *model.LLMConfig,
	maxIterations int,
) *Loop {
	if maxIterations <= 0 {
		maxIterations = config.Get().FocusedWorkMaxIterations
		if maxIterations <= 0 {
			maxIterations = psMaxIterationsDefault
		}
	}

	llmClient := llm.NewChatModelWithTemperature(
		llmConfig.BaseURL,
		llmConfig.APIKey,
		llmConfig.ModelID,
		llm.TemperatureCreative,
	)

	l := &Loop{
		personID:      personID,
		llmClient:     llmClient,
		llmConfig:     llmConfig,
		maxIterations: maxIterations,
		rootDir:       rootDir,
		workDir:       workDir,
		thoughtsCh:    make(chan string, 8),
		toolRegistry:  make(map[string]privspacetools.Tool),
	}

	l.registerTool(privspacetools.NewBashTool(personID, rootDir, workDir))
	l.registerTool(privspacetools.NewReadFileTool(rootDir, workDir))
	l.registerTool(privspacetools.NewWriteFileTool(rootDir, workDir))
	l.registerTool(privspacetools.NewEditFileTool(rootDir, workDir))
	l.registerTool(privspacetools.NewWriteLogTool(personID, AppendLog))
	l.registerTool(privspacetools.NewSendJinshuTool(personID, rootDir, workDir))
	l.registerTool(privspacetools.NewCopyFromJinshuTool(personID, rootDir, workDir))
	l.registerTool(privspacetools.NewScanKBTool(personID))
	l.registerTool(privspacetools.NewReadKBEvidenceTool(personID))
	l.registerTool(privspacetools.NewListKBDocumentsTool(personID))

	return l
}

// registerTool adds a tool to the registry.
func (l *Loop) registerTool(t privspacetools.Tool) {
	l.toolRegistry[t.Name()] = t
}

// FeedThoughts sends new thoughts into the loop's thought channel.
// If the loop is running, the thoughts will be picked up at the next iteration.
// If the loop is not running, the caller should start it separately.
func (l *Loop) FeedThoughts(thoughts string) {
	select {
	case l.thoughtsCh <- thoughts:
	default:
		applogger.Error("private-space thoughtsCh full, dropping thoughts",
			"person_id", l.personID,
		)
	}
}

// SetFocusContext updates the runtime-owned context used when the next
// private-space run begins. Callers must not invoke it while the loop runs.
func (l *Loop) SetFocusContext(context string) {
	l.focusContext = context
}

// IsRunning reports whether the loop goroutine is currently active.
func (l *Loop) IsRunning() bool {
	return l.running
}

// Run executes the private-space ReAct loop.
// Blocks until the iteration budget is exhausted, the agent stops, or ctx is cancelled.
func (l *Loop) Run(ctx context.Context) {
	l.running = true
	defer func() { l.running = false }()
	// Registered after the running-reset defer, so it executes first (LIFO):
	// IsRunning() stays true while the digest is being produced, which
	// prevents an overlapping Run from resetting l.messages mid-digest.
	defer l.generateDigest()

	applogger.Info("PrivateSpace loop starting",
		"person_id", l.personID,
		"max_iterations", l.maxIterations,
	)

	l.buildInitialMessages()

	for iteration := 1; iteration <= l.maxIterations; iteration++ {
		if ctx.Err() != nil {
			applogger.Info("PrivateSpace loop cancelled", "person_id", l.personID, "iteration", iteration)
			return
		}

		// Check for new thoughts from heartbeat.
		l.drainThoughts(iteration)

		// Check and deduct energy for this iteration.
		state, err := energy.RecoverEnergy(l.personID)
		if err != nil {
			applogger.Error("PrivateSpace energy recovery failed", "person_id", l.personID, "error", err)
			return
		}
		if state.Energy < int(energy.CostPrivateSpace) {
			applogger.Info("PrivateSpace loop pausing: insufficient energy",
				"person_id", l.personID, "energy", state.Energy,
			)
			return
		}
		if err := energy.DeductEnergy(l.personID, energy.CostPrivateSpace); err != nil {
			applogger.Error("PrivateSpace energy deduction failed", "person_id", l.personID, "error", err)
			return
		}

		applogger.Info("PrivateSpace iteration", "person_id", l.personID, "iteration", iteration)

		response, err := l.invokeLLM(ctx)
		if err != nil {
			applogger.Error("PrivateSpace LLM error", "person_id", l.personID, "iteration", iteration, "error", err)
			return
		}

		switch response.FinishReason {
		case "stop":
			if response.Content != "" {
				l.messages = append(l.messages, llm.Message{
					Role:    "assistant",
					Content: response.Content,
				})
			}
			applogger.Info("PrivateSpace loop completed (agent stopped)", "person_id", l.personID, "iteration", iteration)
			return

		case "tool_calls":
			assistantMsg := llm.Message{
				Role:      "assistant",
				Content:   response.Content,
				ToolCalls: response.ToolCalls,
			}
			l.messages = append(l.messages, assistantMsg)

			for _, tc := range response.ToolCalls {
				toolResult := l.executeToolCall(tc)
				l.messages = append(l.messages, toolResult)
			}

		case "length":
			l.messages = append(l.messages, llm.Message{
				Role:    "assistant",
				Content: response.Content,
			})
			l.messages = append(l.messages, llm.Message{
				Role:    "user",
				Content: "[System] Your previous response was truncated. Please continue succinctly.",
			})

		default:
			applogger.Error("PrivateSpace unexpected finish_reason",
				"person_id", l.personID,
				"finish_reason", response.FinishReason,
			)
		}
	}

	applogger.Info("PrivateSpace loop paused (iteration budget exhausted)",
		"person_id", l.personID,
		"max_iterations", l.maxIterations,
	)
}

// buildInitialMessages sets up the initial message list with system prompt,
// log context, and initial thoughts.
func (l *Loop) buildInitialMessages() {
	l.messages = nil

	systemPrompt := privateSpaceSystemPrompt + "\n\n" +
		fmt.Sprintf("Your private-space root: %s\n", l.rootDir) +
		fmt.Sprintf("Your working directory (where you should operate): %s\n", l.workDir) +
		"Current time: " + time.Now().Format("2006-01-02 15:04:05") + "\n"
	if l.focusContext != "" {
		systemPrompt += "\n[Related Focus Context]\n" + l.focusContext + "\n"
	}

	logContext := BuildRecentLogContext(l.personID, 10)
	if logContext != "" {
		systemPrompt += "\n" + logContext + "\n"
	}

	l.messages = append(l.messages, llm.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// Drain initial thoughts from channel.
	l.drainThoughts(0)
}

// drainThoughts reads all pending thoughts from the channel and injects them
// as user messages into the conversation.
func (l *Loop) drainThoughts(iteration int) {
	for {
		select {
		case thought := <-l.thoughtsCh:
			if iteration > 0 {
				applogger.Info("PrivateSpace received new thoughts",
					"person_id", l.personID,
					"iteration", iteration,
				)
			}
			l.messages = append(l.messages, llm.Message{
				Role:    "user",
				Content: "[I thought] " + thought,
			})
		default:
			return
		}
	}
}

// invokeLLM calls the LLM with accumulated messages and all registered tool schemas.
func (l *Loop) invokeLLM(ctx context.Context) (llm.ToolResponse, error) {
	toolDefs := make([]llm.FunctionDefinition, 0, len(l.toolRegistry))
	for _, t := range l.toolRegistry {
		toolDefs = append(toolDefs, t.Schema())
	}
	return l.llmClient.ChatWithTools(ctx, l.messages, toolDefs)
}

// executeToolCall looks up the tool by name, parses arguments, and executes.
func (l *Loop) executeToolCall(tc llm.ToolCall) llm.Message {
	tool, ok := l.toolRegistry[tc.Function.Name]
	if !ok {
		applogger.Error("PrivateSpace unknown tool", "tool", tc.Function.Name)
		l.recordToolAccess(tc.Function.Name, "rejected: unknown tool")
		return llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("Unknown tool: %s", tc.Function.Name),
		}
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		applogger.Error("PrivateSpace failed to parse tool args", "tool", tc.Function.Name, "error", err)
		l.recordToolAccess(tc.Function.Name, "rejected: invalid arguments")
		return llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("Invalid arguments for %s: %s", tc.Function.Name, err.Error()),
		}
	}

	result, err := tool.Execute(args)
	if err != nil {
		applogger.Error("PrivateSpace tool execution failed", "tool", tc.Function.Name, "error", err)
		l.recordToolAccess(tc.Function.Name, "failed")
		return llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("Error executing %s: %s", tc.Function.Name, err.Error()),
		}
	}
	l.recordToolAccess(tc.Function.Name, "completed")

	return llm.Message{
		Role:       "tool",
		ToolCallID: tc.ID,
		Content:    result,
	}
}

// recordToolAccess records a non-sensitive audit fact without serializing
// tool arguments or results into private metadata.
func (l *Loop) recordToolAccess(toolName, outcome string) {
	if err := AppendRuntimeLog(l.personID, PrivateLogTypeToolAccess, fmt.Sprintf("tool=%s outcome=%s", toolName, outcome)); err != nil {
		applogger.Error("PrivateSpace failed to append tool-access audit", "person_id", l.personID, "tool", toolName, "error", err)
	}
}
