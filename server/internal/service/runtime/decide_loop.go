package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/aos"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
)

const recallPromptInstruction = `Use tool calls only; submit the final DecisionResult with decide (an empty action list is valid).
Recall when a relevant past fact is missing. Choose the source for that fact: messages show what was said; actions record what you intended, not proof of the result; Works identify past or ongoing tasks; Focus handoffs record the results, findings, artifacts, and unresolved points reported when Focus ended. Completed Works remain searchable even when no active work is listed. Jinshu lists show delivery metadata; a detail adds its description, not how the delivery was produced or what its attachments contain.
Workspaces are owned file environments. Use recall_workspace to browse their names, purposes, and declared Work/PS uses before choosing an existing workspace_id. Recall queries search records, not AOS file paths or contents; locating a nested file or checking its contents belongs inside Focus. Inspect a known ID directly; otherwise search the relevant source by content words or time. recall_message searches across sessions when session_id is omitted; recall_history discovers across source types with query or from_time. Follow returned IDs when more detail is needed. A limited page or lexical miss does not prove absence. Decide once the evidence is sufficient, or state the remaining uncertainty.`

const maxDecideRequests = 5
const maxRecallResultBytes = 8000
const maxRecallContextBytes = 24000

// formatGeneralSubject presents the agent's current state independently of
// the event-specific Matter supplied to this decision.
func formatGeneralSubject(s SituationSubject) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Your energy: %d\n", s.Energy)
	if s.ExecutionSlotSummary != "" {
		b.WriteString("Sustained execution slot: " + s.ExecutionSlotSummary + "\n")
	}
	if s.ActiveWorksSummary == "" {
		b.WriteString("Active works: none\n")
	} else {
		b.WriteString("Active works:\n" + s.ActiveWorksSummary + "\n")
	}
	if s.ActiveActionsSummary == "" {
		b.WriteString("Ongoing actions: none\n")
	} else {
		b.WriteString("Ongoing actions:\n" + s.ActiveActionsSummary + "\n")
	}
	if s.RecentExperienceSummary != "" {
		b.WriteString("Recent experience (earlier observations, choices, and recorded effects; an intention alone is not a result):\n")
		b.WriteString(s.RecentExperienceSummary + "\n")
	}
	return b.String()
}

// runDecideLoop gives one decision opportunity a bounded, temporary recall
// aos. Only the terminal decide call is eligible for persistence.
func runDecideLoop(ctx context.Context, client *llm.ChatModel, personID, eventID int64, prompt string) (DecisionResult, bool) {
	messages := []llm.Message{{Role: "user", Content: prompt}}
	definitions := append(recallToolDefinitions(), jinshuToolDefinitions()...)
	decideDefinition := decisionToolDefinition()
	seen := make(map[string]struct{})
	recalledBytes := 0
	for request := 1; request <= maxDecideRequests; request++ {
		available := []llm.FunctionDefinition{decideDefinition}
		if request < maxDecideRequests {
			available = append(available, definitions...)
		}
		response, err := client.ChatWithRequiredTools(ctx, messages, available)
		if err != nil {
			if ctx.Err() != nil {
				applogger.Info("DecideLoop model request canceled by shutdown", "person_id", personID, "event_id", eventID, "request", request)
				return DecisionResult{}, false
			}
			applogger.Error("DecideLoop model request failed", "person_id", personID, "event_id", eventID, "request", request, "error", err)
			return DecisionResult{}, false
		}
		if len(response.ToolCalls) != 1 || response.FinishReason != "tool_calls" {
			applogger.Error("DecideLoop expected exactly one tool call", "person_id", personID, "event_id", eventID, "request", request, "finish_reason", response.FinishReason, "tool_count", len(response.ToolCalls))
			return DecisionResult{}, false
		}
		call := response.ToolCalls[0]
		if call.Function.Name == "decide" {
			var result DecisionResult
			if err := json.Unmarshal([]byte(call.Function.Arguments), &result); err != nil {
				applogger.Error("DecideLoop invalid decide arguments", "person_id", personID, "event_id", eventID, "error", err)
				return DecisionResult{}, false
			}
			applogger.Info("DecideLoop completed", "person_id", personID, "event_id", eventID, "requests", request, "action_count", len(result.Actions))
			return result, true
		}
		if request == maxDecideRequests {
			applogger.Error("DecideLoop recalled after terminal request", "person_id", personID, "event_id", eventID, "tool", call.Function.Name)
			return DecisionResult{}, false
		}
		key := call.Function.Name + ":" + call.Function.Arguments
		var output string
		if _, duplicate := seen[key]; duplicate {
			output = "The same recall request was already made. Use the previous result or decide with the uncertainty remaining."
		} else if recalledBytes >= maxRecallContextBytes {
			output = "The recall budget for this decision is exhausted. Decide from the available material and state any uncertainty."
		} else {
			seen[key] = struct{}{}
			if isJinshuTool(call.Function.Name) {
				output, err = executeJinshuTool(personID, call.Function.Name, call.Function.Arguments)
			} else {
				output, err = executeRecallTool(personID, eventID, call.Function.Name, call.Function.Arguments)
			}
			if err != nil {
				applogger.Error("DecideLoop recall failed", "person_id", personID, "event_id", eventID, "tool", call.Function.Name, "error", err)
				output = "Recall could not complete: " + err.Error() + ". This is an unavailable source, not evidence of absence."
			}
			if len(output) > maxRecallResultBytes || recalledBytes+len(output) > maxRecallContextBytes {
				applogger.Warn("DecideLoop recall output exceeded budget", "person_id", personID, "event_id", eventID, "tool", call.Function.Name, "bytes", len(output), "already_used", recalledBytes)
				output = "This recall page exceeds the remaining context budget. Narrow the query, time range, or page size; the omitted material is not evidence of absence."
			}
		}
		recalledBytes += len(output)
		messages = append(messages, llm.Message{Role: "assistant", Content: response.Content, ToolCalls: response.ToolCalls}, llm.Message{Role: "tool", ToolCallID: call.ID, Content: output})
	}
	return DecisionResult{}, false
}

