package runtime

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// buildActiveWorksSummary generates a session-level works section for the
// Comprehend phase. It covers both in-progress works (from the in-memory
// active set) and recently failed works in the same session (from the
// database), so the LLM sees not only what the agent is doing but also what
// recently went wrong — e.g. to avoid re-issuing a doomed task.
// Returns empty string when there is nothing to report.
func buildActiveWorksSummary(works []*work, personID, sessionID int64) string {
	var parts []string
	for _, w := range works {
		if w.sessionID == sessionID {
			parts = append(parts, fmt.Sprintf("- %s (running)", w.plan.Guidance))
		}
	}

	var failed []model.Work
	if err := database.DB.Where("person_id = ? AND session_id = ? AND status = ?",
		personID, sessionID, model.WorkStatusFailed).
		Order("id DESC").Limit(3).Find(&failed).Error; err != nil {
		applogger.Error("buildActiveWorksSummary: failed to load failed works",
			"person_id", personID, "session_id", sessionID, "error", err)
	}
	for _, wr := range failed {
		parts = append(parts, fmt.Sprintf("- %s (failed)", truncateWorkDescription(wr.Description)))
	}

	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Agent's works in this session:\n%s", strings.Join(parts, "\n"))
}
