package aos

import (
	"os"
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// TestAOSPathsAndLocators verifies explicit Workspace locators and their
// paired metadata without creating a Session-derived working directory.
func TestAOSPathsAndLocators(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 17
	if err := InitAgentOwnedSpace(personID); err != nil {
		t.Fatalf("initialize agent AOS: %v", err)
	}
	workspaceDir := filepath.Join(GetAgentOwnedSpacePath(personID), "work", "23")
	outputDir := filepath.Join(workspaceDir, "output")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	outputFile := filepath.Join(outputDir, "report.txt")
	privateFile := filepath.Join(GetPrivateSpacePath(personID), "idea.txt")
	if err := os.WriteFile(outputFile, []byte("report"), 0644); err != nil {
		t.Fatalf("write output resource: %v", err)
	}
	if err := os.WriteFile(privateFile, []byte("idea"), 0644); err != nil {
		t.Fatalf("write private resource: %v", err)
	}

	files, paths, err := ResolveAOSFiles(personID, workspaceDir, []string{
		"output/report.txt", "private/idea.txt",
	})
	if err != nil {
		t.Fatalf("resolve AOS files: %v", err)
	}
	if len(files) != 2 || len(paths) != 2 || files["output/report.txt"] == "" || files["private/idea.txt"] == "" {
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
	metaDir, err := GetWorkspaceMetaDir(model.Workspace{PersonID: personID, RelativePath: filepath.Join("work", "23")})
	if err != nil {
		t.Fatal(err)
	}
	if pathWithin(metaDir, GetAgentOwnedSpacePath(personID)) {
		t.Fatal("Workspace metadata must not be placed beneath Agent Owned Space")
	}
	legacyDir := filepath.Join(GetAgentOwnedSpacePath(personID), "work", "24")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyMeta, err := GetWorkspaceMetaDir(model.Workspace{PersonID: personID, RelativePath: filepath.Join("work", "24")})
	if err != nil || legacyMeta != filepath.Join(GetAgentMetaPath(personID), "work", "24", ".meta") {
		t.Fatalf("registered historical Workspace lost its existing metadata: path=%q err=%v", legacyMeta, err)
	}
}

// TestAvailableWorkDirectoryIDSkipsMetadataOnlyPath protects notes left behind
// by a missing resource directory from being assigned to a new Workspace.
func TestAvailableWorkDirectoryIDSkipsMetadataOnlyPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 18
	if err := os.MkdirAll(filepath.Join(GetAgentMetaPath(personID), "work", "5", ".meta"), 0755); err != nil {
		t.Fatal(err)
	}
	id, err := AvailableWorkDirectoryID(personID, 5)
	if err != nil || id != 6 {
		t.Fatalf("metadata-only directory was reused: id=%d err=%v", id, err)
	}
}

// TestNewWorkspaceMetadataCollisionPreservesExistingFiles verifies that a
// failed directory claim cannot erase metadata owned by an older activity.
func TestNewWorkspaceMetadataCollisionPreservesExistingFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 19
	record := model.Workspace{ID: 5, PersonID: personID, RelativePath: filepath.Join("work", "5")}
	oldMeta := filepath.Join(GetAgentMetaPath(personID), "work", "5", ".meta")
	if err := os.MkdirAll(oldMeta, 0755); err != nil {
		t.Fatal(err)
	}
	oldNotes := filepath.Join(oldMeta, "notes.jsonl")
	if err := os.WriteFile(oldNotes, []byte("old notes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareWorkspaceDirectories(record, true); err == nil {
		t.Fatal("expected metadata collision to reject the new Workspace")
	}
	if content, err := os.ReadFile(oldNotes); err != nil || string(content) != "old notes\n" {
		t.Fatalf("metadata was changed on failed creation: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(GetAgentOwnedSpacePath(personID), "work", "5")); !os.IsNotExist(err) {
		t.Fatalf("uncommitted Workspace resource directory remained: %v", err)
	}
}

// TestWorkSandboxPolicyDirKeepsOriginalLayout verifies the policy identity
// changes without adding a directory level to the AAC hierarchy.
func TestWorkSandboxPolicyDirKeepsOriginalLayout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	config.Init()
	if got, want := GetWorkSandboxPolicyDir(17, 42), filepath.Join(root, "aac", "17", "42"); got != want {
		t.Fatalf("sandbox policy directory = %s, want %s", got, want)
	}
}

// TestInitAgentOwnedSpaceCreatesStableSkeleton verifies that a newly started
// agent can inspect its root, private root, and Workspace root before Focus.
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
	if entries, err := InspectOwnedSpace(personID, "work", "", 10); err != nil || len(entries) != 0 {
		t.Fatalf("fresh agent should have an empty work branch: entries=%v err=%v", entries, err)
	}
}

// TestInitAgentOwnedSpacePreservesHistoricalFiles verifies that startup leaves
// registered historical directories in place without recreating their roots.
func TestInitAgentOwnedSpacePreservesHistoricalFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 53
	legacyDir := filepath.Join(GetAgentOwnedSpacePath(personID), "work", "2")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(legacyDir, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := InitAgentOwnedSpace(personID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("agent startup removed historical Workspace data: %v", err)
	}
}

// TestInitPrivateSpaceDoesNotCreateSessionWorkspace verifies that private
// initialization does not infer a work directory from any Session identity.
func TestInitPrivateSpaceDoesNotCreateSessionWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	config.Init()
	applogger.Init()

	const personID int64 = 61
	if _, _, _, err := InitPrivateSpace(personID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(GetPrivateMetaDir(personID)); err != nil {
		t.Fatalf("private metadata was not initialized: %v", err)
	}
	legacyDir := filepath.Join(GetAgentOwnedSpacePath(personID), "work", "44")
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("private handoff created a Session Workspace: %v", err)
	}
}
