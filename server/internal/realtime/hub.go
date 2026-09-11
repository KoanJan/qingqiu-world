package realtime

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// NotificationType is a stable client-protocol route. It is intentionally
// separate from a domain event: one domain fact may produce several client
// notifications.
type NotificationType string

const (
	NotificationChatMessage             NotificationType = "chat.message"
	NotificationChatAgentStatus         NotificationType = "chat.agent_status"
	NotificationChatAgentProcessing     NotificationType = "chat.agent_processing"
	NotificationNewMessage              NotificationType = "new_message"
	NotificationJinshuUpdated           NotificationType = "jinshu.updated"
	NotificationPublicExperienceUpdated NotificationType = "public_experience.updated"
	NotificationPublicExperienceDeleted NotificationType = "public_experience.deleted"
)

// ClientNotification is the protocol notification accepted by Hub. Data remains unencoded
// here so the Hub can construct the single JSON envelope at the transport edge.
type ClientNotification struct {
	Type       NotificationType
	SessionID  int64
	ResourceID int64
	Data       interface{}
}

// NotificationEnvelope is the JSON payload of an SSE message. ID orders notifications in
// this process only; clients must treat it as a cache-invalidation sequence,
// not as a version of the domain resource.
type NotificationEnvelope struct {
	ID         uint64           `json:"id"`
	Type       NotificationType `json:"type"`
	SessionID  int64            `json:"session_id,omitempty"`
	ResourceID int64            `json:"resource_id,omitempty"`
	OccurredAt time.Time        `json:"occurred_at"`
	Data       json.RawMessage  `json:"data,omitempty"`
}

// Subscription represents one browser connection. Close is idempotent and may
// race with Publish or CloseAll safely.
type Subscription interface {
	Notifications() <-chan NotificationEnvelope
	Close()
}

// Hub is an in-memory, best-effort fan-out transport for one user. It has no
// persistence or replay guarantee; missed events are repaired by HTTP reads.
type Hub interface {
	Subscribe(humanPersonID int64) Subscription
	Publish(humanPersonID int64, notification ClientNotification)
	CloseAll()
}
type hub struct {
	// mu protects both the subscriber sets and channel close/send lifecycle.
	// Publish holds RLock while sending so Close cannot close a selected channel.
	mu    sync.RWMutex
	conns map[int64]map[*subscription]struct{}
	// seq gives each emitted envelope a process-local, monotonic notification ID.
	seq atomic.Uint64
}
type subscription struct {
	hub           *hub
	humanPersonID int64
	ch            chan NotificationEnvelope
	once          sync.Once
}

// NewHub creates an empty user-scoped fan-out hub.
func NewHub() Hub { return &hub{conns: make(map[int64]map[*subscription]struct{})} }

// Subscribe adds one connection to a user's fan-out set. The bounded buffer
// isolates publishers from a temporarily slow browser.
func (h *hub) Subscribe(humanPersonID int64) Subscription {
	s := &subscription{hub: h, humanPersonID: humanPersonID, ch: make(chan NotificationEnvelope, 256)}
	h.mu.Lock()
	if h.conns[humanPersonID] == nil {
		h.conns[humanPersonID] = make(map[*subscription]struct{})
	}
	h.conns[humanPersonID][s] = struct{}{}
	h.mu.Unlock()
	return s
}
func (s *subscription) Notifications() <-chan NotificationEnvelope { return s.ch }

// Close removes and closes this subscription exactly once.
func (s *subscription) Close() { s.once.Do(func() { s.hub.remove(s) }) }

// remove performs channel close under the same lock used by Publish; this is
// what prevents a disconnect race from sending to a closed channel.
func (h *hub) remove(s *subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.conns[s.humanPersonID]; set != nil {
		if _, ok := set[s]; ok {
			delete(set, s)
			close(s.ch)
		}
		if len(set) == 0 {
			delete(h.conns, s.humanPersonID)
		}
	}
}

// Publish assigns an ID after the caller's business commit and fans out to the
// user's active connections. A full per-connection buffer drops this event
// rather than delaying business work; the client fallback read repairs it.
func (h *hub) Publish(humanPersonID int64, notification ClientNotification) {
	data, err := json.Marshal(notification.Data)
	if err != nil {
		return
	}
	envelope := NotificationEnvelope{ID: h.seq.Add(1), Type: notification.Type, SessionID: notification.SessionID, ResourceID: notification.ResourceID, OccurredAt: time.Now().UTC(), Data: data}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.conns[humanPersonID] {
		select {
		case s.ch <- envelope:
		default:
		}
	}
}

// CloseAll terminates every active stream during process shutdown.
func (h *hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.conns {
		for s := range set {
			close(s.ch)
		}
	}
	h.conns = make(map[int64]map[*subscription]struct{})
}
