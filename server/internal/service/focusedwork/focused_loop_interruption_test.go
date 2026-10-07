package focusedwork

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	focusedworkcontext "qingqiu-world-server/internal/service/focusedwork/context"
	"qingqiu-world-server/internal/service/focusedwork/tools"
	"qingqiu-world-server/internal/service/llm"
)

// TestInterruptedResultSeparatesContextTerminationFromCancelAttribution
// ensures a deadline is not mislabeled as an explicit Cancel Action.
func TestInterruptedResultSeparatesContextTerminationFromCancelAttribution(t *testing.T) {
	if got := interruptedResult(context.Background()); got != nil {
		t.Fatalf("active context was interrupted: %+v", got)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := interruptedResult(cancelCtx); got == nil || !strings.Contains(got.Reason, context.Canceled.Error()) {
		t.Fatalf("cancellation reason was lost: %+v", got)
	}

	deadlineCtx, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if got := interruptedResult(deadlineCtx); got == nil || !strings.Contains(got.Reason, context.DeadlineExceeded.Error()) {
		t.Fatalf("deadline was mislabeled as cancellation: %+v", got)
	}
}

// blockingFocusedTool simulates a tool that has already begun and cannot be
// interrupted through context because the Tool interface has no context input.
type blockingFocusedTool struct {
	started   chan struct{}
	release   chan struct{}
	firstDone atomic.Int32
	secondRun atomic.Int32
}

// Name registers the test double under the bash tool name used by the model.
func (b *blockingFocusedTool) Name() tools.ToolName { return tools.ToolNameBash }

// Description satisfies the Tool interface for the test response.
func (b *blockingFocusedTool) Description() string { return "Block the first test tool call" }

// Schema exposes the step argument used to distinguish sequential calls.
func (b *blockingFocusedTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{Name: b.Name().String(), Parameters: map[string]interface{}{
		"type": "object", "properties": map[string]interface{}{"step": map[string]interface{}{"type": "string"}},
	}}
}

// Execute blocks the first call until the test releases it and counts later calls.
func (b *blockingFocusedTool) Execute(args map[string]interface{}) (string, error) {
	if args["step"] == "first" {
		close(b.started)
		<-b.release
		b.firstDone.Add(1)
		return "first call finished", nil
	}
	b.secondRun.Add(1)
	return "second call finished", nil
}

// CycleDetect stays neutral so cancellation alone determines whether the loop stops.
func (b *blockingFocusedTool) CycleDetect(map[string]interface{}, string) tools.CycleStatus {
	return tools.CycleStatus{}
}

// TestCancelDuringToolStopsFollowingCalls verifies the physical boundary:
// an already executing tool may finish, but the next tool and LLM turn do not run.
func TestCancelDuringToolStopsFollowingCalls(t *testing.T) {
	var requests atomic.Int32
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local HTTP listener is unavailable: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) != 1 {
			http.Error(w, "unexpected second model request", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"first","type":"function","function":{"name":"bash","arguments":"{\"step\":\"first\"}"}},{"id":"second","type":"function","function":{"name":"bash","arguments":"{\"step\":\"second\"}"}}]}}]}`)
	})}}
	server.Start()
	defer server.Close()

	tool := &blockingFocusedTool{started: make(chan struct{}), release: make(chan struct{})}
	loop := NewFocusedLoop(
		llm.NewChatModel(server.URL, "test", "test-model"), nil, []tools.Tool{tool},
		focusedworkcontext.NewContextManager("test", 10, 20, "", "", "", ""),
		3, 0, 0, 0, nil, nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan *LoopResult, 1)
	go func() { finished <- loop.Run(ctx) }()

	select {
	case <-tool.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first tool did not begin")
	}
	cancel()
	close(tool.release)
	select {
	case result := <-finished:
		if result.Status != "failure" || !strings.Contains(result.Reason, context.Canceled.Error()) {
			t.Fatalf("Focus ignored cancellation: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Focus did not stop after the in-flight tool returned")
	}
	if tool.firstDone.Load() != 1 || tool.secondRun.Load() != 0 || requests.Load() != 1 {
		t.Fatalf("execution continued after cancellation: first=%d second=%d model_requests=%d",
			tool.firstDone.Load(), tool.secondRun.Load(), requests.Load())
	}
}
