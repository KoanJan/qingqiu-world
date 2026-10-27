package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
)

// cognitiveIntent is the agent's explicit subject of inquiry before research.
// It is carried by the LLM conversation, not a separate persisted entity.
type cognitiveIntent struct {
	// NeedUnderstanding decides whether the process proceeds past reflection.
	NeedUnderstanding bool `json:"need_understanding"`
	// Description names the subject to search, without asserting a conclusion.
	Description string `json:"description"`
}

// cognitiveCandidateSet is the system's search result for one stated intent.
type cognitiveCandidateSet struct {
	// Description echoes the expressed subject used for candidate retrieval.
	Description string `json:"description"`
	// Candidates are suggestions, not an exhaustive identity judgment.
	Candidates []memory.CognitiveCandidate `json:"candidates"`
	// HasMore reports whether the bounded candidate response omitted results.
	HasMore bool `json:"has_more"`
	// Coverage records search limitations such as missing vectors.
	Coverage string `json:"coverage"`
}

// cognitiveSelection binds subsequent research to a new or existing memory.
type cognitiveSelection struct {
	// Proceed permits the agent to decline after inspecting candidates.
	Proceed bool `json:"proceed"`
	// MemoryID is zero for a new concern or a shown existing memory ID.
	MemoryID int64 `json:"memory_id"`
	// Revision is zero for a new concern or the shown existing revision.
	Revision int `json:"revision"`
}

// cognitiveIntentTool is the only tool in the intention request.
func cognitiveIntentTool() llm.FunctionDefinition {
	return cognitiveTool("express_cognition", fmt.Sprintf("State whether an object of lasting interest from your experience wants attention now. It may be an experience, a person, a relationship, a matter, or something else you have encountered. If yes, identify the object in 1–%d characters; an object already shown among your memories can be reviewed. If no, leave description empty. Do not add fields outside this schema.", memory.RecallMemoryMaxQueryRunes), map[string]interface{}{
		"need_understanding": map[string]interface{}{"type": "boolean", "description": "True when you want to form or review a view of the object; false to decline."},
		"description":        map[string]interface{}{"type": "string", "description": fmt.Sprintf("Identify the object in 1–%d characters when need_understanding is true; empty when it is false.", memory.RecallMemoryMaxQueryRunes)},
	}, []string{"need_understanding", "description"})
}

// cognitiveSelectionTool is the only tool after the system candidate search.
func cognitiveSelectionTool() llm.FunctionDefinition {
	return cognitiveTool("choose_cognitive_memory", "After seeing potentially related earlier understandings, choose an existing memory_id and its shown revision, or use memory_id=0 and revision=0 for a new concern. Choosing an existing memory means reconsidering that view in light of what happened later; it does not imply that a new version is required. Set proceed=false with both numbers zero if further understanding is unwarranted. Use only an ID and revision shown in the candidates. Candidates are hints, not proof that the concern is identical or absent. Do not add fields outside this schema.", map[string]interface{}{
		"proceed":   map[string]interface{}{"type": "boolean", "description": "True to investigate; false to end without research."},
		"memory_id": map[string]interface{}{"type": "integer", "description": "A shown existing memory ID, or 0 for a new concern; 0 when proceed is false."},
		"revision":  map[string]interface{}{"type": "integer", "description": "The shown revision for an existing memory, or 0 for a new concern; 0 when proceed is false."},
	}, []string{"proceed", "memory_id", "revision"})
}

// decodeCognitiveToolArgs rejects malformed or extended tool arguments before
// they can change the research target or terminal result.
func decodeCognitiveToolArgs[T any](arguments string, required ...string) (T, error) {
	var value T
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &fields); err != nil {
		return value, err
	}
	if fields == nil {
		return value, fmt.Errorf("cognitive tool arguments must be an object")
	}
	for _, name := range required {
		field, ok := fields[name]
		if !ok || string(field) == "null" {
			return value, fmt.Errorf("cognitive tool requires %s", name)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value, fmt.Errorf("cognitive tool requires one JSON object")
	}
	return value, nil
}

// findCognitiveCall locates an accepted tool call in the retained conversation.
func findCognitiveCall(messages []llm.Message, name string) (llm.ToolCall, bool) {
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == name {
				return call, true
			}
		}
	}
	return llm.ToolCall{}, false
}

// cognitiveIntentInConversation reads the accepted intent, including after a
// pause or process restart, from the same messages sent back to the model.
func cognitiveIntentInConversation(messages []llm.Message) (cognitiveIntent, error) {
	call, ok := findCognitiveCall(messages, "express_cognition")
	if !ok {
		return cognitiveIntent{}, fmt.Errorf("cognitive intention missing from conversation")
	}
	intent, err := decodeCognitiveToolArgs[cognitiveIntent](call.Function.Arguments, "need_understanding", "description")
	if err != nil {
		return cognitiveIntent{}, err
	}
	intent.Description = strings.TrimSpace(intent.Description)
	if !intent.NeedUnderstanding {
		if intent.Description != "" {
			return cognitiveIntent{}, fmt.Errorf("declined cognition has a description")
		}
		return intent, nil
	}
	if intent.Description == "" || len([]rune(intent.Description)) > memory.RecallMemoryMaxQueryRunes {
		return cognitiveIntent{}, fmt.Errorf("cognitive intention requires a description of 1–%d characters", memory.RecallMemoryMaxQueryRunes)
	}
	return intent, nil
}

