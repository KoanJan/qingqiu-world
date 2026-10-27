package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/llm"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func cognitiveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cognitive.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{}, &model.Message{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.Event{}, &model.AgentObservation{}, &model.DailyMemory{}, &model.CognitiveProcess{}, &model.CognitiveMemory{}, &model.CognitiveMemoryVersion{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })
	return db
}

func TestCognitiveCheckpointReplacesLastRequestAndResponse(t *testing.T) {
	settings := config.Get()
	oldRoot := settings.DataRoot
	settings.DataRoot = t.TempDir()
	t.Cleanup(func() { settings.DataRoot = oldRoot })
	state := cognitiveCheckpoint{Request: cognitiveLLMRequest{Model: "test-model", ToolChoice: "required", Messages: []llm.Message{{Role: "user", Content: "first"}}, Tools: []llm.FunctionDefinition{cognitiveFinishTool()}}, Response: emptyCognitiveResponse()}
	if err := writeCognitiveCheckpoint(7, 9, state); err != nil {
		t.Fatal(err)
	}
	state.Request.Messages[0].Content = "second"
	state.Response = json.RawMessage(`{"content":"done","tool_calls":[],"finish_reason":"tool_calls"}`)
	if err := writeCognitiveCheckpoint(7, 9, state); err != nil {
		t.Fatal(err)
	}
	read, err := readCognitiveCheckpoint(7, 9)
	if err != nil || read.Request.Messages[0].Content != "second" {
		t.Fatalf("checkpoint=%+v err=%v", read, err)
	}
	var shape map[string]json.RawMessage
	data, err := os.ReadFile(cognitiveCheckpointPath(7, 9))
	if err != nil || json.Unmarshal(data, &shape) != nil || len(shape) != 2 || shape["request"] == nil || shape["response"] == nil {
		t.Fatalf("checkpoint shape: %s err=%v", data, err)
	}
	var modelRequest map[string]json.RawMessage
	if err := json.Unmarshal(shape["request"], &modelRequest); err != nil || modelRequest["model"] == nil || modelRequest["messages"] == nil || modelRequest["tools"] == nil || modelRequest["stage"] != nil || modelRequest["memory_id"] != nil {
		t.Fatalf("request is not an LLM request: %s err=%v", shape["request"], err)
	}
	entries, err := os.ReadDir(filepath.Dir(cognitiveCheckpointPath(7, 9)))
	if err != nil || len(entries) != 1 {
		t.Fatalf("files=%v err=%v", entries, err)
	}
}

