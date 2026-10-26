package memory

import (
	"testing"
	"time"

	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// TestRecallLogsCandidateRevokedBeforeReread forces a membership change after
// candidate selection and checks that the private source is neither returned
// nor silently discarded.
func TestRecallLogsCandidateRevokedBeforeReread(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.Session{ID: 710}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 710, ParticipantID: 711}).Error; err != nil {
		t.Fatal(err)
	}
	message := model.Message{SessionID: 710, PersonID: 712, Content: "private candidate", CreatedAt: time.Now().Add(-time.Minute)}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: message.CreatedAt}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := CreateObservation(711, event.ID); err != nil {
		t.Fatal(err)
	}

	candidates, err := recallSourceCandidates(RecallRequest{Scope: RecallMessages, PersonID: 711},
		recallCursor{Upper: time.Now().Add(time.Minute)}, model.MemorySourceEvent, nil, 6)
	if err != nil || len(candidates) != 1 || candidates[0].SourceID != event.ID {
		t.Fatalf("unexpected candidate query: candidates=%+v err=%v", candidates, err)
	}
	if err := db.Where("session_id = ? AND participant_id = ?", 710, 711).Delete(&model.ParticipantSession{}).Error; err != nil {
		t.Fatal(err)
	}

	var warning string
	var fields []any
	oldWarn := applogger.Warn
	applogger.Warn = func(msg string, args ...any) {
		if msg == "recall candidate became inaccessible during source reread" {
			warning, fields = msg, args
		}
	}
	t.Cleanup(func() { applogger.Warn = oldWarn })
	page := RecallPage{Items: []RecallItem{}}
	if err := appendReadableCandidates(711, 5, candidates, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("revoked candidate leaked: page=%+v", page)
	}
	if warning == "" {
		t.Fatal("revoked candidate was dropped without a diagnostic warning")
	}
	got := map[string]any{}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if ok {
			got[key] = fields[i+1]
		}
	}
	if got["person_id"] != int64(711) || got["source_kind"] != model.MemorySourceEvent || got["source_id"] != event.ID {
		t.Fatalf("warning does not locate the denied candidate: %+v", got)
	}
}
