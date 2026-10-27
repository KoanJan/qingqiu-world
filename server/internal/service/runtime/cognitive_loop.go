package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/vectorutils"
	"qingqiu-world-server/internal/service/world"

	"gorm.io/gorm"
)

// A process file contains the last LLM request and its response. The request's
// messages contain the conversation so no separate loop transcript is needed.
type cognitiveCheckpoint struct {
	Request  cognitiveLLMRequest `json:"request"`
	Response json.RawMessage     `json:"response"`
}

// cognitiveLLMRequest mirrors the JSON fields sent by the required-tool chat
// call. The API key remains in the provider connection, outside the checkpoint.
type cognitiveLLMRequest struct {
	Model             string                   `json:"model"`
	Messages          []llm.Message            `json:"messages"`
	Tools             []llm.FunctionDefinition `json:"tools"`
	ToolChoice        string                   `json:"tool_choice"`
	ParallelToolCalls bool                     `json:"parallel_tool_calls"`
	Temperature       float32                  `json:"temperature"`
}

// cognitiveRequest binds a memory transaction to the target selected before
// research; the final tool cannot choose a different target.
type cognitiveRequest struct {
	Description string
	MemoryID    int64
	Revision    int
}

// cognitiveOutcome identifies the terminal treatment of the selected target.
type cognitiveOutcome int

const (
	cognitiveWriteCognition cognitiveOutcome = iota + 1
	cognitiveConfirmUnchanged
	cognitiveNoResult
)

// cognitiveDayStart returns the local midnight that begins the current contact
// day. It uses the same fixed timezone as energy and daily memories, so a
// process that ended just before midnight belongs to the previous day.
func cognitiveDayStart() time.Time {
	now := energy.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, energy.Timezone())
}

// cognitiveDayUsage records what today's ended processes already consumed.
// Every existing memory may be reviewed once per day and one new object may be
// formed, so a process that ends without a result still spends its target's
// slot. Updates counts system-routed reviews separately: they have their own
// daily cap and never consume the discovery slot. MemoryID zero means the
// process never selected an existing memory; such a process spends the day's
// discovery slot instead.
type cognitiveDayUsage struct {
	Ended     int64
	Updates   int
	Reviewed  map[int64]bool
	Discovery bool
}

// loadCognitiveDayUsage reads the processes that ended today. The process row
// records the selected memory before research begins, so an outcome 3 process
// is counted even though it does not refresh that memory's updated_at.
func loadCognitiveDayUsage(ctx context.Context, personID int64) (cognitiveDayUsage, error) {
	usage := cognitiveDayUsage{Reviewed: make(map[int64]bool)}
	var ended []model.CognitiveProcess
	err := database.DB.WithContext(ctx).
		Select("id", "kind", "memory_id").
		Where("person_id = ? AND status = ? AND updated_at >= ?", personID, model.CognitiveProcessEnded, cognitiveDayStart()).
		Find(&ended).Error
	if err != nil {
		return usage, err
	}
	usage.Ended = int64(len(ended))
	for _, process := range ended {
		if process.Kind == model.CognitiveProcessKindUpdate {
			usage.Updates++
			usage.Reviewed[process.MemoryID] = true
			continue
		}
		// A discovery spends the day's single discovery slot whether it selects
		// an existing memory or forms a new one; a selected memory is still
		// attributed for the per-memory review quota.
		usage.Discovery = true
		if process.MemoryID > 0 {
			usage.Reviewed[process.MemoryID] = true
		}
	}
	return usage, nil
}

// cognitiveUpdateReviewsPerDay bounds system-routed reviews. Each routed review
// is a separate process holding exactly one memory, so this cap is also the
// number of update conversations the router may start in one day. Discovery
// stays at one process per day through cognitiveDayUsage.Discovery.
const cognitiveUpdateReviewsPerDay = 5

const (
	// A process needs calls for intention, target selection, and completion.
	cognitiveMinimumRequestCount = 3
	// This fallback applies only if a configured review interval is invalid.
	cognitiveReviewFallbackHours = 24

	// Opening previews are navigation hints, not the process's complete
	// history. One extra source row detects omitted recent history.
	cognitiveRecentObservationPreview = 8
	cognitiveRecentActionPreview      = 4
	cognitiveDatedPreviewDays         = 7
	cognitiveDatedPreviewRows         = 12
	cognitivePriorMemoryPreviewRows   = 8

	// Rune limits bound only text shown in an opening or routed review prompt.
	cognitiveEventPreviewRunes             = 1300
	cognitiveActionPreviewRunes            = 900
	cognitiveDatedSummaryPreviewRunes      = 360
	cognitivePriorDescriptionPreviewRunes  = 180
	cognitivePriorConclusionPreviewRunes   = 220
	cognitiveRoutedDescriptionPreviewRunes = 320
	cognitiveRoutedConclusionPreviewRunes  = 900

	// These thresholds report unusually large tool results; they do not trim
	// results or reject a recall request.
	cognitiveRecallResultWarnBytes  = 8000
	cognitiveRecallContextWarnBytes = 32000
	cognitivePercentageScale        = 100.0
	cognitiveCheckpointDirMode      = 0o700
)

// cognitiveAdmission describes the next process the system may create.
type cognitiveAdmission struct {
	Kind           model.CognitiveProcessKind
	MemoryID       int64
	MemoryRevision int
	Request        cognitiveLLMRequest
}

// cognitiveAdmissionBudget reports the remaining routed-update slots and
// whether the discovery slot is still free. It is separated from routing so the
// daily quota can be verified without building prompts.
func cognitiveAdmissionBudget(usage cognitiveDayUsage) (int, bool) {
	remaining := cognitiveUpdateReviewsPerDay - usage.Updates
	if remaining < 0 {
		remaining = 0
	}
	return remaining, !usage.Discovery
}

// prepareCognitiveAdmission decides which process to start. Routed updates come
// first because their evidence is already known; a discovery follows when no
// update candidate remains and today's discovery slot is free. A routing error
// is returned instead of silently falling back to discovery: a missing vector
// is a defect that must be visible, not a reason to skip evidence.
func prepareCognitiveAdmission(ctx context.Context, personID int64) (cognitiveAdmission, bool, error) {
	usage, err := loadCognitiveDayUsage(ctx, personID)
	if err != nil {
		return cognitiveAdmission{}, false, err
	}
	remaining, discoveryOpen := cognitiveAdmissionBudget(usage)
	if remaining > 0 {
		minSimilarity := float64(config.Get().CognitiveRouteMinSimilarity) / cognitivePercentageScale
		candidates, coverage, err := memory.SelectCognitiveUpdateCandidates(ctx, personID, cognitiveDayStart(), remaining, minSimilarity)
		if err != nil {
			return cognitiveAdmission{}, false, err
		}
		if len(candidates) > 0 {
			candidate := candidates[0]
			request, err := cognitiveUpdateRequest(personID, candidate)
			if err != nil {
				return cognitiveAdmission{}, false, err
			}
			applogger.Info("cognitive update routed", "person_id", personID, "memory_id", candidate.MemoryID,
				"revision", candidate.Revision, "similarity", candidate.Similarity, "threshold", minSimilarity,
				"remaining_updates", remaining, "coverage", coverage)
			return cognitiveAdmission{
				Kind: model.CognitiveProcessKindUpdate, MemoryID: candidate.MemoryID,
				MemoryRevision: candidate.Revision, Request: request,
			}, true, nil
		}
		applogger.Debug("cognitive update routing found no candidate", "person_id", personID,
			"threshold", minSimilarity, "remaining_updates", remaining, "coverage", coverage)
	}
	if discoveryOpen {
		request, err := initialCognitiveRequest(ctx, personID)
		if err != nil {
			return cognitiveAdmission{}, false, err
		}
		return cognitiveAdmission{Kind: model.CognitiveProcessKindDiscovery, Request: request}, true, nil
	}
	return cognitiveAdmission{}, false, nil
}

// validateCognitiveDailySelection rejects a target whose daily slot is spent.
// Admission may have happened before an earlier process ended, so the usage is
// read again when the model commits to a target.
func validateCognitiveDailySelection(ctx context.Context, personID int64, target cognitiveRequest) error {
	usage, err := loadCognitiveDayUsage(ctx, personID)
	if err != nil {
		return err
	}
	if target.MemoryID == 0 {
		if usage.Discovery {
			return fmt.Errorf("a new concern is unavailable for now; choose an earlier understanding or end without research")
		}
		return nil
	}
	if usage.Reviewed[target.MemoryID] {
		return fmt.Errorf("memory %d is unavailable for review now; choose another understanding or end without research", target.MemoryID)
	}
	// A memory created or revised today is also outside the remaining quota,
	// even when no ended process row recorded it (for example a memory formed
	// earlier today). Ownership is re-checked in the final transaction, so a
	// missing row is left to that check.
	var owned struct{ UpdatedAt time.Time }
	err = database.DB.WithContext(ctx).Model(&model.CognitiveMemory{}).
		Select("updated_at").
		Where("id = ? AND person_id = ?", target.MemoryID, personID).
		Take(&owned).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !owned.UpdatedAt.Before(cognitiveDayStart()) {
		return fmt.Errorf("memory %d is unavailable for review now; choose another understanding or end without research", target.MemoryID)
	}
	return nil
}

