package agent

import "qingqiu-world-server/internal/model"

// Agent contains all informations runtime needs
type Agent struct {
	model.Person
	Config model.AgentConfig
	LLM    model.LLMConfig
}
