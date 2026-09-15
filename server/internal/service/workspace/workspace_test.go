package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	applogger "qingqiu-world-server/internal/logger"
)

// TestAOSPathsAndLocators verifies that session defaults remain compatible
// while explicit locators can access the agent-wide resource root.
func TestAOSPathsAndLocators(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 17
	const sessionID int64 = 23
	InitWorkspace(personID, sessionID)
	if _, _, _, err := InitPrivateSpace(personID); err != nil {
		t.Fatalf("initialize private AOS: %v", err)
	}

	outputFile := filepath.Join(GetOutputDir(personID, sessionID), "report.txt")
	privateFile := filepath.Join(GetPrivateSpacePath(personID), "idea.txt")
	if err := os.WriteFile(outputFile, []byte("report"), 0644); err != nil {
		t.Fatalf("write output resource: %v", err)
	}
	if err := os.WriteFile(privateFile, []byte("idea"), 0644); err != nil {
		t.Fatalf("write private resource: %v", err)
	}

	files, paths, err := ResolveAOSFiles(personID, GetOutputDir(personID, sessionID), []string{
		"report.txt", "private/idea.txt",
	})
	if err != nil {
		t.Fatalf("resolve AOS files: %v", err)
	}
	if len(files) != 2 || len(paths) != 2 || files["report.txt"] == "" || files["private/idea.txt"] == "" {
		t.Fatalf("unexpected resolved files: files=%v paths=%v", files, paths)
	}

	entries, err := InspectOwnedSpace(personID, "private", "idea", 10)
	if err != nil {
		t.Fatalf("inspect private AOS: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "private/idea.txt" {
		t.Fatalf("unexpected inspection entries: %+v", entries)
	}
	if _, err := NormalizeInspectionScope("work/../private"); err == nil {
		t.Fatal("expected traversal-like inspection scope to be rejected")
	}
	if _, err := os.Stat(GetMetaDir(personID, sessionID)); err != nil {
		t.Fatalf("session metadata must be outside AOS but initialized: %v", err)
	}
	if pathWithin(GetMetaDir(personID, sessionID), GetAgentOwnedSpacePath(personID)) {
		t.Fatal("session metadata must not be placed beneath Agent Owned Space")
	}
}

// TestInitAgentOwnedSpaceCreatesStableSkeleton verifies that a newly started
// agent can inspect its root, private root, and work root even before any
// session-specific Focus has been created.
func TestInitAgentOwnedSpaceCreatesStableSkeleton(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 29
	if err := InitAgentOwnedSpace(personID); err != nil {
		t.Fatalf("initialize agent AOS: %v", err)
	}

	for _, dir := range []string{
		GetAgentOwnedSpacePath(personID),
		GetPrivateSpacePath(personID),
		filepath.Join(GetAgentOwnedSpacePath(personID), "work"),
		GetAgentMetaPath(personID),
		GetPrivateMetaDir(personID),
		filepath.Join(GetAgentMetaPath(personID), "work"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected initialized directory %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected initialized path to be a directory: %s", dir)
		}
	}

	for _, scope := range []string{"root", "private", "work"} {
		if _, err := InspectOwnedSpace(personID, scope, "", 10); err != nil {
			t.Fatalf("inspect initialized scope %q: %v", scope, err)
		}
	}
}

// TestRemoveWorkspaceIsSessionScoped verifies that one session's paired
// AOS/AOSMeta cleanup cannot delete another session's resources.
func TestRemoveWorkspaceIsSessionScoped(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 53
	InitWorkspace(personID, 1)
	InitWorkspace(personID, 2)
	if err := os.WriteFile(filepath.Join(GetOutputDir(personID, 2), "keep.txt"), []byte("keep"), 0644); err != nil {
		t.Fatalf("write second-session resource: %v", err)
	}
	RemoveWorkspace(personID, 1)
	if _, err := os.Stat(filepath.Join(GetOutputDir(personID, 2), "keep.txt")); err != nil {
		t.Fatalf("session-scoped cleanup removed another session resource: %v", err)
	}
}
