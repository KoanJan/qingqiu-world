package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/world"
)

// dailyGroupLoadBatchSize bounds one lookup while reconstructing a day's
// related observations, decisions, and actions.
const dailyGroupLoadBatchSize = 400

// dailyGroupKey identifies one resource within a contact day. The type defines
// the ID namespace; day-level singleton resources use zero.
type dailyGroupKey struct {
	Type model.DailyMemoryGroupType
	ID   int64
}

// dailyDecision preserves one accepted choice and all actions selected by it.
type dailyDecision struct {
	Record  model.Decision
	Actions []model.Action
}

// dailyMessageOrigin identifies the choice that sent one observed message.
// It lets a conversation group explain a message sent on another group's
// initiative without repeating that choice as a separate transcript entry.
type dailyMessageOrigin struct {
	DecisionID int64
	Reason     string
}

// dailyMotive recovers the upstream reason for a result that landed on the
// remembered day but was set in motion by a choice on an earlier day.
type dailyMotive struct {
	DecisionID int64
	Reason     string
	Background string
}

// dailyGroup keeps the original causal hierarchy while one day is recomputed.
type dailyGroup struct {
	Key          dailyGroupKey
	Observations []dailySource
	Decisions    []*dailyDecision
	Effects      map[int64][]model.ActionEffect
	Sources      []dailySource
	StartAt      time.Time
	EndAt        time.Time
	// DayStart and DayEnd bound the remembered day. An outcome outside this
	// window belongs to a later day and must not enter this account.
	DayStart time.Time
	DayEnd   time.Time
	// MessageIDs are the messages already shown as observations in this group.
	// Their producing Action must not repeat them as a result line.
	MessageIDs map[int64]bool
	// MessageByEvent maps an observed Message Event to its Message ID.
	MessageByEvent map[int64]int64
	// MessageOrigins maps a Message ID to the choice that sent it.
	MessageOrigins map[int64]dailyMessageOrigin
	// Motives maps an observed Event ID to the earlier-day choice that caused it.
	Motives map[int64]dailyMotive
}

// withinDay reports whether an outcome happened inside the remembered day.
func (g *dailyGroup) withinDay(at time.Time) bool {
	return !at.Before(g.DayStart) && at.Before(g.DayEnd)
}

// dailyGroupTypeForEvent maps a primary resource Event to its contact-day
// group. Action feedback and runtime-only notices return false; their content
// is rendered as part of the producing Action instead of a standalone resource.
func dailyGroupTypeForEvent(event model.Event) (model.DailyMemoryGroupType, bool) {
	switch event.EventType {
	case model.EventTypeMessage:
		return model.DailyMemoryGroupChat, true
	case model.EventTypeJinshu:
		return model.DailyMemoryGroupJinshu, true
	case model.EventTypeWorkCompleted:
		return model.DailyMemoryGroupWork, true
	case model.EventTypeScheduled:
		return model.DailyMemoryGroupReminder, true
	case model.EventTypeSystemNotification:
		return model.DailyMemoryGroupSystemNotice, true
	case model.EventTypeBiography:
		return model.DailyMemoryGroupBiography, true
	case model.EventTypePSDigest:
		return model.DailyMemoryGroupPSDigest, true
	default:
		return 0, false
	}
}

// dailyIsActionFeedback reports an Event whose content belongs to the Action
// that produced it. Such an Event must never become an independent source.
// ExecutionSlotAvailable is included so its notice never becomes a group of
// its own; the Decision it triggers still follows the Action that was waiting.
func dailyIsActionFeedback(eventType model.EventType) bool {
	switch eventType {
	case model.EventTypeJinshuSent, model.EventTypeJinshuListed,
		model.EventTypeJinshuSentListed, model.EventTypeOwnedSpaceInspected,
		model.EventTypeExecutionSlotAvailable:
		return true
	default:
		return false
	}
}

// dailyGroupKeyForEvent resolves the resource identity of one observed Event.
// The second result is false for feedback and runtime-only Events.
func dailyGroupKeyForEvent(event model.Event, messages map[int64]model.Message) (dailyGroupKey, bool, error) {
	groupType, ok := dailyGroupTypeForEvent(event)
	if !ok {
		return dailyGroupKey{}, false, nil
	}
	key := dailyGroupKey{Type: groupType}
	switch groupType {
	case model.DailyMemoryGroupChat:
		message, exists := messages[event.RefID]
		if !exists {
			return dailyGroupKey{}, false, fmt.Errorf("message Event %d has no Message %d", event.ID, event.RefID)
		}
		key.ID = message.SessionID
	case model.DailyMemoryGroupWork, model.DailyMemoryGroupPSDigest:
		key.ID = event.RefID
	}
	if !validDailyGroupKey(key) {
		return dailyGroupKey{}, false, fmt.Errorf("invalid daily group type %d id %d for Event %d", key.Type, key.ID, event.ID)
	}
	return key, true, nil
}