// cognitiveCandidatesInConversation reads the system result paired with the
// accepted intention call, including after a process restart.
func cognitiveCandidatesInConversation(messages []llm.Message) (cognitiveCandidateSet, error) {
	call, ok := findCognitiveCall(messages, "express_cognition")
	if !ok {
		return cognitiveCandidateSet{}, fmt.Errorf("cognitive intention missing before candidate selection")
	}
	for _, message := range messages {
		if message.Role != "tool" || message.ToolCallID != call.ID {
			continue
		}
		var candidates cognitiveCandidateSet
		if err := json.Unmarshal([]byte(message.Content), &candidates); err != nil {
			return cognitiveCandidateSet{}, err
		}
		return candidates, nil
	}
	return cognitiveCandidateSet{}, fmt.Errorf("cognitive candidates missing from conversation")
}

// searchCognitiveCandidates uses the expressed subject as the search query.
// Its result is a tool message so the agent sees both its intention and the
// system's candidates before selecting a research target.
func searchCognitiveCandidates(ctx context.Context, personID int64, intent cognitiveIntent) (string, error) {
	items, more, coverage, err := memory.FindCognitiveMemories(ctx, personID, intent.Description, time.Time{}, time.Time{}, memory.CognitiveTimeDefault, memory.RecallMemoryMaxPageSize, 0, true)
	if err != nil {
		return "", err
	}
	if items == nil {
		items = []memory.CognitiveCandidate{}
	}
	encoded, err := json.Marshal(cognitiveCandidateSet{Description: intent.Description, Candidates: items, HasMore: more, Coverage: coverage})
	return string(encoded), err
}

// selectedCognitiveRequest accepts only a candidate actually shown to the
// agent, or a new memory using the expressed subject. Semantic identity still
// belongs to the agent; the system checks IDs and revisions mechanically.
func selectedCognitiveRequest(messages []llm.Message, call llm.ToolCall) (cognitiveRequest, bool, error) {
	intent, err := cognitiveIntentInConversation(messages)
	if err != nil {
		return cognitiveRequest{}, false, err
	}
	if !intent.NeedUnderstanding {
		return cognitiveRequest{}, false, fmt.Errorf("cognitive target has no active intention")
	}
	candidates, err := cognitiveCandidatesInConversation(messages)
	if err != nil {
		return cognitiveRequest{}, false, err
	}
	if candidates.Description != intent.Description {
		return cognitiveRequest{}, false, fmt.Errorf("cognitive candidates do not match the expressed intention")
	}
	selection, err := decodeCognitiveToolArgs[cognitiveSelection](call.Function.Arguments, "proceed", "memory_id", "revision")
	if err != nil {
		return cognitiveRequest{}, false, err
	}
	if selection.MemoryID < 0 || selection.Revision < 0 {
		return cognitiveRequest{}, false, fmt.Errorf("negative cognitive memory identity or revision")
	}
	if !selection.Proceed {
		if selection.MemoryID != 0 || selection.Revision != 0 {
			return cognitiveRequest{}, false, fmt.Errorf("declined cognition selects a memory")
		}
		return cognitiveRequest{}, false, nil
	}
	if selection.MemoryID == 0 {
		if selection.Revision != 0 {
			return cognitiveRequest{}, false, fmt.Errorf("new cognition requires revision zero")
		}
		return cognitiveRequest{Description: intent.Description}, true, nil
	}
	for _, candidate := range candidates.Candidates {
		if candidate.MemoryID == selection.MemoryID && candidate.Revision == selection.Revision {
			return cognitiveRequest{MemoryID: selection.MemoryID, Revision: selection.Revision}, true, nil
		}
	}
	return cognitiveRequest{}, false, fmt.Errorf("selected cognitive memory %d revision %d was not offered", selection.MemoryID, selection.Revision)
}

// cognitiveTargetInConversation reuses the selected target for final writing;
// the terminal tool cannot replace the concern or memory identity.
func cognitiveTargetInConversation(messages []llm.Message) (cognitiveRequest, error) {
	call, ok := findCognitiveCall(messages, "choose_cognitive_memory")
	if !ok {
		return cognitiveRequest{}, fmt.Errorf("cognitive target missing from conversation")
	}
	target, proceed, err := selectedCognitiveRequest(messages, call)
	if err != nil {
		return cognitiveRequest{}, err
	}
	if !proceed {
		return cognitiveRequest{}, fmt.Errorf("declined cognitive target reached research stage")
	}
	return target, nil
}
