package eventqueue

import (
	"strings"
	"testing"
)

// TestWorkCompletionDescriptionReportsResult verifies that a successful
// execution does not hide what Focus actually reported to the next Decision.
func TestWorkCompletionDescriptionReportsResult(t *testing.T) {
	event := AgentEvent{Type: EventTypeWorkCompleted, Payload: &WorkCompletedPayload{
		WorkID: 64, Guidance: "Check game files", Status: "success",
		WorkOutput: "The files exist; no further work is needed.",
	}}
	description := event.FormatDescription()
	for _, part := range []string{"Work #64", "execution status: success", "Focus reported: The files exist; no further work is needed"} {
		if !strings.Contains(description, part) {
			t.Fatalf("completion description omits %q: %s", part, description)
		}
	}
	event.Payload = &WorkCompletedPayload{WorkID: 65, Guidance: "Check game files", Status: "success"}
	if description = event.FormatDescription(); !strings.Contains(description, "No result was reported") {
		t.Fatalf("empty report was presented as a verified result: %s", description)
	}
}
