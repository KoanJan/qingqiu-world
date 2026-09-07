package tools

import (
	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// ScanKBTool performs semantic search over the person's authorized knowledge
// bases. Thin wrapper over the person-free core in service/tools, binding the
// person's call-time KB authorization inventory.
type ScanKBTool struct {
	core *servicetools.ScanKBTool
}

// NewScanKBTool creates a ScanKBTool for the person. Authorization is resolved
// on every execution so grant changes take effect immediately.
func NewScanKBTool(personID int64) *ScanKBTool {
	return &ScanKBTool{core: servicetools.NewScanKBTool(servicetools.AuthorizedKBsFor(personID))}
}

// Name returns the tool name.
func (t *ScanKBTool) Name() string { return "scan_kb" }

// Description returns a brief description of the tool.
func (t *ScanKBTool) Description() string { return servicetools.ScanKBDescription }

// Schema returns the LLM function definition for the tool.
func (t *ScanKBTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        "scan_kb",
		Description: servicetools.ScanKBSchemaDescription,
		Parameters:  servicetools.ScanKBParameters(),
	}
}

// Execute runs the semantic search with authorization enforcement.
func (t *ScanKBTool) Execute(args map[string]interface{}) (string, error) {
	return t.core.Execute(args)
}

// ListKBDocumentsTool lists the documents of one of the person's authorized
// knowledge bases. It is the discovery step that feeds scan_kb's document
// filter. Thin wrapper over the person-free core in service/tools.
type ListKBDocumentsTool struct {
	core *servicetools.ListKBDocumentsTool
}

// NewListKBDocumentsTool creates a ListKBDocumentsTool for the person.
// Authorization is resolved on every execution so grant changes take effect
// immediately.
func NewListKBDocumentsTool(personID int64) *ListKBDocumentsTool {
	return &ListKBDocumentsTool{core: servicetools.NewListKBDocumentsTool(servicetools.AuthorizedKBsFor(personID))}
}

// Name returns the tool name.
func (t *ListKBDocumentsTool) Name() string { return "list_kb_documents" }

// Description returns a brief description of the tool.
func (t *ListKBDocumentsTool) Description() string { return servicetools.ListKBDocumentsDescription }

// Schema returns the LLM function definition for the tool.
func (t *ListKBDocumentsTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        "list_kb_documents",
		Description: servicetools.ListKBDocumentsSchemaDescription,
		Parameters:  servicetools.ListKBDocumentsParameters(),
	}
}

// Execute lists the documents of the requested KB with authorization enforcement.
func (t *ListKBDocumentsTool) Execute(args map[string]interface{}) (string, error) {
	return t.core.Execute(args)
}