// recordCognitiveTarget stores the selected memory identity on the running
// process row before research begins. The status and epoch conditions prevent
// a stale owner from overwriting a newer process.
func recordCognitiveTarget(process model.CognitiveProcess, memoryID int64) error {
	updated := database.DB.Model(&model.CognitiveProcess{}).
		Where("id = ? AND status = ? AND epoch = ?", process.ID, model.CognitiveProcessRunning, process.Epoch).
		Update("memory_id", memoryID)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("cognitive process %d is not running at epoch %d", process.ID, process.Epoch)
	}
	return nil
}

// cognitiveFinish carries only the result; target identity comes from the
// earlier selection retained in the model conversation.
type cognitiveFinish struct {
	Outcome       cognitiveOutcome `json:"outcome"`
	Revisit       bool             `json:"revisit"`
	Impression    string           `json:"impression"`
	Understanding string           `json:"understanding"`
}

// maxCognitiveRequests keeps room for intention, target selection, and a
// terminal finish call even when the configured request budget is smaller.
func maxCognitiveRequests() int {
	return max(cognitiveMinimumRequestCount, config.Get().CognitiveMaxRequests)
}

// cognitiveRecentReview converts a configured review interval without overflow.
func cognitiveRecentReview() time.Duration {
	hours := int64(config.Get().CognitiveRecentReviewHours)
	const maxDuration = time.Duration(1<<63 - 1)
	if hours <= 0 {
		applogger.Error("invalid cognitive recent review hours", "hours", hours)
		return time.Duration(cognitiveReviewFallbackHours) * time.Hour
	}
	if hours > int64(maxDuration/time.Hour) {
		applogger.Error("cognitive recent review hours exceed duration range", "hours", hours)
		return maxDuration
	}
	return time.Duration(hours) * time.Hour
}

// cognitiveTool builds a required-call schema shared by the process stages.
// additionalProperties is closed because the decoder rejects unknown fields.
func cognitiveTool(name, description string, properties map[string]interface{}, required []string) llm.FunctionDefinition {
	return llm.FunctionDefinition{Name: name, Description: description, Parameters: map[string]interface{}{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
}

// cognitiveFinishTool is the only tool that can commit a cognitive result.
func cognitiveFinishTool() llm.FunctionDefinition {
	return cognitiveTool("finish_cognition", fmt.Sprintf("End the selected cognition. Outcome %[1]d writes a first impression and understanding or a substantive revision of the current version. Outcome %[2]d confirms that an existing version still holds; outcome %[3]d ends this process without a new result and leaves any existing memory unchanged. Compare both fields with the complete current version before revising it. A new event alone does not require a new version. For outcome %[1]d, provide both fields; for outcomes %[2]d and %[3]d, leave them empty. Outcome %[3]d also requires revisit=false. Set revisit=true when you deliberately re-examine an understanding you reviewed recently; it does not determine the outcome. Keep the selected object and memory identity. Do not force a result or add fields outside this schema.", cognitiveWriteCognition, cognitiveConfirmUnchanged, cognitiveNoResult), map[string]interface{}{
		"outcome":       map[string]interface{}{"type": "integer", "enum": []cognitiveOutcome{cognitiveWriteCognition, cognitiveConfirmUnchanged, cognitiveNoResult}, "description": fmt.Sprintf("%d = a first impression and understanding, or a substantive revision; %d = the current version still holds; %d = no new result, leaving any existing memory unchanged.", cognitiveWriteCognition, cognitiveConfirmUnchanged, cognitiveNoResult)},
		"revisit":       map[string]interface{}{"type": "boolean", "description": fmt.Sprintf("True when you intentionally re-examine an understanding you reviewed recently; needed for outcomes %d and %d on such a memory. This confirms your choice to review again, not that your view changed.", cognitiveWriteCognition, cognitiveConfirmUnchanged)},
		"impression":    map[string]interface{}{"type": "string", "description": fmt.Sprintf("Required for outcome %d. Your overall impression of the selected object as you have encountered it: its relevant course and present shape where you can discern them. Form a coherent view rather than a dated event list; do not invent a turning point, a settled state, or another person's inner life. Empty for outcomes %d and %d.", cognitiveWriteCognition, cognitiveConfirmUnchanged, cognitiveNoResult)},
		"understanding": map[string]interface{}{"type": "string", "description": fmt.Sprintf("Required for outcome %d. What you think, feel, or make of the selected object. Include a causal interpretation or expectation only when the material supports one, and mark uncertainty where it does not. Keep the view specific to this object rather than stating a general rule; do not turn it into a plan for your next action. Empty for outcomes %d and %d.", cognitiveWriteCognition, cognitiveConfirmUnchanged, cognitiveNoResult)},
	}, []string{"outcome"})
}

// cognitivePromptTemplate shares the world's factual rules while describing
// the distinct boundaries of voluntary, evidence-based understanding. It uses
// the read-only world prefix so the model is not invited to use action tools
// that this process never offers.
const cognitivePromptTemplate = world.ChatWorldDescription + world.ResourceDescriptions + `

You are %s.

Your internal character (how you think of yourself — never revealed to others):
%s

Your public Bio (what you choose to present to others — this is what they see):
%s

This cognition is private: you cannot speak or act in the world here. Consider whether an object of lasting interest — an experience, a person, a relationship, a matter, or something else you have encountered — deserves attention now. You may also decline. Name the object you want to understand; possible earlier understandings will then be shown for you to compare. Decide whether to reconsider one of them or form a first view. Use the available read-only tools to check what happened, then record a view, confirm that an earlier view still holds, or finish without a result. Keep the selected object as the subject of this reflection. If it proves unsuitable, finish without recording a new understanding. Use only the tools currently offered.

The opening event records, selected actions, dated recollections of past days, and prior understandings are bounded previews, not your complete history. A chat line names its actual sender, including when you were the sender. An observed event establishes what you encountered, while a selected action and its stated reasons record your past intention and judgment, not execution or outcome. A previous cognitive memory is your interpretation, not an independently verified world fact. Daily memories are navigation indexes; consult original records when a detail matters.

A cognitive memory records your view of one selected object over time. The object may be an experience, a person, a relationship, a matter, or something else you have encountered.

impression is your overall view of that object as you have encountered it. Describe its relevant course and present shape where these are discernible. Keep the view coherent rather than listing events or dates; do not invent a turning point or a settled state. It is your own perspective, not a neutral digest or a claim about another person's inner life.

understanding is what you think, feel, or make of this object. Express a feeling, explanation, causal pattern, or expectation when it is part of your actual view and grounded in what you have encountered; none is required merely to fill a form. Keep claims specific to this object and acknowledge what you cannot yet tell. If you express an expectation, present it as uncertain rather than as a future fact. Do not state what you intend to do next.

Both fields are your own perspectives. Ground claims about what happened in records you can inspect, and express uncertainty where needed. Neither field records this process's own bookkeeping — updating a memo or scheduling a reminder — unless that act is part of what the object is.

When a shown memory already has a current version, use it as the baseline for this review. The opening preview may abbreviate that version; read the selected memory in full with recall_cognitive_memory before comparing your impression and understanding. If either has substantively changed, write a new version with outcome %[4]d; if both still hold, confirm it with outcome %[5]d. You may end without a result if the material does not support a meaningful review. If you deliberately reconsider an understanding you reviewed recently, set revisit=true to confirm that choice; it does not determine the outcome.

Preserve the language of the substantive records rather than the English labels and instructions. Write in Chinese when the substantive records are Chinese and in English when they are English; do not translate between languages.`

// cognitiveUpdatePromptTemplate describes a review the system already routed to
// one memory. It shares the world's factual rules and the subject's identity
// with the discovery prompt, but it has no intention or target-selection stage
// and no daily-choice paragraph: the one target is fixed before the model runs.
const cognitiveUpdatePromptTemplate = world.ChatWorldDescription + world.ResourceDescriptions + `

You are %s.

Your internal character (how you think of yourself — never revealed to others):
%s

Your public Bio (what you choose to present to others — this is what they see):
%s

This cognition is a private review of one selected earlier understanding. The dated recollections shown here are possible leads, not proof that a new event belongs to this concern. Consider only the selected understanding. You may decide that it still holds, revise it, or finish without a result if the evidence is insufficient. Do not switch to another object or form a new memory.

Only recall tools and finish_cognition are offered, and they are all read-only except finish_cognition. A dated recollection is a navigation index, not evidence of a connection or an outcome; check original records before relying on its claims to revise or confirm your view. An earlier cognitive memory is your own interpretation, not a verified world fact. The concern or current fields in the opening request may be abbreviated. If any ends with an ellipsis, read the full selected memory with recall_cognitive_memory before comparing versions or finishing. Only the tool offered in the current stage is valid; when only finish_cognition is offered, finish with the evidence you have, without claiming a comparison you could not make.

impression is your overall view of that object as you have encountered it. Describe its relevant course and present shape where these are discernible. Keep the view coherent rather than listing events or dates; do not invent a turning point or a settled state. It is your own perspective, not a neutral digest or a claim about another person's inner life.

understanding is what you think, feel, or make of this object. Express a feeling, explanation, causal pattern, or expectation when it is part of your actual view and grounded in what you have encountered; none is required merely to fill a form. Keep claims specific to this object and acknowledge what you cannot yet tell. If you express an expectation, present it as uncertain rather than as a future fact. Do not state what you intend to do next.

Compare both fields with the complete current version. Use outcome %[4]d only when either has substantively changed, and provide both fields in that case. Use outcome %[5]d when both still hold, and leave both empty. Use outcome %[6]d when the material does not support a meaningful review, and leave everything empty. Set revisit=false for this review. Preserve the language of the substantive records rather than the English labels and instructions.`

// cognitiveCheckpointPath locates one server-owned process checkpoint.
func cognitiveCheckpointPath(personID, processID int64) string {
	return filepath.Join(config.Get().DataRoot, "cognitive", strconv.FormatInt(personID, 10), strconv.FormatInt(processID, 10)+".json")
}

// writeCognitiveCheckpoint atomically replaces one process file. The caller
// holds cognitiveMu and has verified the current process epoch.
func writeCognitiveCheckpoint(personID, processID int64, state cognitiveCheckpoint) error {
	path := cognitiveCheckpointPath(personID, processID)
	if err := os.MkdirAll(filepath.Dir(path), cognitiveCheckpointDirMode); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cognitive-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if goruntime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// readCognitiveCheckpoint rejects incomplete process files before replay.
func readCognitiveCheckpoint(personID, processID int64) (cognitiveCheckpoint, error) {
	var state cognitiveCheckpoint
	path := cognitiveCheckpointPath(personID, processID)
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Request.Model == "" || state.Request.ToolChoice != "required" || state.Request.ParallelToolCalls || len(state.Request.Messages) == 0 || len(state.Request.Tools) == 0 || len(state.Response) == 0 {
		return state, fmt.Errorf("invalid cognitive checkpoint for process %d", processID)
	}
	return state, nil
}

// emptyCognitiveResponse marks a request awaiting its model response.
func emptyCognitiveResponse() json.RawMessage { return json.RawMessage("{}") }

// recentCognitiveEvidence gives the loop a bounded entry point from the
// person's actual observations and committed actions, without reading unseen Events.
func recentCognitiveEvidence(ctx context.Context, personID, afterObservationID, afterActionID int64) (string, int64, int64, error) {
	person, err := dops.GetPerson(personID)
	if err != nil {
		return "", 0, 0, err
	}
	// Fetch one extra row of each kind to tell the model when this entry
	// sample omitted history; it can inspect the original records with recall.
	var rows []model.AgentObservation
	q := database.DB.WithContext(ctx).Where("person_id = ?", personID)
	if afterObservationID > 0 {
		q = q.Where("id > ?", afterObservationID)
	}
	if err := q.Order("id DESC").Limit(cognitiveRecentObservationPreview + 1).Find(&rows).Error; err != nil {
		return "", 0, 0, err
	}
	var actions []model.Action
	actionQuery := database.DB.WithContext(ctx).Table("actions").Select("actions.*").Joins("JOIN decisions ON decisions.id = actions.decision_id").Where("decisions.person_id = ?", personID)
	if afterActionID > 0 {
		actionQuery = actionQuery.Where("actions.id > ?", afterActionID)
	}
	if err := actionQuery.Order("actions.id DESC").Limit(cognitiveRecentActionPreview + 1).Find(&actions).Error; err != nil {
		return "", 0, 0, err
	}
	type item struct {
		at   time.Time
		text string
	}
	items := make([]item, 0, len(rows)+len(actions))
	maxObservationID := afterObservationID
	for i := min(len(rows), cognitiveRecentObservationPreview) - 1; i >= 0; i-- {
		row := rows[i]
		if row.ID > maxObservationID {
			maxObservationID = row.ID
		}
		text, err := memory.DescribeObservedEventAsExperience(ctx, personID, person.Name, row.EventID)
		if err != nil {
			return "", 0, 0, err
		}
		text = truncateCognitivePreview(text, cognitiveEventPreviewRunes)
		items = append(items, item{row.CreatedAt, "Event record: " + text})
	}
	maxActionID := afterActionID
	for i := min(len(actions), cognitiveRecentActionPreview) - 1; i >= 0; i-- {
		a := actions[i]
		if a.ID > maxActionID {
			maxActionID = a.ID
		}
		text := "Selected action: " + a.Type.Label() + "."
		if background := strings.TrimSpace(a.Background); background != "" {
			text += "\n  Your judgment then: " + background
		}
		if reason := strings.TrimSpace(a.Reason); reason != "" {
			text += "\n  Your stated reason: " + reason
		}
		text += "\n  The selection alone does not prove execution or outcome."
		text = truncateCognitivePreview(text, cognitiveActionPreviewRunes)
		items = append(items, item{a.CreatedAt, text})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s — %s\n", it.at.In(energy.Timezone()).Format(time.RFC3339), it.text)
	}
	if len(rows) > cognitiveRecentObservationPreview || len(actions) > cognitiveRecentActionPreview {
		b.WriteString("Further recent records are omitted; use recall tools to inspect them.\n")
	}
	return b.String(), maxObservationID, maxActionID, nil
}

// recentDatedRecollections previews the last week's dated index so a thread
// spread across several days can be recognized before a concern is chosen.
// Each summary is a navigation hint; the daily sources remain the evidence.
func recentDatedRecollections(ctx context.Context, personID int64) (string, error) {
	now := energy.Now()
	today := now.Format("2006-01-02")
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, energy.Timezone()).AddDate(0, 0, -cognitiveDatedPreviewDays).Format("2006-01-02")
	var rows []model.DailyMemory
	if err := database.DB.WithContext(ctx).
		Where("person_id = ? AND local_date >= ? AND local_date < ?", personID, start, today).
		Order("local_date DESC, shard ASC").Limit(cognitiveDatedPreviewRows).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "None in this preview; a thread may still span days outside it.\n", nil
	}
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "- %s: %s\n", row.LocalDate, truncateCognitivePreview(row.Summary, cognitiveDatedSummaryPreviewRunes))
	}
	return b.String(), nil
}