// decisionToolDefinition exposes DecisionResult as the loop's terminal call.
func decisionToolDefinition() llm.FunctionDefinition {
	var schema map[string]interface{}
	if err := json.Unmarshal(llm.GenerateSchema[DecisionResult](), &schema); err != nil {
		applogger.Error("DecideLoop schema generation failed", "error", err)
		schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	return llm.FunctionDefinition{Name: "decide", Description: "Submit this decision once, including an empty actions list if no action is needed.", Parameters: schema}
}

// recallToolDefinitions describes the bounded, read-only memory queries
// available while forming one decision.
func recallToolDefinitions() []llm.FunctionDefinition {
	common := map[string]interface{}{
		"query":     map[string]interface{}{"type": "string", "description": "Optional terms from record text or Workspace name/purpose; not a search of AOS file paths or contents. Omit to browse recent records."},
		"from_time": map[string]interface{}{"type": "string", "description": "Optional inclusive timestamp. RFC3339 with timezone, or local YYYY-MM-DD HH:MM:SS / YYYY-MM-DDTHH:MM:SS in the project timezone."},
		"to_time":   map[string]interface{}{"type": "string", "description": "Optional exclusive timestamp. RFC3339 with timezone, or local YYYY-MM-DD HH:MM:SS / YYYY-MM-DDTHH:MM:SS in the project timezone."},
		"page_size": map[string]interface{}{"type": "integer", "description": "Optional page size, default 5, maximum 20."},
		"cursor":    map[string]interface{}{"type": "string", "description": "Optional next_cursor returned by the previous page."},
	}
	makeTool := func(name, description string, extra map[string]interface{}) llm.FunctionDefinition {
		props := make(map[string]interface{}, len(common)+len(extra))
		for key, value := range common {
			props[key] = value
		}
		for key, value := range extra {
			props[key] = value
		}
		return llm.FunctionDefinition{Name: name, Description: description, Parameters: map[string]interface{}{"type": "object", "properties": props}}
	}
	return []llm.FunctionDefinition{
		makeTool("recall_history", "Discover accessible, observed events (including messages and Jinshu metadata), your actions, works, and Focus handoffs across sources. Provide query or from_time.", nil),
		makeTool("recall_event", "Read an observed event by event_id, find observed effects of your action_id, or search observed events. No arguments reads the current event.", map[string]interface{}{"event_id": map[string]interface{}{"type": "integer"}, "action_id": map[string]interface{}{"type": "integer"}}),
		makeTool("recall_message", "Search or browse observed messages across accessible sessions. Omit session_id to discover conversations; provide it to narrow the search, or use message_id to read one message.", map[string]interface{}{"session_id": map[string]interface{}{"type": "integer"}, "message_id": map[string]interface{}{"type": "integer"}}),
		makeTool("recall_action", "Read your recorded action by action_id, one decision's actions, or actions related to a work_id. For observed effects, use recall_event.", map[string]interface{}{"action_id": map[string]interface{}{"type": "integer"}, "decision_id": map[string]interface{}{"type": "integer"}, "work_id": map[string]interface{}{"type": "integer"}}),
		makeTool("recall_work", "Find your past or ongoing Works by query or time without a work_id, or inspect one by ID. Returns its goal and current status; use recall_focus_handoff for its recorded outcome.", map[string]interface{}{"work_id": map[string]interface{}{"type": "integer"}}),
		makeTool("recall_workspace", "Browse your registered Workspace names and purposes, or inspect one by workspace_id to see its origin and declared Work or private-space uses. Does not read files.", map[string]interface{}{"workspace_id": map[string]interface{}{"type": "integer"}}),
		makeTool("recall_focus_handoff", "Search past Focus handoffs by query or time without an ID, or read one by handoff_id or work_id. Returns the task's reported outcome, findings, artifacts and unresolved points, not a step-by-step tool log.", map[string]interface{}{"handoff_id": map[string]interface{}{"type": "integer"}, "work_id": map[string]interface{}{"type": "integer"}}),
		{Name: "recall_entity_profile", Description: "Read your current impression of one known person or session by identity.", Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"entity_type": map[string]interface{}{"type": "integer", "description": "1 for person, 2 for session"}, "entity_id": map[string]interface{}{"type": "integer"}}, "required": []string{"entity_type", "entity_id"}}},
	}
}

