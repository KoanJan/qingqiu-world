// Package tools provides the tool interface and implementations for the
// private-space loop. Reusable path-based tool cores are in service/tools;
// this package wraps them with privatespace-specific Tool contracts,
// sandbox configuration, and log support.
package tools

import "qingqiu-world-server/internal/service/llm"

// Tool is the interface for tools available in the private-space loop.
type Tool interface {
	Name() string
	Description() string
	Schema() llm.FunctionDefinition
	Execute(args map[string]interface{}) (string, error)
}