// validDailyGroupKey enforces the zero-ID convention: only day-level
// singletons may use zero, while resource identities must be positive.
func validDailyGroupKey(key dailyGroupKey) bool {
	switch key.Type {
	case model.DailyMemoryGroupHeartbeat, model.DailyMemoryGroupJinshu,
		model.DailyMemoryGroupReminder, model.DailyMemoryGroupSystemNotice,
		model.DailyMemoryGroupBiography:
		return key.ID == 0
	case model.DailyMemoryGroupChat, model.DailyMemoryGroupWork, model.DailyMemoryGroupPSDigest:
		return key.ID > 0
	default:
		return false
	}
}

// dailyFeedbackOrigin names the choice whose Action produced one feedback
// Event. EventID is that choice's own trigger, which still identifies the
// resource when the choice belongs to an earlier day.
type dailyFeedbackOrigin struct {
	DecisionID int64
	EventID    int64
}

// dailyFeedbackSource resolves an excluded observation to the Action that
// produced it. JinshuSent points at the Jinshu effect, while list and
// inspection results point at their self-held Event effect.
func dailyFeedbackSource(ctx context.Context, personID int64, excluded []model.Event) (map[int64]dailyFeedbackOrigin, error) {
	byType := make(map[model.ActionEffectType]map[int64]int64)
	for _, event := range excluded {
		if !dailyIsActionFeedback(event.EventType) {
			continue // Unclassified event types are reported by the caller.
		}
		effectType, effectID := event.EffectTarget()
		if effectType == 0 || effectID == 0 {
			applogger.Warn("daily feedback event has no action target", "event_id", event.ID, "event_type", event.EventType)
			continue
		}
		if byType[effectType] == nil {
			byType[effectType] = make(map[int64]int64)
		}
		byType[effectType][effectID] = event.ID
	}
	result := make(map[int64]dailyFeedbackOrigin, len(excluded))
	for effectType, byEffect := range byType {
		ids := make([]int64, 0, len(byEffect))
		for id := range byEffect {
			ids = append(ids, id)
		}
		var rows []struct {
			EffectID   int64
			DecisionID int64
			EventID    int64
		}
		if err := database.DB.WithContext(ctx).
			Table("action_effects").
			Select("action_effects.effect_id AS effect_id, actions.decision_id AS decision_id, decisions.event_id AS event_id").
			Joins("JOIN actions ON actions.id = action_effects.action_id").
			Joins("JOIN decisions ON decisions.id = actions.decision_id").
			Where("decisions.person_id = ? AND action_effects.effect_type = ? AND action_effects.effect_id IN ?", personID, effectType, ids).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if eventID, ok := byEffect[row.EffectID]; ok {
				result[eventID] = dailyFeedbackOrigin{DecisionID: row.DecisionID, EventID: row.EventID}
			}
		}
	}
	return result, nil
}

