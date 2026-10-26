package memory

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// RecallScope is a read intent, not a stored memory category. ScopeAll searches
// every content-indexed source; narrower scopes retain source authorization.
type RecallScope int

const (
	// RecallAll searches every indexed source available to the person.
	RecallAll RecallScope = iota
	// RecallEvents searches observed Event records of any supported type.
	RecallEvents
	// RecallMessages searches observed Message Events only.
	RecallMessages
	// RecallActions searches the person's own top-level Actions.
	RecallActions
	// RecallWorks searches the person's current Work records.
	RecallWorks
	// RecallHandoffs searches the person's recorded Focus handoffs.
	RecallHandoffs
)

// RecallRequest bounds one read of authoritative history.
type RecallRequest struct {
	// Scope selects the source family to query.
	Scope RecallScope
	// PersonID is the subject whose observation and ownership rules apply.
	PersonID int64
	// SourceID selects one row within a source-specific scope.
	SourceID int64
	// ActionID limits Event results to effects recorded for this Action.
	ActionID int64
	// DecisionID limits Action results to one accepted Decision.
	DecisionID int64
	// SessionID and MessageID limit Message Event results.
	SessionID int64
	MessageID int64
	// WorkID limits Action adjustments or Focus handoffs to one Work.
	WorkID int64
	// Query supplies lexical terms; an empty query browses by time.
	Query string
	// FromTime is inclusive and ToTime is exclusive in source occurrence time.
	FromTime time.Time
	ToTime   time.Time
	// PageSize defaults to five and is capped at twenty.
	PageSize int
	// Cursor continues the same query after the previous page.
	Cursor string
}

// RecallItem carries an original source in a form the decision can inspect.
type RecallItem struct {
	// SourceKind and SourceID identify the authoritative row for navigation.
	SourceKind model.MemorySourceKind `json:"source_kind"`
	SourceID   int64                  `json:"source_id"`
	// OccurredAt is the source's occurrence or creation time.
	OccurredAt time.Time `json:"occurred_at"`
	// Text describes that source without converting claims into world facts.
	Text string `json:"text"`
}

// RecallPage is one bounded result. A caller must never treat an empty page
// as proof of nonexistence when Coverage is incomplete.
type RecallPage struct {
	// Items contains only authorized rows on this page.
	Items []RecallItem `json:"items"`
	// HasMore reports whether a subsequent page is available.
	HasMore bool `json:"has_more"`
	// NextCursor is an opaque continuation token when HasMore is true.
	NextCursor string `json:"next_cursor,omitempty"`
	// Coverage states search-index limitations relevant to interpreting misses.
	Coverage string `json:"coverage,omitempty"`
}

// recallCursor binds continuation to one query and a stable upper time bound.
type recallCursor struct {
	// Hash binds the cursor to the request filters and page size.
	Hash string `json:"h"`
	// Upper excludes sources created after the first page was requested.
	Upper time.Time `json:"u"`
	// Time, Kind and ID locate the last row in the total result order.
	Time time.Time              `json:"t"`
	Kind model.MemorySourceKind `json:"k"`
	ID   int64                  `json:"i"`
}

// recallHash binds a continuation cursor to every filter and the page size.
// A cursor from another recall request must not be reused.
func recallHash(r RecallRequest) string {
	raw := fmt.Sprintf("%d|%d|%d|%d|%d|%d|%d|%d|%s|%s|%s|%d", r.Scope, r.PersonID, r.SourceID, r.ActionID, r.DecisionID, r.SessionID, r.MessageID, r.WorkID, r.Query, r.FromTime.UTC().Format(time.RFC3339Nano), r.ToTime.UTC().Format(time.RFC3339Nano), r.PageSize)
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", digest[:12])
}

