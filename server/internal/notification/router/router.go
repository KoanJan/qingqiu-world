// Package notificationrouter maps internal notification intents to the
// client-facing realtime notification protocol.
package notificationrouter

import (
	"context"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/notification"
	"qingqiu-world-server/internal/realtime"
)

// Router is the only layer that knows both notification visibility rules and
// the client protocol. Runtime and domain services remain unaware of browser
// connections, protocol types, and JSON payloads.
type Router struct{ hub realtime.Hub }

// New creates the notification.Publisher wired by the composition root.
func New(hub realtime.Hub) *Router { return &Router{hub: hub} }

// Publish projects one committed intent into zero or more best-effort client
// notifications. A disconnected recipient can never affect completed business
// work; the frontend's HTTP reconciliation handles missed notifications.
func (r *Router) Publish(_ context.Context, intent notification.Intent) {
	switch item := intent.(type) {
	case notification.MessageCommitted:
		r.publishMessageCommitted(item)
	case notification.AgentStatusChanged:
		r.publishToSessionHuman(item.SessionID, realtime.ClientNotification{
			Type:       realtime.NotificationChatAgentStatus,
			SessionID:  item.SessionID,
			ResourceID: item.PersonID,
			Data:       map[string]int64{"agent_id": item.PersonID, "status": int64(item.Status)},
		})
	case notification.AgentProcessingStarted:
		r.publishToSessionHuman(item.SessionID, realtime.ClientNotification{
			Type:      realtime.NotificationChatAgentProcessing,
			SessionID: item.SessionID,
			Data:      map[string]string{"message": "Agent is processing your request..."},
		})
	case notification.JinshuCreated:
		r.publishJinshuCreated(item)
	case notification.PublicExperienceChanged:
		r.publishPublicExperienceChange(item)
	}
}

// publishMessageCommitted always synchronizes the durable message to every
// browser of the human participant. new_message has a narrower meaning: it is
// an unread-state signal and therefore must not be emitted for that human's own
// message, even when the message is echoed to another browser tab.
func (r *Router) publishMessageCommitted(item notification.MessageCommitted) {
	humanPersonID, ok := r.sessionHumanPersonID(item.SessionID)
	if !ok {
		return
	}
	r.hub.Publish(humanPersonID, realtime.ClientNotification{
		Type:       realtime.NotificationChatMessage,
		SessionID:  item.SessionID,
		ResourceID: item.MessageID,
		Data: map[string]interface{}{
			"message_id": item.MessageID,
			"person_id":  item.PersonID,
			"content":    item.Content,
		},
	})
	if item.PersonID == humanPersonID {
		return
	}
	r.hub.Publish(humanPersonID, realtime.ClientNotification{
		Type:       realtime.NotificationNewMessage,
		SessionID:  item.SessionID,
		ResourceID: item.SessionID,
		Data:       map[string]int64{"session_id": item.SessionID},
	})
}

// publishToSessionHuman resolves the only human participant eligible to
// receive a session notification. AI-only sessions have no browser target.
func (r *Router) publishToSessionHuman(sessionID int64, clientNotification realtime.ClientNotification) {
	humanPersonID, ok := r.sessionHumanPersonID(sessionID)
	if ok {
		r.hub.Publish(humanPersonID, clientNotification)
	}
}

// sessionHumanPersonID resolves the only human participant eligible for a
// session-scoped client notification. AI-only sessions have no browser target.
func (r *Router) sessionHumanPersonID(sessionID int64) (int64, bool) {
	humanPersonID, err := dops.GetSessionHumanParticipantID(sessionID)
	if err != nil {
		applogger.Error("notification router: resolve session human", "session_id", sessionID, "error", err)
		return 0, false
	}
	return humanPersonID, humanPersonID > 0
}

// publishJinshuCreated emits a direction-specific list invalidation to each
// human participant. Read state has no notification, preventing a sender from
// inferring when a recipient opened a jinshu.
func (r *Router) publishJinshuCreated(item notification.JinshuCreated) {
	for _, recipient := range []struct {
		humanPersonID int64
		direction     string
	}{
		{item.FromPersonID, "sent"},
		{item.ToPersonID, "received"},
	} {
		person, err := dops.GetPerson(recipient.humanPersonID)
		if err != nil || person.Type != model.PersonTypeHuman {
			continue
		}
		r.hub.Publish(person.ID, realtime.ClientNotification{
			Type:       realtime.NotificationJinshuUpdated,
			ResourceID: item.JinshuID,
			Data: map[string]interface{}{
				"directions": []string{recipient.direction},
				"status":     "created",
			},
		})
	}
}

// publishPublicExperienceChange maps the internal lifecycle to list/detail
// invalidation. Public experience is single-user in the current product model.
func (r *Router) publishPublicExperienceChange(item notification.PublicExperienceChanged) {
	humanPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		return
	}
	notificationType := realtime.NotificationPublicExperienceUpdated
	if item.Change == notification.PublicExperienceDeleted {
		notificationType = realtime.NotificationPublicExperienceDeleted
	}
	r.hub.Publish(humanPersonID, realtime.ClientNotification{
		Type:       notificationType,
		ResourceID: item.ExperienceID,
		Data:       map[string]string{"status": string(item.Change)},
	})
}