// cognitiveReadTools opens recall only after a target has been selected.
func cognitiveReadTools() []llm.FunctionDefinition {
	tools := append([]llm.FunctionDefinition{cognitiveFinishTool()}, recallToolDefinitions(false)...)
	tools = append(tools, cognitiveRecallDefinitions()...)
	return append(tools, jinshuToolDefinitions()...)
}

// initialCognitiveRequest asks for an intention before any memory search or
// research tools are available. Later stages extend the same conversation.
func initialCognitiveRequest(ctx context.Context, personID int64) (cognitiveLLMRequest, error) {
	a, err := agent.GetAgent(personID)
	if err != nil {
		return cognitiveLLMRequest{}, err
	}
	evidence, _, _, err := recentCognitiveEvidence(ctx, personID, 0, 0)
	if err != nil {
		return cognitiveLLMRequest{}, err
	}
	if evidence == "" {
		evidence = "None in this preview; older records may still exist.\n"
	}
	// The dated preview is an index, not evidence. An unavailable index limits
	// the opening view but must not prevent a voluntary cognition.
	dated, err := recentDatedRecollections(ctx, personID)
	if err != nil {
		applogger.Error("cognitive dated recollection preview unavailable", "person_id", personID, "error", err)
		dated = "Unavailable in this preview; original records remain available through recall.\n"
	}
	prior, _, coverage, err := memory.FindCognitiveMemories(ctx, personID, "", time.Time{}, time.Time{}, memory.CognitiveTimeDefault, cognitivePriorMemoryPreviewRows, 0, false)
	if err != nil {
		return cognitiveLLMRequest{}, err
	}
	var priorText strings.Builder
	for _, item := range prior {
		fmt.Fprintf(&priorText, "- %s (concern first recorded %s; current revision recorded %s; last review completed %s): impression=%s | understanding=%s\n",
			truncateCognitivePreview(item.Description, cognitivePriorDescriptionPreviewRunes),
			item.ConcernCreatedAt.Format(time.RFC3339), item.CurrentUnderstandingFormedAt.Format(time.RFC3339), item.LastReviewedAt.Format(time.RFC3339),
			truncateCognitivePreview(item.Impression, cognitivePriorConclusionPreviewRunes), truncateCognitivePreview(item.Understanding, cognitivePriorConclusionPreviewRunes))
	}
	if len(prior) == 0 {
		priorText.WriteString("None.\n")
	}
	return cognitiveLLMRequest{
		Model: a.LLM.ModelID, ToolChoice: "required", ParallelToolCalls: false, Temperature: llm.TemperatureDeterministic,
		Messages: []llm.Message{
			{Role: "system", Content: fmt.Sprintf(cognitivePromptTemplate, a.Person.Name, a.Config.CharacterSettings, a.Person.Bio, cognitiveWriteCognition, cognitiveConfirmUnchanged)},
			{Role: "user", Content: fmt.Sprintf("Recent event records and selected actions (bounded preview):\n%s\nDated recollections of past days (bounded preview; each is a navigation hint, not evidence):\n%sYour current understandings of earlier threads (bounded preview; these are your own earlier conclusions, not evidence; compare against one if you choose it, but do not continue its text or numbering):\n%sCoverage: %s\nChoose which thread, if any, you want to understand for the first time or review against what has happened since.", evidence, dated, priorText.String(), coverage)},
		},
		Tools: []llm.FunctionDefinition{cognitiveIntentTool()},
	}, nil
}

