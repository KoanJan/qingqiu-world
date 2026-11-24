package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
)

// The child executes the actual server entry point against disposable product
// data. No Git operations or model/provider calls are required by this fixture.
func TestQueueRestartServerChild(t *testing.T) {
	if os.Getenv("QINGQIU_QUEUE_RESTART_CHILD") != "1" {
		return
	}
	main()
	os.Exit(0)
}

// Verify real startup, migration, shutdown and restart: a returned delivery must
// stay removed even without Observation/Decision, while an undecodable pending
// row and an absent recipient's row remain durable. This does not judge LLMs.
func TestQueueDeliverySurvivesRealServerRestart(t *testing.T) {
	if os.Getenv("QINGQIU_QUEUE_RESTART_CHILD") == "1" {
		return
	}
	root, err := os.MkdirTemp("", "qingqiu-eventqueue-restart-")
	if err != nil {
		t.Fatal(err)
	}
	// Registered before process cleanups so shutdown failures retain evidence too.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Retained restart data and logs: %s", root)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove restart test directory %s: %v", root, err)
		}
	})
	dataRoot := filepath.Join(root, "product")
	start := func(label string) func() {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		logPath := filepath.Join(root, label+".log")
		log, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestQueueRestartServerChild$")
		cmd.Env = append(os.Environ(), "QINGQIU_QUEUE_RESTART_CHILD=1", "TEST_DATA_MODE=1", "DEV_DATA_MODE=0", "RELEASE_MIGRATION_ONLY=0",
			"DATA_ROOT="+dataRoot, "LOG_DIR="+filepath.Join(root, "logs"), "TEST_MIGRATION_STATE_PATH="+filepath.Join(root, "test-migrations.json"), fmt.Sprintf("PORT=%d", port), "GIN_MODE=release")
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cmd.Process.Signal(os.Interrupt)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("%s server exit: %v", label, err)
				}
			case <-time.After(15 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Errorf("%s shutdown timed out", label)
			}
			log.Close()
		}
		t.Cleanup(stop)
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/version", port))
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return stop
				}
			}
			select {
			case err := <-done:
				stopped = true
				log.Close()
				content, _ := os.ReadFile(logPath)
				t.Fatalf("%s startup: %v\n%s", label, err, content)
			case <-time.After(25 * time.Millisecond):
			}
		}
		content, _ := os.ReadFile(logPath)
		t.Fatalf("%s readiness timed out\n%s", label, content)
		return stop
	}
	stop := start("initialize")
	stop()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dataRoot, "db", "database.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	persons := []model.Person{{Name: "Subscribed restart agent", Type: model.PersonTypeAI}, {Name: "Absent restart agent", Type: model.PersonTypeAI}}
	if err := db.Create(&persons).Error; err != nil {
		t.Fatal(err)
	}
	// Missing LLM config stops business processing deterministically after retrieval.
	if err := db.Create(&model.AgentConfig{PersonID: persons[0].ID, LLMConfigID: 0}).Error; err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i, personID := range []int64{persons[0].ID, persons[0].ID, persons[1].ID} {
		event := model.Event{EventType: model.EventTypeSystemNotification, PayloadJSON: `"Restart fixture"`}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		encoded, err := eventqueue.Serialize(&eventqueue.AgentEvent{Type: eventqueue.EventTypeSystemNotification, EventID: event.ID, Payload: "Restart fixture"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			encoded = "{"
		}
		row := model.AgentEventBuffer{PersonID: personID, EventType: int(eventqueue.EventTypeSystemNotification), EventID: event.ID, PayloadJSON: encoded}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.ID)
	}
	stop = start("consume")
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int64
		if err := db.Model(&model.AgentEventBuffer{}).Where("id = ?", ids[0]).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Runtime did not retrieve committed delivery")
		}
		time.Sleep(25 * time.Millisecond)
	}
	stop()
	stop = start("restart")
	stop()
	var rows []model.AgentEventBuffer
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != ids[1] || rows[1].ID != ids[2] {
		t.Fatalf("restart rows=%+v; expected remaining IDs %v", rows, ids[1:])
	}
	var n int64
	if err := db.Model(&model.Event{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("queue removed business Event history: %d", n)
	}
	if err := db.Model(&model.AgentObservation{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("fixture unexpectedly reached Observation")
	}
	if err := db.Model(&model.Decision{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("fixture unexpectedly reached Decision")
	}
	t.Log("Real server startup and restart retained only the two remaining rows; removed delivery stayed absent without Observation or Decision")
}
