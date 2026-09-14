package schema

// ActivityEvent represents a single event in the agent's activity timeline.
type ActivityEvent struct {
	ID       string `json:"id"`               // Stable derived ID: interaction ID plus event role/index
	Time     string `json:"time"`             // Formatted timestamp (2006-01-02 15:04:05)
	Type     string `json:"type"`             // "thinking" | "tool_call" | "guidance"
	Content  string `json:"content"`          // Raw content: thinking text, guidance text, or empty for tool_call
	Tool     string `json:"tool,omitempty"`   // Only for tool_call: tool name
	Target   string `json:"target,omitempty"` // Only for tool_call: extracted target from arguments
	PersonID int64  `json:"agent_id"`         // Person that produced this event
}

// ActivityPage is one cursor-bounded page of the session activity timeline.
// The cursor is an interaction ID because one interaction can produce multiple
// ActivityEvents and must never be split across pages.
type ActivityPage struct {
	Events                  []ActivityEvent `json:"events"`
	HasMore                 bool            `json:"has_more"`
	NextBeforeInteractionID int64           `json:"next_before_interaction_id,omitempty"`
}
