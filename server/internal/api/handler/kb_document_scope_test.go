package handler

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestDocumentRouteRejectsDocumentFromOtherKB keeps a nested document route
// scoped to the KB in its URL, including its destructive path.
func TestDocumentRouteRejectsDocumentFromOtherKB(t *testing.T) {
	root := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "documents.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.KnowledgeBase{}, &model.Document{}, &model.DocumentChunk{},
		&model.KBRelation{}, &model.KBRelationEvidence{},
	); err != nil {
		t.Fatalf("migrate document tables: %v", err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = previousDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	first := &model.KnowledgeBase{Name: "first"}
	second := &model.KnowledgeBase{Name: "second"}
	for _, kb := range []*model.KnowledgeBase{first, second} {
		if err := db.Create(kb).Error; err != nil {
			t.Fatalf("create KB: %v", err)
		}
	}
	filePath := filepath.Join(root, "second.md")
	if err := os.WriteFile(filePath, []byte("second KB source"), 0600); err != nil {
		t.Fatalf("create document source: %v", err)
	}
	document := &model.Document{KnowledgeBaseID: second.ID, Title: "second.md", FilePath: filePath}
	if err := db.Create(document).Error; err != nil {
		t.Fatalf("create document: %v", err)
	}

	router := gin.New()
	h := &Handler{}
	router.GET("/kb/:id/documents/:doc_id", h.GetDocument)
	router.DELETE("/kb/:id/documents/:doc_id", h.DeleteDocument)
	path := func(kbID int64) string {
		return "/kb/" + strconv.FormatInt(kbID, 10) + "/documents/" + strconv.FormatInt(document.ID, 10)
	}
	request := func(method, path string) struct {
		Code int `json:"code"`
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	} {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		var result struct {
			Code int `json:"code"`
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %s %s response: %v", method, path, err)
		}
		return result
	}

	if got := request("GET", path(first.ID)); got.Code != response.CodeNotFound {
		t.Fatalf("foreign KB GET code = %d, want not found", got.Code)
	}
	if got := request("DELETE", path(first.ID)); got.Code != response.CodeNotFound {
		t.Fatalf("foreign KB DELETE code = %d, want not found", got.Code)
	}
	var count int64
	if err := db.Model(&model.Document{}).Where("id = ?", document.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("foreign KB delete changed document: count=%d err=%v", count, err)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("foreign KB delete removed source file: %v", err)
	}
	if got := request("GET", path(second.ID)); got.Code != response.CodeSuccess || got.Data.ID != document.ID {
		t.Fatalf("own KB GET = %+v, want document %d", got, document.ID)
	}
	if got := request("DELETE", path(second.ID)); got.Code != response.CodeSuccess {
		t.Fatalf("own KB DELETE code = %d, want success", got.Code)
	}
	if err := db.Model(&model.Document{}).Where("id = ?", document.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("own KB delete left document: count=%d err=%v", count, err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("own KB delete left source file: stat err=%v", err)
	}
}