func TestCognitiveDailyUpdateAndDiscoveryQuota(t *testing.T) {
	db := cognitiveTestDB(t)
	ctx := context.Background()
	// A fresh day offers five routed updates and one discovery.
	usage, err := loadCognitiveDayUsage(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if remaining, discovery := cognitiveAdmissionBudget(usage); remaining != 5 || !discovery {
		t.Fatalf("fresh day: updates=%d discovery=%t", remaining, discovery)
	}
	// Each ended update spends one update slot but leaves discovery open.
	for i := 0; i < 5; i++ {
		if err := db.Create(&model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessEnded,
			Kind: model.CognitiveProcessKindUpdate, MemoryID: int64(20 + i), Outcome: model.CognitiveProcessOutcomeWritten}).Error; err != nil {
			t.Fatal(err)
		}
	}
	usage, err = loadCognitiveDayUsage(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if remaining, discovery := cognitiveAdmissionBudget(usage); remaining != 0 || !discovery {
		t.Fatalf("five updates: updates=%d discovery=%t", remaining, discovery)
	}
	if usage.Updates != 5 || usage.Ended != 5 || len(usage.Reviewed) != 5 {
		t.Fatalf("usage=%+v", usage)
	}
	// A process without a memory spends the discovery slot.
	if err := db.Create(&model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessEnded,
		Kind: model.CognitiveProcessKindDiscovery}).Error; err != nil {
		t.Fatal(err)
	}
	usage, err = loadCognitiveDayUsage(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if remaining, discovery := cognitiveAdmissionBudget(usage); remaining != 0 || discovery {
		t.Fatalf("spent discovery: updates=%d discovery=%t", remaining, discovery)
	}
	// Another owner and a process that ended yesterday do not count.
	other := model.CognitiveProcess{PersonID: 8, Status: model.CognitiveProcessEnded, Kind: model.CognitiveProcessKindUpdate}
	yesterday := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessEnded, Kind: model.CognitiveProcessKindUpdate}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&yesterday).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CognitiveProcess{}).Where("id = ?", yesterday.ID).
		UpdateColumn("updated_at", energy.Now().Add(-24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	usage, err = loadCognitiveDayUsage(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Updates != 5 || usage.Ended != 6 {
		t.Fatalf("foreign or previous day leaked: %+v", usage)
	}
}

func TestCognitiveDailySelectionQuota(t *testing.T) {
	db := cognitiveTestDB(t)
	ctx := context.Background()
	reviewed := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessEnded, Epoch: 1, MemoryID: 11,
		Kind: model.CognitiveProcessKindDiscovery}
	discovery := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessEnded, Epoch: 1,
		Kind: model.CognitiveProcessKindDiscovery}
	if err := db.Create(&reviewed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&discovery).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateCognitiveDailySelection(ctx, 7, cognitiveRequest{MemoryID: 11, Revision: 1}); err == nil {
		t.Fatal("a memory reviewed today was selectable again")
	}
	if err := validateCognitiveDailySelection(ctx, 7, cognitiveRequest{MemoryID: 12, Revision: 1}); err != nil {
		t.Fatalf("an unreviewed memory was rejected: %v", err)
	}
	if err := validateCognitiveDailySelection(ctx, 7, cognitiveRequest{}); err == nil {
		t.Fatal("a second new object was selectable")
	}
	// A memory formed today is outside today's remaining review quota too.
	fresh := model.CognitiveMemory{PersonID: 7, Description: "Formed today", Embedding: []byte{}, ImpressionEmbedding: []byte{}}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateCognitiveDailySelection(ctx, 7, cognitiveRequest{MemoryID: fresh.ID, Revision: 1}); err == nil {
		t.Fatal("a memory formed today was selectable")
	}
	// A memory last reviewed before today is still available.
	old := model.CognitiveMemory{PersonID: 7, Description: "Formed earlier", Embedding: []byte{}, ImpressionEmbedding: []byte{}}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CognitiveMemory{}).Where("id = ?", old.ID).
		UpdateColumn("updated_at", energy.Now().Add(-24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateCognitiveDailySelection(ctx, 7, cognitiveRequest{MemoryID: old.ID, Revision: 1}); err != nil {
		t.Fatalf("a memory last reviewed yesterday was rejected: %v", err)
	}
}

func TestCognitiveRetryBudget(t *testing.T) {
	db := cognitiveTestDB(t)
	process := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: 1}
	if err := db.Create(&process).Error; err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= cognitiveRetryLimit+1; attempt++ {
		before := time.Now().Unix()
		status, err := retryOrEndCognitiveProcess(process)
		if err != nil {
			t.Fatal(err)
		}
		var stored model.CognitiveProcess
		if err := db.First(&stored, process.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Failures != attempt {
			t.Fatalf("attempt %d: failures=%d", attempt, stored.Failures)
		}
		if stored.Epoch != process.Epoch+1 {
			t.Fatalf("attempt %d: epoch=%d", attempt, stored.Epoch)
		}
		if attempt > cognitiveRetryLimit {
			if status != model.CognitiveProcessEnded {
				t.Fatalf("attempt %d: status=%d", attempt, status)
			}
			if stored.RetryAt != 0 {
				t.Fatalf("attempt %d: retry_at=%d", attempt, stored.RetryAt)
			}
			continue
		}
		if status != model.CognitiveProcessPaused {
			t.Fatalf("attempt %d: status=%d", attempt, status)
		}
		if stored.RetryAt < before+int64(cognitiveRetryDelay/time.Second) {
			t.Fatalf("attempt %d: retry_at=%d is too early", attempt, stored.RetryAt)
		}
		// Simulate the next admission cycle, which advances the epoch again.
		if err := transitionCognitiveProcess(db, process.ID,
			cognitiveProcessState{Status: stored.Status, Epoch: stored.Epoch},
			cognitiveProcessState{Status: model.CognitiveProcessRunning, Epoch: stored.Epoch + 1}); err != nil {
			t.Fatal(err)
		}
		process.Status = model.CognitiveProcessRunning
		process.Epoch = stored.Epoch + 1
		process.Failures = stored.Failures
	}
}

func TestCognitiveStorageRetryBudget(t *testing.T) {
	db := cognitiveTestDB(t)
	process := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: 1,
		Kind: model.CognitiveProcessKindUpdate, MemoryID: 21, MemoryRevision: 1}
	if err := db.Create(&process).Error; err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= cognitiveStorageRetryLimit+1; attempt++ {
		before := time.Now().Unix()
		status, err := retryOrEndCognitiveStorage(process)
		if err != nil {
			t.Fatal(err)
		}
		var stored model.CognitiveProcess
		if err := db.First(&stored, process.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.StorageFailures != attempt {
			t.Fatalf("attempt %d: storage_failures=%d", attempt, stored.StorageFailures)
		}
		if stored.Failures != 0 {
			t.Fatalf("attempt %d: storage retry consumed the model budget: failures=%d", attempt, stored.Failures)
		}
		if stored.Outcome != model.CognitiveProcessOutcomeUnrecorded {
			t.Fatalf("attempt %d: storage retry recorded an outcome: %d", attempt, stored.Outcome)
		}
		if stored.Epoch != process.Epoch+1 {
			t.Fatalf("attempt %d: epoch=%d", attempt, stored.Epoch)
		}
		if attempt > cognitiveStorageRetryLimit {
			if status != model.CognitiveProcessEnded || stored.RetryAt != 0 {
				t.Fatalf("attempt %d: status=%d retry_at=%d", attempt, status, stored.RetryAt)
			}
			continue
		}
		if status != model.CognitiveProcessPaused {
			t.Fatalf("attempt %d: status=%d", attempt, status)
		}
		if stored.RetryAt < before+int64(cognitiveRetryDelay/time.Second) {
			t.Fatalf("attempt %d: retry_at=%d is too early", attempt, stored.RetryAt)
		}
		// Simulate the next admission cycle, which advances the epoch again.
		if err := transitionCognitiveProcess(db, process.ID,
			cognitiveProcessState{Status: stored.Status, Epoch: stored.Epoch},
			cognitiveProcessState{Status: model.CognitiveProcessRunning, Epoch: stored.Epoch + 1}); err != nil {
			t.Fatal(err)
		}
		process.Status = model.CognitiveProcessRunning
		process.Epoch = stored.Epoch + 1
		process.StorageFailures = stored.StorageFailures
	}
}

func TestCognitiveUpdateTargetSkipsSelectionAndRevisit(t *testing.T) {
	db := cognitiveTestDB(t)
	ctx := context.Background()
	process := model.CognitiveProcess{ID: 5, PersonID: 7, Kind: model.CognitiveProcessKindUpdate,
		MemoryID: 21, MemoryRevision: 2}
	target, err := cognitiveProcessTarget(process, nil)
	if err != nil {
		t.Fatal(err)
	}
	if target.MemoryID != 21 || target.Revision != 2 || target.Description != "" {
		t.Fatalf("fixed target=%+v", target)
	}
	// A discovery process still reads its target from the conversation.
	if _, err := cognitiveProcessTarget(model.CognitiveProcess{ID: 6, Kind: model.CognitiveProcessKindDiscovery}, nil); err == nil {
		t.Fatal("a discovery process without a selection produced a target")
	}
	// A routed update is exempt from the recent-review confirmation, while a
	// discovery that forgot revisit=true is still rejected.
	memory := model.CognitiveMemory{PersonID: 7, Description: "A thread", Embedding: []byte{}, ImpressionEmbedding: []byte{}}
	if err := db.Create(&memory).Error; err != nil {
		t.Fatal(err)
	}
	req := cognitiveRequest{MemoryID: memory.ID, Revision: 1}
	result := cognitiveFinish{Outcome: cognitiveWriteCognition}
	if err := validateCognitiveRecentRevisit(ctx, process, req, result); err != nil {
		t.Fatalf("routed update was rejected: %v", err)
	}
	discovery := model.CognitiveProcess{ID: 6, Kind: model.CognitiveProcessKindDiscovery}
	if err := validateCognitiveRecentRevisit(ctx, discovery, req, result); err == nil {
		t.Fatal("a recent discovery review without confirmation passed")
	}
	if err := validateCognitiveRecentRevisit(ctx, discovery, req, cognitiveFinish{Outcome: cognitiveWriteCognition, Revisit: true}); err != nil {
		t.Fatalf("confirmed revisit was rejected: %v", err)
	}
}

func TestCognitiveRecordTarget(t *testing.T) {
	db := cognitiveTestDB(t)
	process := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: 2}
	if err := db.Create(&process).Error; err != nil {
		t.Fatal(err)
	}
	if err := recordCognitiveTarget(process, 11); err != nil {
		t.Fatal(err)
	}
	var stored model.CognitiveProcess
	if err := db.First(&stored, process.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.MemoryID != 11 {
		t.Fatalf("memory_id=%d", stored.MemoryID)
	}
	if err := recordCognitiveTarget(model.CognitiveProcess{ID: process.ID, PersonID: 7, Epoch: 1}, 12); err == nil {
		t.Fatal("a stale epoch wrote the target")
	}
}

func TestCognitiveVersionCommitAndEpoch(t *testing.T) {
	db := cognitiveTestDB(t)
	person := model.Person{ID: 7, Name: "Memory owner", Type: model.PersonTypeAI}
	if err := db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	makeProcess := func(epoch int64) model.CognitiveProcess {
		p := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: epoch}
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1 := makeProcess(1)
	req := cognitiveRequest{Description: "How our conversations changed"}
	if err := commitCognitiveFinish(context.Background(), p1, req, cognitiveFinish{Outcome: cognitiveWriteCognition, Impression: "The thread is still open and recurring", Understanding: "I now see a recurring pattern"}, nil, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := commitCognitiveFinish(context.Background(), p1, req, cognitiveFinish{Outcome: cognitiveWriteCognition, Impression: "duplicate", Understanding: "duplicate"}, nil, nil); err == nil {
		t.Fatal("ended process committed twice")
	}
	var memory model.CognitiveMemory
	if err := db.Where("person_id = ?", 7).Take(&memory).Error; err != nil {
		t.Fatal(err)
	}
	if memory.Description != req.Description {
		t.Fatalf("description=%q", memory.Description)
	}
	if string(memory.ImpressionEmbedding) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("initial impression vector=%v", memory.ImpressionEmbedding)
	}
	previousReview := memory.UpdatedAt
	var first model.CognitiveMemoryVersion
	if err := db.Where("memory_id = ?", memory.ID).Take(&first).Error; err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.ProcessID != p1.ID {
		t.Fatalf("first version=%+v", first)
	}
	p2 := makeProcess(1)
	if err := commitCognitiveFinish(context.Background(), p2, cognitiveRequest{MemoryID: memory.ID, Revision: 1}, cognitiveFinish{Outcome: cognitiveConfirmUnchanged, Revisit: true}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&memory, memory.ID).Error; err != nil {
		t.Fatal(err)
	}
	if memory.UpdatedAt.Before(previousReview) {
		t.Fatal("completed review moved time backwards")
	}
	if string(memory.ImpressionEmbedding) != string([]byte{1, 2, 3, 4}) {
		t.Fatal("unchanged review replaced the understanding vector")
	}
	var count int64
	if err := db.Model(&model.CognitiveMemoryVersion{}).Where("memory_id = ?", memory.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("unchanged review added version: %d err=%v", count, err)
	}
	p3 := makeProcess(1)
	if err := commitCognitiveFinish(context.Background(), p3, cognitiveRequest{MemoryID: memory.ID, Revision: 1}, cognitiveFinish{Outcome: cognitiveWriteCognition, Impression: "The thread moved past its first phase", Understanding: "A revised understanding", Revisit: true}, nil, []byte{4, 3, 2, 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CognitiveMemoryVersion{}).Where("memory_id = ?", memory.ID).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("revision count=%d err=%v", count, err)
	}
	if err := db.First(&memory, memory.ID).Error; err != nil || string(memory.ImpressionEmbedding) != string([]byte{4, 3, 2, 1}) {
		t.Fatalf("latest impression vector=%v err=%v", memory.ImpressionEmbedding, err)
	}
	if err := db.First(&first, first.ID).Error; err != nil || first.Impression != "The thread is still open and recurring" || first.Understanding != "I now see a recurring pattern" {
		t.Fatalf("old version changed: %+v err=%v", first, err)
	}
	p4 := makeProcess(2)
	if err := commitCognitiveFinish(context.Background(), model.CognitiveProcess{ID: p4.ID, PersonID: 7, Epoch: 1}, cognitiveRequest{MemoryID: memory.ID, Revision: 2}, cognitiveFinish{Outcome: cognitiveWriteCognition, Impression: "stale", Understanding: "stale"}, nil, nil); err == nil {
		t.Fatal("stale epoch committed")
	}
	if err := db.Model(&model.CognitiveMemoryVersion{}).Where("memory_id = ?", memory.ID).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("stale result created version: %d err=%v", count, err)
	}
}

