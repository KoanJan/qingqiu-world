package sse

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/realtime"
)

func TestWriteNotificationUsesSSEIDAndEnvelope(t *testing.T) {
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	err := WriteNotification(writer, realtime.NotificationEnvelope{
		ID:         42,
		Type:       realtime.NotificationNewMessage,
		SessionID:  7,
		OccurredAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		Data:       []byte(`{"session_id":7}`),
	})
	if err != nil {
		t.Fatalf("WriteNotification: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := output.String()
	if !strings.HasPrefix(got, "id: 42\ndata: ") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("unexpected SSE frame: %q", got)
	}
	if !strings.Contains(got, `"id":42`) || !strings.Contains(got, `"session_id":7`) {
		t.Fatalf("envelope fields missing: %q", got)
	}
}
