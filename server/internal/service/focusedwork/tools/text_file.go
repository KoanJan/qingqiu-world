package tools

import (
	"fmt"
	"qingqiu-world-server/internal/service/aos"
	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// TextFileTool binds one shared text operation to Focus and its person scope.
type TextFileTool struct {
	core *servicetools.TextFileTools
	name ToolName
	servicetools.CycleDetector
}

func newTextFileTool(personID int64, workspaceDir string, name ToolName) *TextFileTool {
	return &TextFileTool{core: servicetools.NewTextFileTools(aos.GetAgentOwnedSpacePath(personID), workspaceDir), name: name}
}

// NewReadTextLinesTool creates the numbered-line reader for this Focus.
func NewReadTextLinesTool(personID int64, workspaceDir string) *TextFileTool {
	return newTextFileTool(personID, workspaceDir, ToolNameReadTextLines)
}

// NewReadTextBlockTool creates the byte-offset reader for this Focus.
func NewReadTextBlockTool(personID int64, workspaceDir string) *TextFileTool {
	return newTextFileTool(personID, workspaceDir, ToolNameReadTextBlock)
}

// NewSearchTextFileTool creates the literal file searcher for this Focus.
func NewSearchTextFileTool(personID int64, workspaceDir string) *TextFileTool {
	return newTextFileTool(personID, workspaceDir, ToolNameSearchTextFile)
}

func (t *TextFileTool) Name() ToolName { return t.name }
func (t *TextFileTool) Description() string {
	switch t.name {
	case ToolNameReadTextLines:
		return "Read bounded numbered text lines"
	case ToolNameReadTextBlock:
		return "Read bounded text from a source byte offset"
	case ToolNameSearchTextFile:
		return "Search literal text within one file"
	default:
		return "Unknown text file operation"
	}
}
func (t *TextFileTool) Schema() llm.FunctionDefinition {
	description, parameters := servicetools.TextToolSpec(t.name.String())
	return llm.FunctionDefinition{Name: t.name.String(), Description: description, Parameters: parameters}
}
func (t *TextFileTool) Execute(args map[string]interface{}) (string, error) {
	switch t.name {
	case ToolNameReadTextLines:
		return t.core.ReadTextLines(args)
	case ToolNameReadTextBlock:
		return t.core.ReadTextBlock(args)
	case ToolNameSearchTextFile:
		return t.core.SearchTextFile(args)
	default:
		return "", fmt.Errorf("unknown text file operation: %d", t.name)
	}
}
func (t *TextFileTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	s := t.CycleDetector.CycleDetect(args, result)
	return CycleStatus{Warning: s.Warning, Blocked: s.Blocked, Reason: s.Reason}
}
