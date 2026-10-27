package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/vectorutils"

	"gorm.io/gorm"
)

const (
	// dailyMemoryRefreshInterval spaces background index passes.
	dailyMemoryRefreshInterval = 5 * time.Minute
	// dailyMemoryRecentBackfillDays includes the seven completed local dates
	// before today; the seed query also includes the still-open current date.
	dailyMemoryRecentBackfillDays = 7
	// dailyMemorySourceBatchSize bounds one database scan while discovering days.
	dailyMemorySourceBatchSize = 1000
)

// DailyMemoryPage is a bounded navigation page, not a source transcript.
type DailyMemoryPage struct {
	Items    []model.DailyMemory `json:"items"`
	HasMore  bool                `json:"has_more"`
	Coverage string              `json:"coverage"`
}

// RecallDailyMemories finds owned date resource groups by date, text, or an optional
// semantic query. A caller must return to observed sources to check details.
func RecallDailyMemories(ctx context.Context, personID, memoryID int64, query string, from, to time.Time, limit, offset int) (DailyMemoryPage, error) {
	if personID <= 0 || memoryID < 0 {
		return DailyMemoryPage{}, fmt.Errorf("daily memory owner must be positive and memory_id cannot be negative")
	}
	if limit < 1 || limit > RecallMemoryMaxPageSize || offset < 0 {
		return DailyMemoryPage{}, fmt.Errorf("daily memory page requires limit 1–%d and nonnegative offset", RecallMemoryMaxPageSize)
	}
	if len([]rune(query)) > RecallMemoryMaxQueryRunes {
		return DailyMemoryPage{}, fmt.Errorf("daily memory query exceeds %d characters", RecallMemoryMaxQueryRunes)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return DailyMemoryPage{}, fmt.Errorf("daily memory from_time must precede to_time")
	}
	if memoryID > 0 && (strings.TrimSpace(query) != "" || !from.IsZero() || !to.IsZero() || offset != 0) {
		return DailyMemoryPage{}, fmt.Errorf("daily memory_id read does not accept search filters or page offset")
	}
	page := DailyMemoryPage{Items: []model.DailyMemory{}, Coverage: "Dated recollections cover past days, not today. Older days may have no dated recollection. Check the original events and actions when details matter; an empty result does not establish that nothing happened."}
	q := database.DB.WithContext(ctx).Where("person_id = ? AND local_date < ?", personID, energy.Today())
	if memoryID > 0 {
		q = q.Where("id = ?", memoryID)
	}
	if !from.IsZero() {
		q = q.Where("end_at >= ?", from)
	}
	if !to.IsZero() {
		q = q.Where("start_at < ?", to)
	}
	// Browse pages are read in the database. Semantic search currently scans all
	// owned groups in the time range until a vector index is available.
	if strings.TrimSpace(query) == "" {
		var rows []model.DailyMemory
		if err := q.Order("start_at DESC, shard DESC").Offset(offset).Limit(limit + 1).Find(&rows).Error; err != nil {
			return page, err
		}
		if len(rows) > limit {
			page.HasMore = true
			rows = rows[:limit]
		}
		page.Items = rows
		return page, nil
	}
	var rows []model.DailyMemory
	if err := q.Order("start_at DESC, shard DESC").Find(&rows).Error; err != nil {
		return page, err
	}
	if strings.TrimSpace(query) != "" {
		var vector []float32
		if embeddingSvc != nil {
			var err error
			vector, err = embeddingSvc.EmbedSingle(ctx, query)
			if err != nil {
				applogger.Error("daily memory semantic query failed", "person_id", personID, "error", err)
				page.Coverage += " Only matching wording could be searched; related wording may be missed."
			}
		} else {
			page.Coverage += " Only matching wording could be searched; related wording may be missed."
		}
		if len(vector) > 0 {
			type ranked struct {
				row   model.DailyMemory
				score float64
			}
			candidates := make([]ranked, 0, len(rows))
			missingVectors := false
			for _, row := range rows {
				stored := vectorutils.BlobToFloat32Slice(row.Embedding)
				if len(stored) != len(vector) || len(stored) == 0 {
					missingVectors = true
					if strings.Contains(strings.ToLower(row.Summary), strings.ToLower(query)) {
						candidates = append(candidates, ranked{row, 1})
					}
					continue
				}
				score := vectorutils.CosineSimilarity(vector, stored)
				if strings.Contains(strings.ToLower(row.Summary), strings.ToLower(query)) {
					score = literalMatchSimilarity
				}
				candidates = append(candidates, ranked{row, score})
			}
			if missingVectors {
				page.Coverage += " Some recollections may be missed by a meaning-based search."
			}
			sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
			rows = rows[:0]
			for _, candidate := range candidates {
				rows = append(rows, candidate.row)
			}
		} else {
			filtered := rows[:0]
			for _, row := range rows {
				if strings.Contains(strings.ToLower(row.Summary), strings.ToLower(query)) {
					filtered = append(filtered, row)
				}
			}
			rows = filtered
		}
	}
	if offset >= len(rows) {
		return page, nil
	}
	end := offset + limit
	if end < len(rows) {
		page.HasMore = true
	} else {
		end = len(rows)
	}
	page.Items = rows[offset:end]
	return page, nil
}

