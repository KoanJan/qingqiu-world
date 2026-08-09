package schema

// AgentBrief is the minimal agent representation for the sidebar agent list.
//
// It intentionally does NOT embed AgentResponse — it only carries the fields
// the sidebar needs to display an agent card (avatar, name, bio for hover,
// energy, and a chat button).
type AgentBrief struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Bio    string `json:"bio"`
	Avatar string `json:"avatar"`
	Energy int    `json:"energy"`
}
