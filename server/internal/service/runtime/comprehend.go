package runtime

import (
	"fmt"
	"strings"
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
		parts = append(parts, fmt.Sprintf("- %s", w.plan.Guidance))
	}
	return fmt.Sprintf("Agent's current active works:\n%s", strings.Join(parts, "\n"))
}