// dailySourceKind distinguishes the original records used by a contact-day
// account. A Decision is listed separately only when it produced no Action.
type dailySourceKind int

const (
	dailySourceObservation dailySourceKind = iota + 1
	dailySourceAction
	dailySourceDecision
)

// dailySource carries one source identity and its contact time. RefID and Text
// are filled only when a source page needs to be shown.
type dailySource struct {
	ID    int64
	RefID int64
	Kind  dailySourceKind
	At    time.Time
	Text  string
}

// DailySourceView names an original record reached through a contact-day
// group. EventID and ActionID can be followed with the existing recall tools;
// an actionless Decision's recorded thoughts are read in this source page.
type DailySourceView struct {
	EventID    int64     `json:"event_id,omitempty"`
	ActionID   int64     `json:"action_id,omitempty"`
	DecisionID int64     `json:"decision_id,omitempty"`
	ContactAt  time.Time `json:"contact_at"`
	Text       string    `json:"text"`
}

// ReadDailyMemorySources re-reads the selected causal/resource group and
// checks current ownership of each source on the requested page.
func ReadDailyMemorySources(ctx context.Context, personID, memoryID int64, limit, offset int) ([]DailySourceView, bool, string, error) {
	if personID <= 0 || memoryID <= 0 {
		return nil, false, "", fmt.Errorf("daily source read requires positive owner and memory IDs")
	}
	if limit < 1 || limit > RecallMemoryMaxPageSize || offset < 0 {
		return nil, false, "", fmt.Errorf("daily source page requires limit 1–%d and nonnegative offset", RecallMemoryMaxPageSize)
	}
	var record model.DailyMemory
	if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ? AND local_date < ?", memoryID, personID, energy.Today()).Take(&record).Error; err != nil {
		return nil, false, "", err
	}
	groups, err := dailyGroupsForDate(ctx, personID, record.LocalDate)
	if err != nil {
		return nil, false, "", err
	}
	key := dailyGroupKey{Type: record.GroupType, ID: record.GroupID}
	var group *dailyGroup
	for _, candidate := range groups {
		if candidate.Key == key {
			group = candidate
			break
		}
	}
	if group == nil {
		return nil, false, "", fmt.Errorf("daily memory %d no longer has its source group", memoryID)
	}
	// A completed day's inputs are frozen, so the recomputed group is exactly
	// the group the saved account summarized. No snapshot boundary is needed.
	sources := group.Sources
	coverage := "This account may omit other events from that day. An action shown here may also have a later outcome. Check other records before concluding that an event did not occur."
	if offset >= len(sources) {
		return []DailySourceView{}, false, coverage, nil
	}
	end := offset + limit
	more := end < len(sources)
	if !more {
		end = len(sources)
	}
	sources = sources[offset:end]
	if err := fillDailySourceText(ctx, personID, group.DayStart, group.DayEnd, sources); err != nil {
		return nil, false, "", err
	}
	items := make([]DailySourceView, 0, len(sources))
	for _, s := range sources {
		view := DailySourceView{ContactAt: s.At, Text: s.Text}
		switch s.Kind {
		case dailySourceObservation:
			view.EventID = s.RefID
		case dailySourceAction:
			view.ActionID = s.RefID
		case dailySourceDecision:
			view.DecisionID = s.RefID
		default:
			return nil, false, "", fmt.Errorf("invalid daily source kind %d", s.Kind)
		}
		items = append(items, view)
	}
	return items, more, coverage, nil
}

