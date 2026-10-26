package tools

import (
	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// ScanKBTool performs semantic search over the agent's authorized knowledge
// bases. Thin wrapper over the person-free core in service/tools, binding the
// person's call-time KB authorization inventory.
type ScanKBTool struct {
	core *servicetools.ScanKBTool
}

// NewScanKBTool creates a ScanKBTool for the given person and Focus runtime.
// Authorization is resolved on every execution so grant changes take effect
// immediately; work/session IDs are recorded only as KB usage trace metadata.
func NewScanKBTool(personID, workID, sessionID int64) *ScanKBTool {
	core := servicetools.NewScanKBTool(servicetools.AuthorizedKBsFor(personID)).
		WithTraceContext(servicetools.ScanKBTraceContext{WorkID: workID, SessionID: sessionID})
	return &ScanKBTool{core: core}
}

// Name returns the tool name.
func (s *ScanKBTool) Name() ToolName { return ToolNameScanKB }

// Description returns a brief description of the tool.
func (s *ScanKBTool) Description() string { return servicetools.ScanKBDescription }

// Schema returns the LLM function definition for the tool.
func (s *ScanKBTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        s.Name().String(),
		Description: servicetools.ScanKBSchemaDescription,
		Parameters:  servicetools.ScanKBParameters(),
	}
}

// Execute runs the semantic search with authorization enforcement.
func (s *ScanKBTool) Execute(args map[string]interface{}) (string, error) {
	return s.core.Execute(args)
}

// CycleDetect forwards cycle detection to the core.
func (s *ScanKBTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	return s.core.CycleDetect(args, result)
}

// ReadKBEvidenceTool reads authorized active-revision evidence returned by a
// prior scan_kb call. It is progressive disclosure, not a raw file reader.
type ReadKBEvidenceTool struct {
	core *servicetools.ReadKBEvidenceTool
}

// NewReadKBEvidenceTool creates a direct evidence reader for the given person.
func NewReadKBEvidenceTool(personID int64) *ReadKBEvidenceTool {
	return &ReadKBEvidenceTool{core: servicetools.NewReadKBEvidenceTool(servicetools.AuthorizedKBsFor(personID))}
}

// Name returns the tool name.
func (t *ReadKBEvidenceTool) Name() ToolName { return ToolNameReadKBEvidence }

// Description returns a brief description of the tool.
func (t *ReadKBEvidenceTool) Description() string { return servicetools.ReadKBEvidenceDescription }

// Schema returns the function definition for progressive evidence disclosure.
func (t *ReadKBEvidenceTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        t.Name().String(),
		Description: servicetools.ReadKBEvidenceSchemaDescription,
		Parameters:  servicetools.ReadKBEvidenceParameters(),
	}
}

// Execute reads full evidence bodies with authorization and active-revision checks.
func (t *ReadKBEvidenceTool) Execute(args map[string]interface{}) (string, error) {
	return t.core.Execute(args)
}

// CycleDetect forwards cycle detection to the shared core.
func (t *ReadKBEvidenceTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	return t.core.CycleDetect(args, result)
}

// ListKBDocumentsTool lists the documents of one of the agent's authorized
// knowledge bases. It is the discovery step that feeds scan_kb's document
// filter. Thin wrapper over the person-free core in service/tools.
type ListKBDocumentsTool struct {
	core *servicetools.ListKBDocumentsTool
}

// NewListKBDocumentsTool creates a ListKBDocumentsTool for the given person.
// Authorization is resolved on every execution so grant changes take effect
// immediately.
func NewListKBDocumentsTool(personID int64) *ListKBDocumentsTool {
	return &ListKBDocumentsTool{core: servicetools.NewListKBDocumentsTool(servicetools.AuthorizedKBsFor(personID))}
}

// Name returns the tool name.
func (t *ListKBDocumentsTool) Name() ToolName { return ToolNameListKBDocuments }

// Description returns a brief description of the tool.
func (t *ListKBDocumentsTool) Description() string { return servicetools.ListKBDocumentsDescription }

// Schema returns the LLM function definition for the tool.
func (t *ListKBDocumentsTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        t.Name().String(),
		Description: servicetools.ListKBDocumentsSchemaDescription,
		Parameters:  servicetools.ListKBDocumentsParameters(),
	}
}

// Execute lists the documents of the requested KB with authorization enforcement.
func (t *ListKBDocumentsTool) Execute(args map[string]interface{}) (string, error) {
	return t.core.Execute(args)
}

// CycleDetect forwards cycle detection to the core.
func (t *ListKBDocumentsTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	return t.core.CycleDetect(args, result)
}