func TestCognitivePauseInvalidatesLateCheckpoint(t *testing.T) {
	db := cognitiveTestDB(t)
	p := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: 3}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &agentRuntime{agentPersonID: 7, cognitionWake: make(chan struct{}, 1), cognitiveCancel: cancel, cognitiveProcessID: p.ID, cognitiveEpoch: p.Epoch}
	if !r.acquireCognitiveSlot(p.ID) {
		t.Fatal("slot not acquired")
	}
	start := time.Now()
	r.pauseCognitiveProcess(p.ID)
	if time.Since(start) > time.Second {
		t.Fatal("pause blocked")
	}
	if ctx.Err() == nil {
		t.Fatal("model request was not canceled")
	}
	if err := db.First(&p, p.ID).Error; err != nil || p.Status != model.CognitiveProcessPaused || p.Epoch != 4 {
		t.Fatalf("process=%+v err=%v", p, err)
	}
	if r.currentExecutionSlot() != nil {
		t.Fatal("slot stayed occupied")
	}
	if err := r.saveCognitiveCheckpoint(model.CognitiveProcess{ID: p.ID, PersonID: 7, Epoch: 3}, cognitiveCheckpoint{}); err == nil {
		t.Fatal("late response wrote checkpoint")
	}
}

func TestCognitiveProcessCreationKeepsOneUnfinished(t *testing.T) {
	cognitiveTestDB(t)
	ctx := context.Background()
	first, created, err := createCognitiveProcess(ctx, 7, time.Now(), model.CognitiveProcessKindUpdate, 21, 3)
	if err != nil || !created || first.ID == 0 {
		t.Fatalf("first creation: process=%+v created=%t err=%v", first, created, err)
	}
	if first.Kind != model.CognitiveProcessKindUpdate || first.MemoryID != 21 || first.MemoryRevision != 3 {
		t.Fatalf("fixed target was not stored: %+v", first)
	}
	// The check and the insert share one transaction, so a second attempt sees
	// the unfinished row and creates nothing.
	second, created, err := createCognitiveProcess(ctx, 7, time.Now(), model.CognitiveProcessKindDiscovery, 0, 0)
	if err != nil || created || second.ID != 0 {
		t.Fatalf("second creation duplicated: process=%+v created=%t err=%v", second, created, err)
	}
	// A completed process does not block the next one.
	if err := database.DB.Model(&model.CognitiveProcess{}).Where("id = ?", first.ID).
		Updates(map[string]interface{}{"status": model.CognitiveProcessEnded, "epoch": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, created, err := createCognitiveProcess(ctx, 7, time.Now(), model.CognitiveProcessKindDiscovery, 0, 0); err != nil || !created {
		t.Fatalf("creation after completion: created=%t err=%v", created, err)
	}
	// Another person has an independent invariant.
	if _, created, err := createCognitiveProcess(ctx, 8, time.Now(), model.CognitiveProcessKindDiscovery, 0, 0); err != nil || !created {
		t.Fatalf("other owner creation: created=%t err=%v", created, err)
	}
}

func TestCognitiveNormalizeAbandonedRunning(t *testing.T) {
	db := cognitiveTestDB(t)
	process := model.CognitiveProcess{PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: 4}
	if err := db.Create(&process).Error; err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeCognitiveProcess(context.Background(), process)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Status != model.CognitiveProcessPaused || normalized.Epoch != 5 {
		t.Fatalf("normalized=%+v", normalized)
	}
	var stored model.CognitiveProcess
	if err := db.First(&stored, process.ID).Error; err != nil || stored.Status != model.CognitiveProcessPaused || stored.Epoch != 5 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	// The lost owner's epoch is fenced by the normalization.
	if err := transitionCognitiveProcess(db, process.ID,
		cognitiveProcessState{Status: model.CognitiveProcessRunning, Epoch: 4},
		cognitiveProcessState{Status: model.CognitiveProcessEnded, Epoch: 5}); err == nil {
		t.Fatal("stale epoch transitioned")
	}
}

func TestCognitiveRecallCursorBindsOwnerAndFilters(t *testing.T) {
	args := cognitiveRecallArgs{MemoryID: 11, Query: "shared history", PageSize: 5}
	cursor := encodeCognitiveRecallCursor(7, "recall_cognitive_memory", args, 5)
	if got, err := decodeCognitiveRecallCursor(7, "recall_cognitive_memory", args, cursor); err != nil || got != 5 {
		t.Fatalf("cursor offset=%d err=%v", got, err)
	}
	if _, err := decodeCognitiveRecallCursor(8, "recall_cognitive_memory", args, cursor); err == nil {
		t.Fatal("cursor crossed owner")
	}
	args.Query = "different topic"
	if _, err := decodeCognitiveRecallCursor(7, "recall_cognitive_memory", args, cursor); err == nil {
		t.Fatal("cursor crossed query")
	}
	args.Query = "shared history"
	args.Cursor = cursor
	if got, err := decodeCognitiveRecallCursor(7, "recall_cognitive_memory", args, cursor); err != nil || got != 5 {
		t.Fatalf("cursor from the previous response did not continue: offset=%d err=%v", got, err)
	}
}

func TestDailyRecallDirectReadPagesSourcesButKeepsMemory(t *testing.T) {
	db := cognitiveTestDB(t)
	local := time.Now().In(energy.Timezone())
	contact := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, energy.Timezone()).AddDate(0, 0, -1)
	for _, person := range []model.Person{{ID: 7, Name: "Self"}, {ID: 8, Name: "Other"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Session{ID: 9}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{7, 8} {
		if err := db.Create(&model.ParticipantSession{SessionID: 9, ParticipantID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, content := range []string{"first", "second"} {
		message := model.Message{SessionID: 9, PersonID: 8, Content: content, CreatedAt: contact}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: contact}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 7, EventID: event.ID, CreatedAt: contact}).Error; err != nil {
			t.Fatal(err)
		}
	}
	shard := model.DailyMemory{PersonID: 7, LocalDate: contact.In(energy.Timezone()).Format("2006-01-02"), GroupType: model.DailyMemoryGroupChat, GroupID: 9, Summary: "Two observations", StartAt: contact, EndAt: contact, Embedding: []byte{}}
	if err := db.Create(&shard).Error; err != nil {
		t.Fatal(err)
	}
	readPage := func(cursor string) struct {
		Items   []json.RawMessage `json:"items"`
		Sources []struct {
			EventID int64 `json:"event_id"`
		} `json:"sources"`
		NextCursor string `json:"next_cursor"`
	} {
		t.Helper()
		arguments, _ := json.Marshal(map[string]any{"memory_id": shard.ID, "page_size": 1, "cursor": cursor})
		output, err := executeCognitiveRecall(context.Background(), 7, "recall_daily_memory", string(arguments))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Items   []json.RawMessage `json:"items"`
			Sources []struct {
				EventID int64 `json:"event_id"`
			} `json:"sources"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := readPage("")
	second := readPage(first.NextCursor)
	if len(first.Items) != 1 || len(second.Items) != 1 || len(first.Sources) != 1 || len(second.Sources) != 1 || first.Sources[0].EventID == second.Sources[0].EventID || first.NextCursor == "" || second.NextCursor != "" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if _, err := executeCognitiveRecall(context.Background(), 7, "recall_daily_memory", fmt.Sprintf(`{"memory_id":%d,"query":"ignored"}`, shard.ID)); err == nil {
		t.Fatal("direct memory read silently ignored a search filter")
	}
}

func TestCognitiveRecallRevisionCursorOnlyWhenMoreExist(t *testing.T) {
	db := cognitiveTestDB(t)
	record := model.CognitiveMemory{PersonID: 7, Description: "An enduring concern", Embedding: []byte{}, ImpressionEmbedding: []byte{}}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	version := model.CognitiveMemoryVersion{MemoryID: record.ID, Revision: 1, Impression: "A concern that keeps returning", Understanding: "An initial understanding", ProcessID: 1}
	if err := db.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	arguments := fmt.Sprintf(`{"memory_id":%d,"page_size":1}`, record.ID)
	output, err := executeCognitiveRecall(context.Background(), 7, "recall_cognitive_memory", arguments)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(output), &page); err != nil || page.NextCursor != "" {
		t.Fatalf("exact final page advertised continuation: %s err=%v", output, err)
	}
	if _, err := executeCognitiveRecall(context.Background(), 7, "recall_cognitive_memory", fmt.Sprintf(`{"memory_id":%d,"from_time":"2026-01-01 00:00:00"}`, record.ID)); err == nil {
		t.Fatal("direct cognitive memory read silently ignored a time filter")
	}
}

func TestCognitiveRecallSeparatesConcernVersionAndReviewTimes(t *testing.T) {
	db := cognitiveTestDB(t)
	concernAt := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	formedAt := concernAt.Add(24 * time.Hour)
	reviewedAt := formedAt.Add(24 * time.Hour)
	record := model.CognitiveMemory{PersonID: 7, Description: "A continuing concern", Embedding: []byte{}, ImpressionEmbedding: []byte{}, CreatedAt: concernAt}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	for _, version := range []model.CognitiveMemoryVersion{
		{MemoryID: record.ID, Revision: 1, Impression: "Still at the start", Understanding: "Initial view", ProcessID: 1, CreatedAt: concernAt},
		{MemoryID: record.ID, Revision: 2, Impression: "It has moved on", Understanding: "Revised view", ProcessID: 2, CreatedAt: formedAt},
	} {
		if err := db.Create(&version).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&record).UpdateColumn("updated_at", reviewedAt).Error; err != nil {
		t.Fatal(err)
	}
	type candidate struct {
		Understanding                string    `json:"understanding"`
		ConcernCreatedAt             time.Time `json:"concern_created_at"`
		CurrentUnderstandingFormedAt time.Time `json:"current_understanding_formed_at"`
		LastReviewedAt               time.Time `json:"last_reviewed_at"`
	}
	check := func(item candidate) {
		t.Helper()
		if item.Understanding != "Revised view" || !item.ConcernCreatedAt.Equal(concernAt) || !item.CurrentUnderstandingFormedAt.Equal(formedAt) || !item.LastReviewedAt.Equal(reviewedAt) {
			t.Fatalf("cognitive times or current version confused: %+v", item)
		}
	}
	output, err := executeCognitiveRecall(context.Background(), 7, "recall_cognitive_memory", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Items []candidate `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &search); err != nil || len(search.Items) != 1 {
		t.Fatalf("search response=%s err=%v", output, err)
	}
	check(search.Items[0])
	if strings.Contains(output, `"created_at":`) || strings.Contains(output, `"updated_at":`) {
		t.Fatalf("search response has ambiguous timestamps: %s", output)
	}
	output, err = executeCognitiveRecall(context.Background(), 7, "recall_cognitive_memory", fmt.Sprintf(`{"memory_id":%d}`, record.ID))
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Memory candidate `json:"memory"`
	}
	if err := json.Unmarshal([]byte(output), &detail); err != nil {
		t.Fatal(err)
	}
	check(detail.Memory)
}

func TestRecallEventDescriptionMatchesLoopContext(t *testing.T) {
	description := func(definitions []llm.FunctionDefinition) string {
		t.Helper()
		for _, definition := range definitions {
			if definition.Name == "recall_event" {
				return definition.Description
			}
		}
		t.Fatal("recall_event tool is missing")
		return ""
	}
	if got := description(recallToolDefinitions(true)); !strings.Contains(got, "event that triggered this decision") {
		t.Fatalf("event Decide description=%q", got)
	}
	for _, definitions := range [][]llm.FunctionDefinition{recallToolDefinitions(false), cognitiveReadTools()} {
		if got := description(definitions); !strings.Contains(got, "browse recent observed events") || strings.Contains(got, "triggered this decision") {
			t.Fatalf("no-event description=%q", got)
		}
	}
}

func TestCognitiveRecallResponseRebuildsNextRequest(t *testing.T) {
	db := cognitiveTestDB(t)
	if err := db.Create(&model.Person{ID: 7, Name: "Memory owner", Type: model.PersonTypeAI}).Error; err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().Add(-time.Minute)
	settings := config.Get()
	oldRoot := settings.DataRoot
	settings.DataRoot = t.TempDir()
	t.Cleanup(func() { settings.DataRoot = oldRoot })
	event := model.Event{EventType: model.EventTypeSystemNotification, PayloadJSON: `"a witnessed change"`}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 7, EventID: event.ID}).Error; err != nil {
		t.Fatal(err)
	}
	newEvidence, err := recentCognitiveEvidenceSince(context.Background(), 7, startedAt)
	if err != nil || !strings.Contains(newEvidence, "witnessed change") {
		t.Fatalf("resumed evidence=%q err=%v", newEvidence, err)
	}
	request := cognitiveLLMRequest{Model: "test-model", ToolChoice: "required", Messages: []llm.Message{{Role: "user", Content: "Consider what changed"}}, Tools: cognitiveReadTools()}
	var response llm.ToolResponse
	responseJSON := fmt.Sprintf(`{"finish_reason":"tool_calls","tool_calls":[{"id":"call-1","type":"function","function":{"name":"recall_event","arguments":%q}}]}`, fmt.Sprintf(`{"event_id":%d}`, event.ID))
	if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
		t.Fatal(err)
	}
	process := model.CognitiveProcess{ID: 9, PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: 1}
	next, err := advanceCognitiveRecall(context.Background(), process, request, response)
	if err != nil {
		t.Fatal(err)
	}
	if cognitiveRequestNumber(next.Messages) != 2 || len(next.Messages) != 3 || next.Messages[2].Role != "tool" || !strings.Contains(next.Messages[2].Content, "witnessed change") {
		t.Fatalf("next request=%+v", next)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCognitiveCheckpoint(7, 9, cognitiveCheckpoint{Request: request, Response: data}); err != nil {
		t.Fatal(err)
	}
	// A source committed before the file write is still new to the model
	// request saved in that file, so the process-start boundary must find it.
	resumedEvidence, err := recentCognitiveEvidenceSince(context.Background(), 7, startedAt)
	if err != nil || !strings.Contains(resumedEvidence, "witnessed change") {
		t.Fatalf("checkpoint write hid an unseen observation: %q err=%v", resumedEvidence, err)
	}
	read, err := readCognitiveCheckpoint(7, 9)
	if err != nil {
		t.Fatal(err)
	}
	var replay llm.ToolResponse
	if err := json.Unmarshal(read.Response, &replay); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := advanceCognitiveRecall(context.Background(), process, read.Request, replay)
	if err != nil || rebuilt.Messages[2].Content != next.Messages[2].Content {
		t.Fatalf("resumed request=%+v err=%v", rebuilt, err)
	}
	continued, err := advanceCognitiveRecall(context.Background(), process, rebuilt, replay)
	if err != nil || len(continued.Messages) != 5 || continued.Messages[0].Content != request.Messages[0].Content || continued.Messages[2].Content != rebuilt.Messages[2].Content {
		t.Fatalf("multi-turn history was reset: %+v err=%v", continued, err)
	}
}