// dailyLoadEvents reads Event rows and their Message rows into the caches,
// skipping identities already loaded.
func dailyLoadEvents(ctx context.Context, ids []int64, events map[int64]model.Event, messages map[int64]model.Message) error {
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := events[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var rows []model.Event
	if err := database.DB.WithContext(ctx).Where("id IN ?", missing).Find(&rows).Error; err != nil {
		return err
	}
	messageIDs := make([]int64, 0)
	for _, event := range rows {
		events[event.ID] = event
		if event.EventType != model.EventTypeMessage {
			continue
		}
		if _, ok := messages[event.RefID]; !ok {
			messageIDs = append(messageIDs, event.RefID)
		}
	}
	if len(messageIDs) == 0 {
		return nil
	}
	var messageRows []model.Message
	if err := database.DB.WithContext(ctx).Where("id IN ?", messageIDs).Find(&messageRows).Error; err != nil {
		return err
	}
	for _, message := range messageRows {
		messages[message.ID] = message
	}
	return nil
}

// dailyResolveDecisionEventKey finds the resource named by a Decision's trigger.
// The trigger Event may predate the remembered day, so it is loaded on demand.
func dailyResolveDecisionEventKey(ctx context.Context, eventID int64, events map[int64]model.Event, messages map[int64]model.Message) (dailyGroupKey, model.Event, bool, error) {
	if eventID == 0 {
		return dailyGroupKey{Type: model.DailyMemoryGroupHeartbeat}, model.Event{}, true, nil
	}
	if err := dailyLoadEvents(ctx, []int64{eventID}, events, messages); err != nil {
		return dailyGroupKey{}, model.Event{}, false, err
	}
	event := events[eventID]
	key, forms, err := dailyGroupKeyForEvent(event, messages)
	if err != nil {
		return dailyGroupKey{}, model.Event{}, false, err
	}
	return key, event, forms, nil
}

// dailyGroupsForDate groups one local day by resource. Observations enter the
// day they reached the agent, while a Decision enters the day it was made.
// Heartbeat decisions share one day-level group. Feedback observations never
// form groups; their decisions follow the group of the Action that produced the
// result. A result may also explain an earlier choice's reason, never the reverse.
func dailyGroupsForDate(ctx context.Context, personID int64, date string) ([]*dailyGroup, error) {
	start, err := time.ParseInLocation("2006-01-02", date, energy.Timezone())
	if err != nil {
		return nil, err
	}
	end := start.AddDate(0, 0, 1)
	var observations []model.AgentObservation
	if err := database.DB.WithContext(ctx).Where("person_id = ? AND created_at >= ? AND created_at < ?", personID, start, end).
		Order("created_at, id").Find(&observations).Error; err != nil {
		return nil, err
	}
	events := make(map[int64]model.Event, len(observations))
	messages := make(map[int64]model.Message)
	for from := 0; from < len(observations); from += dailyGroupLoadBatchSize {
		to := from + dailyGroupLoadBatchSize
		if to > len(observations) {
			to = len(observations)
		}
		ids := make([]int64, 0, to-from)
		for _, observation := range observations[from:to] {
			ids = append(ids, observation.EventID)
		}
		if err := dailyLoadEvents(ctx, ids, events, messages); err != nil {
			return nil, err
		}
	}
	groups := make(map[dailyGroupKey]*dailyGroup)
	byEvent := make(map[int64]*dailyGroup, len(observations))
	newGroup := func(key dailyGroupKey) *dailyGroup {
		group := &dailyGroup{Key: key, DayStart: start, DayEnd: end}
		groups[key] = group
		return group
	}
	for _, observation := range observations {
		event, ok := events[observation.EventID]
		if !ok {
			return nil, fmt.Errorf("observation %d has no Event %d", observation.ID, observation.EventID)
		}
		key, forms, err := dailyGroupKeyForEvent(event, messages)
		if err != nil {
			return nil, err
		}
		if !forms {
			if !dailyIsActionFeedback(event.EventType) {
				applogger.Warn("daily memory skipped an unclassified event type", "event_id", event.ID, "event_type", event.EventType)
			}
			continue
		}
		group := groups[key]
		if group == nil {
			group = newGroup(key)
		}
		source := dailySource{ID: observation.ID, RefID: event.ID, Kind: dailySourceObservation, At: observation.CreatedAt}
		group.Observations = append(group.Observations, source)
		group.Sources = append(group.Sources, source)
		group.includeTime(source.At)
		if event.EventType == model.EventTypeMessage {
			message := messages[event.RefID]
			if group.MessageIDs == nil {
				group.MessageIDs = make(map[int64]bool)
			}
			if group.MessageByEvent == nil {
				group.MessageByEvent = make(map[int64]int64)
			}
			group.MessageIDs[message.ID] = true
			group.MessageByEvent[event.ID] = message.ID
		}
		byEvent[event.ID] = group
	}
	// A Decision owns its Actions, so the Decision's own local date decides
	// which day's account it enters. Its triggering Event may have been observed
	// on an earlier day and stays in that earlier day's account.
	var decisions []model.Decision
	if err := database.DB.WithContext(ctx).Where("person_id = ? AND created_at >= ? AND created_at < ?", personID, start, end).
		Order("created_at, id").Find(&decisions).Error; err != nil {
		return nil, err
	}
	decisionGroups := make(map[int64]*dailyGroup, len(decisions))
	sameDayDecision := make(map[int64]bool, len(decisions))
	droppedDecision := make(map[int64]bool)
	var pending []model.Decision
	var pendingEvents []model.Event
	for _, decision := range decisions {
		sameDayDecision[decision.ID] = true
		key, event, forms, err := dailyResolveDecisionEventKey(ctx, decision.EventID, events, messages)
		if err != nil {
			return nil, err
		}
		if !forms {
			switch {
			case dailyIsActionFeedback(event.EventType):
				pending = append(pending, decision)
				pendingEvents = append(pendingEvents, event)
			default:
				applogger.Warn("daily memory skipped a decision with an unclassified trigger", "person_id", personID, "date", date, "decision_id", decision.ID, "event_id", decision.EventID, "event_type", event.EventType)
				droppedDecision[decision.ID] = true
			}
			continue
		}
		group := groups[key]
		if group == nil {
			group = newGroup(key)
		}
		group.Decisions = append(group.Decisions, &dailyDecision{Record: decision})
		group.includeTime(decision.CreatedAt)
		decisionGroups[decision.ID] = group
	}
	// Feedback observations do not form resources. Their decisions belong to
	// the group of the Action that produced the feedback, so a list result and
	// the choice it prompted stay in one account.
	if len(pending) > 0 {
		// The feedback trigger may itself belong to an earlier day, so resolve
		// the producing Action from the Event rather than from today's observations.
		origins, err := dailyFeedbackSource(ctx, personID, pendingEvents)
		if err != nil {
			return nil, err
		}
		for len(pending) > 0 {
			deferred := make([]model.Decision, 0, len(pending))
			progressed := false
			for _, decision := range pending {
				origin, ok := origins[decision.EventID]
				if !ok {
					applogger.Warn("daily feedback event has no producing action", "person_id", personID, "date", date, "decision_id", decision.ID, "event_id", decision.EventID)
					continue
				}
				if droppedDecision[origin.DecisionID] {
					continue // The producing choice was intentionally dropped too.
				}
				group := decisionGroups[origin.DecisionID]
				if group == nil {
					if sameDayDecision[origin.DecisionID] {
						// The producing choice may itself be waiting for its group.
						deferred = append(deferred, decision)
						continue
					}
					// The producing choice belongs to an earlier, already closed
					// day. Recover only its resource, then place today's follow-up
					// in today's account instead of rewriting that closed day.
					key, _, forms, err := dailyResolveDecisionEventKey(ctx, origin.EventID, events, messages)
					if err != nil {
						return nil, err
					}
					if !forms {
						applogger.Warn("daily feedback source has no resource group", "person_id", personID, "date", date, "decision_id", decision.ID, "source_decision_id", origin.DecisionID)
						continue
					}
					group = groups[key]
					if group == nil {
						group = newGroup(key)
					}
				}
				group.Decisions = append(group.Decisions, &dailyDecision{Record: decision})
				group.includeTime(decision.CreatedAt)
				decisionGroups[decision.ID] = group
				progressed = true
			}
			if !progressed {
				for _, decision := range deferred {
					applogger.Warn("daily feedback decision has no day-level source group", "person_id", personID, "date", date, "decision_id", decision.ID, "event_id", decision.EventID)
				}
				break
			}
			pending = deferred
		}
	}
	allDecisions := make(map[int64]*dailyDecision)
	for _, group := range groups {
		for _, decision := range group.Decisions {
			allDecisions[decision.Record.ID] = decision
		}
	}
	ids := make([]int64, 0, len(allDecisions))
	for id := range allDecisions {
		ids = append(ids, id)
	}
	for from := 0; from < len(ids); from += dailyGroupLoadBatchSize {
		to := from + dailyGroupLoadBatchSize
		if to > len(ids) {
			to = len(ids)
		}
		var actions []model.Action
		if err := database.DB.WithContext(ctx).Where("decision_id IN ?", ids[from:to]).Order("created_at, id").Find(&actions).Error; err != nil {
			return nil, err
		}
		actionIDs := make([]int64, 0, len(actions))
		actionByID := make(map[int64]model.Action, len(actions))
		for _, action := range actions {
			actionIDs = append(actionIDs, action.ID)
			actionByID[action.ID] = action
			decision := allDecisions[action.DecisionID]
			group := decisionGroups[action.DecisionID]
			if decision == nil || group == nil {
				return nil, fmt.Errorf("daily action %d has no selected decision %d", action.ID, action.DecisionID)
			}
			decision.Actions = append(decision.Actions, action)
			group.Sources = append(group.Sources, dailySource{ID: action.ID, RefID: action.ID, Kind: dailySourceAction, At: action.CreatedAt})
			group.includeTime(action.CreatedAt)
		}
		if len(actionIDs) > 0 {
			var effects []model.ActionEffect
			if err := database.DB.WithContext(ctx).Where("action_id IN ?", actionIDs).Order("id").Find(&effects).Error; err != nil {
				return nil, err
			}
			for _, effect := range effects {
				action, ok := actionByID[effect.ActionID]
				if !ok {
					return nil, fmt.Errorf("daily effect %d has no selected action %d", effect.ID, effect.ActionID)
				}
				group := decisionGroups[action.DecisionID]
				if group.Effects == nil {
					group.Effects = make(map[int64][]model.ActionEffect)
				}
				group.Effects[effect.ActionID] = append(group.Effects[effect.ActionID], effect)
			}
		}
	}
	// A result observed today may have been set in motion on an earlier day.
	// Recover only that earlier reason; the earlier choice stays in its own day.
	if err := dailyAttachMotives(ctx, personID, start, byEvent, events); err != nil {
		return nil, err
	}
	// A sent message is both an observation and an effect. Record its origin
	// once so a conversation group can explain messages sent on another
	// group's initiative without repeating the result line.
	origins := make(map[int64]dailyMessageOrigin)
	for _, group := range groups {
		for _, decision := range group.Decisions {
			for _, action := range decision.Actions {
				for _, effect := range group.Effects[action.ID] {
					if effect.EffectType != model.ActionEffectMessage {
						continue
					}
					origins[effect.EffectID] = dailyMessageOrigin{DecisionID: decision.Record.ID, Reason: strings.TrimSpace(action.Reason)}
				}
			}
		}
	}
	result := make([]*dailyGroup, 0, len(groups))
	for _, group := range groups {
		// A decision recorded before its reasoning was persisted has neither
		// Actions nor Thoughts: the row carries no content, so it contributes
		// nothing to the day instead of appearing as an empty choice.
		kept := group.Decisions[:0]
		for _, decision := range group.Decisions {
			if len(decision.Actions) == 0 && strings.TrimSpace(decision.Record.Thoughts) == "" {
				continue
			}
			kept = append(kept, decision)
		}
		group.Decisions = kept
		if len(group.Sources) == 0 && len(group.Decisions) == 0 {
			continue // Observations or a kept decision are the only content a group has.
		}
		// Dropping an empty decision must not keep its clock time in the
		// group's contact range, so recompute it from what remains.
		group.StartAt, group.EndAt = time.Time{}, time.Time{}
		for _, source := range group.Sources {
			group.includeTime(source.At)
		}
		for _, decision := range group.Decisions {
			if len(decision.Actions) == 0 {
				// A choice with no Action still contributed its recorded thoughts
				// to the account, so it must be available during source recall.
				group.Sources = append(group.Sources, dailySource{ID: decision.Record.ID, Kind: dailySourceDecision, At: decision.Record.CreatedAt})
				group.includeTime(decision.Record.CreatedAt)
			}
		}
		group.MessageOrigins = origins
		sort.Slice(group.Decisions, func(i, j int) bool { return group.Decisions[i].Record.ID < group.Decisions[j].Record.ID })
		sort.Slice(group.Sources, func(i, j int) bool { return dailySourceLess(group.Sources[i], group.Sources[j]) })
		result = append(result, group)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].StartAt.Equal(result[j].StartAt) {
			return result[i].StartAt.Before(result[j].StartAt)
		}
		return dailyGroupKeyLess(result[i].Key, result[j].Key)
	})
	return result, nil
}

