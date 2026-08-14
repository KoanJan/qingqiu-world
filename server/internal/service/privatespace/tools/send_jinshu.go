package tools

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/service/jinshu"
	"qingqiu-world-server/internal/service/llm"
)

// SendJinshuTool sends files from the agent's private-space working directory
// to another person as a jinshu (锦书). Unlike the task loop variant, there is
// no session context to embed; the source directory is the private-space workDir.
type SendJinshuTool struct {
	personID int64
	workDir  string
}

// NewSendJinshuTool creates a SendJinshuTool for the given person and private
// space. rootDir is kept for API symmetry with other privatespace tools but the
// source is intentionally restricted to workDir.
func NewSendJinshuTool(personID int64, rootDir, workDir string) *SendJinshuTool {
	return &SendJinshuTool{
		personID: personID,
		workDir:  workDir,
	}
}

func (t *SendJinshuTool) Name() string { return "send_jinshu" }
func (t *SendJinshuTool) Description() string {
	return "Send files from your private space to another person as a jinshu (锦书)"
}

func (t *SendJinshuTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: "send_jinshu",
		Description: "Send files from your private space to another person as a jinshu (锦书). " +
			"The files are copied to the recipient's jinshu/received/ directory, and a copy is kept " +
			"in your jinshu/sent/ directory.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"receiver": map[string]interface{}{
					"type":        "string",
					"description": "The name of the recipient person.",
				},
				"topic": map[string]interface{}{
					"type":        "string",
					"description": "A short subject/topic for this jinshu.",
				},
				"description": map[string]interface{}{
					"type":        "string",
					"description": "Optional note describing what is being sent and why.",
				},
				"paths": map[string]interface{}{
					"type":        "array",
					"description": "List of file or directory paths to send (relative to your private-space working directory)",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
			},
			"required": []string{"receiver", "topic", "paths"},
		},
	}
}

// Execute resolves the source paths relative to the private-space workDir and
// delegates the delivery to the shared jinshu.Send core.
func (t *SendJinshuTool) Execute(args map[string]interface{}) (string, error) {
	receiverName, ok := args["receiver"].(string)
	if !ok || receiverName == "" {
		return "", fmt.Errorf("receiver must be a non-empty string")
	}

	targetPerson, err := dops.GetPersonByName(receiverName)
	if err != nil {
		return "", fmt.Errorf("recipient '%s' not found", receiverName)
	}

	topic, ok := args["topic"].(string)
	if !ok || topic == "" {
		return "", fmt.Errorf("topic must be a non-empty string")
	}

	description := ""
	if d, ok := args["description"].(string); ok {
		description = d
	}

	paths, err := parsePaths(args["paths"])
	if err != nil {
		return "", err
	}

	files, relPaths, err := jinshu.ResolveWorkDirFiles(t.workDir, paths)
	if err != nil {
		return "", err
	}

	record, err := jinshu.Send(jinshu.SendParams{
		FromPersonID: t.personID,
		ToPersonID:   targetPerson.ID,
		Topic:        topic,
		Description:  description,
		Files:        files,
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Sent %d file(s) to %s (jinshu #%d, topic: %s): %s",
		len(relPaths), receiverName, record.ID, topic, strings.Join(relPaths, ", ")), nil
}

// parsePaths extracts and validates a "paths" argument as a non-empty list of
// strings.
func parsePaths(raw interface{}) ([]string, error) {
	pathsRaw, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("paths must be an array of strings")
	}
	if len(pathsRaw) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}

	paths := make([]string, 0, len(pathsRaw))
	for _, p := range pathsRaw {
		pathStr, ok := p.(string)
		if !ok {
			return nil, fmt.Errorf("each path must be a string")
		}
		paths = append(paths, pathStr)
	}
	return paths, nil
}
