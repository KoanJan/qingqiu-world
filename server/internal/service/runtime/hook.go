package runtime

import (
	"context"

	"qingqiu-world-server/internal/notification"
)

// notificationPublisher is Runtime's output port. It is configured by cmd/main
// and reports completed changes without importing SSE, Handler, or protocol
// JSON packages. NopPublisher keeps startup and focused tests safe.
var notificationPublisher notification.Publisher = notification.NopPublisher{}

// notify is intentionally fire-and-forget. Notification delivery cannot delay or
// roll back Runtime work because HTTP reconciliation handles missed events.
func notify(intent notification.Intent) {
	notificationPublisher.Publish(context.Background(), intent)
}