// dailyAttachMotives explains results that landed on the remembered day but
// were set in motion by a choice on an earlier day. Only the upstream reason is
// recovered: the result belongs to this day, so the earlier choice is never
// re-listed as this day's own decision.
func dailyAttachMotives(ctx context.Context, personID int64, start time.Time, byEvent map[int64]*dailyGroup, events map[int64]model.Event) error {
	byType := make(map[model.ActionEffectType]map[int64]int64)
	for eventID, group := range byEvent {
		if group == nil {
			continue
		}
		effectType, effectID := events[eventID].EffectTarget()
		if effectType == 0 || effectID == 0 {
			continue
		}
		if byType[effectType] == nil {
			byType[effectType] = make(map[int64]int64)
		}
		byType[effectType][effectID] = eventID
	}
	type motiveRow struct {
		EffectID   int64
		DecisionID int64
		Reason     string
		Background string
	}
	for effectType, byEffect := range byType {
		ids := make([]int64, 0, len(byEffect))
		for id := range byEffect {
			ids = append(ids, id)
		}
		var rows []motiveRow
		// Ownership is checked on the Decision, so another person's Action can
		// never explain this person's result.
		if err := database.DB.WithContext(ctx).
			Table("action_effects").
			Select("action_effects.effect_id AS effect_id, decisions.id AS decision_id, actions.reason AS reason, actions.background AS background").
			Joins("JOIN actions ON actions.id = action_effects.action_id").
			Joins("JOIN decisions ON decisions.id = actions.decision_id").
			Where("decisions.person_id = ? AND decisions.created_at < ? AND action_effects.effect_type = ? AND action_effects.effect_id IN ?", personID, start, effectType, ids).
			Order("action_effects.id").
			Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			eventID, ok := byEffect[row.EffectID]
			if !ok {
				continue
			}
			group := byEvent[eventID]
			if group == nil {
				continue
			}
			if _, exists := group.Motives[eventID]; exists {
				continue
			}
			if group.Motives == nil {
				group.Motives = make(map[int64]dailyMotive)
			}
			group.Motives[eventID] = dailyMotive{
				DecisionID: row.DecisionID,
				Reason:     strings.TrimSpace(row.Reason),
				Background: strings.TrimSpace(row.Background),
			}
		}
	}
	return nil
}