// cognitiveUpdateRequest builds the single-target prompt for a routed review.
// It carries only the selected memory and its triggering dated accounts; the
// model may expand any account with recall_daily_memory by its identity.
func cognitiveUpdateRequest(personID int64, candidate memory.CognitiveReviewCandidate) (cognitiveLLMRequest, error) {
	a, err := agent.GetAgent(personID)
	if err != nil {
		return cognitiveLLMRequest{}, err
	}
	var evidence strings.Builder
	for _, row := range candidate.Evidence {
		fmt.Fprintf(&evidence, "- dated recollection %d (%s): %s\n", row.ID, row.LocalDate, truncateCognitivePreview(row.Summary, cognitiveDatedSummaryPreviewRunes))
	}
	if evidence.Len() == 0 {
		evidence.WriteString("None in this preview; original records remain available through recall.\n")
	}
	impression := candidate.Impression
	if strings.TrimSpace(impression) == "" {
		impression = "None recorded."
	}
	understanding := candidate.Understanding
	if strings.TrimSpace(understanding) == "" {
		understanding = "None recorded."
	}
	return cognitiveLLMRequest{
		Model: a.LLM.ModelID, ToolChoice: "required", ParallelToolCalls: false, Temperature: llm.TemperatureDeterministic,
		Messages: []llm.Message{
			{Role: "system", Content: fmt.Sprintf(cognitiveUpdatePromptTemplate, a.Person.Name, a.Config.CharacterSettings, a.Person.Bio, cognitiveWriteCognition, cognitiveConfirmUnchanged, cognitiveNoResult)},
			{Role: "user", Content: fmt.Sprintf("Review this selected understanding. The dated recollections below may or may not bear on it.\nMemory ID: %d\nConcern (may be abbreviated): %s\nLast completed review: %s\nCurrent impression (may be abbreviated): %s\nCurrent understanding (may be abbreviated): %s\n\nPossible dated leads (bounded preview; each is a navigation index, not evidence; expand one with recall_daily_memory using its identity when a detail matters):\n%sCoverage: this is not your complete history, and unrelated days may be missing. Check original records before concluding that an event did not occur.",
				candidate.MemoryID, truncateCognitivePreview(candidate.Description, cognitiveRoutedDescriptionPreviewRunes),
				candidate.LastReviewedAt.Format(time.RFC3339), truncateCognitivePreview(impression, cognitiveRoutedConclusionPreviewRunes), truncateCognitivePreview(understanding, cognitiveRoutedConclusionPreviewRunes), evidence.String())},
		},
		Tools: cognitiveReadTools(),
	}, nil
}

// truncateCognitivePreview bounds the initial sample without changing sources.
func truncateCognitivePreview(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// recentCognitiveEvidenceSince previews records committed after this process
// began. The process start is a conservative boundary: a source committed
// between a model response and its checkpoint write must remain discoverable.
func recentCognitiveEvidenceSince(ctx context.Context, personID int64, startedAt time.Time) (string, error) {
	if startedAt.IsZero() {
		return "", nil
	}
	var lastObservationID, lastActionID int64
	if err := database.DB.WithContext(ctx).Model(&model.AgentObservation{}).Where("person_id = ? AND created_at < ?", personID, startedAt).Select("COALESCE(MAX(id),0)").Scan(&lastObservationID).Error; err != nil {
		return "", err
	}
	if err := database.DB.WithContext(ctx).Table("actions a").Joins("JOIN decisions d ON d.id = a.decision_id").Where("d.person_id = ? AND a.created_at < ?", personID, startedAt).Select("COALESCE(MAX(a.id),0)").Scan(&lastActionID).Error; err != nil {
		return "", err
	}
	evidence, _, _, err := recentCognitiveEvidence(ctx, personID, lastObservationID, lastActionID)
	return evidence, err
}

// cognitiveRequestNumber follows the actual multi-turn message history.
func cognitiveRequestNumber(messages []llm.Message) int {
	number := 1
	for _, message := range messages {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			number++
		}
	}
	return number
}

// cognitiveRecallBytes counts retained tool output for diagnostic logging.
func cognitiveRecallBytes(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		if message.Role == "tool" {
			total += len(message.Content)
		}
	}
	return total
}

// validateCognitiveFinish rejects contradictory fields instead of silently
// discarding a requested memory identity, revision, or result.
func validateCognitiveFinish(req cognitiveRequest, result cognitiveFinish) error {
	if result.Outcome < cognitiveWriteCognition || result.Outcome > cognitiveNoResult {
		return fmt.Errorf("invalid cognitive outcome %d", result.Outcome)
	}
	if req.MemoryID < 0 || req.Revision < 0 {
		return fmt.Errorf("cognitive memory_id and revision cannot be negative")
	}
	if result.Outcome == cognitiveNoResult {
		if req.MemoryID != 0 || req.Revision != 0 || req.Description != "" || result.Impression != "" || result.Understanding != "" || result.Revisit {
			return fmt.Errorf("outcome %d requires empty impression and understanding, and revisit=false", cognitiveNoResult)
		}
		return nil
	}
	if req.MemoryID == 0 {
		if result.Outcome != cognitiveWriteCognition || strings.TrimSpace(req.Description) == "" || req.Revision != 0 || result.Revisit {
			return fmt.Errorf("a new cognitive memory requires outcome %d, a non-empty description, revision 0, and revisit=false", cognitiveWriteCognition)
		}
	} else if req.Revision == 0 || req.Description != "" {
		return fmt.Errorf("an existing cognitive memory requires its current revision and an empty description")
	}
	if result.Outcome == cognitiveWriteCognition && (strings.TrimSpace(result.Impression) == "" || strings.TrimSpace(result.Understanding) == "") {
		return fmt.Errorf("outcome %d requires a non-empty impression and understanding", cognitiveWriteCognition)
	}
	if result.Outcome == cognitiveConfirmUnchanged && (result.Impression != "" || result.Understanding != "") {
		return fmt.Errorf("outcome %d requires impression and understanding to be empty", cognitiveConfirmUnchanged)
	}
	return nil
}

// commitCognitiveFinish atomically applies one terminal result and closes the
// process. Epoch and current revision checks reject old model responses.
func commitCognitiveFinish(ctx context.Context, process model.CognitiveProcess, req cognitiveRequest, result cognitiveFinish, descriptionEmbedding, impressionEmbedding []byte) error {
	if err := validateCognitiveFinish(req, result); err != nil {
		return err
	}
	if descriptionEmbedding == nil {
		descriptionEmbedding = []byte{}
	}
	if impressionEmbedding == nil {
		impressionEmbedding = []byte{}
	}
	return database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.CognitiveProcess
		if err := tx.Where("id = ? AND person_id = ?", process.ID, process.PersonID).Take(&current).Error; err != nil {
			return err
		}
		if current.Status != model.CognitiveProcessRunning || current.Epoch != process.Epoch {
			return fmt.Errorf("stale cognitive process %d epoch %d", process.ID, process.Epoch)
		}
		if result.Outcome != cognitiveNoResult {
			if req.MemoryID == 0 {
				created := model.CognitiveMemory{PersonID: process.PersonID, Description: req.Description, Embedding: descriptionEmbedding, ImpressionEmbedding: impressionEmbedding}
				if err := tx.Create(&created).Error; err != nil {
					return err
				}
				version := model.CognitiveMemoryVersion{MemoryID: created.ID, Revision: 1, Impression: result.Impression, Understanding: result.Understanding, ProcessID: process.ID}
				if err := tx.Create(&version).Error; err != nil {
					return err
				}
			} else {
				var owned model.CognitiveMemory
				if err := tx.Where("id = ? AND person_id = ?", req.MemoryID, process.PersonID).Take(&owned).Error; err != nil {
					return err
				}
				var latest model.CognitiveMemoryVersion
				if err := tx.Where("memory_id = ?", owned.ID).Order("revision DESC").Take(&latest).Error; err != nil {
					return err
				}
				if latest.Revision != req.Revision {
					return fmt.Errorf("cognitive memory %d changed during process", owned.ID)
				}
				if time.Since(owned.UpdatedAt) < cognitiveRecentReview() && !result.Revisit && process.Kind != model.CognitiveProcessKindUpdate {
					return fmt.Errorf("recent cognitive memory %d needs explicit revisit confirmation", owned.ID)
				}
				if result.Outcome == cognitiveWriteCognition {
					version := model.CognitiveMemoryVersion{MemoryID: owned.ID, Revision: latest.Revision + 1, Impression: result.Impression, Understanding: result.Understanding, ProcessID: process.ID}
					if err := tx.Create(&version).Error; err != nil {
						return err
					}
				}
				updates := map[string]any{"updated_at": time.Now()}
				if result.Outcome == cognitiveWriteCognition {
					updates["impression_embedding"] = impressionEmbedding
				}
				if err := tx.Model(&model.CognitiveMemory{}).Where("id = ? AND person_id = ?", owned.ID, process.PersonID).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
		if err := endCognitiveProcess(tx, process.ID, process.Epoch, result.Outcome); err != nil {
			return err
		}
		return nil
	})
}

