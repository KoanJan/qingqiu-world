package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLLMConfigSaveWarnsAndRetestRepairsProbe(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/probe.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close(); database.DB = previous })
	if err := db.AutoMigrate(&model.LLMConfig{}, &model.SystemLLMConfig{}, &model.AgentConfig{}); err != nil {
		t.Fatal(err)
	}

	failRequired := true
	var requestCount atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		var req struct {
			ToolChoice json.RawMessage `json:"tool_choice"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if failRequired && string(req.ToolChoice) == `"required"` {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"required tool choice rejected"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"compatibility_check","arguments":"{\"value\":\"ok\"}"}}]}}]}`))
	}))
	defer provider.Close()

	h := &Handler{}
	call := func(method, path string, body any, id string, fn func(*gin.Context)) schema.LLMConfigResponse {
		t.Helper()
		payload, _ := json.Marshal(body)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(method, path, bytes.NewReader(payload))
		ctx.Request.Header.Set("Content-Type", "application/json")
		if id != "" {
			ctx.Params = gin.Params{{Key: "id", Value: id}}
		}
		fn(ctx)
		var envelope struct {
			Code int                      `json:"code"`
			Data schema.LLMConfigResponse `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Code != 0 {
			t.Fatalf("request failed: %s", recorder.Body.String())
		}
		return envelope.Data
	}
	created := call(http.MethodPost, "/api/llm-configs", map[string]any{"name": "test", "model_id": "model", "base_url": provider.URL, "api_key": "secret"}, "", h.CreateLLMConfig)
	if created.Probe.Status != model.ProbeStatusFailed || created.Probe.RequiredTools.OK || !created.Probe.StructuredOutput.OK || created.Probe.CheckedAt == nil {
		t.Fatalf("failed probe not reported: %+v", created.Probe)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("create made %d probe requests", requestCount.Load())
	}
	stored, err := dops.GetLLMConfig(created.ID)
	if err != nil || stored.ProbeStatus != model.ProbeStatusFailed {
		t.Fatalf("failed config not saved: %+v, %v", stored, err)
	}

	failRequired = false
	retested := call(http.MethodPost, "/api/llm-configs/1/probe", nil, "1", h.ProbeLLMConfig)
	if retested.Probe.Status != model.ProbeStatusPassed {
		t.Fatalf("retest: %+v", retested.Probe)
	}
	if requestCount.Load() != 4 {
		t.Fatalf("retest made %d total probe requests", requestCount.Load())
	}
	updated := call(http.MethodPut, "/api/llm-configs/1", map[string]any{"model_id": "new-model"}, "1", h.UpdateLLMConfig)
	if updated.Probe.Status != model.ProbeStatusPassed {
		t.Fatalf("edit probe: %+v", updated.Probe)
	}
	if updated.ModelID != "new-model" {
		t.Fatalf("model edit lost: %q", updated.ModelID)
	}
	if updated.Probe.CheckedAt == nil {
		t.Fatal("missing probe check time")
	}
	if requestCount.Load() != 6 {
		t.Fatalf("model edit made %d total probe requests", requestCount.Load())
	}
	credentialEdit := call(http.MethodPut, "/api/llm-configs/1", map[string]any{"api_key": "new-secret"}, "1", h.UpdateLLMConfig)
	if credentialEdit.Probe.Status != model.ProbeStatusPassed || requestCount.Load() != 8 {
		t.Fatalf("credential edit did not rerun probe: %+v, %d requests", credentialEdit.Probe, requestCount.Load())
	}
	metadataEdit := call(http.MethodPut, "/api/llm-configs/1", map[string]any{"name": "renamed"}, "1", h.UpdateLLMConfig)
	if metadataEdit.Probe.Status != model.ProbeStatusPassed || requestCount.Load() != 8 ||
		metadataEdit.Probe.CheckedAt == nil || !metadataEdit.Probe.CheckedAt.Equal(*credentialEdit.Probe.CheckedAt) {
		t.Fatalf("metadata edit changed probe result: %+v, %d requests", metadataEdit.Probe, requestCount.Load())
	}
	if err := db.Model(&model.LLMConfig{}).Where("id = ?", updated.ID).Update("probe_config_hash", "").Error; err != nil {
		t.Fatal(err)
	}
	legacyRetest := call(http.MethodPost, "/api/llm-configs/1/probe", nil, "1", h.ProbeLLMConfig)
	if legacyRetest.Probe.Status != model.ProbeStatusPassed {
		t.Fatalf("legacy config retest: %+v", legacyRetest.Probe)
	}
}
