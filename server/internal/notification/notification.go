// Package notification defines the internal intent contract for notifying a
// client after a completed business change. It is distinct from AgentEvent
// (Runtime input) and model.Event (a persisted memory record).
package notification

import "context"

// Intent describes a committed business change that should be considered for
// client notification. It contains no HTTP, SSE, JSON, or UI protocol details.
type Intent interface{ isNotificationIntent() }

// Publisher is the output port used by services and Runtime. Publishing is
// best-effort and must never alter the outcome of completed business work.
type Publisher interface{ Publish(context.Context, Intent) }

// MessageCommitted asks the notification layer to inform the session's human
// participant that a durable message is available.
type MessageCommitted struct {
	SessionID, MessageID, PersonID int64
	Content                        string
}

func (MessageCommitted) isNotificationIntent() {}

// AgentStatusChanged asks the notification layer to update the visible agent
// status for a session.
type AgentStatusChanged struct {
	SessionID, PersonID int64
	Status              int
}

func (AgentStatusChanged) isNotificationIntent() {}

// AgentProcessingStarted asks the notification layer to surface the beginning
// of an agent task. It is a lifecycle hint, not a user message.
type AgentProcessingStarted struct{ SessionID int64 }

func (AgentProcessingStarted) isNotificationIntent() {}

// JinshuCreated asks the notification layer to invalidate the relevant sender
// and receiver lists. Recipient read state deliberately has no intent.
type JinshuCreated struct{ JinshuID, FromPersonID, ToPersonID int64 }

func (JinshuCreated) isNotificationIntent() {}

// PublicExperienceChange identifies a committed lifecycle change that
// invalidates the public-experience list or detail cache.
type PublicExperienceChange string

const (
	PublicExperienceCreated    PublicExperienceChange = "created"
	PublicExperienceGenerating PublicExperienceChange = "generating"
	PublicExperienceActive     PublicExperienceChange = "active"
	PublicExperienceError      PublicExperienceChange = "error"
	PublicExperienceDeleted    PublicExperienceChange = "deleted"
)

// PublicExperienceChanged asks the notification layer to invalidate a public
// experience after its state was committed.
type PublicExperienceChanged struct {
	ExperienceID int64
	Change       PublicExperienceChange
}

func (PublicExperienceChanged) isNotificationIntent() {}

// NopPublisher is safe before the composition root installs the real
// notification router and in focused tests.
type NopPublisher struct{}

func (NopPublisher) Publish(context.Context, Intent) {}
