package sse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"qingqiu-world-server/internal/realtime"
)

// Serve writes one Hub subscription as a standard SSE response. The caller
// already resolved authorization; this package only owns framing, heartbeats,
// flushing, and cancellation cleanup.
func Serve(w http.ResponseWriter, r *http.Request, sub realtime.Subscription) {
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	writer := bufio.NewWriter(w)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case notification, ok := <-sub.Notifications():
			if !ok {
				return
			}
			if WriteNotification(writer, notification) != nil {
				return
			}
			writer.Flush()
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(writer, ": heartbeat\n\n")
			writer.Flush()
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// WriteNotification emits an unnamed SSE message. Keeping the default event name lets
// the frontend use EventSource.onmessage; the envelope's Type handles routing.
// The SSE id field is emitted as well so a future replay implementation can
// consume the browser's Last-Event-ID without changing frame format.
func WriteNotification(w *bufio.Writer, notification realtime.NotificationEnvelope) error {
	data, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", notification.ID, data)
	return err
}
