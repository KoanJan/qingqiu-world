package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
)

const (
	// cognitiveRecallDefaultPageSize keeps an unqualified recall concise.
	cognitiveRecallDefaultPageSize = 5
	// cognitiveRecallCursorHashBytes keeps cursor tokens short while retaining
	// enough filter identity to reject accidental cross-query reuse.
	cognitiveRecallCursorHashBytes = 12
)

// cognitiveRecallDefinitions adds discovery of subjective understandings and
// date navigation to both Decide and the background CognitiveLoop.
func cognitiveRecallDefinitions() []llm.FunctionDefinition {
	common := map[string]interface{}{
		"memory_id": map[string]interface{}{"type": "integer", "description": "Read one owned memory by ID; omit to search or browse. Do not combine with search filters."},
		"query":     map[string]interface{}{"type": "string", "description": fmt.Sprintf("Optional semantic concern or literal words, at most %d characters; omit to browse recent entries.", memory.RecallMemoryMaxQueryRunes)},
		"from_time": map[string]interface{}{"type": "string", "description": "Optional inclusive time in RFC3339 or project-local date/time."},
		"to_time":   map[string]interface{}{"type": "string", "description": "Optional exclusive time in RFC3339 or project-local date/time."},
		"page_size": map[string]interface{}{"type": "integer", "description": fmt.Sprintf("0 or omitted defaults to %d; maximum %d.", cognitiveRecallDefaultPageSize, memory.RecallMemoryMaxPageSize)},
		"cursor":    map[string]interface{}{"type": "string", "description": "Continuation from a previous result; repeat the same filters."},
	}
	cognitive := make(map[string]interface{}, len(common)+1)
	for key, value := range common {
		cognitive[key] = value
	}
	cognitive["time_field"] = map[string]interface{}{"type": "integer", "enum": []memory.CognitiveTimeField{memory.CognitiveTimeCreated, memory.CognitiveTimeReviewed}, "description": fmt.Sprintf("%d filters when the concern was first recorded (concern_created_at); %d or omitted filters the last completed review (last_reviewed_at), including reviews without a new version. Neither filters when the current understanding was formed.", memory.CognitiveTimeCreated, memory.CognitiveTimeReviewed)}
	return []llm.FunctionDefinition{
		{Name: "recall_daily_memory", Description: "Find a completed contact day or related resource group by time, words, or semantic clue. Today's records are available through original-record recall. A summary is a navigation hint: check original observations and actions for evidence. An index miss is not proof of absence.", Parameters: map[string]interface{}{"type": "object", "properties": common}},
		{Name: "recall_cognitive_memory", Description: "Find your current understanding of a concern, or read one memory and its earlier revisions. concern_created_at is when you first recorded the concern; current_understanding_formed_at is when the current version was written; last_reviewed_at is the last completed review, even if it changed nothing. These are your interpretations, not world facts; check original records when needed.", Parameters: map[string]interface{}{"type": "object", "properties": cognitive}},
	}
}

// cognitiveRecallArgs is the shared filter schema for date and cognitive recall.
type cognitiveRecallArgs struct {
	MemoryID  int64                     `json:"memory_id"`
	Query     string                    `json:"query"`
	FromTime  string                    `json:"from_time"`
	ToTime    string                    `json:"to_time"`
	TimeField memory.CognitiveTimeField `json:"time_field"`
	PageSize  int                       `json:"page_size"`
	Cursor    string                    `json:"cursor"`
}

