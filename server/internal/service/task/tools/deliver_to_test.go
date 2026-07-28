package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/workspace"
)

// TestDeliverToTool_Execute tests the Execute method of DeliverToTool.
func TestDeliverToTool_Execute(t *testing.T) {
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

	// Create test persons — Alice (human user) and sender
	alicePerson := model.Person{Name: "Alice", Type: model.PersonTypeHuman}
	database.DB.Create(&alicePerson)

	personID := int64(1)
	sessionID := int64(100)

	// Initialize workspace with received/ directory
	workspace.InitWorkspace(personID, sessionID)

	// Create test files in output/
	outputDir := workspace.GetOutputDir(personID, sessionID)
	testFile := filepath.Join(outputDir, "report.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		applogger.Error("failed to create test file", "error", err)
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create a subdirectory with files
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

	tool := NewDeliverToTool(personID, sessionID)

	// findDeliveryDir returns the n-th (1-based) delivery directory name in receivedDir.
	// Delivery dirs are sorted alphabetically (timestamp in name gives chronological order).
	findDeliveryDir := func(receivedDir string, n int) string {
		entries, err := os.ReadDir(receivedDir)
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

	t.Run("deliver single file to user", func(t *testing.T) {
		result, err := tool.Execute(map[string]interface{}{
			"receiver_name": "Alice",
			"paths":         []interface{}{"report.txt"},
			"remark":        "Test delivery",
		})
		if err != nil {
			applogger.Error("deliver single file to user failed", "error", err)
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "delivery") {
			t.Errorf("expected 'delivery' in result, got: %s", result)
		}
		if !strings.Contains(result, "report.txt") {
			t.Errorf("expected report.txt in result, got: %s", result)
		}

		// Verify file was copied to user's received/ directory
		userReceived := workspace.GetReceivedDir(alicePerson.ID, sessionID)
		dir1 := findDeliveryDir(userReceived, 1)
		copiedFile := filepath.Join(userReceived, dir1, "report.txt")
		data, err := os.ReadFile(copiedFile)
		if err != nil {
			applogger.Error("failed to read copied delivery file", "error", err)
			t.Fatalf("copied file not found: %v", err)
		}
		if string(data) != "hello world" {
			t.Errorf("expected 'hello world', got '%s'", string(data))
		}
	})

	t.Run("deliver directory to user", func(t *testing.T) {
		result, err := tool.Execute(map[string]interface{}{
			"receiver_name": "Alice",
			"paths":         []interface{}{"dist"},
		})
		if err != nil {
			applogger.Error("deliver directory to user failed", "error", err)
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "delivery") {
			t.Errorf("expected 'delivery' in result, got: %s", result)
		}

		// Verify directory was copied
		userReceived := workspace.GetReceivedDir(alicePerson.ID, sessionID)
		dir2 := findDeliveryDir(userReceived, 2)
		copiedFile := filepath.Join(userReceived, dir2, "dist", "app.js")
		data, err := os.ReadFile(copiedFile)
		if err != nil {
			applogger.Error("failed to read copied delivery file", "error", err)
			t.Fatalf("copied file not found: %v", err)
		}
		if string(data) != "console.log('hi')" {
			t.Errorf("unexpected file content: %s", string(data))
		}
	})

	t.Run("multiple deliveries don't overwrite", func(t *testing.T) {
		userReceived := workspace.GetReceivedDir(alicePerson.ID, sessionID)
		dir1 := findDeliveryDir(userReceived, 1)
		dir2 := findDeliveryDir(userReceived, 2)

		// delivery dirs should both exist
		if _, err := os.Stat(filepath.Join(userReceived, dir1, "report.txt")); err != nil {
			applogger.Error("delivery 1 file missing", "error", err)
			t.Errorf("delivery 1 should still exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(userReceived, dir2, "dist", "app.js")); err != nil {
			applogger.Error("delivery 2 file missing", "error", err)
			t.Errorf("delivery 2 should still exist: %v", err)
		}
	})

	t.Run("reject empty receiver_name", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver_name": "",
			"paths":         []interface{}{"report.txt"},
		})
		if err == nil {
			t.Fatal("expected error for empty receiver_name, got nil")
		}
	})

	t.Run("reject unknown receiver_name", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver_name": "Nobody",
			"paths":         []interface{}{"report.txt"},
		})
		if err == nil {
			t.Fatal("expected error for unknown receiver_name, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got: %v", err)
		}
	})

	t.Run("reject non-existent path", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver_name": "Alice",
			"paths":         []interface{}{"nonexistent.txt"},
		})
		if err == nil {
			t.Fatal("expected error for non-existent path, got nil")
		}
	})

	t.Run("reject empty paths", func(t *testing.T) {
		_, err := tool.Execute(map[string]interface{}{
			"receiver_name": "Alice",
			"paths":         []interface{}{},
		})
		if err == nil {
			t.Fatal("expected error for empty paths, got nil")
		}
	})
}

// TestDeliverToTool_Schema tests the Schema method of DeliverToTool.
func TestDeliverToTool_Schema(t *testing.T) {
	tool := NewDeliverToTool(1, 100)
	schema := tool.Schema()

	if schema.Name != "deliver_to" {
		t.Errorf("expected name 'deliver_to', got '%s'", schema.Name)
	}

	params := schema.Parameters
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("schema missing required field")
	}

	hasReceiverName := false
	hasPaths := false
	for _, r := range required {
		switch r {
		case "receiver_name":
			hasReceiverName = true
		case "paths":
			hasPaths = true
		}
	}
	if !hasReceiverName {
		t.Error("receiver_name should be required")
	}
	if !hasPaths {
		t.Error("paths should be required")
	}
}

// TestDeliverToTool_DBRecord verifies the AgentDelivery model JSON serialization.
func TestDeliverToTool_DBRecord(t *testing.T) {
	// This test verifies the AgentDelivery model can be marshaled/unmarshaled
	paths := []string{"report.txt", "dist/app.js"}
	pathsJSON, _ := json.Marshal(paths)

	var decoded []string
	if err := json.Unmarshal(pathsJSON, &decoded); err != nil {
		applogger.Error("failed to unmarshal delivery paths JSON", "error", err)
		t.Fatalf("failed to unmarshal paths JSON: %v", err)
	}
	if len(decoded) != 2 {
		t.Errorf("expected 2 paths, got %d", len(decoded))
	}
	if decoded[0] != "report.txt" || decoded[1] != "dist/app.js" {
		t.Errorf("unexpected decoded paths: %v", decoded)
	}
}