// recallArguments decodes the union of all read-only recall tool parameters.
// validateRecallArguments rejects fields irrelevant to the selected tool.
type recallArguments struct {
	// EventID selects one occurrence for recall_event.
	EventID int64 `json:"event_id"`
	// SessionID and MessageID narrow recall_message to one conversation or row.
	SessionID int64 `json:"session_id"`
	MessageID int64 `json:"message_id"`
	// ActionID and DecisionID navigate recorded behavior and its effects.
	ActionID   int64 `json:"action_id"`
	DecisionID int64 `json:"decision_id"`
	// WorkID and HandoffID select continuing work or its recorded handoff.
	WorkID      int64 `json:"work_id"`
	WorkspaceID int64 `json:"workspace_id"`
	HandoffID   int64 `json:"handoff_id"`
	// EntityType and EntityID select a current, owner-specific impression.
	EntityType model.EntityType `json:"entity_type"`
	EntityID   int64            `json:"entity_id"`
	// Query supplies optional lexical terms for source-content discovery.
	Query string `json:"query"`
	// FromTime and ToTime bound source occurrence time as [from, to).
	FromTime string `json:"from_time"`
	ToTime   string `json:"to_time"`
	// PageSize and Cursor control bounded continuation of batch results.
	PageSize int    `json:"page_size"`
	Cursor   string `json:"cursor"`
}

// executeRecallTool validates a tool call, maps it to a scoped memory request,
// and serializes the result. A bare recall_event call reads the trigger Event.
func executeRecallTool(personID, eventID int64, name, arguments string) (string, error) {
	var args recallArguments
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid recall arguments: %w", err)
	}
	if err := validateRecallArguments(name, args); err != nil {
		return "", err
	}
	if name == "recall_entity_profile" {
		return memory.RecallEntityProfile(personID, args.EntityType, args.EntityID)
	}
	if name == "recall_workspace" {
		return executeWorkspaceRecall(personID, args)
	}
	req := memory.RecallRequest{PersonID: personID, Query: args.Query, PageSize: args.PageSize, Cursor: args.Cursor}
	switch name {
	case "recall_history":
		req.Scope = memory.RecallAll
	case "recall_event":
		req.Scope = memory.RecallEvents
		req.SourceID = args.EventID
		req.ActionID = args.ActionID
		if req.SourceID == 0 && req.ActionID == 0 && args.Query == "" && args.FromTime == "" && args.ToTime == "" && args.Cursor == "" && args.PageSize == 0 && eventID > 0 {
			req.SourceID = eventID
		}
	case "recall_message":
		req.Scope = memory.RecallMessages
		req.SessionID = args.SessionID
		req.MessageID = args.MessageID
	case "recall_action":
		req.Scope = memory.RecallActions
		req.SourceID = args.ActionID
		req.DecisionID = args.DecisionID
		req.WorkID = args.WorkID
	case "recall_work":
		req.Scope = memory.RecallWorks
		req.SourceID = args.WorkID
	case "recall_focus_handoff":
		req.Scope = memory.RecallHandoffs
		req.SourceID = args.HandoffID
		req.WorkID = args.WorkID
	default:
		return "", fmt.Errorf("unknown recall tool %q", name)
	}
	var err error
	if args.FromTime != "" {
		req.FromTime, err = parseRecallTime(args.FromTime, energy.Timezone())
		if err != nil {
			return "", fmt.Errorf("invalid from_time: %w", err)
		}
	}
	if args.ToTime != "" {
		req.ToTime, err = parseRecallTime(args.ToTime, energy.Timezone())
		if err != nil {
			return "", fmt.Errorf("invalid to_time: %w", err)
		}
	}
	page, err := memory.Recall(req)
	if err != nil {
		return "", err
	}
	if name == "recall_work" {
		return encodeWorkRecallWithWorkspace(personID, page)
	}
	encoded, err := json.Marshal(page)
	return string(encoded), err
}