// executeCognitiveRecall confines every query to the calling person, including
// a direct ID lookup. A cursor binds an offset to this owner and filter set.
func executeCognitiveRecall(ctx context.Context, personID int64, name, arguments string) (string, error) {
	if personID <= 0 {
		return "", fmt.Errorf("cognitive recall owner must be positive")
	}
	if name != "recall_daily_memory" && name != "recall_cognitive_memory" {
		return "", fmt.Errorf("unknown cognitive recall tool %q", name)
	}
	var args cognitiveRecallArgs
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", fmt.Errorf("cognitive recall arguments must contain one JSON object")
	}
	if args.MemoryID < 0 {
		return "", fmt.Errorf("memory_id cannot be negative")
	}
	if args.PageSize < 0 || args.PageSize > memory.RecallMemoryMaxPageSize {
		return "", fmt.Errorf("page_size must be 0 for the default or between 1 and %d", memory.RecallMemoryMaxPageSize)
	}
	if args.TimeField < memory.CognitiveTimeDefault || args.TimeField > memory.CognitiveTimeReviewed || (name == "recall_daily_memory" && args.TimeField != memory.CognitiveTimeDefault) {
		return "", fmt.Errorf("time_field must be omitted for daily memory and be %d or %d when specified for cognitive memory", memory.CognitiveTimeCreated, memory.CognitiveTimeReviewed)
	}
	if len([]rune(args.Query)) > memory.RecallMemoryMaxQueryRunes {
		return "", fmt.Errorf("query exceeds %d characters", memory.RecallMemoryMaxQueryRunes)
	}
	if args.MemoryID > 0 && (args.Query != "" || args.FromTime != "" || args.ToTime != "" || args.TimeField != memory.CognitiveTimeDefault) {
		return "", fmt.Errorf("memory_id read does not accept search filters")
	}
	pageSize := args.PageSize
	if pageSize == 0 {
		pageSize = cognitiveRecallDefaultPageSize
	}
	args.PageSize = pageSize
	offset := 0
	if args.Cursor != "" {
		var err error
		offset, err = decodeCognitiveRecallCursor(personID, name, args, args.Cursor)
		if err != nil {
			return "", fmt.Errorf("invalid cognitive recall cursor")
		}
	}
	from, to, err := parseRecallBounds(args.FromTime, args.ToTime)
	if err != nil {
		return "", err
	}
	if name == "recall_daily_memory" {
		searchOffset := offset
		if args.MemoryID > 0 {
			// The cursor pages sources; the selected memory remains visible.
			searchOffset = 0
		}
		page, err := memory.RecallDailyMemories(ctx, personID, args.MemoryID, args.Query, from, to, pageSize, searchOffset)
		if err != nil {
			return "", err
		}
		type item struct {
			ID      int64     `json:"memory_id"`
			Date    string    `json:"date"`
			StartAt time.Time `json:"start_at"`
			EndAt   time.Time `json:"end_at"`
			Summary string    `json:"summary"`
		}
		items := make([]item, 0, len(page.Items))
		for _, row := range page.Items {
			items = append(items, item{row.ID, row.LocalDate, row.StartAt, row.EndAt, row.Summary})
		}
		if args.MemoryID > 0 {
			sources, more, sourceCoverage, err := memory.ReadDailyMemorySources(ctx, personID, args.MemoryID, pageSize, offset)
			if err != nil {
				return "", err
			}
			var next string
			if more {
				next = encodeCognitiveRecallCursor(personID, name, args, offset+len(sources))
			}
			encoded, err := json.Marshal(map[string]interface{}{"items": items, "sources": sources, "next_cursor": next, "coverage": page.Coverage + " " + sourceCoverage})
			return string(encoded), err
		}
		var next string
		if page.HasMore {
			next = encodeCognitiveRecallCursor(personID, name, args, offset+len(items))
		}
		encoded, err := json.Marshal(map[string]interface{}{"items": items, "next_cursor": next, "coverage": page.Coverage})
		return string(encoded), err
	}
	if args.MemoryID > 0 {
		record, versions, more, err := memory.ReadCognitiveMemory(ctx, personID, args.MemoryID, pageSize, offset)
		if err != nil {
			return "", err
		}
		var next string
		if more {
			next = encodeCognitiveRecallCursor(personID, name, args, offset+len(versions))
		}
		encoded, err := json.Marshal(map[string]interface{}{"memory": record, "versions": versions, "next_cursor": next, "coverage": "Subjective understanding; original records require separate recall."})
		return string(encoded), err
	}
	items, more, coverage, err := memory.FindCognitiveMemories(ctx, personID, args.Query, from, to, args.TimeField, pageSize, offset, true)
	if err != nil {
		return "", err
	}
	var next string
	if more {
		next = encodeCognitiveRecallCursor(personID, name, args, offset+len(items))
	}
	encoded, err := json.Marshal(map[string]interface{}{"items": items, "next_cursor": next, "coverage": coverage})
	return string(encoded), err
}

// cognitivePageCursor binds pagination to an owner and normalized filters.
type cognitivePageCursor struct {
	Offset int    `json:"offset"`
	Hash   string `json:"hash"`
}

// cognitiveRecallHash prevents a cursor from being reused for another query.
func cognitiveRecallHash(personID int64, name string, args cognitiveRecallArgs) string {
	args.Cursor = ""
	key, _ := json.Marshal(struct {
		PersonID int64               `json:"person_id"`
		Name     string              `json:"name"`
		Args     cognitiveRecallArgs `json:"args"`
	}{PersonID: personID, Name: name, Args: args})
	digest := sha256.Sum256(key)
	return fmt.Sprintf("%x", digest[:cognitiveRecallCursorHashBytes])
}

// encodeCognitiveRecallCursor carries the next offset with its filter hash.
func encodeCognitiveRecallCursor(personID int64, name string, args cognitiveRecallArgs, offset int) string {
	data, _ := json.Marshal(cognitivePageCursor{Offset: offset, Hash: cognitiveRecallHash(personID, name, args)})
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeCognitiveRecallCursor rejects mismatched or malformed continuations.
func decodeCognitiveRecallCursor(personID int64, name string, args cognitiveRecallArgs, encoded string) (int, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, err
	}
	var cursor cognitivePageCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return 0, err
	}
	if cursor.Offset < 0 || cursor.Hash != cognitiveRecallHash(personID, name, args) {
		return 0, fmt.Errorf("cursor does not match the owner or filters")
	}
	return cursor.Offset, nil
}

// parseRecallBounds applies the project timezone to unzoned input times.
func parseRecallBounds(fromText, toText string) (from, to time.Time, err error) {
	if fromText != "" {
		from, err = parseRecallTime(fromText, energy.Timezone())
		if err != nil {
			return
		}
	}
	if toText != "" {
		to, err = parseRecallTime(toText, energy.Timezone())
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		err = fmt.Errorf("from_time must precede to_time")
	}
	return
}
