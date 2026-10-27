package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

// TestVectorBackfillKeepsReviewTimeAndDoesNotBlockOtherRows checks that a
// failed dated account leaves room for a cognitive repair in the same pass.
// Vector repair is storage maintenance and must not count as a new review.
func TestVectorBackfillKeepsReviewTimeAndDoesNotBlockOtherRows(t *testing.T) {
	recallTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Input) != 1 {
			http.Error(w, "expected one input", http.StatusBadRequest)
			return
		}
		if strings.Contains(request.Input[0], "unavailable") {
			http.Error(w, "embedding unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}],"model":"test","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer server.Close()
	previous := embeddingSvc
	embeddingSvc = llm.NewEmbeddingService(server.URL, "test-key", "test", 2)
	t.Cleanup(func() { embeddingSvc = previous })

	lastReview := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	row := model.CognitiveMemory{PersonID: 7, Description: "An enduring concern", Embedding: []byte{}, ImpressionEmbedding: []byte{}}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&model.CognitiveMemory{}).Where("id = ?", row.ID).UpdateColumn("updated_at", lastReview).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&model.CognitiveMemoryVersion{MemoryID: row.ID, Revision: 1, Impression: "A lasting impression", Understanding: "An understanding", ProcessID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for shard, summary := range []string{"unavailable dated account", "available dated account"} {
		daily := model.DailyMemory{PersonID: 7, LocalDate: "2026-10-25", Shard: shard, GroupType: 1,
			GroupID: int64(shard + 1), Summary: summary, Embedding: []byte{}, StartAt: lastReview, EndAt: lastReview}
		if err := database.DB.Create(&daily).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := backfillMemoryVectors(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	var repaired model.CognitiveMemory
	if err := database.DB.First(&repaired, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !repaired.UpdatedAt.Equal(lastReview) {
		t.Fatalf("vector repair advanced last review from %s to %s", lastReview, repaired.UpdatedAt)
	}
	if len(repaired.Embedding) == 0 || len(repaired.ImpressionEmbedding) == 0 {
		t.Fatal("cognitive vectors were blocked by the failed dated account")
	}
	var dailies []model.DailyMemory
	if err := database.DB.Order("shard").Find(&dailies).Error; err != nil {
		t.Fatal(err)
	}
	if len(dailies) != 2 || len(dailies[0].Embedding) != 0 || len(dailies[1].Embedding) == 0 {
		t.Fatalf("unexpected dated account vector results: %+v", dailies)
	}
}