func TestCognitiveRecallKeepsLargeToolResult(t *testing.T) {
	db := cognitiveTestDB(t)
	record := model.CognitiveMemory{PersonID: 7, Description: "A long understanding", Embedding: []byte{}, ImpressionEmbedding: []byte{}}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	understanding := strings.Repeat("evidence remains available ", 1600)
	version := model.CognitiveMemoryVersion{MemoryID: record.ID, Revision: 1, Impression: "A long impression", Understanding: understanding, ProcessID: 1}
	if err := db.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	request := cognitiveLLMRequest{Model: "test-model", ToolChoice: "required", Messages: []llm.Message{{Role: "user", Content: "Review this understanding"}}, Tools: cognitiveReadTools()}
	var response llm.ToolResponse
	responseJSON := fmt.Sprintf(`{"finish_reason":"tool_calls","tool_calls":[{"id":"call-1","type":"function","function":{"name":"recall_cognitive_memory","arguments":%q}}]}`, fmt.Sprintf(`{"memory_id":%d,"page_size":1}`, record.ID))
	if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
		t.Fatal(err)
	}
	process := model.CognitiveProcess{ID: 9, PersonID: 7, Status: model.CognitiveProcessRunning, Epoch: 1}
	next, err := advanceCognitiveRecall(context.Background(), process, request, response)
	if err != nil || len(next.Messages) != 3 {
		t.Fatalf("large recall did not return a tool message: count=%d err=%v", len(next.Messages), err)
	}
	if !strings.Contains(next.Messages[2].Content, understanding) {
		t.Fatalf("large recall result was not preserved: bytes=%d", len(next.Messages[2].Content))
	}
}