// runDailyMemoryIndex updates groups off the agent's decision path. On each
// pass it discovers contact days from observations and committed own actions.
func runDailyMemoryIndex(ctx context.Context) {
	ticker := time.NewTicker(dailyMemoryRefreshInterval)
	defer ticker.Stop()
	state := dailyIndexState{pending: make(map[int64]map[string]struct{})}
	for {
		if err := refreshDailyMemoryIndex(ctx, &state); err != nil && ctx.Err() == nil {
			applogger.Error("daily memory index refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// dailyIndexState tracks newly discovered contact days between index passes.
// Initial seeding covers only recent history; cursors then follow new IDs.
type dailyIndexState struct {
	initialized       bool
	lastObservationID int64
	lastDecisionID    int64
	pending           map[int64]map[string]struct{}
}

// recentDailyBackfillStart covers yesterday and the six preceding local dates.
func recentDailyBackfillStart(now time.Time) time.Time {
	local := now.In(energy.Timezone())
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, energy.Timezone())
	return today.AddDate(0, 0, -dailyMemoryRecentBackfillDays)
}

// seedRecentDailyIndex discovers seven completed dates and queues the current
// date without summarizing it. Cursors then follow newly committed source IDs.
func seedRecentDailyIndex(ctx context.Context, state *dailyIndexState) error {
	cutoff := recentDailyBackfillStart(time.Now())
	tomorrow := cutoff.AddDate(0, 0, dailyMemoryRecentBackfillDays+1)
	var maxObservationID, maxDecisionID int64
	if err := database.DB.WithContext(ctx).Model(&model.AgentObservation{}).Select("COALESCE(MAX(id),0)").Scan(&maxObservationID).Error; err != nil {
		return err
	}
	if err := database.DB.WithContext(ctx).Model(&model.Decision{}).Select("COALESCE(MAX(id),0)").Scan(&maxDecisionID).Error; err != nil {
		return err
	}
	if state.pending == nil {
		state.pending = make(map[int64]map[string]struct{})
	}
	add := func(personID int64, at time.Time) {
		if state.pending[personID] == nil {
			state.pending[personID] = make(map[string]struct{})
		}
		state.pending[personID][at.In(energy.Timezone()).Format("2006-01-02")] = struct{}{}
	}
	var indexed []struct {
		PersonID  int64
		LocalDate string
	}
	if err := database.DB.WithContext(ctx).Model(&model.DailyMemory{}).
		Distinct("person_id", "local_date").Where("local_date >= ? AND local_date < ?", cutoff.Format("2006-01-02"), tomorrow.Format("2006-01-02")).Find(&indexed).Error; err != nil {
		return err
	}
	for _, row := range indexed {
		if state.pending[row.PersonID] == nil {
			state.pending[row.PersonID] = make(map[string]struct{})
		}
		state.pending[row.PersonID][row.LocalDate] = struct{}{}
	}
	var observations []model.AgentObservation
	if err := database.DB.WithContext(ctx).Where("id <= ? AND created_at >= ? AND created_at < ?", maxObservationID, cutoff, tomorrow).
		Select("id, person_id, created_at").FindInBatches(&observations, dailyMemorySourceBatchSize, func(_ *gorm.DB, _ int) error {
		for _, row := range observations {
			add(row.PersonID, row.CreatedAt)
		}
		return ctx.Err()
	}).Error; err != nil {
		return err
	}
	// A Decision owns its Actions, so the Decision's local date is the day the
	// activity belongs to. Every Decision is queued, including a heartbeat that
	// produced no Action: the choice itself is part of that day's account.
	var decisions []model.Decision
	if err := database.DB.WithContext(ctx).Where("created_at >= ? AND created_at < ?", cutoff, tomorrow).
		Select("id, person_id, created_at").FindInBatches(&decisions, dailyMemorySourceBatchSize, func(_ *gorm.DB, _ int) error {
		for _, row := range decisions {
			add(row.PersonID, row.CreatedAt)
		}
		return ctx.Err()
	}).Error; err != nil {
		return err
	}
	state.lastObservationID = maxObservationID
	state.lastDecisionID = maxDecisionID
	state.initialized = true
	return nil
}

// refreshDailyMemoryIndex discovers closed days that still need an account and
// appends only the groups those days lack, within the configured per-pass budget.
func refreshDailyMemoryIndex(ctx context.Context, state *dailyIndexState) error {
	type sourceKey struct {
		PersonID  int64
		ID        int64
		CreatedAt time.Time
	}
	add := func(s sourceKey) {
		if state.pending[s.PersonID] == nil {
			state.pending[s.PersonID] = make(map[string]struct{})
		}
		state.pending[s.PersonID][s.CreatedAt.In(energy.Timezone()).Format("2006-01-02")] = struct{}{}
	}
	if !state.initialized {
		if err := seedRecentDailyIndex(ctx, state); err != nil {
			return err
		}
	}
	for {
		var observations []sourceKey
		if err := database.DB.WithContext(ctx).Table("agent_observations").Select("person_id, id, created_at").Where("id > ?", state.lastObservationID).Order("id").Limit(dailyMemorySourceBatchSize).Find(&observations).Error; err != nil {
			return err
		}
		for _, s := range observations {
			add(s)
			state.lastObservationID = s.ID
		}
		if len(observations) < dailyMemorySourceBatchSize {
			break
		}
	}
	for {
		var decisions []sourceKey
		// A Decision defines its day. Its Actions share that date, so following
		// Decisions also covers every Action, including a heartbeat choice that
		// produced none.
		if err := database.DB.WithContext(ctx).Table("decisions").Select("person_id, id, created_at").Where("id > ?", state.lastDecisionID).Order("id").Limit(dailyMemorySourceBatchSize).Find(&decisions).Error; err != nil {
			return err
		}
		for _, s := range decisions {
			add(s)
			state.lastDecisionID = s.ID
		}
		if len(decisions) < dailyMemorySourceBatchSize {
			break
		}
	}
	remaining := config.Get().DailyMemoryMaxGroupsPerPass
	todayDate := energy.Today()
	for personID, dates := range state.pending {
		orderedDates := make([]string, 0, len(dates))
		for date := range dates {
			orderedDates = append(orderedDates, date)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(orderedDates)))
		for _, date := range orderedDates {
			if date >= todayDate {
				continue // The day is still open; retain it for the next local date.
			}
			if remaining <= 0 {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			completed, err := refreshDailyMemoryDay(ctx, personID, date, &remaining)
			if err != nil {
				applogger.Error("daily memory day refresh failed", "person_id", personID, "date", date, "error", err)
			} else if completed {
				delete(dates, date)
			}
		}
		if len(dates) == 0 {
			delete(state.pending, personID)
		}
	}
	return nil
}

// fillDailySourceText resolves owned source identities only for the requested
// source page. The daily summary itself uses the causal hierarchy directly.
func fillDailySourceText(ctx context.Context, personID int64, dayStart, dayEnd time.Time, keys []dailySource) error {
	if !dayStart.Before(dayEnd) {
		return fmt.Errorf("daily source read requires a valid contact-day range")
	}
	for i := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := &keys[i]
		switch key.Kind {
		case dailySourceObservation:
			var observation model.AgentObservation
			if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", key.ID, personID).Take(&observation).Error; err != nil {
				return err
			}
			text, err := DescribeObservedEvent(ctx, personID, observation.EventID)
			if err != nil {
				return err
			}
			key.RefID = observation.EventID
			key.Text = text
		case dailySourceAction:
			var action model.Action
			if err := database.DB.WithContext(ctx).Table("actions").Select("actions.*").Joins("JOIN decisions ON decisions.id = actions.decision_id").Where("actions.id = ? AND decisions.person_id = ?", key.ID, personID).Take(&action).Error; err != nil {
				return err
			}
			key.RefID = action.ID
			key.Text = fmt.Sprintf("Your action %d (%s). Your situation assessment then: %q. Your reason then: %q.", action.ID, action.Type.Label(), action.Background, action.Reason)
			if action.PlanJSON != "" {
				key.Text += " Historical plan (JSON): " + action.PlanJSON + "."
			}
			key.Text += " This is an intention, not proof of its outcome."
			var effects []model.ActionEffect
			// The date index describes one completed day. Later effects remain
			// available through the original Action and their own contact day.
			if err := database.DB.WithContext(ctx).Where("action_id = ? AND created_at >= ? AND created_at < ?", action.ID, dayStart, dayEnd).Order("id").Find(&effects).Error; err != nil {
				return err
			}
			if action.Type == model.ActionTypeChat && !dailyHasMessageEffect(effects) {
				key.Text += " No sent message is recorded for this action on this day; a later outcome remains possible."
			} else if len(effects) == 0 {
				key.Text += " No result is recorded for this action on this day."
			}
			for _, effect := range effects {
				result, err := describeDailyActionEffect(ctx, personID, effect)
				if err != nil {
					return err
				}
				if result == "" {
					continue // Runtime-only feedback is not a recorded outcome.
				}
				key.Text += " Recorded result: " + result
			}
		case dailySourceDecision:
			var decision model.Decision
			if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ? AND created_at >= ? AND created_at < ?", key.ID, personID, dayStart, dayEnd).Take(&decision).Error; err != nil {
				return err
			}
			if strings.TrimSpace(decision.Thoughts) == "" {
				return fmt.Errorf("daily decision %d has no recorded thoughts", decision.ID)
			}
			key.RefID = decision.ID
			key.Text = fmt.Sprintf("Your decision %d produced no action. What you were thinking then: %q. This was your consideration, not an independently established fact.", decision.ID, decision.Thoughts)
		default:
			return fmt.Errorf("invalid daily source kind %d", key.Kind)
		}
	}
	return nil
}

