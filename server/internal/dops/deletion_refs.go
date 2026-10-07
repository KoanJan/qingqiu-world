package dops

import (
	"fmt"

	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// deleteSourceEventsTx removes occurrences whose authoritative source is about
// to be deleted. Their observations, vectors, index terms and decisions cannot
// remain meaningful after the source disappears.
func deleteSourceEventsTx(tx *gorm.DB, eventType model.EventType, table, condition string, args ...any) error {
	sourceIDs := tx.Table(table).Select("id").Where(condition, args...)
	var eventIDs []int64
	if err := tx.Model(&model.Event{}).Where("event_type = ? AND ref_id IN (?)", eventType, sourceIDs).Pluck("id", &eventIDs).Error; err != nil {
		return fmt.Errorf("find %s events: %w", table, err)
	}
	for start := 0; start < len(eventIDs); start += 300 {
		end := min(start+300, len(eventIDs))
		ids := eventIDs[start:end]
		if err := deleteDecisionsTx(tx, "event_id IN ?", ids); err != nil {
			return err
		}
		for _, spec := range []struct {
			table any
			where string
			args  []any
		}{
			{&model.AgentObservation{}, "event_id IN ?", []any{ids}},
			{&model.EventVector{}, "event_id IN ?", []any{ids}},
			{&model.AgentEventBuffer{}, "event_id IN ?", []any{ids}},
			{&model.MemoryTerm{}, "source_kind = ? AND source_id IN ?", []any{model.MemorySourceEvent, ids}},
			{&model.ActionEffect{}, "effect_type = ? AND effect_id IN ?", []any{model.ActionEffectSelfHeldEvent, ids}},
		} {
			if err := tx.Where(spec.where, spec.args...).Delete(spec.table).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id IN ?", ids).Delete(&model.Event{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// deleteDecisionsTx removes accepted choices and their private action records
// before their owner or triggering occurrence disappears.
func deleteDecisionsTx(tx *gorm.DB, condition string, args ...any) error {
	var decisionIDs []int64
	if err := tx.Model(&model.Decision{}).Where(condition, args...).Pluck("id", &decisionIDs).Error; err != nil {
		return err
	}
	for start := 0; start < len(decisionIDs); start += 300 {
		end := min(start+300, len(decisionIDs))
		ids := decisionIDs[start:end]
		actions := tx.Model(&model.Action{}).Select("id").Where("decision_id IN ?", ids)
		if err := tx.Where("action_id IN (?)", actions).Delete(&model.ActionEffect{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_kind = ? AND source_id IN (?)", model.MemorySourceAction, actions).Delete(&model.MemoryTerm{}).Error; err != nil {
			return err
		}
		if err := tx.Where("decision_id IN ?", ids).Delete(&model.Action{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", ids).Delete(&model.Decision{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// deleteSessionReferencesTx cleans occurrence and derived-index references
// while the session's messages, works and handoffs still exist.
func deleteSessionReferencesTx(tx *gorm.DB, sessionIDs []int64) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	for _, source := range []struct {
		eventType model.EventType
		table     string
	}{
		{model.EventTypeMessage, "messages"},
		{model.EventTypeWorkCompleted, "works"},
		{model.EventTypeScheduled, "scheduled_events"},
	} {
		if err := deleteSourceEventsTx(tx, source.eventType, source.table, "session_id IN ?", sessionIDs); err != nil {
			return err
		}
	}
	for _, source := range []struct {
		kind       model.MemorySourceKind
		table      string
		effectType model.ActionEffectType
	}{
		{model.MemorySourceWork, "works", model.ActionEffectWork},
		{model.MemorySourceFocusHandoff, "focus_handoffs", 0},
	} {
		ids := tx.Table(source.table).Select("id").Where("session_id IN ?", sessionIDs)
		if err := tx.Where("source_kind = ? AND source_id IN (?)", source.kind, ids).Delete(&model.MemoryTerm{}).Error; err != nil {
			return err
		}
		if source.effectType != 0 {
			if err := tx.Where("effect_type = ? AND effect_id IN (?)", source.effectType, ids).Delete(&model.ActionEffect{}).Error; err != nil {
				return err
			}
		}
	}
	for _, source := range []struct {
		table      string
		effectType model.ActionEffectType
	}{
		{"messages", model.ActionEffectMessage},
		{"scheduled_events", model.ActionEffectScheduledEvent},
	} {
		ids := tx.Table(source.table).Select("id").Where("session_id IN ?", sessionIDs)
		if err := tx.Where("effect_type = ? AND effect_id IN (?)", source.effectType, ids).Delete(&model.ActionEffect{}).Error; err != nil {
			return err
		}
	}
	return nil
}
