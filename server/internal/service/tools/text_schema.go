package tools

// TextToolSpec provides one agent-facing contract to both execution loops.
// Its map is newly allocated so wrappers can pass it to their own LLM schema.
func TextToolSpec(name string) (string, map[string]interface{}) {
	path := map[string]interface{}{"type": "string", "description": "Text file path, relative to the current working directory or absolute within Agent Owned Space."}
	properties := map[string]interface{}{"file_path": path}
	var description string
	switch name {
	case "read_text_lines":
		description = "Read numbered lines of a text file. Use start_line to page after the last complete line. If last_returned_line_truncated is true, continue at its end_byte_offset_exclusive with read_text_block. An empty page with eof=false may mean max_bytes is too small. Content is presented as UTF-8; non-UTF-8 source bytes may not be preserved."
		properties["start_line"] = map[string]interface{}{"type": "integer", "description": "First line number (1-based).", "default": 1, "minimum": 1}
		properties["max_lines"] = map[string]interface{}{"type": "integer", "description": "Maximum lines to return (1–500).", "default": 200, "minimum": 1, "maximum": 500}
		properties["max_bytes"] = map[string]interface{}{"type": "integer", "description": "Maximum source bytes to return (1–12288). Increase if no whole UTF-8 character fits.", "default": 8192, "minimum": 1, "maximum": 12288}
	case "read_text_block":
		description = "Read a text file from a byte offset, including a search match or the remainder of a truncated line. Continue with next_byte_offset. A too-small max_bytes may return empty content with eof=false. Content is presented as UTF-8; non-UTF-8 source bytes may not be preserved."
		properties["start_byte_offset"] = map[string]interface{}{"type": "integer", "description": "Source byte offset, starting at zero.", "default": 0, "minimum": 0}
		properties["max_bytes"] = map[string]interface{}{"type": "integer", "description": "Maximum source bytes to return (1–12288).", "default": 8192, "minimum": 1, "maximum": 12288}
	case "search_text_file":
		description = "Find literal, case-sensitive UTF-8 text within one file. Pass next_byte_offset to continue; only a complete empty search from offset zero establishes absence. Match offsets can be read with read_text_block. If the file changes, restart at zero."
		properties["query"] = map[string]interface{}{"type": "string", "description": "Exact text to find, at most 1024 UTF-8 bytes."}
		properties["start_byte_offset"] = map[string]interface{}{"type": "integer", "description": "Resume cursor returned by a previous search, or zero.", "default": 0, "minimum": 0}
		properties["max_matches"] = map[string]interface{}{"type": "integer", "description": "Maximum matches per page (1–20).", "default": 10, "minimum": 1, "maximum": 20}
	}
	required := []string{"file_path"}
	if name == "search_text_file" {
		required = append(required, "query")
	}
	return description, map[string]interface{}{"type": "object", "properties": properties, "required": required}
}
