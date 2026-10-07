package memory

import (
	"errors"
	"fmt"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// recallCandidate is a bounded source identity. Full source and permission
// checks happen only after candidates from all sources have been ordered.
type recallCandidate struct {
	// SourceKind and SourceID locate a row that passed the SQL filters.
	SourceKind model.MemorySourceKind
	SourceID   int64
	// OccurredAt positions the row in the shared chronological order.
	OccurredAt time.Time
}

// recallSourceCandidates keeps source-specific authorization in the database,
// before each branch's limit. The caller merges these bounded candidate lists.
func recallSourceCandidates(r RecallRequest, cur recallCursor, kind model.MemorySourceKind, terms []string, limit int) ([]recallCandidate, error) {
	switch kind {
	case model.MemorySourceEvent:
		return recallEventCandidates(r, cur, terms, limit)
	case model.MemorySourceAction:
		if r.WorkID > 0 {
			return recallWorkActionCandidates(r, cur, terms, limit)
		}
		db := database.DB.Table("actions").Joins("JOIN decisions ON decisions.id = actions.decision_id").
			Where("decisions.person_id = ?", r.PersonID)
		if r.DecisionID > 0 {
			db = db.Where("actions.decision_id = ?", r.DecisionID)
		}
		return selectRecallCandidates(db, r, cur, kind, "actions.id", "actions.created_at", terms, limit)
	case model.MemorySourceWork:
		db := database.DB.Table("works").Where("works.person_id = ?", r.PersonID)
		return selectRecallCandidates(db, r, cur, kind, "works.id", "works.created_at", terms, limit)
	case model.MemorySourceFocusHandoff:
		db := database.DB.Table("focus_handoffs").Where("focus_handoffs.person_id = ?", r.PersonID)
		if r.WorkID > 0 {
			db = db.Where("focus_handoffs.work_id = ?", r.WorkID)
		}
		return selectRecallCandidates(db, r, cur, kind, "focus_handoffs.id", "focus_handoffs.created_at", terms, limit)
	default:
		return nil, fmt.Errorf("invalid recall source kind %d", kind)
	}
}

// selectRecallCandidates applies common time, cursor and lexical predicates.
// SQL identifiers are supplied only by the fixed source plans below.
func selectRecallCandidates(db *gorm.DB, r RecallRequest, cur recallCursor, kind model.MemorySourceKind, idColumn, occurredColumn string, terms []string, limit int) ([]recallCandidate, error) {
	if r.SourceID > 0 {
		db = db.Where(idColumn+" = ?", r.SourceID)
	}
	if !r.FromTime.IsZero() {
		db = db.Where(occurredColumn+" >= ?", r.FromTime.UTC().Local())
	}
	if !r.ToTime.IsZero() {
		db = db.Where(occurredColumn+" < ?", r.ToTime.UTC().Local())
	}
	db = db.Where(occurredColumn+" <= ?", cur.Upper.Local())
	if !cur.Time.IsZero() {
		switch {
		case kind < cur.Kind:
			db = db.Where(occurredColumn+" <= ?", cur.Time.Local())
		case kind > cur.Kind:
			db = db.Where(occurredColumn+" < ?", cur.Time.Local())
		default:
			db = db.Where("("+occurredColumn+" < ? OR ("+occurredColumn+" = ? AND "+idColumn+" < ?))", cur.Time.Local(), cur.Time.Local(), cur.ID)
		}
	}
	if len(terms) > 0 {
		// The provisional index requires every query term to occur somewhere in
		// the same source. This is only a lexical candidate filter: it does not
		// verify phrase order, rank relevance, or recover paraphrased wording.
		indexQuery := database.DB.Table("memory_terms").Select("source_id").
			Where("source_kind = ? AND term IN ?", kind, terms)
		if kind != model.MemorySourceEvent {
			indexQuery = indexQuery.Where("owner_person_id = ?", r.PersonID)
		}
		indexQuery = indexQuery.Group("source_id").Having("COUNT(DISTINCT term) = ?", len(terms))
		db = db.Where(idColumn+" IN (?)", indexQuery)
	}
	var rows []recallCandidate
	err := db.Select(idColumn + " AS source_id, " + occurredColumn + " AS occurred_at").
		Order(occurredColumn + " DESC").Order(idColumn + " DESC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].SourceKind = kind
	}
	return rows, nil
}

// eventRecallBranch defines one event source's ownership and action-effect
// path so candidate queries can filter inaccessible rows in SQL.
type eventRecallBranch struct {
	// eventType is zero only for self-held Events, which share one branch.
	eventType model.EventType
	// table and ownerField define the referenced source's permission path.
	table      string
	ownerField string
	// effectType links an Event to its producing Action when one is recorded.
	effectType model.ActionEffectType
	// selfHeld selects Events with an embedded snapshot rather than RefID.
	selfHeld bool
}

var eventRecallBranches = []eventRecallBranch{
	{selfHeld: true, effectType: model.ActionEffectSelfHeldEvent},
	{eventType: model.EventTypeMessage, table: "messages", effectType: model.ActionEffectMessage},
	{eventType: model.EventTypeBiography, table: "agent_biographies", ownerField: "person_id"},
	{eventType: model.EventTypeJinshu, table: "jinshus", ownerField: "to_person_id", effectType: model.ActionEffectJinshu},
	{eventType: model.EventTypeJinshuSent, table: "jinshus", ownerField: "from_person_id", effectType: model.ActionEffectJinshu},
	{eventType: model.EventTypeWorkCompleted, table: "works", ownerField: "person_id", effectType: model.ActionEffectWork},
	{eventType: model.EventTypePSDigest, table: "ps_digests", ownerField: "person_id"},
	{eventType: model.EventTypeScheduled, table: "scheduled_events", ownerField: "person_id", effectType: model.ActionEffectScheduledEvent},
}

// recallEventCandidates runs only the relevant event-type branches. Each
// branch has one authorization rule and one chronological column, so the
// message join and CASE ordering do not burden unrelated events.
func recallEventCandidates(r RecallRequest, cur recallCursor, terms []string, limit int) ([]recallCandidate, error) {
	var anchored *model.Event
	if r.SourceID > 0 {
		var event model.Event
		err := database.DB.First(&event, r.SourceID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		anchored = &event
	}
	if r.ActionID > 0 {
		var count int64
		err := database.DB.Table("actions").Joins("JOIN decisions ON decisions.id = actions.decision_id").
			Where("actions.id = ? AND decisions.person_id = ?", r.ActionID, r.PersonID).Count(&count).Error
		if err != nil || count == 0 {
			return nil, err
		}
	}
	var found []recallCandidate
	for _, branch := range eventRecallBranches {
		if r.Scope == RecallMessages && branch.eventType != model.EventTypeMessage {
			continue
		}
		if anchored != nil && ((anchored.RefID == 0) != branch.selfHeld || (!branch.selfHeld && anchored.EventType != branch.eventType)) {
			continue
		}
		if r.ActionID > 0 && branch.effectType == 0 {
			continue
		}
		var db *gorm.DB
		occurred := "events.created_at"
		if branch.eventType == model.EventTypeMessage {
			occurred = "messages.created_at"
			db = database.DB.Table("messages").Joins("JOIN events ON events.event_type = ? AND events.ref_id = messages.id", branch.eventType).
				Where("EXISTS (SELECT 1 FROM participant_sessions ps WHERE ps.session_id = messages.session_id AND ps.participant_id = ?)", r.PersonID)
			if r.SessionID > 0 {
				db = db.Where("messages.session_id = ?", r.SessionID)
			}
			if r.MessageID > 0 {
				db = db.Where("messages.id = ?", r.MessageID)
			}
		} else if branch.selfHeld {
			db = database.DB.Table("events").Where("events.ref_id = 0")
		} else {
			db = database.DB.Table("events").Joins("JOIN "+branch.table+" ON "+branch.table+".id = events.ref_id").
				Where("events.event_type = ? AND "+branch.table+"."+branch.ownerField+" = ?", branch.eventType, r.PersonID)
		}
		db = db.Joins("JOIN agent_observations obs ON obs.event_id = events.id AND obs.person_id = ?", r.PersonID)
		if r.ActionID > 0 {
			effectID := "events.ref_id"
			if branch.selfHeld {
				effectID = "events.id"
			}
			db = db.Where("EXISTS (SELECT 1 FROM action_effects ae WHERE ae.action_id = ? AND ae.effect_type = ? AND ae.effect_id = "+effectID+")", r.ActionID, branch.effectType)
		}
		rows, err := selectRecallCandidates(db, r, cur, model.MemorySourceEvent, "events.id", occurred, terms, limit)
		if err != nil {
			return nil, err
		}
		found = append(found, rows...)
	}
	return found, nil
}

// recallWorkActionCandidates separates Work creation effects from Route/Cancel
// plans. Their union is deduplicated after global chronological ordering.
func recallWorkActionCandidates(r RecallRequest, cur recallCursor, terms []string, limit int) ([]recallCandidate, error) {
	origins := database.DB.Table("action_effects ae").
		Joins("JOIN actions ON actions.id = ae.action_id").
		Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where("ae.effect_type = ? AND ae.effect_id = ? AND decisions.person_id = ?", model.ActionEffectWork, r.WorkID, r.PersonID).
		Group("actions.id")
	if r.DecisionID > 0 {
		origins = origins.Where("actions.decision_id = ?", r.DecisionID)
	}
	first, err := selectRecallCandidates(origins, r, cur, model.MemorySourceAction, "actions.id", "actions.created_at", terms, limit)
	if err != nil {
		return nil, err
	}
	adjustments := database.DB.Table("actions").Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where(fmt.Sprintf("decisions.person_id = ? AND actions.type IN (%d, %d) AND CASE WHEN json_valid(actions.plan_json) THEN CAST(json_extract(actions.plan_json, '$.target_work_id') AS INTEGER) ELSE 0 END = ?", model.ActionTypeRouteFocusedWork, model.ActionTypeCancelFocusedWork), r.PersonID, r.WorkID)
	if r.DecisionID > 0 {
		adjustments = adjustments.Where("actions.decision_id = ?", r.DecisionID)
	}
	second, err := selectRecallCandidates(adjustments, r, cur, model.MemorySourceAction, "actions.id", "actions.created_at", terms, limit)
	if err != nil {
		return nil, err
	}
	return append(first, second...), nil
}
