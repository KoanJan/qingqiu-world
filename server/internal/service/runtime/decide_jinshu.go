package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	"gorm.io/gorm"
)

const defaultDecideJinshuPageSize = 5
const maxDecideJinshuPageSize = 20

// jinshuToolDefinitions exposes lightweight delivery lookups inside DecideLoop.
// These tools only read database metadata; attachment reading remains in Focus.
func jinshuToolDefinitions() []llm.FunctionDefinition {
	listProperties := map[string]interface{}{
		"query": map[string]interface{}{"type": "string", "description": "Optional substring of the topic or description; omit to browse newest first."},
		"page":  map[string]interface{}{"type": "integer", "description": "1-based page number; default 1."},
		"limit": map[string]interface{}{"type": "integer", "description": "Page size; default 5, maximum 20."},
	}
	return []llm.FunctionDefinition{
		{Name: "list_sent_jinshu", Description: "List Jinshu you sent, newest first. Returns IDs and summaries; use read_jinshu for a description. No attachments.", Parameters: map[string]interface{}{"type": "object", "properties": listProperties}},
		{Name: "list_received_jinshu", Description: "List Jinshu you received, newest first. Returns IDs and summaries; use read_jinshu for a description. No attachments.", Parameters: map[string]interface{}{"type": "object", "properties": listProperties}},
		{Name: "read_jinshu", Description: "Read the topic, description and delivery metadata of one Jinshu you sent or received. Does not return attachment contents.", Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"jinshu_id": map[string]interface{}{"type": "integer", "description": "ID returned by a Jinshu list."}}, "required": []string{"jinshu_id"}}},
	}
}

// isJinshuTool identifies the read-only delivery tools handled in DecideLoop.
func isJinshuTool(name string) bool {
	return name == "list_sent_jinshu" || name == "list_received_jinshu" || name == "read_jinshu"
}

// decideJinshuArguments carries the shared list and detail lookup parameters.
type decideJinshuArguments struct {
	Query    string `json:"query"`
	Page     int    `json:"page"`
	Limit    int    `json:"limit"`
	JinshuID int64  `json:"jinshu_id"`
}

