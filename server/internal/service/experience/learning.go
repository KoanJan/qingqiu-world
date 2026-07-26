package experience

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

const (
	learningSearchTopN     = 10  // Max candidate public experiences to retrieve
	learningSearchMinScore = 0.4 // Minimum cosine similarity for candidates
)

// learnDecision is the structured output from the LLM judging which public
// experiences are worth learning by the agent.
type learnDecision struct {
	LearnIDs []int64 `json:"learn_ids" jsonschema:"description=IDs of public experiences worth learning based on your long-term interaction patterns. Include only those clearly relevant to the domains you actually work in. Leave empty if none."`
}

// CheckLearning evaluates whether an agent should learn any public experiences.
//
// It uses the agent's session-level entity_profiles (long-term interaction
// narratives) to semantically search the public experience library, then asks
// the LLM to judge which candidates are worth learning based on the agent's
// actual work patterns. Selected experiences are mechanically copied into the
// agent's private experience store.
//
// This is the public entry point called from the agent heartbeat at a low
// frequency (every 30 ticks). Safe to call when the embedding service is
// not configured — does nothing.
func CheckLearning(ctx context.Context, personID int64) {
	if embeddingSvc == nil {
		return
	}

	// Load session-level entity profiles for this agent.
	// These narratives describe what domains/tasks the agent actually works on.
	var profiles []model.EntityProfile
	if err := database.DB.Where("entity_type = ? AND person_id = ?", model.EntityTypeSession, personID).
		Find(&profiles).Error; err != nil {
		applogger.Error("CheckLearning: failed to load entity profiles",
			"person_id", personID, "error", err)
		return
	}
	if len(profiles) == 0 {
		applogger.Debug("CheckLearning: no session profiles yet, skipping",
			"person_id", personID)
		return
	}

	// Concatenate all profile narratives into a single query string
	var narratives []string
	for _, p := range profiles {
		narratives = append(narratives, p.Narrative)
	}
	query := strings.Join(narratives, "\n\n")

	// Semantic search over public experiences
	candidates, err := SearchPublicExperiences(ctx, query,
		learningSearchTopN, learningSearchMinScore)
	if err != nil {
		applogger.Error("CheckLearning: public experience search failed",
			"person_id", personID, "error", err)
		return
	}
	if len(candidates) == 0 {
		applogger.Debug("CheckLearning: no matching public experiences",
			"person_id", personID)
		return
	}

	// Load agent config LLM config for the judgment call
	var ac model.AgentConfig
	if err := database.DB.Where("person_id = ?", personID).First(&ac).Error; err != nil {
		applogger.Error("CheckLearning: failed to load agent config",
			"person_id", personID, "error", err)
		return
	}
	llmCfg, err := dops.GetLLMConfig(ac.LLMConfigID)
	if err != nil {
		applogger.Error("CheckLearning: failed to load LLM config",
			"person_id", personID, "error", err)
		return
	}

	// Ask LLM to judge which public experiences are worth learning
	learnIDs, err := judgeLearning(ctx, &ac, llmCfg, candidates, narratives)
	if err != nil {
		applogger.Error("CheckLearning: LLM judgment failed",
			"person_id", personID, "error", err)
		return
	}
	if len(learnIDs) == 0 {
		applogger.Debug("CheckLearning: nothing worth learning",
			"person_id", personID)
		return
	}

	// Build a lookup map for fast field access
	candidateMap := make(map[int64]model.PublicExperience)
	for _, c := range candidates {
		candidateMap[c.Experience.ID] = c.Experience
	}

	// Mechanical copy: field-to-field from PublicExperience to AgentExperience
	learned := 0
	for _, pubID := range learnIDs {
		pub, ok := candidateMap[pubID]
		if !ok {
			applogger.Error("CheckLearning: LLM returned unknown public experience ID",
				"person_id", personID, "public_experience_id", pubID)
			continue
		}

		// source_id = public_experience_id: this lesson was copied from pub.
		copied := func() bool {
			writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			_, err := createExperience(writeCtx, personID, model.AgentExperienceSourceLearn, pub.ID,
				pub.Title, pub.Description, pub.WhenToUse, pub.Guidelines, pub.Pitfalls, pub.Procedure)
			if err != nil {
				applogger.Error("CheckLearning: failed to copy experience",
					"person_id", personID,
					"public_experience_id", pub.ID,
					"error", err,
				)
				return false
			}
			return true
		}()

		if copied {
			learned++
		}
	}

	applogger.Info("CheckLearning: completed",
		"person_id", personID,
		"candidates", len(candidates),
		"learned", learned,
	)
}

// judgeLearning asks the LLM to decide which public experiences are worth
// learning based on the agent's long-term interaction patterns and personality.
func judgeLearning(ctx context.Context, ac *model.AgentConfig, llmCfg *model.LLMConfig,
	candidates []PublicSearchResult, narratives []string) ([]int64, error) {

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	chatModel := llm.NewChatModelWithTemperature(
		llmCfg.BaseURL, llmCfg.APIKey, llmCfg.ModelID, llm.TemperatureControlled,
	)

	// Build the candidate list for the prompt
	var candidateLines []string
	for _, c := range candidates {
		candidateLines = append(candidateLines, fmt.Sprintf(
			"[%d] Title: %s\nDescription: %s",
			c.Experience.ID, c.Experience.Title, c.Experience.Description,
		))
	}
	candidateText := strings.Join(candidateLines, "\n\n")

	profileText := strings.Join(narratives, "\n\n---\n\n")

	schema := llm.GenerateSchema[learnDecision]()

	prompt := fmt.Sprintf(`Decide which of the following public experiences are worth learning.

## Your Interaction Patterns
%s

## Candidate Public Experiences
%s

Judge which candidates are worth learning. For each candidate, consider:
- Does the experience address a domain you actually work in?
- Would you likely encounter the problem pattern or task signature described?
- Is the guidance applicable to the types of tasks you perform?

Reject a candidate if:
- It shares surface-level technology with your work but targets a different task category (e.g., both involve H5/canvas, but one is game development and the other is generative art)
- Its core methodology is not transferable to the types of tasks you actually perform
- You are uncertain — when in doubt, do not learn

Only include IDs of clearly relevant experiences. It is better to learn nothing than to learn irrelevant knowledge.

Return the IDs of experiences worth learning in the learn_ids field.`, profileText, candidateText)

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}

	schemaDef := llm.JSONSchemaDefinition{
		Name:   "learn_decision",
		Schema: schema,
		Strict: true,
	}

	response, err := chatModel.ChatWithJSONSchema(ctx, messages, schemaDef)
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	var decision learnDecision
	if err := json.Unmarshal([]byte(response), &decision); err != nil {
		return nil, fmt.Errorf("parse LLM output: %w", err)
	}

	return decision.LearnIDs, nil
}
