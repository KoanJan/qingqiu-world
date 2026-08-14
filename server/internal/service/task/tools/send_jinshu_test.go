package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/jinshu"
	"qingqiu-world-server/internal/service/workspace"
)

// TestSendJinshuTool_Execute tests the Execute method of SendJinshuTool.
func TestSendJinshuTool_Execute(t *testing.T) {
	// Setup a temp workspace root. Must set before any config access.
	tmpRoot := t.TempDir()
	// macOS temp dirs are symlinks — resolve to real path for workspace safety checks
	realRoot, err := filepath.EvalSymlinks(tmpRoot)
	if err != nil {
		applogger.Error("failed to resolve temp dir symlink", "error", err)
		t.Fatalf("failed to resolve temp dir: %v", err)
	}
	os.Setenv("DATA_ROOT", realRoot)
	os.Setenv("WORKSPACE_ROOT", filepath.Join(realRoot, "workspace"))
	os.Setenv("LOG_DIR", filepath.Join(realRoot, "logs"))

	// Initialize logger and DB
	applogger.Init()
	database.Init()

	// Create test persons — Bob (sender) and Alice (recipient).
	bobPerson := model.Person{Name: "Bob", Type: model.PersonTypeAI}
	database.DB.Create(&bobPerson)
	alicePerson := model.Person{Name: "Alice", Type: model.PersonTypeHuman}
	database.DB.Create(&alicePerson)

	senderID := bobPerson.ID
	sessionID := int64(100)

	// Initialize the sender's session workspace with output/ directory.
	workspace.InitWorkspace(senderID, sessionID)

	// Create test files in output/.
	outputDir := workspace.GetOutputDir(senderID, sessionID)
	testFile := filepath.Join(outputDir, "report.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		applogger.Error("failed to create test file", "error", err)
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create a subdirectory with files.
	subDir := filepath.Join(outputDir, "dist")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		applogger.Error("failed to create test subdirectory", "error", err)
		t.Fatalf("failed to create subdir: %v", err)
	}
	subFile := filepath.Join(subDir, "app.js")
	if err := os.WriteFile(subFile, []byte("console.log('hi')"), 0644); err != nil {
		applogger.Error("failed to create test sub file", "error", err)
		t.Fatalf("failed to create sub file: %v", err)
	}

	tool := NewSendJinshuTool(senderID, sessionID)

	// findJinshuDir returns the n-th (1-based) jinshu directory name in baseDir.
	// Directories are sorted alphabetically (id in the name gives chronological order).
	findJinshuDir := func(baseDir string, n int) string {
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			return ""
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		if n < 1 || n > len(names) {
			return ""
		}
		return names[n-1]
	}

	sentDir := jinshu.SentDir(senderID)
	receivedDir := jinshu.ReceivedDir(alicePerson.ID)

	t.Run("send single file to user", func(t *testing.T) {
		result, err := tool.Execute(map[string]interface{}{
			"receiver":    "Alice",
			"topic":       "Monthly report",
			"description": "Please review",
			"paths":       []interface{}{"report.txt"},
		})
		if err != nil {
			applogger.Error("send single file to user failed", "error", err)
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "report.txt") {
			t.Errorf("expected report.txt in result, got: %s", result)
		}

		// Verify the file was copied into both the sender's sent/ and the
		// recipient's received/ directories.
		id := findJinshuDir(sentDir, 1)
		sentCopy := filepath.Join(sentDir, id, "report.txt")
		data, err := os.ReadFile(sentCopy)
		if err != nil {
			applogger.Error("failed to read sent copy", "error", err)
			t.Fatalf("sent copy not found: %v", err)
		}
		if string(data) != "hello world" {
			t.Errorf("expected 'hello world', got '%s'", string(data))
		}

		receivedCopy := filepath.Join(receivedDir, id, "report.txt")
		data, err = os.ReadFile(receivedCopy)
		if err != nil {
			applogger.Error("failed to read received copy", "error", err)
			t.Fatalf("received copy not found: %v", err)
		}
		if string(data) != "hello world" {
			t.Errorf("expected 'hello world', got '%s'", string(data))
		}
	})

	t.Run("send directory to user", func(t *testing.T) {
		result, err := tool.Execute(map[string]interface{}{
			"receiver": "Alice",
			"topic":    "Build artifacts",
			"paths":    []interface{}{"dist"},
		})
		if err != nil {
			applogger.Error("send directory to user failed", "error", err)
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "dist") {
			t.Errorf("expected dist in result, got: %s", result)
		}

		id := findJinshuDir(receivedDir, 2)
		copiedFile := filepath.Join(receivedDir, id, "dist", "app.js")
		data, err := os.ReadFile(copiedFile)
		if err != nil {
			applogger.Error("failed to read copied directory file", "error", err)
			t.Fatalf("copied file not found: %v", err)
		}
		if string(data) != "console.log('hi')" {
			t.Errorf("unexpected file content: %s", string(data))
		}
	})

	t.Run("multiple sends don't overwrite", func(t *testing.T) {
		id1 := findJinshuDir(sentDir, 1)
		id2 := findJinshuDir(sentDir, 2)

		if _, err := os.Stat(filepath.Join(sentDir, id1, "report.txt")); err != nil {
			applogger.Error("jinshu 1 file missing", "error", err)
			t.Errorf("jinshu 1 should still exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(sentDir, id2, "dist", "app.js")); err != nil {
			applogger.Error("jinshu 2 file missing", "error", err)
			t.Errorf("jinshu 2 should still exist: %v", err)
		}
	})

	t.Run("writes a Jinshu record with topic and description", func(t *testing.T) {
		var records []model.Jinshu
		if err := database.DB.Where("from_person_id = ?", senderID).Find(&records).Error; err != nil {
			applogger.Error("failed to query jinshu records", "error", err)
			t.Fatalf("failed to query jinshu records: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("expected 2 jinshu records, got %d", len(records))
		}
		// Records are ordered by id; the first send has topic "Monthly report".
		found := false
		for _, r := range records {
			if r.Topic == "Monthly report" && r.ToPersonID == alicePerson.ID {
				if r.Description == "" {
					t.Errorf("expected description to be non-empty")
				}
				found = true
			}
		}
		if !found {
			t.Errorf("expected a jinshu record with topic 'Monthly report' to Alice")
		}
	})

	t.Run("reject empty receiver", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver": "",
			"topic":    "x",
			"paths":    []interface{}{"report.txt"},
		})
		if err == nil {
			t.Fatal("expected error for empty receiver, got nil")
		}
	})

	t.Run("reject unknown receiver", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver": "Nobody",
			"topic":    "x",
			"paths":    []interface{}{"report.txt"},
		})
		if err == nil {
			t.Fatal("expected error for unknown receiver, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got: %v", err)
		}
	})

	t.Run("reject non-existent path", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver": "Alice",
			"topic":    "x",
			"paths":    []interface{}{"nonexistent.txt"},
		})
		if err == nil {
			t.Fatal("expected error for non-existent path, got nil")
		}
	})

	t.Run("reject empty paths", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver": "Alice",
			"topic":    "x",
			"paths":    []interface{}{},
		})
		if err == nil {
			t.Fatal("expected error for empty paths, got nil")
		}
	})

	t.Run("reject empty topic", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver": "Alice",
			"topic":    "",
			"paths":    []interface{}{"report.txt"},
		})
		if err == nil {
			t.Fatal("expected error for empty topic, got nil")
		}
	})
}

// TestSendJinshuTool_Schema tests the Schema method of SendJinshuTool.
func TestSendJinshuTool_Schema(t *testing.T) {
	tool := NewSendJinshuTool(1, 100)
	schema := tool.Schema()

	if schema.Name != "send_jinshu" {
		t.Errorf("expected name 'send_jinshu', got '%s'", schema.Name)
	}

	params := schema.Parameters
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("schema missing required field")
	}

	hasReceiver := false
	hasTopic := false
	hasPaths := false
	for _, r := range required {
		switch r {
		case "receiver":
			hasReceiver = true
		case "topic":
			hasTopic = true
		case "paths":
			hasPaths = true
		}
	}
	if !hasReceiver {
		t.Error("receiver should be required")
	}
	if !hasTopic {
		t.Error("topic should be required")
	}
	if !hasPaths {
		t.Error("paths should be required")
	}
}
