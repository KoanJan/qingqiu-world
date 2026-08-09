package agent

import (
	"fmt"
	"sync"

	"qingqiu-world-server/internal/dops"

	applogger "qingqiu-world-server/internal/logger"
)

// cache stores cached agents keyed by personID.
var cache sync.Map

// refreshCh receives personID signals to invalidate cached agents.
var refreshCh = make(chan int64, 64)

func init() {
	go watchRefresh()
}

// watchRefresh listens for refresh signals and evicts stale cache entries.
func watchRefresh() {
	for personID := range refreshCh {
		cache.Delete(personID)
		applogger.Debug("agent cache invalidated", "person_id", personID)
	}
}

// GetAgent returns the cached Agent for the given personID, loading from DB
// on cache miss. The returned Agent bundles Person, AgentConfig, and LLMConfig
// — everything runtime needs in a single lookup.
func GetAgent(personID int64) (*Agent, error) {
	if v, ok := cache.Load(personID); ok {
		return v.(*Agent), nil
	}
	return loadAndCache(personID)
}

// Refresh signals that the cached Agent for personID is stale and should be
// reloaded on the next GetAgent call. Call this when any component of the
// agent (Person, AgentConfig, LLMConfig) is modified in the database.
func Refresh(personID int64) {
	refreshCh <- personID
}

// loadAndCache fetches all agent components from DB, assembles an Agent, and
// stores it in the cache.
func loadAndCache(personID int64) (*Agent, error) {
	person, err := dops.GetPerson(personID)
	if err != nil {
		return nil, fmt.Errorf("agent: load person %d: %w", personID, err)
	}

	ac, err := dops.GetAgentConfigByPersonID(personID)
	if err != nil {
		return nil, fmt.Errorf("agent: load agent config for person %d: %w", personID, err)
	}

	llm, err := dops.GetLLMConfig(ac.LLMConfigID)
	if err != nil {
		return nil, fmt.Errorf("agent: load llm config %d: %w", ac.LLMConfigID, err)
	}

	a := &Agent{
		Person: *person,
		Config: *ac,
		LLM:    *llm,
	}
	cache.Store(personID, a)
	return a, nil
}