func TestCognitiveIntentSearchPrecedesTargetSelection(t *testing.T) {
	db := cognitiveTestDB(t)
	settings := config.Get()
	oldRoot := settings.DataRoot
	settings.DataRoot = t.TempDir()
	t.Cleanup(func() { settings.DataRoot = oldRoot })
	record := model.CognitiveMemory{PersonID: 7, Description: "shared plans", Embedding: []byte{}, ImpressionEmbedding: []byte{}}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CognitiveMemoryVersion{MemoryID: record.ID, Revision: 1, Impression: "Plans we share", Understanding: "We plan together", ProcessID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	var intentCall llm.ToolCall
	if err := json.Unmarshal([]byte(`{"id":"intent-1","type":"function","function":{"name":"express_cognition","arguments":"{\"need_understanding\":true,\"description\":\"shared plans\"}"}}`), &intentCall); err != nil {
		t.Fatal(err)
	}
	intent, err := cognitiveIntentInConversation([]llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{intentCall}}})
	if err != nil || intent.Description != "shared plans" {
		t.Fatalf("intention=%+v err=%v", intent, err)
	}
	output, err := searchCognitiveCandidates(context.Background(), 7, intent)
	if err != nil || !strings.Contains(output, `"description":"shared plans"`) {
		t.Fatalf("system candidate search=%q err=%v", output, err)
	}
	request := cognitiveLLMRequest{Model: "test-model", ToolChoice: "required", Messages: []llm.Message{{Role: "user", Content: "Recent experience"}}, Tools: []llm.FunctionDefinition{cognitiveIntentTool()}}
	searchResponse := llm.ToolResponse{ToolCalls: []llm.ToolCall{intentCall}, FinishReason: "tool_calls"}
	selectionRequest := appendCognitiveToolResult(request, searchResponse, output)
	selectionRequest.Tools = []llm.FunctionDefinition{cognitiveSelectionTool()}
	if len(selectionRequest.Tools) != 1 || selectionRequest.Tools[0].Name != "choose_cognitive_memory" {
		t.Fatalf("selection stage tools=%+v", selectionRequest.Tools)
	}
	var selectionCall llm.ToolCall
	selectionJSON := fmt.Sprintf(`{"id":"choose-1","type":"function","function":{"name":"choose_cognitive_memory","arguments":%q}}`, fmt.Sprintf(`{"proceed":true,"memory_id":%d,"revision":1}`, record.ID))
	if err := json.Unmarshal([]byte(selectionJSON), &selectionCall); err != nil {
		t.Fatal(err)
	}
	target, proceed, err := selectedCognitiveRequest(selectionRequest.Messages, selectionCall)
	if err != nil || !proceed || target.MemoryID != record.ID || target.Revision != 1 {
		t.Fatalf("selected target=%+v proceed=%t err=%v", target, proceed, err)
	}
	selected := appendCognitiveToolResult(selectionRequest, llm.ToolResponse{ToolCalls: []llm.ToolCall{selectionCall}}, "Selected existing memory")
	if resumed, err := cognitiveTargetInConversation(selected.Messages); err != nil || resumed != target {
		t.Fatalf("target lost across conversation continuation: %+v err=%v", resumed, err)
	}
	if err := writeCognitiveCheckpoint(7, 91, cognitiveCheckpoint{Request: selected, Response: emptyCognitiveResponse()}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := readCognitiveCheckpoint(7, 91)
	if err != nil {
		t.Fatal(err)
	}
	if resumed, err := cognitiveTargetInConversation(checkpoint.Request.Messages); err != nil || resumed != target {
		t.Fatalf("target lost from checkpoint: %+v err=%v", resumed, err)
	}
	var newCall llm.ToolCall
	if err := json.Unmarshal([]byte(`{"id":"choose-new","type":"function","function":{"name":"choose_cognitive_memory","arguments":"{\"proceed\":true,\"memory_id\":0,\"revision\":0}"}}`), &newCall); err != nil {
		t.Fatal(err)
	}
	if fresh, proceed, err := selectedCognitiveRequest(selectionRequest.Messages, newCall); err != nil || !proceed || fresh.Description != intent.Description {
		t.Fatalf("new target did not retain intention: %+v proceed=%t err=%v", fresh, proceed, err)
	}
	var unrelated llm.ToolCall
	if err := json.Unmarshal([]byte(`{"id":"choose-2","type":"function","function":{"name":"choose_cognitive_memory","arguments":"{\"proceed\":true,\"memory_id\":999,\"revision\":1}"}}`), &unrelated); err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectedCognitiveRequest(selectionRequest.Messages, unrelated); err == nil {
		t.Fatal("selection accepted a memory absent from the system's candidates")
	}
	if _, err := decodeCognitiveToolArgs[cognitiveIntent](`{"description":"shared plans"}`, "need_understanding", "description"); err == nil {
		t.Fatal("missing intent choice silently became a declined cognition")
	}
}

