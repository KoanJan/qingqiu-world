package handler

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// TestListActivityWorksAcceptsNoCursor verifies the first page needs no cursor
// while an explicitly invalid cursor is still rejected.
func TestListActivityWorksAcceptsNoCursor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("LOG_DIR", filepath.Join(root, "logs"))
	previous := database.DB
	database.Init()
	t.Cleanup(func() { database.DB = previous })
	create := func(value any) {
		t.Helper()
		if err := database.DB.Create(value).Error; err != nil {
			t.Fatalf("create %T: %v", value, err)
		}
	}
	person := &model.Person{Name: "activity owner", Type: model.PersonTypeAI, Status: model.PersonStatusActive}
	create(person)
	create(&model.AgentConfig{PersonID: person.ID})
	workspace := &model.Workspace{PersonID: person.ID, RelativePath: "work/1", Name: "sample"}
	create(workspace)
	work := &model.Work{PersonID: person.ID, Description: "sample work"}
	create(work)
	create(&model.WorkspaceUse{WorkspaceID: workspace.ID, SourceType: model.WorkspaceUseWork, SourceID: work.ID, Role: model.WorkspaceUseDefault})

	router := gin.New()
	router.GET("/agents/:id/workspaces/:workspace_id/works", (&Handler{}).ListActivityWorks)
	path := "/agents/" + strconv.FormatInt(person.ID, 10) + "/workspaces/" + strconv.FormatInt(workspace.ID, 10) + "/works"
	for _, testCase := range []struct {
		query    string
		wantCode int
	}{
		{query: "", wantCode: response.CodeSuccess},
		{query: "?before_work_id=0", wantCode: response.CodeBadRequest},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", path+testCase.query, nil))
		var body struct {
			Code int `json:"code"`
			Data struct {
				Works []struct {
					ID int64 `json:"id"`
				} `json:"works"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Code != testCase.wantCode {
			t.Fatalf("query %q: code = %d, want %d; body = %s", testCase.query, body.Code, testCase.wantCode, recorder.Body.String())
		}
		if testCase.wantCode == response.CodeSuccess && (len(body.Data.Works) != 1 || body.Data.Works[0].ID != work.ID) {
			t.Fatalf("first page works = %+v, want Work %d", body.Data.Works, work.ID)
		}
	}
}