// endCognitiveProcess closes a running process and records its terminal result
// in the same statement. The outcome is what lets a later routing pass advance
// a review watermark; a process that ended without one leaves it untouched.
func endCognitiveProcess(tx *gorm.DB, processID, epoch int64, outcome cognitiveOutcome) error {
	updated := tx.Model(&model.CognitiveProcess{}).
		Where("id = ? AND status = ? AND epoch = ?", processID, model.CognitiveProcessRunning, epoch).
		Updates(map[string]interface{}{
			"status":  model.CognitiveProcessEnded,
			"outcome": model.CognitiveProcessOutcome(outcome),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("cognitive process %d is not running at epoch %d", processID, epoch)
	}
	return nil
}

// cognitiveToolOutput dispatches only read-only research calls.
func cognitiveToolOutput(ctx context.Context, personID int64, call llm.ToolCall) (string, error) {
	if call.Function.Name == "recall_daily_memory" || call.Function.Name == "recall_cognitive_memory" {
		return executeCognitiveRecall(ctx, personID, call.Function.Name, call.Function.Arguments)
	}
	if call.Function.Name == "recall_memo" {
		return executeMemoRecall(personID, call.Function.Arguments)
	}
	if isJinshuTool(call.Function.Name) {
		return executeJinshuTool(personID, call.Function.Name, call.Function.Arguments)
	}
	return executeRecallTool(personID, 0, call.Function.Name, call.Function.Arguments)
}

// appendCognitiveToolResult preserves the model's entire conversation in the
// next LLM request. The saved response can be replayed after interruption.
func appendCognitiveToolResult(req cognitiveLLMRequest, response llm.ToolResponse, output string) cognitiveLLMRequest {
	next := req
	call := response.ToolCalls[0]
	next.Messages = append(append([]llm.Message(nil), req.Messages...),
		llm.Message{Role: "assistant", Content: response.Content, ToolCalls: response.ToolCalls},
		llm.Message{Role: "tool", ToolCallID: call.ID, Content: output})
	if cognitiveRequestNumber(next.Messages) >= maxCognitiveRequests() {
		next.Tools = []llm.FunctionDefinition{cognitiveFinishTool()}
	}
	return next
}

// appendCognitiveToolError returns a correctable rejection instead of ending
// the whole process when the model supplied fixable arguments or used a tool
// from an earlier stage.
func appendCognitiveToolError(req cognitiveLLMRequest, response llm.ToolResponse, cause error) cognitiveLLMRequest {
	return appendCognitiveToolResult(req, response, "Cognitive tool call rejected: "+cause.Error()+". Correct the arguments and call the currently offered tool again.")
}

// cognitiveProcessTarget resolves the identity the finish tool must write to.
// A routed update fixes its target on the process row before the conversation
// starts, so its first prompt carries no selection turn; a discovery process
// takes the identity from its own selection turn in the conversation.
func cognitiveProcessTarget(process model.CognitiveProcess, messages []llm.Message) (cognitiveRequest, error) {
	if process.Kind == model.CognitiveProcessKindUpdate {
		if process.MemoryID <= 0 || process.MemoryRevision <= 0 {
			return cognitiveRequest{}, fmt.Errorf("cognitive update process %d has no fixed target", process.ID)
		}
		return cognitiveRequest{MemoryID: process.MemoryID, Revision: process.MemoryRevision}, nil
	}
	return cognitiveTargetInConversation(messages)
}

// validateCognitiveRecentRevisit rejects a recent-memory write before
// embedding or committing it. The transaction repeats this check as a race
// guard, but a model that only forgot revisit=true can correct the call. A
// routed update is exempt: the system already chose that review.
func validateCognitiveRecentRevisit(ctx context.Context, process model.CognitiveProcess, req cognitiveRequest, result cognitiveFinish) error {
	if process.Kind == model.CognitiveProcessKindUpdate || req.MemoryID == 0 || result.Outcome == cognitiveNoResult || result.Revisit {
		return nil
	}
	var owned model.CognitiveMemory
	if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", req.MemoryID, process.PersonID).Take(&owned).Error; err != nil {
		return err
	}
	if time.Since(owned.UpdatedAt) < cognitiveRecentReview() {
		return fmt.Errorf("this understanding was reviewed recently; set revisit=true if you still choose to reconsider it. Then choose outcome %d if the shown understanding still holds, or outcome %d only if you can name a substantive change from it", cognitiveConfirmUnchanged, cognitiveWriteCognition)
	}
	return nil
}

// logCognitiveToolResultSize reports oversized results without removing the
// evidence from the continuing model conversation.
func logCognitiveToolResultSize(process model.CognitiveProcess, req cognitiveLLMRequest, name, output string) {
	if len(output) > cognitiveRecallResultWarnBytes {
		applogger.Warn("cognitive recall result exceeds previous size threshold", "person_id", process.PersonID, "process_id", process.ID, "tool", name, "bytes", len(output), "threshold_bytes", cognitiveRecallResultWarnBytes)
	}
	if total := cognitiveRecallBytes(req.Messages) + len(output); total > cognitiveRecallContextWarnBytes {
		applogger.Warn("cognitive recall context exceeds previous size threshold", "person_id", process.PersonID, "process_id", process.ID, "tool", name, "bytes", total, "threshold_bytes", cognitiveRecallContextWarnBytes)
	}
}

// advanceCognitiveRecall reexecutes only a read-only call when resuming from
// the last completed model response; it never replays an external action.
func advanceCognitiveRecall(ctx context.Context, process model.CognitiveProcess, req cognitiveLLMRequest, response llm.ToolResponse) (cognitiveLLMRequest, error) {
	if len(response.ToolCalls) != 1 {
		return cognitiveLLMRequest{}, fmt.Errorf("expected one cognitive recall call")
	}
	if cognitiveRequestNumber(req.Messages) >= maxCognitiveRequests() {
		return cognitiveLLMRequest{}, fmt.Errorf("recall requested at cognitive iteration limit")
	}
	call := response.ToolCalls[0]
	output, err := cognitiveToolOutput(ctx, process.PersonID, call)
	if ctx.Err() != nil {
		return cognitiveLLMRequest{}, ctx.Err()
	}
	if err != nil {
		applogger.Error("cognitive recall failed", "person_id", process.PersonID, "process_id", process.ID, "tool", call.Function.Name, "error", err)
		output = "Recall unavailable: " + err.Error() + ". This is not evidence of absence."
	}
	logCognitiveToolResultSize(process, req, call.Function.Name, output)
	return appendCognitiveToolResult(req, response, output), nil
}