// parseRecallTime interprets timezone-free wall times in the world's fixed
// timezone while preserving the instant specified by an explicit offset.
func parseRecallTime(value string, location *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("timestamp %q must include a date and time, with an optional RFC3339 timezone", value)
}

// validateRecallArguments rejects keys that a tool cannot apply. Silently
// ignoring an anchor would make a broader result look like a scoped read.
func validateRecallArguments(name string, a recallArguments) error {
	other := func(ids ...int64) bool {
		for _, id := range ids {
			if id != 0 {
				return true
			}
		}
		return false
	}
	switch name {
	case "recall_history":
		if other(a.EventID, a.SessionID, a.MessageID, a.ActionID, a.DecisionID, a.WorkID, a.HandoffID, int64(a.EntityType), a.EntityID) {
			return fmt.Errorf("recall_history does not accept source IDs")
		}
		if strings.TrimSpace(a.Query) == "" && a.FromTime == "" {
			return fmt.Errorf("recall_history needs query words or from_time to bound cross-source discovery")
		}
	case "recall_event":
		if other(a.SessionID, a.MessageID, a.DecisionID, a.WorkID, a.HandoffID, int64(a.EntityType), a.EntityID) {
			return fmt.Errorf("recall_event received unrelated IDs")
		}
	case "recall_message":
		if other(a.EventID, a.ActionID, a.DecisionID, a.WorkID, a.HandoffID, int64(a.EntityType), a.EntityID) {
			return fmt.Errorf("recall_message received unrelated IDs")
		}
	case "recall_action":
		if other(a.EventID, a.SessionID, a.MessageID, a.HandoffID, int64(a.EntityType), a.EntityID) {
			return fmt.Errorf("recall_action received unrelated IDs")
		}
	case "recall_work":
		if other(a.EventID, a.SessionID, a.MessageID, a.ActionID, a.DecisionID, a.HandoffID, int64(a.EntityType), a.EntityID) {
			return fmt.Errorf("recall_work received unrelated IDs")
		}
	case "recall_workspace":
		if other(a.EventID, a.SessionID, a.MessageID, a.ActionID, a.DecisionID, a.WorkID, a.HandoffID, int64(a.EntityType), a.EntityID) {
			return fmt.Errorf("recall_workspace received unrelated IDs")
		}
	case "recall_focus_handoff":
		if other(a.EventID, a.SessionID, a.MessageID, a.ActionID, a.DecisionID, int64(a.EntityType), a.EntityID) {
			return fmt.Errorf("recall_focus_handoff received unrelated IDs")
		}
	case "recall_entity_profile":
		if other(a.EventID, a.SessionID, a.MessageID, a.ActionID, a.DecisionID, a.WorkID, a.HandoffID) || a.Query != "" || a.FromTime != "" || a.ToTime != "" || a.Cursor != "" || a.PageSize != 0 {
			return fmt.Errorf("recall_entity_profile only accepts entity_type and entity_id")
		}
	default:
		return fmt.Errorf("unknown recall tool %q", name)
	}
	return nil
}

