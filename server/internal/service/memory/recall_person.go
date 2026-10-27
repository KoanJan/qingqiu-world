package memory

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// personHistoryPaths derives candidate identities from explicit two-person
// membership, Jinshu endpoints, and recorded Decision/ActionEffect links.
// No name, time proximity, or vector similarity establishes a relationship.
func personHistoryPaths(selfID, otherID int64) (messages, jinshu, events, actions, works *gorm.DB) {
	messages = database.DB.Table("messages m").Select("m.id").
		Where("EXISTS (SELECT 1 FROM participant_sessions a JOIN participant_sessions b ON b.session_id = a.session_id WHERE a.session_id = m.session_id AND a.participant_id = ? AND b.participant_id = ? AND (SELECT COUNT(*) FROM participant_sessions p WHERE p.session_id = m.session_id) = 2)", selfID, otherID)
	jinshu = database.DB.Table("jinshus j").Select("j.id").Where("(j.from_person_id = ? AND j.to_person_id = ?) OR (j.from_person_id = ? AND j.to_person_id = ?)", selfID, otherID, otherID, selfID)
	events = database.DB.Table("events e").Select("e.id").Where("(e.event_type = ? AND e.ref_id IN (?)) OR (e.event_type IN (?, ?) AND e.ref_id IN (?))", model.EventTypeMessage, messages, model.EventTypeJinshu, model.EventTypeJinshuSent, jinshu)
	// An Action is related only if its accepted Decision was triggered by a
	// related Event, or its recorded effect is an actual message/Jinshu to the
	// counterpart. Its intent alone does not prove a delivery.
	actions = database.DB.Table("actions a").Select("a.id").Joins("JOIN decisions d ON d.id = a.decision_id").
		Where("d.person_id = ? AND (d.event_id IN (?) OR a.id IN (SELECT ae.action_id FROM action_effects ae WHERE (ae.effect_type = ? AND ae.effect_id IN (?)) OR (ae.effect_type = ? AND ae.effect_id IN (?))))", selfID, events, model.ActionEffectMessage, messages, model.ActionEffectJinshu, jinshu)
	works = database.DB.Table("action_effects ae").Select("ae.effect_id").Where("ae.effect_type = ? AND ae.action_id IN (?)", model.ActionEffectWork, actions)
	return
}

// recallPersonCandidates applies the person's own observation and ownership
// filters before source limits. readRecallItem checks them again afterward.
func recallPersonCandidates(r RecallRequest, cur recallCursor, kind model.MemorySourceKind, terms []string, limit int) ([]recallCandidate, error) {
	_, _, relatedEvents, relatedActions, relatedWorks := personHistoryPaths(r.PersonID, r.CounterpartID)
	switch kind {
	case model.MemorySourceEvent:
		// Include the observed dyad exchange and observed completion events
		// for a Work explicitly started along that exchange's causal path.
		db := database.DB.Table("events").Joins("JOIN agent_observations obs ON obs.event_id = events.id AND obs.person_id = ?", r.PersonID).
			Where("events.id IN (?) OR (events.event_type = ? AND events.ref_id IN (?))", relatedEvents, model.EventTypeWorkCompleted, relatedWorks)
		return selectRecallCandidates(db, r, cur, kind, "events.id", "events.created_at", terms, limit)
	case model.MemorySourceAction:
		db := database.DB.Table("actions").Joins("JOIN decisions ON decisions.id = actions.decision_id").Where("decisions.person_id = ? AND actions.id IN (?)", r.PersonID, relatedActions)
		return selectRecallCandidates(db, r, cur, kind, "actions.id", "actions.created_at", terms, limit)
	case model.MemorySourceWork:
		db := database.DB.Table("works").Where("works.person_id = ? AND works.id IN (?)", r.PersonID, relatedWorks)
		return selectRecallCandidates(db, r, cur, kind, "works.id", "works.created_at", terms, limit)
	case model.MemorySourceFocusHandoff:
		db := database.DB.Table("focus_handoffs").Where("focus_handoffs.person_id = ? AND focus_handoffs.work_id IN (?)", r.PersonID, relatedWorks)
		return selectRecallCandidates(db, r, cur, kind, "focus_handoffs.id", "focus_handoffs.created_at", terms, limit)
	default:
		return nil, fmt.Errorf("invalid person-history source kind %d", kind)
	}
}
