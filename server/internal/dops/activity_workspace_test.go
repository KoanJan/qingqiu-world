package dops

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// TestActivityWorkspaceQueries verifies owner scope, historical identity, and
// default-versus-explicit Work display without duplicate activity ownership.
func TestActivityWorkspaceQueries(t *testing.T) {
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
	person := &model.Person{Name: "historical", Type: model.PersonTypeAI, Status: model.PersonStatusDeceased}
	other := &model.Person{Name: "other", Type: model.PersonTypeAI, Status: model.PersonStatusActive}
	create(person)
	create(other)
	create(&model.AgentConfig{PersonID: person.ID})
	create(&model.AgentConfig{PersonID: other.ID})
	primary := &model.Workspace{PersonID: person.ID, RelativePath: "work/1", Name: "primary"}
	extra := &model.Workspace{PersonID: person.ID, RelativePath: "work/2", Name: "extra"}
	foreign := &model.Workspace{PersonID: other.ID, RelativePath: "work/3", Name: "foreign"}
	create(primary)
	create(extra)
	create(foreign)
	work := &model.Work{PersonID: person.ID, Description: "prepare", Status: model.WorkStatusRunning}
	create(work)
	create(&model.WorkspaceUse{WorkspaceID: primary.ID, SourceType: model.WorkspaceUseWork, SourceID: work.ID, Role: model.WorkspaceUseDefault})
	create(&model.WorkspaceUse{WorkspaceID: primary.ID, SourceType: model.WorkspaceUseWork, SourceID: work.ID, Role: model.WorkspaceUseExplicit})
	create(&model.WorkspaceUse{WorkspaceID: extra.ID, SourceType: model.WorkspaceUseWork, SourceID: work.ID, Role: model.WorkspaceUseExplicit})

	agents, err := ListActivityAgents()
	if err != nil || len(agents) != 2 || agents[0].ID != person.ID || !agents[0].HasActiveWork {
		t.Fatalf("agents = %+v, err = %v", agents, err)
	}
	spaces, _, err := ListActivityWorkspaces(person.ID, 1, 50)
	if err != nil || len(spaces) != 2 || !spaces[0].HasActiveWork || !spaces[1].HasActiveWork {
		t.Fatalf("workspaces = %+v, err = %v", spaces, err)
	}
	for _, space := range spaces {
		if space.ID == foreign.ID {
			t.Fatalf("foreign workspace exposed: %+v", spaces)
		}
	}
	primaryWorks, _, err := ListActivityWorks(person.ID, primary.ID, 0, 30)
	if err != nil || len(primaryWorks) != 1 || primaryWorks[0].Role != model.WorkspaceUseDefault || primaryWorks[0].DefaultWorkspaceID != primary.ID {
		t.Fatalf("primary works = %+v, err = %v", primaryWorks, err)
	}
	extraWorks, _, err := ListActivityWorks(person.ID, extra.ID, 0, 30)
	if err != nil || len(extraWorks) != 1 || extraWorks[0].DefaultWorkspaceName != primary.Name || extraWorks[0].Role != model.WorkspaceUseExplicit {
		t.Fatalf("extra works = %+v, err = %v", extraWorks, err)
	}
	foreignWorks, _, err := ListActivityWorks(other.ID, primary.ID, 0, 30)
	if err != nil || len(foreignWorks) != 0 {
		t.Fatalf("owner scope failed: works = %+v, err = %v", foreignWorks, err)
	}
}
