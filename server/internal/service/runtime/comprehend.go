package runtime

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/model"
)

// buildActiveWorksSummary generates a natural language description of the
// agent's currently running works in the given session.
// Returns empty string if no works are active.
func buildActiveWorksSummary(works []*work, sessionID int64) string {
	var sameSessionWorks []*work
	for _, w := range works {
		if w.sessionID == sessionID {
			sameSessionWorks = append(sameSessionWorks, w)
		}
	}
	if len(sameSessionWorks) == 0 {
		return ""
	}

	var parts []string
	for _, w := range sameSessionWorks {
		typeName := "chat"
		if w.plan.Type == model.WorkTypeTask {
			typeName = "task"
		}
		parts = append(parts, fmt.Sprintf("- [%s] %s", typeName, w.plan.Guidance))
	}
	return fmt.Sprintf("Agent's current active works:\n%s", strings.Join(parts, "\n"))
}