// decideJinshuItem is the bounded delivery metadata returned by list tools.
type decideJinshuItem struct {
	JinshuID  int64  `json:"jinshu_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Topic     string `json:"topic"`
	CreatedAt string `json:"created_at"`
	IsRead    *bool  `json:"is_read,omitempty"`
}

// decideJinshuDetail adds the description but never attachment contents.
type decideJinshuDetail struct {
	decideJinshuItem
	Description          string `json:"description"`
	DescriptionTruncated bool   `json:"description_truncated,omitempty"`
}

// executeJinshuTool runs a scoped, synchronous database lookup. Reading a
// delivery does not mark it as read or create another decision event.
func executeJinshuTool(personID int64, name, arguments string) (string, error) {
	var args decideJinshuArguments
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", fmt.Errorf("invalid Jinshu arguments: %w", err)
	}
	if err := decoder.Decode(new(interface{})); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("invalid Jinshu arguments: trailing JSON data")
	}
	if !isJinshuTool(name) {
		return "", fmt.Errorf("unknown Jinshu tool %q", name)
	}
	if name == "read_jinshu" {
		if args.JinshuID <= 0 || args.Query != "" || args.Page != 0 || args.Limit != 0 {
			return "", fmt.Errorf("read_jinshu requires a positive jinshu_id only")
		}
		return readDecideJinshu(personID, args.JinshuID)
	}
	if args.JinshuID != 0 {
		return "", fmt.Errorf("%s does not accept jinshu_id", name)
	}
	page, limit := args.Page, args.Limit
	if page == 0 {
		page = 1
	}
	if limit == 0 {
		limit = defaultDecideJinshuPageSize
	}
	if page < 1 || limit < 1 || limit > maxDecideJinshuPageSize {
		return "", fmt.Errorf("page must be positive and limit must be between 1 and %d", maxDecideJinshuPageSize)
	}
	// Reject an offset that would overflow int before querying the database.
	maxInt := int(^uint(0) >> 1)
	if page-1 > (maxInt-limit-1)/limit {
		return "", fmt.Errorf("page is too large")
	}
	offset := (page - 1) * limit
	return listDecideJinshu(personID, name, args.Query, page, offset, limit)
}

// listDecideJinshu queries only deliveries sent or received by this person.
// One extra row determines whether the bounded result has another page.
func listDecideJinshu(personID int64, name, query string, page, offset, limit int) (string, error) {
	var records []model.Jinshu
	var err error
	if name == "list_sent_jinshu" {
		records, err = dops.SearchSentJinshu(personID, query, offset, limit+1)
	} else {
		records, err = dops.SearchReceivedJinshu(personID, query, offset, limit+1)
	}
	if err != nil {
		return "", err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	names, err := namesForDecideJinshu(records)
	if err != nil {
		return "", err
	}
	items := make([]decideJinshuItem, 0, len(records))
	for _, record := range records {
		items = append(items, newDecideJinshuItem(record, names, personID, name == "list_received_jinshu" && record.ToPersonID == personID))
	}
	result := struct {
		Results  []decideJinshuItem `json:"results"`
		Page     int                `json:"page"`
		HasMore  bool               `json:"has_more"`
		NextPage int                `json:"next_page,omitempty"`
	}{Results: items, Page: page, HasMore: hasMore}
	if hasMore {
		result.NextPage = page + 1
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

// readDecideJinshu rechecks sender or recipient access before returning
// metadata. A long description is abbreviated to fit the decision budget.
func readDecideJinshu(personID, jinshuID int64) (string, error) {
	record, err := dops.GetJinshu(jinshuID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	if record == nil || (record.FromPersonID != personID && record.ToPersonID != personID) {
		// Do not reveal whether an inaccessible ID exists.
		return "", fmt.Errorf("Jinshu not found or inaccessible")
	}
	names, err := namesForDecideJinshu([]model.Jinshu{*record})
	if err != nil {
		return "", err
	}
	detail := decideJinshuDetail{decideJinshuItem: newDecideJinshuItem(*record, names, personID, record.ToPersonID == personID), Description: record.Description}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxRecallResultBytes {
		// Preserve a bounded excerpt instead of discarding a large description.
		detail.DescriptionTruncated = true
		for maxBytes := 4000; maxBytes > 0; maxBytes /= 2 {
			detail.Description = truncateUTF8Bytes(record.Description, maxBytes)
			encoded, err = json.Marshal(detail)
			if err != nil || len(encoded) <= maxRecallResultBytes {
				break
			}
		}
	}
	if len(encoded) > maxRecallResultBytes {
		return "", fmt.Errorf("Jinshu metadata exceeds the decision tool result limit")
	}
	return string(encoded), err
}

// namesForDecideJinshu resolves the participants shown in delivery results.
func namesForDecideJinshu(records []model.Jinshu) (map[int64]string, error) {
	ids := make([]int64, 0, len(records)*2)
	for _, record := range records {
		ids = append(ids, record.FromPersonID, record.ToPersonID)
	}
	return dops.GetPersonNames(ids)
}

// newDecideJinshuItem exposes read status only for the recipient's own view.
func newDecideJinshuItem(record model.Jinshu, names map[int64]string, selfPersonID int64, showIsRead bool) decideJinshuItem {
	item := decideJinshuItem{
		JinshuID:  record.ID,
		From:      decideJinshuPersonName(names, record.FromPersonID, selfPersonID),
		To:        decideJinshuPersonName(names, record.ToPersonID, selfPersonID),
		Topic:     record.Topic,
		CreatedAt: record.CreatedAt.Format(time.RFC3339),
	}
	if showIsRead {
		item.IsRead = &record.IsRead
	}
	return item
}

// decideJinshuPersonName marks the reader's own role and makes a missing
// identity explicit instead of presenting a database ID as a name.
func decideJinshuPersonName(names map[int64]string, personID, selfPersonID int64) string {
	if personID == selfPersonID {
		return "You"
	}
	if name := names[personID]; name != "" {
		return name
	}
	applogger.Error("decide Jinshu participant identity missing", "person_id", personID)
	return fmt.Sprintf("Unknown person (person_id=%d)", personID)
}

// truncateUTF8Bytes fits a description in a byte budget without cutting a rune.
func truncateUTF8Bytes(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
