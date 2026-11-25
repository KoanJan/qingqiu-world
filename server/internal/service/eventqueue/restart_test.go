package eventqueue

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

const crashAfterDeleteExit = 23

// Exit after SQLite commits deletion, before Next can advance its cache or
// return. This tests the accepted loss window at a real process boundary.
func TestQueueDeleteCrashChild(t *testing.T) {
	path := os.Getenv("QINGQIU_QUEUE_DELETE_CRASH_DB")
	if path == "" {
		return
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	if err := db.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register("exit_after_committed_delete", func(tx *gorm.DB) {
		if tx.Statement.Table == "agent_event_buffers" && tx.Error == nil && tx.RowsAffected == 1 {
			os.Exit(crashAfterDeleteExit)
		}
	}); err != nil {
		t.Fatal(err)
	}
	q := newEventQueue()
	s := q.Subscribe(1)
	s.Next(context.Background())
	t.Fatal("Next returned without the injected process exit")
}

func TestCommittedDeleteSurvivesExitBeforeNextReturns(t *testing.T) {
	root, err := os.MkdirTemp("", "qingqiu-eventqueue-delete-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Retained deletion-crash data: %s", root)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove deletion-crash test directory %s: %v", root, err)
		}
	})
	path := filepath.Join(root, "crash.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.AgentEventBuffer{}, &model.ScheduledEvent{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{101, 102, 103} {
		if err := AppendTx(db, delivery(id), 1); err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, _ := db.DB()
	sqlDB.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestQueueDeleteCrashChild$")
	cmd.Env = append(os.Environ(), "QINGQIU_QUEUE_DELETE_CRASH_DB="+path)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != crashAfterDeleteExit {
		t.Fatalf("child exit=%v output=%s", err, output)
	}
	db, err = gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ = db.DB()
	defer sqlDB.Close()
	old := database.DB
	database.DB = db
	defer func() { database.DB = old }()
	q := newEventQueue()
	s := q.Subscribe(1)
	defer q.Unsubscribe(1)
	for _, id := range []int64{102, 103} {
		event, err := s.Next(context.Background())
		if err != nil || event == nil || event.EventID != id {
			t.Fatalf("restart expected %d, got %+v %v", id, event, err)
		}
	}
	event, err := s.Next(context.Background())
	if err != nil || event != nil {
		t.Fatalf("removed record replayed: %+v %v", event, err)
	}
}
