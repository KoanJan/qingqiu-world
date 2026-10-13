package tools_test

import (
	"reflect"
	"testing"

	focustools "qingqiu-world-server/internal/service/focusedwork/tools"
	pstools "qingqiu-world-server/internal/service/privatespace/tools"
)

// TestTextToolContracts verifies both loops expose the same names and parameters.
func TestTextToolContracts(t *testing.T) {
	focus := []focustools.Tool{
		focustools.NewReadTextLinesTool(1, "."),
		focustools.NewReadTextBlockTool(1, "."),
		focustools.NewSearchTextFileTool(1, "."),
	}
	for _, ft := range focus {
		ps := pstools.NewTextFileTool(".", ".", ft.Name().String())
		if ft.Schema().Name != ps.Schema().Name || !reflect.DeepEqual(ft.Schema().Parameters, ps.Schema().Parameters) {
			t.Fatalf("schema mismatch for %s", ft.Name())
		}
	}
}
