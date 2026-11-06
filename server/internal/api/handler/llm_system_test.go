package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSystemLLMManualAssignmentRejectsFailedProbe(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/system-llm.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close(); database.DB = previous })
	if err := db.AutoMigrate(&model.LLMConfig{}, &model.SystemLLMConfig{}); err != nil {
		t.Fatal(err)
	}

	// The first config is assigned automatically, even if its probe failed.
	first := model.LLMConfig{Name: "first", ModelID: "model-1", BaseURL: "https://example.test", APIKey: "key", ProbeStatus: model.ProbeStatusFailed}
	first.ProbeConfigHash = first.ProbeFingerprint()
	first.ProbeCheckedAt = time.Now().UnixMilli()
	if err := dops.CreateLLMConfig(&first); err != nil {
		t.Fatal(err)
	}
	if current := dops.GetSystemLLMConfig(); current == nil || current.ID != first.ID {
		t.Fatalf("first config was not auto assigned: %+v", current)
	}

	candidate := model.LLMConfig{Name: "candidate", ModelID: "model-2", BaseURL: "https://example.test", APIKey: "key", ProbeStatus: model.ProbeStatusFailed}
	candidate.ProbeConfigHash = candidate.ProbeFingerprint()
	candidate.ProbeCheckedAt = time.Now().UnixMilli()
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	if err := dops.UpdateSystemLLMConfig(candidate.ID); !errors.Is(err, dops.ErrSystemLLMProbeFailed) {
		t.Fatalf("direct assignment should reject failed probe: %v", err)
	}

	assign := func() response.Response {
		t.Helper()
		body, err := json.Marshal(map[string]int64{"llm_config_id": candidate.ID})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/system-llm-config", bytes.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		(&Handler{}).UpdateSystemLLMConfigHandler(ctx)
		var result response.Response
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	if result := assign(); result.Code != response.CodeBadRequest || !strings.Contains(result.Message, "probe failed") {
		t.Fatalf("failed probe should return a client error: %+v", result)
	}
	if current := dops.GetSystemLLMConfig(); current == nil || current.ID != first.ID {
		t.Fatalf("failed assignment changed system model: %+v", current)
	}

	if err := db.Model(&candidate).Updates(map[string]interface{}{
		"probe_status":               model.ProbeStatusPassed,
		"probe_required_tools_ok":    true,
		"probe_structured_output_ok": true,
		"probe_checked_at":           time.Now().UnixMilli(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if result := assign(); result.Code != response.CodeSuccess {
		t.Fatalf("passing probe should allow assignment: %+v", result)
	}
	if current := dops.GetSystemLLMConfig(); current == nil || current.ID != candidate.ID {
		t.Fatalf("passing assignment did not update system model: %+v", current)
	}
}