// includeTime expands the group's actual contact-time range.
func (g *dailyGroup) includeTime(at time.Time) {
	if g.StartAt.IsZero() || at.Before(g.StartAt) {
		g.StartAt = at
	}
	if at.After(g.EndAt) {
		g.EndAt = at
	}
}

// dailySourceLess orders observations, actions, and actionless decisions within a group.
func dailySourceLess(a, b dailySource) bool {
	if !a.At.Equal(b.At) {
		return a.At.Before(b.At)
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.ID < b.ID
}

// dailyGroupKeyLess resolves equal-time group order deterministically.
func dailyGroupKeyLess(a, b dailyGroupKey) bool {
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	return a.ID < b.ID
}

// dailySummaryPrompt applies the shared world grounding, resource meanings, and
// evidence boundaries used for one dated semantic navigation note.
const dailySummaryPrompt = world.ChatWorldDescription + world.ResourceDescriptions + `

You are %s.

Below are records from one part of a past day: what reached you, what you chose, and what followed. Write a short first-person recollection of what these records establish, speaking from afterward. Other parts of that day may be absent; do not imply this is the whole day. The input is a record, not prose: do not quote or imitate its labels, order, or layout, and do not open by restating the date. Use the language of the input.

What the evidence establishes:
- Encountering and acting differ. The named speaker said the quoted words; your own name marks your speech. Only words shown as actually sent may be remembered as your speech.
- Your reading, reasons, and plans were yours at the time. They show what you believed or intended then and may have been mistaken.
- A choice does not show what happened. If no sent message or later result is shown, leave the outcome open.
- A result that occurred after the date belongs later in the story; do not imply you already knew it that day.
- A result on this day may follow a choice you made on an earlier day. Such an earlier reason explains what set the result in motion; it is not a choice of this day.
- Numbers that identify records are storage details. Never include an internal record number, such as a session, jinshu, work, alarm, action, memo, event, or person identifier, or the name of such a field.

How to write it:
- Recollect, do not report. Never describe yourself as processing, recording, reviewing, or deciding at a clock time, and never refer to evidence, a quiet period, a prompt, a trigger, or a system.
- You have no body or biological rhythms. Never describe yourself falling asleep, waking, staying up late, or feeling tired, hungry, or physically uncomfortable.
- Recount this part of the day as continuous prose in your own voice. Compress repeated exchanges and repeated choices; if a pattern recurred, say so once instead of retelling every instance.
- Mention a time only when it helps order events, and never to the second.
- Preserve exact wording only when quoting someone. Words such as "now" and "tomorrow" inside quoted speech or past thoughts belong to their original moment.
- Do not invent a circumstance, motive, connection, outcome, or fact that the evidence does not establish. If little happened, write little.

Return only the account.`

// summarizeDailyGroup gives the model the observation, each resulting
// decision, its recorded considerations, and all selected actions together.
func summarizeDailyGroup(ctx context.Context, personID int64, date string, group *dailyGroup) (string, error) {
	a, err := agent.GetAgent(personID)
	if err != nil {
		return "", err
	}
	if !validDailyGroupKey(group.Key) {
		return "", fmt.Errorf("invalid daily group type %d id %d", group.Key.Type, group.Key.ID)
	}
	var text strings.Builder
	printed := make(map[int64]bool, len(group.Decisions))
	printedMotives := make(map[int64]bool)
	groupDecisionIDs := make(map[int64]bool, len(group.Decisions))
	for _, decision := range group.Decisions {
		groupDecisionIDs[decision.Record.ID] = true
	}
	for _, observed := range group.Observations {
		line, err := dailyObservedEvent(ctx, personID, a.Name, observed.At, observed.RefID)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&text, "- %s\n", line)
		// A result may follow a choice made on an earlier day. Only its reason is
		// recovered here; the earlier choice itself stays in that earlier day.
		if motive, ok := group.Motives[observed.RefID]; ok && !printedMotives[motive.DecisionID] {
			printedMotives[motive.DecisionID] = true
			if motive.Background != "" {
				fmt.Fprintf(&text, "    How you saw things when you set this in motion: %q\n", motive.Background)
			}
			if motive.Reason != "" {
				fmt.Fprintf(&text, "    Why you chose it then: %q\n", motive.Reason)
			}
		}
		for _, decision := range group.Decisions {
			if decision.Record.EventID == observed.RefID {
				if err := formatDailyDecision(ctx, personID, &text, group, decision); err != nil {
					return "", err
				}
				printed[decision.Record.ID] = true
			}
		}
		// A message sent on another group's initiative stays in this
		// conversation, but its motive is recorded with that choice. State the
		// motive here once instead of repeating the choice as a second line.
		if group.Key.Type == model.DailyMemoryGroupChat {
			if messageID, ok := group.MessageByEvent[observed.RefID]; ok {
				if origin, exists := group.MessageOrigins[messageID]; exists && !groupDecisionIDs[origin.DecisionID] && origin.Reason != "" {
					fmt.Fprintf(&text, "    Why you spoke then: %q\n", origin.Reason)
				}
			}
		}
	}
	// Heartbeat choices have no observation of their own, and a choice made
	// after an excluded feedback result follows the group that produced it.
	// Both are rendered after the observations of this group.
	for _, decision := range group.Decisions {
		if printed[decision.Record.ID] {
			continue
		}
		if err := formatDailyDecision(ctx, personID, &text, group, decision); err != nil {
			return "", err
		}
	}
	client := llm.NewChatModelWithTemperature(a.LLM.BaseURL, a.LLM.APIKey, a.LLM.ModelID, llm.TemperatureDeterministic)
	summary, err := client.Chat(ctx, []llm.Message{{Role: "system", Content: fmt.Sprintf(dailySummaryPrompt, a.Name)}, {Role: "user", Content: fmt.Sprintf("Some of what reached you on %s, what you chose, and what followed:\n%s", date, text.String())}})
	if err != nil {
		return "", err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", fmt.Errorf("empty daily summary for person %d day %s group %+v", personID, date, group.Key)
	}
	return summary, nil
}