// Recall searches with authorization and time filtering before source limits.
// The provisional term index returns lexical candidate identities only; source
// text is re-read for facts and permissions, not semantic relevance. A hit does
// not establish that a source is useful, and a miss does not establish that no
// relevant history exists.
func Recall(r RecallRequest) (RecallPage, error) {
	page := RecallPage{Items: []RecallItem{}}
	if r.PersonID <= 0 || r.Scope < RecallAll || r.Scope > RecallHandoffs {
		return page, errors.New("invalid recall identity or scope")
	}
	if r.SourceID < 0 || r.ActionID < 0 || r.DecisionID < 0 || r.SessionID < 0 || r.MessageID < 0 || r.WorkID < 0 {
		return page, errors.New("recall IDs cannot be negative")
	}
	if r.PageSize == 0 {
		r.PageSize = 5
	}
	if r.PageSize < 1 || r.PageSize > 20 {
		return page, errors.New("page_size must be between 1 and 20")
	}
	if len([]rune(r.Query)) > 200 {
		return page, errors.New("query is too long; use a narrower phrase")
	}
	if !r.FromTime.IsZero() && !r.ToTime.IsZero() && !r.FromTime.Before(r.ToTime) {
		return page, errors.New("from_time must precede to_time")
	}
	if r.SessionID > 0 && r.Scope != RecallMessages {
		return page, errors.New("session_id is only valid for message recall")
	}
	if r.MessageID > 0 && r.Scope != RecallMessages {
		return page, errors.New("message_id is only valid for message recall")
	}
	if r.ActionID > 0 && r.Scope != RecallEvents {
		return page, errors.New("action_id is only valid for event recall")
	}
	if r.DecisionID > 0 && r.Scope != RecallActions {
		return page, errors.New("decision_id is only valid for action recall")
	}
	if r.WorkID > 0 && r.Scope != RecallHandoffs && r.Scope != RecallActions {
		return page, errors.New("work_id is only valid for action or Focus handoff recall")
	}
	hash := recallHash(r)
	cur := recallCursor{Hash: hash, Upper: time.Now().UTC()}
	if r.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(r.Cursor)
		if err != nil || json.Unmarshal(data, &cur) != nil || cur.Hash != hash || cur.Time.IsZero() || cur.Upper.IsZero() {
			return page, errors.New("invalid or mismatched recall cursor")
		}
	}
	var kinds []model.MemorySourceKind
	switch r.Scope {
	case RecallAll:
		kinds = []model.MemorySourceKind{model.MemorySourceEvent, model.MemorySourceAction, model.MemorySourceWork, model.MemorySourceFocusHandoff}
	case RecallEvents, RecallMessages:
		kinds = []model.MemorySourceKind{model.MemorySourceEvent}
	case RecallActions:
		kinds = []model.MemorySourceKind{model.MemorySourceAction}
	case RecallWorks:
		kinds = []model.MemorySourceKind{model.MemorySourceWork}
	case RecallHandoffs:
		kinds = []model.MemorySourceKind{model.MemorySourceFocusHandoff}
	}
	var terms []string
	if strings.TrimSpace(r.Query) != "" {
		terms = searchTerms(r.Query)
		if len(terms) == 0 || len(terms) > 64 {
			return page, errors.New("query must contain 1 to 64 searchable terms; use a shorter phrase")
		}
	}
	limit := r.PageSize + 1
	var candidates []recallCandidate
	for _, kind := range kinds {
		found, err := recallSourceCandidates(r, cur, kind, terms, limit)
		if err != nil {
			return page, err
		}
		candidates = append(candidates, found...)
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.After(b.OccurredAt)
		}
		if a.SourceKind != b.SourceKind {
			return a.SourceKind > b.SourceKind
		}
		return a.SourceID > b.SourceID
	})
	if err := appendReadableCandidates(r.PersonID, r.PageSize, candidates, &page); err != nil {
		return page, err
	}
	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		cur.Time, cur.Kind, cur.ID = last.OccurredAt.UTC(), last.SourceKind, last.SourceID
		data, _ := json.Marshal(cur)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	if strings.TrimSpace(r.Query) != "" {
		page.Coverage = "Lexical index candidates; wording changes or text beyond the indexed head may require another query. An empty page does not establish that no event occurred."
		if !termIndexReady.Load() {
			page.Coverage = "Lexical index is building or unavailable; results may be incomplete. " + page.Coverage
		}
	}
	return page, nil
}

// appendReadableCandidates rechecks current authorization when a candidate's
// authoritative row is read, after the earlier indexed candidate query.
func appendReadableCandidates(personID int64, pageSize int, candidates []recallCandidate, page *RecallPage) error {
	seen := make(map[[2]int64]struct{}, len(candidates))
	for _, candidate := range candidates {
		key := [2]int64{int64(candidate.SourceKind), candidate.SourceID}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		item, ok, err := readRecallItem(personID, candidate.SourceKind, candidate.SourceID)
		if err != nil {
			return err
		}
		if !ok {
			applogger.Warn("recall candidate became inaccessible during source reread", "person_id", personID, "source_kind", candidate.SourceKind, "source_id", candidate.SourceID)
			continue
		}
		page.Items = append(page.Items, item)
		if len(page.Items) > pageSize {
			page.HasMore = true
			page.Items = page.Items[:pageSize]
			break
		}
	}
	return nil
}

// RecallEntityProfile reads only a currently contactable entity's latest
// impression. It has no lexical or temporal search semantics.
func RecallEntityProfile(personID int64, entityType model.EntityType, entityID int64) (string, error) {
	if personID <= 0 || entityID <= 0 {
		return "", errors.New("invalid profile identity")
	}
	var subject string
	switch entityType {
	case model.EntityTypePerson:
		var person model.Person
		if err := database.DB.Where("id = ?", entityID).Take(&person).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.New("person is not contactable")
		} else if err != nil {
			return "", err
		}
		subject = fmt.Sprintf("%s (person_id=%d)", person.Name, person.ID)
	case model.EntityTypeSession:
		if !canReadSession(personID, entityID) {
			return "", errors.New("session is not accessible")
		}
		subject = fmt.Sprintf("the conversation (session_id=%d)", entityID)
	default:
		return "", errors.New("invalid entity type")
	}
	var profile model.EntityProfile
	err := database.DB.Where("person_id = ? AND entity_type = ? AND entity_id = ?", personID, entityType, entityID).Take(&profile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Sprintf("No current impression is recorded for %s.", subject), nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Your current impression of %s, last updated %s: %s", subject, profile.LastUpdatedAt.Format(time.RFC3339), profile.Narrative), nil
}

// canReadSession checks current session membership before returning a
// session-scoped impression. Query failures deny access and are logged.
func canReadSession(personID, sessionID int64) bool {
	if sessionID <= 0 {
		return false
	}
	var count int64
	err := database.DB.Model(&model.ParticipantSession{}).Where("participant_id = ? AND session_id = ?", personID, sessionID).Count(&count).Error
	if err != nil {
		applogger.Error("memory session permission query failed", "person_id", personID, "session_id", sessionID, "error", err)
		return false
	}
	return count > 0
}