// executeWorkspaceRecall exposes only owned Workspace metadata and declared
// uses. The cursor is a bounded page number, not a claim of exhaustive access
// to files or actual filesystem operations.
func executeWorkspaceRecall(personID int64, args recallArguments) (string, error) {
	if args.WorkspaceID < 0 || args.PageSize < 0 || args.PageSize > 20 || len([]rune(args.Query)) > 200 {
		return "", fmt.Errorf("invalid Workspace recall range")
	}
	page := 1
	if args.Cursor != "" {
		parsed, err := strconv.Atoi(args.Cursor)
		if err != nil || parsed < 1 || parsed > 10000 {
			return "", fmt.Errorf("invalid Workspace cursor")
		}
		page = parsed
	}
	limit := args.PageSize
	if limit == 0 {
		limit = 5
	}
	from, to := time.Time{}, time.Time{}
	var err error
	if args.FromTime != "" {
		from, err = parseRecallTime(args.FromTime, energy.Timezone())
		if err != nil {
			return "", err
		}
	}
	if args.ToTime != "" {
		to, err = parseRecallTime(args.ToTime, energy.Timezone())
		if err != nil {
			return "", err
		}
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return "", fmt.Errorf("from_time must precede to_time")
	}
	if args.WorkspaceID == 0 {
		records, err := dops.SearchWorkspaces(personID, 0, strings.TrimSpace(args.Query), from, to, page, limit+1)
		if err != nil {
			return "", err
		}
		hasMore := len(records) > limit
		if hasMore {
			records = records[:limit]
		}
		result := map[string]any{"workspaces": records, "has_more": hasMore}
		if hasMore {
			result["next_cursor"] = strconv.Itoa(page + 1)
		}
		encoded, err := json.Marshal(result)
		return string(encoded), err
	}
	record, err := dops.GetOwnedWorkspace(personID, args.WorkspaceID)
	if err != nil {
		return "", err
	}
	_, pathErr := aos.ResolveRegisteredPath(*record)
	uses, err := dops.ListWorkspaceUses(personID, record.ID, page, limit+1)
	if err != nil {
		return "", err
	}
	hasMore := len(uses) > limit
	if hasMore {
		uses = uses[:limit]
	}
	type activity struct {
		SourceType  model.WorkspaceUseSource `json:"source_type"`
		SourceID    int64                    `json:"source_id"`
		Role        model.WorkspaceUseRole   `json:"role"`
		Description string                   `json:"description"`
		Status      model.WorkStatus         `json:"work_status,omitempty"`
		At          time.Time                `json:"at"`
	}
	activities := make([]activity, 0, len(uses))
	for _, use := range uses {
		item := activity{SourceType: use.SourceType, SourceID: use.SourceID, Role: use.Role, At: use.CreatedAt}
		if use.SourceType == model.WorkspaceUseWork {
			var work model.Work
			if err := database.DB.Where("id = ? AND person_id = ?", use.SourceID, personID).Take(&work).Error; err != nil {
				return "", fmt.Errorf("Workspace use points to unavailable Work %d: %w", use.SourceID, err)
			}
			item.Description, item.Status = work.Description, work.Status
		} else {
			item.Description = "Private-space activity"
		}
		activities = append(activities, item)
	}
	var source struct {
		ActionID int64 `json:"action_id"`
		EventID  int64 `json:"event_id"`
	}
	if err := database.DB.Table("action_effects AS ae").Select("ae.action_id, d.event_id").Joins("JOIN actions AS a ON a.id = ae.action_id").Joins("JOIN decisions AS d ON d.id = a.decision_id").Where("ae.effect_type = ? AND ae.effect_id = ? AND d.person_id = ?", model.ActionEffectWorkspace, record.ID, personID).Limit(1).Scan(&source).Error; err != nil {
		return "", err
	}
	result := map[string]any{"workspace": record, "directory_available": pathErr == nil, "creation_source": source, "declared_uses": activities, "has_more": hasMore}
	if hasMore {
		result["next_cursor"] = strconv.Itoa(page + 1)
	}
	if pathErr != nil {
		result["directory_error"] = pathErr.Error()
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

// encodeWorkRecallWithWorkspace adds the Work's registered file environment
// without changing the general memory retrieval or its observation rules.
func encodeWorkRecallWithWorkspace(personID int64, page memory.RecallPage) (string, error) {
	if !database.DB.Migrator().HasTable(&model.WorkspaceUse{}) {
		encoded, err := json.Marshal(page)
		return string(encoded), err
	}
	workspaces := make(map[int64]*model.Workspace)
	for _, item := range page.Items {
		if item.SourceKind != model.MemorySourceWork {
			continue
		}
		record, err := dops.GetDefaultWorkWorkspace(personID, item.SourceID)
		if err == nil {
			workspaces[item.SourceID] = record
		}
	}
	encoded, err := json.Marshal(map[string]any{"page": page, "default_workspaces_by_work_id": workspaces})
	return string(encoded), err
}