// refreshDailyMemoryDay appends accounts for resource groups that one completed
// day still lacks. The day's inputs ended at local midnight, so an existing row
// is final: it is never rewritten, and a vanished group is never deleted.
func refreshDailyMemoryDay(ctx context.Context, personID int64, date string, remaining *int) (bool, error) {
	groups, err := dailyGroupsForDate(ctx, personID, date)
	if err != nil {
		return false, err
	}
	var existing []model.DailyMemory
	if err := database.DB.WithContext(ctx).Where("person_id = ? AND local_date = ?", personID, date).Find(&existing).Error; err != nil {
		return false, err
	}
	byKey := make(map[dailyGroupKey]bool, len(existing))
	nextSlot := 0
	for _, row := range existing {
		if row.Shard >= nextSlot {
			nextSlot = row.Shard + 1
		}
		key := dailyGroupKey{Type: row.GroupType, ID: row.GroupID}
		if byKey[key] {
			return false, fmt.Errorf("daily memory %d duplicates another resource %+v on %s", row.ID, key, date)
		}
		byKey[key] = true
	}
	current := make(map[dailyGroupKey]bool, len(groups))
	for _, group := range groups {
		current[group.Key] = true
		if byKey[group.Key] {
			continue // A completed day is append-only; its account stays as written.
		}
		if *remaining <= 0 {
			return false, nil
		}
		*remaining--
		summary, err := summarizeDailyGroup(ctx, personID, date, group)
		if err != nil {
			return false, err
		}
		if embeddingSvc == nil {
			return false, fmt.Errorf("daily memory embedding service is not initialized")
		}
		// Routing compares stored vectors, so a row must not be written without
		// one. Failure returns the day to the pending set and the next pass
		// regenerates the account instead of persisting an empty vector.
		vector, err := embeddingSvc.EmbedSingle(ctx, summary)
		if err != nil {
			applogger.Error("daily memory embedding unavailable; row not written", "person_id", personID, "date", date, "group_type", group.Key.Type, "group_id", group.Key.ID, "error", err)
			return false, err
		}
		row := model.DailyMemory{
			PersonID:  personID,
			LocalDate: date,
			Shard:     nextSlot,
			GroupType: group.Key.Type,
			GroupID:   group.Key.ID,
			Summary:   summary,
			StartAt:   group.StartAt,
			EndAt:     group.EndAt,
			Embedding: vectorutils.Float32SliceToBlob(vector),
		}
		if err := database.DB.WithContext(ctx).Create(&row).Error; err != nil {
			return false, err
		}
		nextSlot++
	}
	// A saved row without a recomputed source group means its inputs and its
	// account disagree. Report it instead of silently rewriting or deleting it.
	for _, row := range existing {
		key := dailyGroupKey{Type: row.GroupType, ID: row.GroupID}
		if !current[key] {
			applogger.Warn("daily memory row has no source group for its date", "person_id", personID, "date", date, "group_type", row.GroupType, "group_id", row.GroupID, "memory_id", row.ID)
		}
	}
	return true, nil
}