// TestCognitivePreviewShowsDatedThread verifies that the opening preview
// exposes a multi-day thread from the dated index before the subject chooses a
// concern, while keeping today and other owners out.
func TestCognitivePreviewShowsDatedThread(t *testing.T) {
	db := cognitiveTestDB(t)
	now := energy.Now()
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, energy.Timezone())
	for i := 1; i <= 3; i++ {
		day := base.AddDate(0, 0, -i)
		row := model.DailyMemory{PersonID: 7, LocalDate: day.Format("2006-01-02"), GroupType: model.DailyMemoryGroupChat, GroupID: 9, Summary: fmt.Sprintf("Day -%d of the same thread", i), StartAt: day, EndAt: day, Embedding: []byte{}}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, excluded := range []model.DailyMemory{
		{PersonID: 7, LocalDate: energy.Today(), GroupType: model.DailyMemoryGroupHeartbeat, Summary: "Today's unfinished day", StartAt: now, EndAt: now, Embedding: []byte{}},
		{PersonID: 7, LocalDate: base.AddDate(0, 0, -8).Format("2006-01-02"), GroupType: model.DailyMemoryGroupChat, GroupID: 9, Summary: "Outside the preview window", StartAt: now, EndAt: now, Embedding: []byte{}},
		{PersonID: 8, LocalDate: base.AddDate(0, 0, -1).Format("2006-01-02"), GroupType: model.DailyMemoryGroupChat, GroupID: 9, Summary: "Another owner's thread", StartAt: now, EndAt: now, Embedding: []byte{}},
	} {
		if err := db.Create(&excluded).Error; err != nil {
			t.Fatal(err)
		}
	}
	preview, err := recentDatedRecollections(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(preview, fmt.Sprintf("Day -%d of the same thread", i)) {
			t.Fatalf("dated preview omits day -%d: %s", i, preview)
		}
	}
	for _, unwanted := range []string{"Today's unfinished day", "Outside the preview window", "Another owner's thread"} {
		if strings.Contains(preview, unwanted) {
			t.Fatalf("dated preview leaked %q: %s", unwanted, preview)
		}
	}
}