// executeCognitiveProcess advances one durable three-stage model conversation.
func (r *agentRuntime) executeCognitiveProcess(ctx context.Context, process model.CognitiveProcess) {
	// Only a completed LLM call replaces the durable pair. A derived next
	// request stays in memory until its own response arrives.
	var pending *cognitiveLLMRequest
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		var checkpoint cognitiveCheckpoint
		if pending != nil {
			checkpoint = cognitiveCheckpoint{Request: *pending, Response: emptyCognitiveResponse()}
			pending = nil
		} else {
			var err error
			checkpoint, err = readCognitiveCheckpoint(process.PersonID, process.ID)
			if err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
		}
		if cognitiveRequestNumber(checkpoint.Request.Messages) > maxCognitiveRequests() {
			r.finishCognitiveProcess(ctx, process, cognitiveRequest{}, cognitiveFinish{Outcome: cognitiveNoResult})
			return
		}
		var newEvidence string
		if first {
			var err error
			newEvidence, err = recentCognitiveEvidenceSince(ctx, process.PersonID, process.CreatedAt)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
			first = false
		}
		if string(checkpoint.Response) == "{}" {
			if newEvidence != "" {
				checkpoint.Request.Messages = append(checkpoint.Request.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
			}
			if err := r.chargeCognitiveRequest(ctx, process); errors.Is(err, energy.ErrInsufficientEnergy) {
				r.pauseCognitiveProcess(process.ID)
				return
			} else if ctx.Err() != nil {
				return
			} else if err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
			a, err := agent.GetAgent(process.PersonID)
			if err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
			// A paused process may resume much later; give every model request
			// the current world time instead of relying on its first prompt.
			checkpoint.Request.Messages = append(checkpoint.Request.Messages, llm.Message{Role: "user", Content: "Current time in Qingqiu World: " + time.Now().In(energy.Timezone()).Format(time.RFC3339)})
			client := llm.NewChatModelWithTemperature(a.LLM.BaseURL, a.LLM.APIKey, checkpoint.Request.Model, checkpoint.Request.Temperature)
			response, err := client.ChatWithRequiredTools(ctx, checkpoint.Request.Messages, checkpoint.Request.Tools)
			// A pause may have arrived while the model was running. Discard that
			// response before it can enter the conversation or checkpoint.
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
			data, err := json.Marshal(response)
			if err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
			checkpoint.Response = data
			if err := r.saveCognitiveCheckpoint(process, checkpoint); err != nil {
				if ctx.Err() == nil {
					r.failCognitiveProcess(process, err)
				}
				return
			}
			// This response already considered evidence appended to its request.
			newEvidence = ""
		}
		var response llm.ToolResponse
		if err := json.Unmarshal(checkpoint.Response, &response); err != nil {
			r.failCognitiveProcess(process, err)
			return
		}
		if response.FinishReason != "tool_calls" || len(response.ToolCalls) != 1 {
			r.failCognitiveProcess(process, fmt.Errorf("expected one cognitive tool call"))
			return
		}
		call := response.ToolCalls[0]
		allowed := false
		for _, definition := range checkpoint.Request.Tools {
			if definition.Name == call.Function.Name {
				allowed = true
				break
			}
		}
		if !allowed {
			next := appendCognitiveToolError(checkpoint.Request, response, fmt.Errorf("tool %q is unavailable in this stage", call.Function.Name))
			if newEvidence != "" {
				next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
			}
			pending = &next
			continue
		}
		switch call.Function.Name {
		case "express_cognition":
			withIntent := append(append([]llm.Message(nil), checkpoint.Request.Messages...), llm.Message{Role: "assistant", ToolCalls: response.ToolCalls})
			intent, err := cognitiveIntentInConversation(withIntent)
			if err != nil {
				next := appendCognitiveToolError(checkpoint.Request, response, err)
				if newEvidence != "" {
					next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
				}
				pending = &next
				continue
			}
			if !intent.NeedUnderstanding {
				r.finishCognitiveProcess(ctx, process, cognitiveRequest{}, cognitiveFinish{Outcome: cognitiveNoResult})
				return
			}
			output, err := searchCognitiveCandidates(ctx, process.PersonID, intent)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
			logCognitiveToolResultSize(process, checkpoint.Request, call.Function.Name, output)
			next := appendCognitiveToolResult(checkpoint.Request, response, output)
			next.Tools = []llm.FunctionDefinition{cognitiveSelectionTool()}
			if newEvidence != "" {
				next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
			}
			pending = &next
			continue
		case "choose_cognitive_memory":
			target, proceed, err := selectedCognitiveRequest(checkpoint.Request.Messages, call)
			if err != nil {
				next := appendCognitiveToolError(checkpoint.Request, response, err)
				if newEvidence != "" {
					next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
				}
				pending = &next
				continue
			}
			if !proceed {
				r.finishCognitiveProcess(ctx, process, cognitiveRequest{}, cognitiveFinish{Outcome: cognitiveNoResult})
				return
			}
			// The daily quota is checked again here: admission may have run
			// before an earlier process ended, so the model can select a target
			// whose slot was spent in the meantime.
			if err := validateCognitiveDailySelection(ctx, process.PersonID, target); err != nil {
				next := appendCognitiveToolError(checkpoint.Request, response, err)
				if newEvidence != "" {
					next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
				}
				pending = &next
				continue
			}
			// Persist the selected identity before research so the daily quota
			// can attribute this process even if it ends without a result.
			if target.MemoryID > 0 {
				if err := recordCognitiveTarget(process, target.MemoryID); err != nil {
					if ctx.Err() != nil {
						return
					}
					r.failCognitiveProcess(process, err)
					return
				}
			}
			selectionText := "You selected a new concern: " + target.Description + ". Investigate it with the read-only tools; if the evidence does not support an understanding, finish without recording a result."
			if target.MemoryID > 0 {
				selectionText = fmt.Sprintf("You selected existing memory %d at revision %d. The earlier view shown above may be abbreviated. Read this memory in full with recall_cognitive_memory before comparing it with what has happened since its last review. If both the impression and understanding still hold, finish with outcome %d (confirm unchanged) instead of restating them; use outcome %d only if you can name a substantive difference from revision %d.", target.MemoryID, target.Revision, cognitiveConfirmUnchanged, cognitiveWriteCognition, target.Revision)
			}
			next := appendCognitiveToolResult(checkpoint.Request, response, selectionText)
			if cognitiveRequestNumber(next.Messages) < maxCognitiveRequests() {
				next.Tools = cognitiveReadTools()
			} else {
				next.Tools = []llm.FunctionDefinition{cognitiveFinishTool()}
			}
			if newEvidence != "" {
				next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
			}
			pending = &next
			continue
		case "finish_cognition":
			finish, err := decodeCognitiveToolArgs[cognitiveFinish](call.Function.Arguments, "outcome")
			if err != nil {
				next := appendCognitiveToolError(checkpoint.Request, response, err)
				if newEvidence != "" {
					next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
				}
				pending = &next
				continue
			}
			target, err := cognitiveProcessTarget(process, checkpoint.Request.Messages)
			if err != nil {
				next := appendCognitiveToolError(checkpoint.Request, response, err)
				if newEvidence != "" {
					next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
				}
				pending = &next
				continue
			}
			if newEvidence != "" {
				if cognitiveRequestNumber(checkpoint.Request.Messages) >= maxCognitiveRequests() {
					applogger.Warn("cognitive completion invalidated by new observations at request limit", "person_id", process.PersonID, "process_id", process.ID)
					r.finishCognitiveProcess(ctx, process, cognitiveRequest{}, cognitiveFinish{Outcome: cognitiveNoResult})
					return
				}
				next := appendCognitiveToolResult(checkpoint.Request, response, "Completion was interrupted by new observed experience. Review the new information before finishing again.")
				next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
				pending = &next
				continue
			}
			if finish.Outcome == cognitiveNoResult {
				target = cognitiveRequest{}
			}
			if err := validateCognitiveFinish(target, finish); err != nil {
				next := appendCognitiveToolError(checkpoint.Request, response, err)
				pending = &next
				continue
			}
			if err := validateCognitiveRecentRevisit(ctx, process, target, finish); err != nil {
				next := appendCognitiveToolError(checkpoint.Request, response, err)
				pending = &next
				continue
			}
			r.finishCognitiveProcess(ctx, process, target, finish)
			return
		default:
			if _, err := cognitiveProcessTarget(process, checkpoint.Request.Messages); err != nil {
				r.failCognitiveProcess(process, err)
				return
			}
		}
		next, err := advanceCognitiveRecall(ctx, process, checkpoint.Request, response)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.failCognitiveProcess(process, err)
			return
		}
		if newEvidence != "" {
			next.Messages = append(next.Messages, llm.Message{Role: "user", Content: "Recent records since this cognition began (bounded sample; some may already appear above):\n" + newEvidence})
		}
		pending = &next
	}
}

// chargeCognitiveRequest serializes the energy write with preemption. The
// event loop cannot proceed to Decide until it has invalidated this epoch.
func (r *agentRuntime) chargeCognitiveRequest(ctx context.Context, process model.CognitiveProcess) error {
	r.cognitiveMu.Lock()
	defer r.cognitiveMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.cognitiveCancel == nil || r.cognitiveProcessID != process.ID || r.cognitiveEpoch != process.Epoch {
		return context.Canceled
	}
	state, err := energy.RecoverEnergy(process.PersonID)
	if err != nil {
		return err
	}
	if state.Energy < int(energy.CostPrivateSpace) {
		return energy.ErrInsufficientEnergy
	}
	return energy.DeductEnergy(process.PersonID, energy.CostPrivateSpace)
}

// saveCognitiveCheckpoint rejects responses from an invalidated process epoch.
func (r *agentRuntime) saveCognitiveCheckpoint(process model.CognitiveProcess, checkpoint cognitiveCheckpoint) error {
	r.cognitiveMu.Lock()
	defer r.cognitiveMu.Unlock()
	if r.cognitiveCancel == nil || r.cognitiveProcessID != process.ID || r.cognitiveEpoch != process.Epoch {
		return fmt.Errorf("stale cognitive checkpoint")
	}
	return writeCognitiveCheckpoint(process.PersonID, process.ID, checkpoint)
}

// finishCognitiveProcess commits the selected result before releasing its slot.
// Embedding and database failures block the write and take the storage retry
// path: an empty vector is a defect, not an acceptable fallback.
func (r *agentRuntime) finishCognitiveProcess(ctx context.Context, process model.CognitiveProcess, req cognitiveRequest, result cognitiveFinish) {
	if ctx.Err() != nil {
		return
	}
	if err := validateCognitiveFinish(req, result); err != nil {
		r.failCognitiveProcess(process, err)
		return
	}
	var descriptionEmbedding, impressionEmbedding []byte
	if result.Outcome == cognitiveWriteCognition && req.MemoryID == 0 {
		vector, err := memory.EmbedCognitiveText(ctx, req.Description)
		if err != nil {
			r.storageFailCognitiveProcess(process, fmt.Errorf("description embedding: %w", err))
			return
		}
		descriptionEmbedding = vectorutils.Float32SliceToBlob(vector)
	}
	if result.Outcome == cognitiveWriteCognition && ctx.Err() == nil {
		vector, err := memory.EmbedCognitiveText(ctx, result.Impression)
		if err != nil {
			r.storageFailCognitiveProcess(process, fmt.Errorf("impression embedding: %w", err))
			return
		}
		impressionEmbedding = vectorutils.Float32SliceToBlob(vector)
	}
	if ctx.Err() != nil {
		return
	}
	r.cognitiveMu.Lock()
	if r.cognitiveCancel == nil || r.cognitiveProcessID != process.ID || r.cognitiveEpoch != process.Epoch {
		r.cognitiveMu.Unlock()
		return
	}
	if err := commitCognitiveFinish(ctx, process, req, result, descriptionEmbedding, impressionEmbedding); err != nil {
		r.cognitiveMu.Unlock()
		applogger.Error("cognitive process completion failed", "person_id", process.PersonID, "process_id", process.ID, "error", err)
		r.storageFailCognitiveProcess(process, err)
		return
	}
	r.cognitiveCancel()
	r.cognitiveCancel = nil
	r.cognitiveProcessID = 0
	r.releaseCognitiveSlot(process.ID)
	r.cognitiveMu.Unlock()
}

// cognitiveProcessState is one durable state of a process. The epoch is a
// fencing value: every transition except a successful completion advances it,
// so a late model response cannot write against a newer state.
type cognitiveProcessState struct {
	Status model.CognitiveProcessStatus
	Epoch  int64
}

// transitionCognitiveProcess applies exactly one legal state change. It fails
// when the durable row is not in the expected state, so a caller never keeps
// acting on a process whose stored state changed underneath it.
func transitionCognitiveProcess(tx *gorm.DB, processID int64, from, to cognitiveProcessState) error {
	updated := tx.Model(&model.CognitiveProcess{}).
		Where("id = ? AND status = ? AND epoch = ?", processID, from.Status, from.Epoch).
		Updates(map[string]interface{}{"status": to.Status, "epoch": to.Epoch})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("cognitive process %d is not in state %d/%d", processID, from.Status, from.Epoch)
	}
	return nil
}

// normalizeCognitiveProcess converts a durable Running row that this runtime
// does not own back to Paused. This is the same repair used at startup; the
// epoch advance fences any response that belonged to the lost owner.
func normalizeCognitiveProcess(ctx context.Context, process model.CognitiveProcess) (model.CognitiveProcess, error) {
	if err := transitionCognitiveProcess(database.DB.WithContext(ctx), process.ID,
		cognitiveProcessState{Status: model.CognitiveProcessRunning, Epoch: process.Epoch},
		cognitiveProcessState{Status: model.CognitiveProcessPaused, Epoch: process.Epoch + 1}); err != nil {
		return process, err
	}
	process.Status = model.CognitiveProcessPaused
	process.Epoch++
	return process, nil
}