// formatDailyDecision presents one choice as the evidence a retrospective
// needs: the agent's view then, its reason, the intention recorded in its plan,
// and what actually followed. Plans are rendered as prose so routing parameters
// and record identifiers cannot become part of the remembered account.
func formatDailyDecision(ctx context.Context, personID int64, text *strings.Builder, group *dailyGroup, decision *dailyDecision) error {
	if len(decision.Actions) == 0 {
		// The choice itself is the day's content. A heartbeat that produced no
		// Action still recorded what it was thinking, so keep that here instead
		// of leaving an empty line in the account.
		fmt.Fprintf(text, "  - At about %s, you chose not to act.\n", dailyClock(decision.Record.CreatedAt))
		if thoughts := strings.TrimSpace(decision.Record.Thoughts); thoughts != "" {
			fmt.Fprintf(text, "    What you were thinking then: %q\n", thoughts)
		}
		return nil
	}
	considerations := make(map[string]bool)
	for _, action := range decision.Actions {
		fmt.Fprintf(text, "  - At about %s, you chose to %s.\n", dailyClock(decision.Record.CreatedAt), action.Type.Label())
		background := strings.TrimSpace(action.Background)
		if background != "" && !considerations[background] {
			fmt.Fprintf(text, "    How you saw things then (your belief, not established fact): %q\n", background)
			considerations[background] = true
		}
		if reason := strings.TrimSpace(action.Reason); reason != "" {
			fmt.Fprintf(text, "    Why you chose it: %q\n", reason)
		}
		if intention := dailyPlanIntention(action.Type, action.PlanJSON); intention != "" {
			fmt.Fprintf(text, "    What you intended: %q\n", intention)
		}
		effects := group.Effects[action.ID]
		// A missing message effect leaves execution unresolved here. A Chat
		// selected on this day may still commit its speech on a later day.
		for _, effect := range effects {
			if !group.withinDay(effect.CreatedAt) {
				continue // A later outcome belongs to that later day's account.
			}
			// A message already shown as this group's conversation line must not
			// repeat as a result line.
			if effect.EffectType == model.ActionEffectMessage && group.MessageIDs[effect.EffectID] {
				continue
			}
			result, err := describeDailyActionEffect(ctx, personID, effect)
			if err != nil {
				return err
			}
			if result == "" {
				continue // Runtime-only feedback carries no retained recollection.
			}
			fmt.Fprintf(text, "    What followed: %s\n", result)
		}
	}
	return nil
}

