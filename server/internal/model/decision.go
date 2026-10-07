package model

import "time"

// ActionType identifies a top-level behavior selected by Decide.
// Its values are shared by the prompt schema and the persisted action record.
type ActionType int

const (
	ActionTypeChat ActionType = iota
	ActionTypeStartFocusedWork
	ActionTypeRouteFocusedWork
	ActionTypeCancelFocusedWork
	ActionTypeCreateAlarm
	ActionTypeUpdateBio
	ActionTypeEnterPrivateSpace
	ActionTypeInspectJinshu
	ActionTypeListReceivedJinshu
	ActionTypeSendJinshu
	ActionTypeListSentJinshu
	ActionTypeInspectOwnedSpace
)

// Label names an Action for historical presentation without changing its
// persisted integer identity or claiming that execution succeeded.
func (t ActionType) Label() string {
	switch t {
	case ActionTypeChat:
		return "chat"
	case ActionTypeStartFocusedWork:
		return "start focused work"
	case ActionTypeRouteFocusedWork:
		return "route focused work"
	case ActionTypeCancelFocusedWork:
		return "cancel focused work"
	case ActionTypeCreateAlarm:
		return "create alarm"
	case ActionTypeUpdateBio:
		return "update bio"
	case ActionTypeEnterPrivateSpace:
		return "enter private space"
	case ActionTypeInspectJinshu:
		return "inspect jinshu"
	case ActionTypeListReceivedJinshu:
		return "list received jinshu"
	case ActionTypeSendJinshu:
		return "send jinshu"
	case ActionTypeListSentJinshu:
		return "list sent jinshu"
	case ActionTypeInspectOwnedSpace:
		return "inspect owned space"
	default:
		return "unknown action"
	}
}

// ActionStatus records whether execution is still ongoing, not its outcome.
type ActionStatus int

const (
	ActionStatusInProgress ActionStatus = iota + 1
	ActionStatusEnded
)

// ActionEffectType identifies a result object produced by a top-level Action.
// Zero is invalid so an omitted type cannot be mistaken for a real effect.
type ActionEffectType int

const (
	ActionEffectWork ActionEffectType = iota + 1
	ActionEffectMessage
	ActionEffectScheduledEvent
	ActionEffectJinshu
	ActionEffectSelfHeldEvent
)

// Decision is one accepted Decide result. EventID is zero only for a true
// internal heartbeat; external decisions point to their triggering Event.
type Decision struct {
	// ID identifies one accepted decision opportunity.
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// PersonID identifies the agent that made this decision.
	PersonID int64 `gorm:"not null;index:idx_decisions_person_event;column:person_id" json:"person_id"`
	// EventID identifies the trigger; zero represents a heartbeat.
	EventID int64 `gorm:"not null;default:0;index:idx_decisions_person_event;column:event_id" json:"event_id"`
	// CreatedAt records when this accepted decision was persisted.
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table for accepted decisions.
func (Decision) TableName() string { return "decisions" }

// Action stores one accepted top-level behavior and its original semantic
// account. PlanJSON contains only the selected action's plan, not its effects.
type Action struct {
	// ID identifies one top-level behavior, even when execution is asynchronous.
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// DecisionID links this action to its accepted choice and trigger.
	DecisionID int64 `gorm:"not null;index;column:decision_id" json:"decision_id"`
	// Type selects the behavior dispatched by the runtime.
	Type ActionType `gorm:"not null;column:type" json:"type"`
	// PlanJSON preserves this action's selected plan, not its eventual result.
	PlanJSON string `gorm:"type:text;not null;default:'';column:plan_json" json:"plan_json"`
	// Background and Reason preserve the decision's semantic explanation.
	Background string `gorm:"type:text;not null;default:''" json:"background"`
	Reason     string `gorm:"type:text;not null;default:''" json:"reason"`
	// Status tracks execution activity, not whether the intended goal succeeded.
	Status ActionStatus `gorm:"not null;column:status;index" json:"status"`
	// CreatedAt and UpdatedAt record creation and the latest persistence update.
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table for accepted actions.
func (Action) TableName() string { return "actions" }

// ActionEffect privately links an Action to a result object. Public result
// records never point back to this table or to their producer's Action.
type ActionEffect struct {
	// ID identifies one recorded effect link.
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// ActionID identifies the private action that produced the linked result.
	ActionID int64 `gorm:"not null;index;column:action_id" json:"action_id"`
	// EffectType and EffectID locate the result in its authoritative table.
	EffectType ActionEffectType `gorm:"not null;index:idx_action_effect_target;column:effect_type" json:"effect_type"`
	EffectID   int64            `gorm:"not null;index:idx_action_effect_target;column:effect_id" json:"effect_id"`
	// CreatedAt records when the effect link was persisted.
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the private effect relation table.
func (ActionEffect) TableName() string { return "action_effects" }