// cognitiveRetryDelay is the wait before a failed process is admitted again.
const cognitiveRetryDelay = 5 * time.Minute

// cognitiveRetryLimit is the number of retries allowed after the first
// failure. When the retry budget is spent the process ends and its target
// counts against the day's quota.
const cognitiveRetryLimit = 3

// retryOrEndCognitiveProcess records one failed model attempt. It pauses the
// process with a retry time while the budget remains, or ends it otherwise. The
// epoch advances in both cases, so the failed owner cannot write again. Outcome
// is deliberately untouched: an exhausted process produced no result, and its
// review watermark must not advance.
func retryOrEndCognitiveProcess(process model.CognitiveProcess) (model.CognitiveProcessStatus, error) {
	status := model.CognitiveProcessPaused
	retryAt := time.Now().Add(cognitiveRetryDelay).Unix()
	if process.Failures >= cognitiveRetryLimit {
		status = model.CognitiveProcessEnded
		retryAt = 0
	}
	updated := database.DB.Model(&model.CognitiveProcess{}).
		Where("id = ? AND status = ? AND epoch = ?", process.ID, model.CognitiveProcessRunning, process.Epoch).
		Updates(map[string]interface{}{
			"status":   status,
			"epoch":    process.Epoch + 1,
			"failures": gorm.Expr("failures + 1"),
			"retry_at": retryAt,
		})
	if updated.Error != nil {
		return status, updated.Error
	}
	if updated.RowsAffected != 1 {
		return status, fmt.Errorf("cognitive process %d is not running at epoch %d", process.ID, process.Epoch)
	}
	return status, nil
}

// failCognitiveProcess records a failed attempt and releases local ownership.
// The process stays paused so admission can retry the same conversation after
// the delay; only after the retry budget is spent does it end.
func (r *agentRuntime) failCognitiveProcess(process model.CognitiveProcess, cause error) {
	applogger.Error("cognitive process failed", "person_id", process.PersonID, "process_id", process.ID, "failures", process.Failures, "error", cause)
	r.cognitiveMu.Lock()
	defer r.cognitiveMu.Unlock()
	if r.cognitiveCancel == nil || r.cognitiveProcessID != process.ID || r.cognitiveEpoch != process.Epoch {
		return
	}
	r.cognitiveCancel()
	r.cognitiveCancel = nil
	r.cognitiveProcessID = 0
	status, err := retryOrEndCognitiveProcess(process)
	if err != nil {
		// A durable Running row without an owner is normalized at admission,
		// so a transient write failure leads to a retry, not a stuck process.
		applogger.Error("failed to record cognitive failure", "process_id", process.ID, "epoch", process.Epoch, "error", err)
	} else if status == model.CognitiveProcessEnded {
		applogger.Error("cognitive process ended after retry budget", "person_id", process.PersonID, "process_id", process.ID, "failures", process.Failures+1)
	}
	r.releaseCognitiveSlot(process.ID)
}

// cognitiveStorageRetryLimit bounds embedding and database retries. These
// failures never consume the model retry budget: the checkpoint already holds
// the terminal tool call, so a retry replays it without another model request.
const cognitiveStorageRetryLimit = 3

// retryOrEndCognitiveStorage records one failed embedding or database write.
// A process that exhausts this budget ends without an outcome, so the same
// evidence can be routed again after the next review watermark.
func retryOrEndCognitiveStorage(process model.CognitiveProcess) (model.CognitiveProcessStatus, error) {
	status := model.CognitiveProcessPaused
	retryAt := time.Now().Add(cognitiveRetryDelay).Unix()
	if process.StorageFailures >= cognitiveStorageRetryLimit {
		status = model.CognitiveProcessEnded
		retryAt = 0
	}
	updated := database.DB.Model(&model.CognitiveProcess{}).
		Where("id = ? AND status = ? AND epoch = ?", process.ID, model.CognitiveProcessRunning, process.Epoch).
		Updates(map[string]interface{}{
			"status":           status,
			"epoch":            process.Epoch + 1,
			"storage_failures": gorm.Expr("storage_failures + 1"),
			"retry_at":         retryAt,
		})
	if updated.Error != nil {
		return status, updated.Error
	}
	if updated.RowsAffected != 1 {
		return status, fmt.Errorf("cognitive process %d is not running at epoch %d", process.ID, process.Epoch)
	}
	return status, nil
}

// storageFailCognitiveProcess releases local ownership and schedules a storage
// retry. Unlike a model failure it leaves Failures untouched, so a provider
// outage cannot spend the model's three-attempt budget.
func (r *agentRuntime) storageFailCognitiveProcess(process model.CognitiveProcess, cause error) {
	applogger.Error("cognitive process storage failed", "person_id", process.PersonID, "process_id", process.ID,
		"storage_failures", process.StorageFailures, "failures", process.Failures, "error", cause)
	r.cognitiveMu.Lock()
	defer r.cognitiveMu.Unlock()
	if r.cognitiveCancel == nil || r.cognitiveProcessID != process.ID || r.cognitiveEpoch != process.Epoch {
		return
	}
	r.cognitiveCancel()
	r.cognitiveCancel = nil
	r.cognitiveProcessID = 0
	status, err := retryOrEndCognitiveStorage(process)
	if err != nil {
		// A durable Running row without an owner is normalized at admission,
		// so a transient write failure leads to a retry, not a stuck process.
		applogger.Error("failed to record cognitive storage failure", "process_id", process.ID, "epoch", process.Epoch, "error", err)
	} else if status == model.CognitiveProcessEnded {
		applogger.Error("cognitive process ended after storage retry budget", "person_id", process.PersonID,
			"process_id", process.ID, "storage_failures", process.StorageFailures+1)
	}
	r.releaseCognitiveSlot(process.ID)
}

// pauseCognitiveProcess invalidates the old epoch before releasing the slot.
// The remote model may still return, but its response cannot advance state.
func (r *agentRuntime) pauseCognitiveProcess(processID int64) {
	r.cognitiveMu.Lock()
	defer r.cognitiveMu.Unlock()
	if r.cognitiveCancel == nil || r.cognitiveProcessID != processID {
		return
	}
	epoch := r.cognitiveEpoch
	// Invalidate the local owner first: the model request is canceled before
	// the durable state changes, so a response racing with the transition is
	// rejected by the epoch checks instead of being written.
	r.cognitiveCancel()
	r.cognitiveCancel = nil
	r.cognitiveProcessID = 0
	if err := transitionCognitiveProcess(database.DB, processID,
		cognitiveProcessState{Status: model.CognitiveProcessRunning, Epoch: epoch},
		cognitiveProcessState{Status: model.CognitiveProcessPaused, Epoch: epoch + 1}); err != nil {
		// The durable row stays Running without a local owner. Admission
		// normalizes that state before acting, so a failed write delays the
		// process instead of wedging it until the next restart.
		applogger.Error("failed to pause cognitive process", "process_id", processID, "epoch", epoch, "error", err)
	}
	r.releaseCognitiveSlot(processID)
}

// pauseCurrentCognition preempts the process active in this agent runtime.
func (r *agentRuntime) pauseCurrentCognition() {
	r.cognitiveMu.Lock()
	id := r.cognitiveProcessID
	r.cognitiveMu.Unlock()
	if id > 0 {
		r.pauseCognitiveProcess(id)
	}
}

// cognitiveAdmissionPollInterval bounds how long a paused process can wait for
// an idle runtime when no Event or heartbeat arrives. The tick only rechecks
// busy; it never queues work, so a busy runtime simply skips the attempt.
const cognitiveAdmissionPollInterval = time.Minute

// runCognitionWorker is the single owner of cognitive admission. Event
// handling, heartbeats, loop exits and the poll tick only wake it when the
// runtime may be quiet; the worker decides whether to resume a paused process
// or to create a new one. How often that happens is the cognitive side's
// concern, not the scheduler's.
func (r *agentRuntime) runCognitionWorker(ctx context.Context) {
	ticker := time.NewTicker(cognitiveAdmissionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.cognitionWake:
		case <-ticker.C:
		}
		if r.busy.Load() != 0 {
			continue
		}
		r.serveCognition(ctx)
	}
}

// signalCognition wakes the worker without blocking the caller, so the event
// loop never waits for the background loop.
func (r *agentRuntime) signalCognition() {
	select {
	case r.cognitionWake <- struct{}{}:
	default:
	}
}

// unfinishedCognitiveProcesses lists every row that may still be resumed,
// oldest first. Each owner must have at most one.
func unfinishedCognitiveProcesses(ctx context.Context, personID int64) ([]model.CognitiveProcess, error) {
	var existing []model.CognitiveProcess
	err := database.DB.WithContext(ctx).
		Where("person_id = ? AND status <> ?", personID, model.CognitiveProcessEnded).
		Order("id").
		Find(&existing).Error
	return existing, err
}