// dailyClock renders a moment from the remembered day without seconds. The
// account is a recollection, not a timestamped log.
func dailyClock(at time.Time) string {
	return at.In(energy.Timezone()).Format("15:04")
}

// dailyPlanIntention returns only the agent's own prose from a stored plan.
// A plan is routing JSON; the remembered account keeps what was intended, never
// the parameters used to route it. Fields are read by name because the plan
// types live in the action runtime, which already depends on memory.
func dailyPlanIntention(actionType model.ActionType, planJSON string) string {
	if strings.TrimSpace(planJSON) == "" {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(planJSON), &fields); err != nil {
		applogger.Error("daily summary: invalid action plan", "action_type", actionType, "error", err)
		return ""
	}
	text := func(key string) string {
		raw, ok := fields[key]
		if !ok {
			return ""
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			applogger.Error("daily summary: non-text plan field", "action_type", actionType, "field", key, "error", err)
			return ""
		}
		return strings.TrimSpace(value)
	}
	switch actionType {
	case model.ActionTypeChat, model.ActionTypeStartFocusedWork, model.ActionTypeRouteFocusedWork, model.ActionTypeCancelFocusedWork:
		return text("guidance")
	case model.ActionTypeUpdateBio:
		return text("bio")
	case model.ActionTypeSendJinshu:
		if description := text("description"); description != "" {
			return text("topic") + " — " + description
		}
		return text("topic")
	case model.ActionTypeWaitForExecutionSlot:
		return text("intention")
	case model.ActionTypeManageMemo:
		return text("content")
	}
	// A created alarm's note is an instruction for the future, and its time is
	// already stated by the recorded effect, so no plan prose is repeated here.
	return ""
}

// dailyHasMessageEffect distinguishes a committed Chat message from any other
// effect and from an Action's plan or ended status.
func dailyHasMessageEffect(effects []model.ActionEffect) bool {
	for _, effect := range effects {
		if effect.EffectType == model.ActionEffectMessage {
			return true
		}
	}
	return false
}

// describeDailyActionEffect states what was recorded after one chosen action.
// It names the result in ordinary language and omits storage identifiers and
// exact clock seconds so a retrospective cannot mistake them for memory.
func describeDailyActionEffect(ctx context.Context, personID int64, effect model.ActionEffect) (string, error) {
	at := dailyClock(effect.CreatedAt)
	switch effect.EffectType {
	case model.ActionEffectMessage:
		var message model.Message
		if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", effect.EffectID, personID).Take(&message).Error; err != nil {
			return "", fmt.Errorf("daily action %d message effect %d: %w", effect.ActionID, effect.ID, err)
		}
		return fmt.Sprintf("At %s, you sent %q.", dailyClock(message.CreatedAt), message.Content), nil
	case model.ActionEffectScheduledEvent:
		var scheduled model.ScheduledEvent
		if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", effect.EffectID, personID).Take(&scheduled).Error; err != nil {
			return "", fmt.Errorf("daily action %d scheduled effect %d: %w", effect.ActionID, effect.ID, err)
		}
		return fmt.Sprintf("At %s, you set a reminder for %s.", at, scheduled.TriggerAt.In(energy.Timezone()).Format("2006-01-02 15:04")), nil
	case model.ActionEffectScheduledEventCancellation:
		var scheduled model.ScheduledEvent
		if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", effect.EffectID, personID).Take(&scheduled).Error; err != nil {
			return "", fmt.Errorf("daily action %d cancelled alarm effect %d: %w", effect.ActionID, effect.ID, err)
		}
		return fmt.Sprintf("At %s, you cancelled the reminder set for %s.", at, scheduled.TriggerAt.In(energy.Timezone()).Format("2006-01-02 15:04")), nil
	case model.ActionEffectWork:
		var work model.Work
		if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", effect.EffectID, personID).Take(&work).Error; err != nil {
			return "", fmt.Errorf("daily action %d work effect %d: %w", effect.ActionID, effect.ID, err)
		}
		return fmt.Sprintf("At %s, you started work on %q.", at, work.Description), nil
	case model.ActionEffectJinshu:
		var row model.Jinshu
		if err := database.DB.WithContext(ctx).Where("id = ? AND from_person_id = ?", effect.EffectID, personID).Take(&row).Error; err != nil {
			applogger.Error("daily jinshu effect source unavailable to owner", "person_id", personID, "action_id", effect.ActionID, "effect_id", effect.ID, "jinshu_id", effect.EffectID, "error", err)
			return "", fmt.Errorf("daily action %d jinshu effect %d: %w", effect.ActionID, effect.ID, err)
		}
		recipient := presentedPersonName(ctx, personID, row.ToPersonID, effect.ID)
		return fmt.Sprintf("At %s, you sent %s a jinshu about %q: %q.", at, recipient, row.Topic, row.Description), nil
	case model.ActionEffectWorkspace:
		var workspace model.Workspace
		if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", effect.EffectID, personID).Take(&workspace).Error; err != nil {
			return "", fmt.Errorf("daily action %d workspace effect %d: %w", effect.ActionID, effect.ID, err)
		}
		return fmt.Sprintf("At %s, you registered a workspace named %q.", at, workspace.Name), nil
	case model.ActionEffectWorkControl:
		return fmt.Sprintf("At %s, you adjusted a work already in progress.", at), nil
	case model.ActionEffectSelfHeldEvent:
		return describeDailyFeedbackEffect(ctx, effect)
	case model.ActionEffectPSDigest:
		return fmt.Sprintf("At %s, you wrote a private-space entry.", at), nil
	case model.ActionEffectMemo:
		return fmt.Sprintf("At %s, you changed a private memo.", at), nil
	default:
		return "", fmt.Errorf("daily action %d has invalid effect type %d", effect.ActionID, effect.EffectType)
	}
}

