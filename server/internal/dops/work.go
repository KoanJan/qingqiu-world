package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ListSessionActivityWorks finds Works caused by a message in the Session, or
// by a Work explicitly recorded as originating from that Session.
// Descendants must stay with the same Person: a message sent to someone else
// can cause their own Decision, but that Work is not this Person's activity.
// Workspace reuse is deliberately absent because it records resource use,
// not the cause of a new Work.
func ListSessionActivityWorks(sessionID int64) ([]model.Work, error) {
	var works []model.Work
	// The effect-to-Event branches mirror model.Event.EffectTarget. Self-held
	// Events use their own ID; referenced Events use the source row's ID.
	query := fmt.Sprintf(`
WITH RECURSIVE related_decisions(id, person_id) AS (
    SELECT d.id, d.person_id
    FROM decisions d
    JOIN events e ON e.id = d.event_id AND e.event_type = %d
    JOIN messages m ON m.id = e.ref_id AND m.session_id = ?
    UNION
    SELECT d.id, d.person_id
    FROM works origin
    JOIN action_effects source ON source.effect_type = %d AND source.effect_id = origin.id
    JOIN actions a ON a.id = source.action_id
    JOIN decisions d ON d.id = a.decision_id AND d.person_id = origin.person_id
    WHERE origin.session_id = ?
    UNION
    SELECT child.id, child.person_id
    FROM related_decisions parent
    JOIN actions a ON a.decision_id = parent.id
    JOIN action_effects effect ON effect.action_id = a.id
    JOIN events e ON
        (effect.effect_type = %d AND e.id = effect.effect_id AND e.ref_id = 0)
        OR (effect.effect_type = %d AND e.event_type = %d AND e.ref_id = effect.effect_id)
        OR (effect.effect_type = %d AND e.event_type = %d AND e.ref_id = effect.effect_id)
        OR (effect.effect_type = %d AND e.event_type = %d AND e.ref_id = effect.effect_id)
        OR (effect.effect_type = %d AND e.event_type IN (%d, %d) AND e.ref_id = effect.effect_id)
    JOIN decisions child ON child.event_id = e.id AND child.person_id = parent.person_id
)
SELECT DISTINCT w.id, w.person_id
FROM related_decisions parent
JOIN actions a ON a.decision_id = parent.id
JOIN action_effects effect ON effect.action_id = a.id AND effect.effect_type = %d
JOIN works w ON w.id = effect.effect_id AND w.person_id = parent.person_id
UNION
SELECT id, person_id FROM works WHERE session_id = ?`,
		model.EventTypeMessage,
		model.ActionEffectWork,
		model.ActionEffectSelfHeldEvent,
		model.ActionEffectWork, model.EventTypeWorkCompleted,
		model.ActionEffectMessage, model.EventTypeMessage,
		model.ActionEffectScheduledEvent, model.EventTypeScheduled,
		model.ActionEffectJinshu, model.EventTypeJinshu, model.EventTypeJinshuSent,
		model.ActionEffectWork,
	)
	if err := database.DB.Raw(query, sessionID, sessionID, sessionID).Scan(&works).Error; err != nil {
		return nil, err
	}
	return works, nil
}