// createCognitiveProcess inserts a new paused process only when this person has
// no unfinished one. The check and the insert share one transaction, so two
// admission attempts (for example a stop/start overlap in which the previous
// worker has not exited yet) cannot both create a row. The transaction contains
// only that indexed lookup and the insert: identity reads, prompt building and
// the checkpoint write stay outside, and nothing inside may call database.DB
// again because the pool has a single connection. Kind and the fixed target are
// part of the insert so a resumed row knows whether it may choose a target.
func createCognitiveProcess(ctx context.Context, personID int64, startedAt time.Time, kind model.CognitiveProcessKind, memoryID int64, memoryRevision int) (model.CognitiveProcess, bool, error) {
	var process model.CognitiveProcess
	created := false
	err := database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var unfinishedID int64
		if err := tx.Model(&model.CognitiveProcess{}).
			Select("id").
			Where("person_id = ? AND status <> ?", personID, model.CognitiveProcessEnded).
			Limit(1).
			Scan(&unfinishedID).Error; err != nil {
			return err
		}
		if unfinishedID != 0 {
			return nil
		}
		process = model.CognitiveProcess{PersonID: personID, Status: model.CognitiveProcessPaused, Kind: kind,
			MemoryID: memoryID, MemoryRevision: memoryRevision, Epoch: 0, CreatedAt: startedAt}
		if err := tx.Create(&process).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return model.CognitiveProcess{}, false, err
	}
	return process, created, nil
}

// serveCognition resumes a paused process when one exists; otherwise it
// creates a new process. The process itself runs in its own goroutine and is
// paused by pauseCurrentCognition when a new input arrives.
func (r *agentRuntime) serveCognition(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// A fresh installation can run without embeddings so the Settings API is
	// available. Cognitive processes need vectors to select and write memories;
	// leave existing processes paused until a configured restart.
	if !memory.EmbeddingAvailable() {
		return
	}
	// busy is the only admission signal: it is non-zero while an input is
	// being processed, while any finite Action is still running, and while a
	// Focused Work or the private-space loop is active. A queued Event that
	// has not been taken yet is covered by pauseCurrentCognition in the event
	// loop, so starting here would only cost one wasted admission.
	if r.busy.Load() != 0 {
		return
	}
	// The cognitive process itself is not counted in busy, otherwise admission
	// could never pass; a wakeup arriving while it runs must not start another.
	r.cognitiveMu.Lock()
	running := r.cognitiveProcessID != 0
	r.cognitiveMu.Unlock()
	if running {
		return
	}
	person, err := dops.GetPerson(r.agentPersonID)
	if err != nil {
		applogger.Error("cognitive admission person lookup failed", "person_id", r.agentPersonID, "error", err)
		return
	}
	if person.Status != model.PersonStatusActive {
		return
	}
	sleep, err := dops.GetAgentSleepSince(r.agentPersonID)
	if err != nil {
		applogger.Error("cognitive admission sleep lookup failed", "person_id", r.agentPersonID, "error", err)
		return
	}
	if sleep != "" {
		return
	}
	state, err := energy.RecoverEnergy(r.agentPersonID)
	if err != nil {
		applogger.Error("cognitive admission energy lookup failed", "person_id", r.agentPersonID, "error", err)
		return
	}
	if state.Energy < int(energy.CostPrivateSpace) {
		return
	}
	existing, err := unfinishedCognitiveProcesses(ctx, r.agentPersonID)
	if err != nil {
		applogger.Error("cognitive process lookup failed", "person_id", r.agentPersonID, "error", err)
		return
	}
	if len(existing) > 1 {
		// Admission is single-owner and creation is transactional, so the
		// runtime itself cannot produce this. Report it instead of guessing
		// which row to keep.
		applogger.Error("multiple unfinished cognitive processes; admission skipped", "person_id", r.agentPersonID, "count", len(existing))
		return
	}
	var process model.CognitiveProcess
	if len(existing) == 1 {
		process = existing[0]
		if process.Status == model.CognitiveProcessRunning {
			// A durable Running row without a local owner means the owner was
			// lost, for example because a pause write failed. Normalize it
			// back to Paused so the process continues instead of staying
			// stuck until the next restart.
			process, err = normalizeCognitiveProcess(ctx, process)
			if err != nil {
				applogger.Error("cognitive process running without local slot could not be recovered", "person_id", r.agentPersonID, "process_id", process.ID, "error", err)
				return
			}
			applogger.Error("cognitive process running without local slot; recovered as paused", "person_id", r.agentPersonID, "process_id", process.ID, "epoch", process.Epoch)
		}
		if process.RetryAt > time.Now().Unix() {
			applogger.Debug("cognitive retry deferred", "person_id", r.agentPersonID, "process_id", process.ID, "failures", process.Failures, "retry_at", process.RetryAt)
			return
		}
		if _, err := readCognitiveCheckpoint(process.PersonID, process.ID); err != nil {
			applogger.Error("cognitive checkpoint unavailable; ending process", "person_id", process.PersonID, "process_id", process.ID, "error", err)
			if updateErr := transitionCognitiveProcess(database.DB, process.ID,
				cognitiveProcessState{Status: process.Status, Epoch: process.Epoch},
				cognitiveProcessState{Status: model.CognitiveProcessEnded, Epoch: process.Epoch + 1}); updateErr != nil {
				applogger.Error("failed to end process with missing checkpoint", "process_id", process.ID, "epoch", process.Epoch, "error", updateErr)
			}
			return
		}
	} else {
		// Capture the boundary before building the first prompt, so a
		// concurrent self-observation cannot fall between prompt and process.
		startedAt := time.Now()
		admission, ok, err := prepareCognitiveAdmission(ctx, r.agentPersonID)
		if err != nil {
			applogger.Error("cognitive admission routing failed", "person_id", r.agentPersonID, "error", err)
			return
		}
		if !ok {
			applogger.Debug("cognitive admission deferred: today's routing quota is used or no evidence is due", "person_id", r.agentPersonID)
			return
		}
		var created bool
		process, created, err = createCognitiveProcess(ctx, r.agentPersonID, startedAt, admission.Kind, admission.MemoryID, admission.MemoryRevision)
		if err != nil {
			applogger.Error("cognitive process creation failed", "person_id", r.agentPersonID, "error", err)
			return
		}
		if !created {
			// A concurrent admission created the row while this attempt was
			// building its prompt; the worker will resume it on the next wake.
			applogger.Error("cognitive process creation lost the race to another admission", "person_id", r.agentPersonID)
			return
		}
		if err := writeCognitiveCheckpoint(process.PersonID, process.ID, cognitiveCheckpoint{Request: admission.Request, Response: emptyCognitiveResponse()}); err != nil {
			applogger.Error("cognitive initial checkpoint failed", "process_id", process.ID, "error", err)
			if updateErr := transitionCognitiveProcess(database.DB, process.ID,
				cognitiveProcessState{Status: model.CognitiveProcessPaused, Epoch: process.Epoch},
				cognitiveProcessState{Status: model.CognitiveProcessEnded, Epoch: process.Epoch + 1}); updateErr != nil {
				applogger.Error("failed to end process without initial checkpoint", "process_id", process.ID, "epoch", process.Epoch, "error", updateErr)
			}
			return
		}
		// The process exists; the next wake resumes it, and a new process is
		// created only once no paused process remains.
		applogger.Info("cognitive process started", "person_id", r.agentPersonID, "process_id", process.ID,
			"kind", admission.Kind, "memory_id", admission.MemoryID)
	}
	if !r.acquireCognitiveSlot(process.ID) {
		applogger.Debug("cognitive admission deferred: slot claimed by another loop", "person_id", r.agentPersonID, "process_id", process.ID)
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	// The first busy check is made before the slot is acquired. An Event may
	// arrive in between, so commit the running flag under cognitiveMu, which
	// pauseCurrentCognition also takes. The Event then either blocks this
	// admission or pauses the process right after it starts.
	r.cognitiveMu.Lock()
	// A stop may cancel the runtime after the checks above. Admit nothing once
	// that happened: the process would be marked Running although the model
	// request can no longer run, leaving a row without an owner.
	if ctx.Err() != nil {
		r.cognitiveMu.Unlock()
		cancel()
		r.releaseCognitiveSlot(process.ID)
		applogger.Info("cognitive admission skipped: runtime is stopping", "person_id", r.agentPersonID, "process_id", process.ID)
		return
	}
	if r.busy.Load() != 0 {
		r.cognitiveMu.Unlock()
		cancel()
		r.releaseCognitiveSlot(process.ID)
		applogger.Debug("cognitive admission deferred: input arrived before start", "person_id", r.agentPersonID, "process_id", process.ID)
		return
	}
	if err := transitionCognitiveProcess(database.DB, process.ID,
		cognitiveProcessState{Status: model.CognitiveProcessPaused, Epoch: process.Epoch},
		cognitiveProcessState{Status: model.CognitiveProcessRunning, Epoch: process.Epoch + 1}); err != nil {
		r.cognitiveMu.Unlock()
		cancel()
		r.releaseCognitiveSlot(process.ID)
		applogger.Error("cognitive process admission failed", "process_id", process.ID, "epoch", process.Epoch, "error", err)
		return
	}
	process.Status = model.CognitiveProcessRunning
	process.Epoch++
	r.cognitiveCancel = cancel
	r.cognitiveProcessID = process.ID
	r.cognitiveEpoch = process.Epoch
	r.cognitiveMu.Unlock()
	go r.executeCognitiveProcess(workerCtx, process)
}

// recoverCognitiveProcesses invalidates any process left running at shutdown.
// The checkpoint is retained and checked when the runtime can resume it.
func recoverCognitiveProcesses(personID int64) error {
	return database.DB.Model(&model.CognitiveProcess{}).Where("person_id = ? AND status = ?", personID, model.CognitiveProcessRunning).Updates(map[string]interface{}{"status": model.CognitiveProcessPaused, "epoch": gorm.Expr("epoch + 1")}).Error
}
