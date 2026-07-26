package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"

	applogger "qingqiu-world-server/internal/logger"
)

// DeliverToTool copies files from the agent's output/ directory to another
// participant's received/ directory. Each delivery creates a numbered
// subdirectory (delivery_1, delivery_2, ...) to avoid conflicts between
// multiple deliveries in the same session.
type DeliverToTool struct {
	personID      int64
	sessionID     int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewDeliverToTool creates a DeliverToTool for the given person and session.
func NewDeliverToTool(personID, sessionID int64) *DeliverToTool {
	return &DeliverToTool{
		personID:  personID,
		sessionID: sessionID,
	}
}

// Name returns the tool name.
func (d *DeliverToTool) Name() ToolName { return ToolNameDeliverTo }

// Description returns a brief description of the tool.
func (d *DeliverToTool) Description() string {
	return "Deliver files from your output directory to another participant's received/ directory"
}

// Schema returns the LLM function definition for the tool.
func (d *DeliverToTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: d.Name().String(),
		Description: "Deliver files from your output/ directory to another participant. " +
			"The files will be copied to the recipient's received/ directory. Use this " +
			"when you have completed work products that someone else needs.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"receiver_name": map[string]interface{}{
					"type":        "string",
					"description": "The name of the recipient to deliver to.",
				},
				"paths": map[string]interface{}{
					"type":        "array",
					"description": "List of file or directory paths to deliver (relative to your output/ directory)",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
				"remark": map[string]interface{}{
					"type":        "string",
					"description": "Optional note describing what is being delivered and why",
				},
			},
			"required": []string{"receiver_name", "paths"},
		},
	}
}

// resolveReceiver looks up the receiver name in the persons table and returns the person_id.
func resolveReceiver(name string) (personID int64, err error) {
	var person model.Person
	if err := database.DB.Where("name = ?", name).First(&person).Error; err != nil {
		return 0, fmt.Errorf("recipient '%s' not found", name)
	}
	return person.ID, nil
}

// Execute copies files from the agent's output directory to the recipient's received directory.
func (d *DeliverToTool) Execute(args map[string]interface{}) (string, error) {
	// Parse receiver_name and resolve to agent ID.
	receiverName, ok := args["receiver_name"].(string)
	if !ok || receiverName == "" {
		return "", fmt.Errorf("receiver_name must be a non-empty string")
	}

	targetPersonID, err := resolveReceiver(receiverName)
	if err != nil {
		return "", err
	}

	// Parse paths
	pathsRaw, ok := args["paths"].([]interface{})
	if !ok {
		return "", fmt.Errorf("paths must be an array of strings")
	}
	if len(pathsRaw) == 0 {
		return "", fmt.Errorf("paths must not be empty")
	}

	var paths []string
	for _, p := range pathsRaw {
		pathStr, ok := p.(string)
		if !ok {
			return "", fmt.Errorf("each path must be a string")
		}
		paths = append(paths, pathStr)
	}

	// Parse remark (optional)
	remark := ""
	if r, ok := args["remark"].(string); ok {
		remark = r
	}

	// Validate each source path exists and is within output/
	outputDir := workspace.GetOutputDir(d.personID, d.sessionID)
	var validatedPaths []string
	for _, p := range paths {
		resolved, err := resolvePath(p, d.personID, d.sessionID)
		if err != nil {
			return "", fmt.Errorf("invalid path '%s': %w", p, err)
		}
		if _, err := os.Stat(resolved); os.IsNotExist(err) {
			return "", fmt.Errorf("path '%s' does not exist in your output/ directory", p)
		}
		// Record the relative path from output/ for DB and target
		relPath, err := filepath.Rel(outputDir, resolved)
		if err != nil {
			return "", fmt.Errorf("failed to resolve relative path for '%s': %w", p, err)
		}
		validatedPaths = append(validatedPaths, relPath)
	}

	// Determine delivery number by scanning existing delivery_* directories
	receivedDir := workspace.GetReceivedDir(targetPersonID, d.sessionID)
	if err := os.MkdirAll(receivedDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create received directory: %w", err)
	}

	// Generate delivery directory name: {senderName}_{yyyyMMddhhmmssSSS}
	var senderName string
	if err := database.DB.Model(&model.Person{}).Where("id = ?", d.personID).Select("name").Scan(&senderName).Error; err != nil {
		applogger.Error("failed to resolve sender name, using fallback", "error", err, "person_id", d.personID)
		senderName = fmt.Sprintf("person_%d", d.personID)
	}
	now := time.Now()
	ts := now.Format("20060102150405") + "_" + fmt.Sprintf("%06d", now.Nanosecond()/1e3)
	deliveryName := fmt.Sprintf("%s_%s", senderName, ts)
	deliveryDir := filepath.Join(receivedDir, deliveryName)
	if err := os.MkdirAll(deliveryDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create delivery directory: %w", err)
	}

	// Copy each path into the delivery directory
	for _, relPath := range validatedPaths {
		src := filepath.Join(outputDir, relPath)
		dst := filepath.Join(deliveryDir, relPath)

		// Ensure parent directory exists
		dstParent := filepath.Dir(dst)
		if err := os.MkdirAll(dstParent, 0755); err != nil {
			return "", fmt.Errorf("failed to create target directory: %w", err)
		}

		if err := copyPath(src, dst); err != nil {
			return "", fmt.Errorf("failed to copy '%s': %w", relPath, err)
		}
	}

	// Write delivery record to database
	pathsJSON, err := json.Marshal(validatedPaths)
	if err != nil {
		return "", fmt.Errorf("failed to marshal paths: %w", err)
	}

	record := model.AgentDelivery{
		FromPersonID: d.personID,
		ToPersonID:   targetPersonID,
		SessionID:    d.sessionID,
		Paths:        string(pathsJSON),
		Remark:       remark,
	}
	if err := database.DB.Create(&record).Error; err != nil {
		applogger.Error("Failed to write agent_delivery record", "error", err)
		// File copy succeeded, DB write failed — log but don't fail the delivery
	}

	// Build result message
	pathList := strings.Join(validatedPaths, ", ")
	result := fmt.Sprintf("Delivered %d file(s) to %s (delivery %s): %s",
		len(validatedPaths), receiverName, deliveryName, pathList)

	return result, nil
}

// copyPath copies a file or directory tree from src to dst.
// If src is a directory, its contents are copied recursively.
func copyPath(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return copyDirectory(src, dst)
	}
	return copyFile(src, dst)
}

// copyDirectory recursively copies a directory tree from src to dst.
func copyDirectory(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies a single file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