// describeDailyFeedbackEffect renders a self-held Event that records what one
// Action returned. Payloads are read by their stored field names because the
// producer marshals without JSON tags; identifiers and runtime-only notices are
// omitted so the account keeps the result, not its routing data.
func describeDailyFeedbackEffect(ctx context.Context, effect model.ActionEffect) (string, error) {
	var event model.Event
	if err := database.DB.WithContext(ctx).Where("id = ?", effect.EffectID).Take(&event).Error; err != nil {
		return "", fmt.Errorf("daily action %d self-held effect %d: %w", effect.ActionID, effect.ID, err)
	}
	at := dailyClock(effect.CreatedAt)
	switch event.EventType {
	case model.EventTypeJinshuListed:
		var payload struct {
			Query   string
			Page    int
			Results []struct {
				FromName string
				Topic    string
			}
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			applogger.Error("daily summary: invalid received-jinshu list payload", "event_id", event.ID, "error", err)
			return "", nil
		}
		looked := "you reviewed the jinshu you had received"
		if strings.TrimSpace(payload.Query) != "" {
			looked = fmt.Sprintf("you searched the jinshu you had received for %q", payload.Query)
		}
		if len(payload.Results) == 0 {
			return fmt.Sprintf("At %s, %s; that page held nothing.", at, looked), nil
		}
		items := make([]string, 0, len(payload.Results))
		for _, item := range payload.Results {
			items = append(items, fmt.Sprintf("%q from %s", item.Topic, item.FromName))
		}
		return fmt.Sprintf("At %s, %s; that page listed %s.", at, looked, strings.Join(items, "; ")), nil
	case model.EventTypeJinshuSentListed:
		var payload struct {
			Query   string
			Page    int
			Results []struct {
				ToName string
				Topic  string
			}
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			applogger.Error("daily summary: invalid sent-jinshu list payload", "event_id", event.ID, "error", err)
			return "", nil
		}
		looked := "you reviewed the jinshu you had sent"
		if strings.TrimSpace(payload.Query) != "" {
			looked = fmt.Sprintf("you searched the jinshu you had sent for %q", payload.Query)
		}
		if len(payload.Results) == 0 {
			return fmt.Sprintf("At %s, %s; that page held nothing.", at, looked), nil
		}
		items := make([]string, 0, len(payload.Results))
		for _, item := range payload.Results {
			items = append(items, fmt.Sprintf("%q to %s", item.Topic, item.ToName))
		}
		return fmt.Sprintf("At %s, %s; that page listed %s.", at, looked, strings.Join(items, "; ")), nil
	case model.EventTypeOwnedSpaceInspected:
		var payload struct {
			Scope  string
			Result string
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			applogger.Error("daily summary: invalid owned-space inspection payload", "event_id", event.ID, "error", err)
			return "", nil
		}
		if strings.TrimSpace(payload.Result) == "" {
			return fmt.Sprintf("At %s, you inspected %q and it showed nothing.", at, payload.Scope), nil
		}
		return fmt.Sprintf("At %s, you inspected %q; it showed: %s", at, payload.Scope, payload.Result), nil
	case model.EventTypeExecutionSlotAvailable:
		// A reopened waiting slot is runtime routing, not a recalled experience.
		// The decision it triggers is still kept with the waiting Action's group.
		return "", nil
	case model.EventTypeSystemNotification:
		// The shared system-notice group already records what reached you.
		return "", nil
	default:
		applogger.Error("daily summary: unsupported self-held feedback event", "event_id", event.ID, "event_type", event.EventType, "action_id", effect.ActionID)
		return "", nil
	}
}
