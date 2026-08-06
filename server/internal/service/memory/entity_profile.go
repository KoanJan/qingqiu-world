package memory

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

// Profile generation constants.
const (
	profileTriggerMin   = 10  // 10 Minimum observations in one direction to trigger generation
	profileTopK         = 50  // Top K observations selected for LLM reflection (by importance DESC, id DESC as tiebreaker)
	profileRateLimitMin = 360 // 360 Minutes between profile regenerations for the same entity
)

// entityDirection is a composite key for grouping observations by entity.
type entityDirection struct {
	EntityType model.EntityType
	EntityID   int64
}

// CheckDensity scans an agent's observations and identifies entity directions
// with enough observations to warrant EntityProfile generation. For each
// eligible direction, it triggers LLM reflection asynchronously.
//
// Evidence selection is done by top-K importance (with id DESC as tiebreaker),
// not survival_count. See loadProfileEvidences.
//
// Returns the number of profiles triggered.
func checkDensity(ctx context.Context, personID int64) int {
	// Load all observations for this agent
	var observations []model.AgentObservation
	if err := database.DB.Where("person_id = ?", personID).
		Order("id").Find(&observations).Error; err != nil {
		applogger.Error("EntityProfile density check: failed to load observations",
			"person_id", personID, "error", err)
		return 0
	}

	if len(observations) < profileTriggerMin {
		applogger.Debug("EntityProfile density check: insufficient observations",
			"person_id", personID,
			"qualified", len(observations),
			"required", profileTriggerMin,
		)
		return 0
	}

	// Build entity direction counts from observations
	directionCounts := resolveEntityDirections(observations)

	triggered := 0
	for dir, count := range directionCounts {
		if count < profileTriggerMin {
			continue
		}
		if isRateLimited(dir.EntityType, dir.EntityID, personID) {
			applogger.Debug("EntityProfile rate limited",
				"person_id", personID,
				"entity_type", dir.EntityType,
				"entity_id", dir.EntityID,
			)
			continue
		}
		go generateProfile(ctx, personID, dir.EntityType, dir.EntityID)
		triggered++
	}

	return triggered
}

// resolveEntityDirections maps observations to entity directions by walking
// the event → message → session → participants chain.
//
// Each observation can map to multiple entity directions. For example, a
// user message in a session contributes to both the user profile and the
// session profile.
func resolveEntityDirections(observations []model.AgentObservation) map[entityDirection]int {
	// Collect unique event IDs
	eventIDs := make(map[int64]bool)
	for _, o := range observations {
		eventIDs[o.EventID] = true
	}

	// Batch-load events
	var events []model.Event
	ids := make([]int64, 0, len(eventIDs))
	for id := range eventIDs {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		if err := database.DB.Where("id IN ?", ids).Find(&events).Error; err != nil {
			applogger.Error("resolveEntityDirections: failed to load events", "error", err)
		}
	}

	// Map event_id → event for quick lookup
	eventMap := make(map[int64]model.Event)
	for _, e := range events {
		eventMap[e.ID] = e
	}

	// Collect unique message ref_ids (only for message-type events)
	msgIDs := make(map[int64]bool)
	for _, e := range events {
		if e.EventType == model.EventTypeMessage {
			msgIDs[e.RefID] = true
		}
	}

	// Batch-load messages
	var messages []model.Message
	mids := make([]int64, 0, len(msgIDs))
	for id := range msgIDs {
		mids = append(mids, id)
	}
	if len(mids) == 0 {
		return nil
	}
	if err := database.DB.Where("id IN ?", mids).Find(&messages).Error; err != nil {
		applogger.Error("resolveEntityDirections: failed to load messages", "error", err)
		return nil
	}
	msgMap := make(map[int64]model.Message)
	sessionIDs := make(map[int64]bool)
	for _, m := range messages {
		msgMap[m.ID] = m
		sessionIDs[m.SessionID] = true
	}

	// Batch-load sessions
	var sessions []model.Session
	sids := make([]int64, 0, len(sessionIDs))
	for id := range sessionIDs {
		sids = append(sids, id)
	}
	if len(sids) > 0 {
		if err := database.DB.Where("id IN ?", sids).Find(&sessions).Error; err != nil {
			applogger.Error("resolveEntityDirections: failed to load sessions", "error", err)
		}
	}
	sessionMap := make(map[int64]model.Session)
	for _, s := range sessions {
		sessionMap[s.ID] = s
	}

	// Batch-load participant sessions to find human person IDs
	// Participants now use person_id directly (no participant_type).
	var participants []model.ParticipantSession
	if len(sids) > 0 {
		var pErr error
		participants, pErr = dops.GetSessionParticipantsByPersonTypeMulti(sids, model.PersonTypeHuman)
		if pErr != nil {
			applogger.Error("resolveEntityDirections: failed to load participants", "error", pErr)
		}
	}
	participantMap := make(map[int64]int64) // session_id → person_id of the first human
	humanPersonIDs := make(map[int64]bool)  // set of human person IDs
	seenSessions := make(map[int64]bool)
	for _, p := range participants {
		if !seenSessions[p.SessionID] {
			participantMap[p.SessionID] = p.ParticipantID
			seenSessions[p.SessionID] = true
		}
		humanPersonIDs[p.ParticipantID] = true
	}

	// Count observations per entity direction
	counts := make(map[entityDirection]int)
	dedup := make(map[entityDirection]map[int64]bool) // dir → set of observation IDs

	for _, o := range observations {
		ev, ok := eventMap[o.EventID]
		if !ok || ev.EventType != model.EventTypeMessage {
			continue
		}

		msg, ok := msgMap[ev.RefID]
		if !ok {
			continue
		}

		// Direction 1: Session
		{
			dir := entityDirection{EntityType: model.EntityTypeSession, EntityID: msg.SessionID}
			if dedup[dir] == nil {
				dedup[dir] = make(map[int64]bool)
			}
			if !dedup[dir][o.ID] {
				dedup[dir][o.ID] = true
				counts[dir]++
			}
		}

		// Direction 2: Person (sender of the message)
		{
			dir := entityDirection{EntityType: model.EntityTypePerson, EntityID: msg.PersonID}
			if dedup[dir] == nil {
				dedup[dir] = make(map[int64]bool)
			}
			if !dedup[dir][o.ID] {
				dedup[dir][o.ID] = true
				counts[dir]++
			}
		}
	}

	return counts
}

// isRateLimited checks whether a profile was recently updated (within the
// rate limit window). Returns true if the profile should be skipped.
func isRateLimited(entityType model.EntityType, entityID, personID int64) bool {
	var profile model.EntityProfile
	err := database.DB.Where("person_id = ? AND entity_type = ? AND entity_id = ?",
		personID, entityType, entityID).First(&profile).Error
	if err != nil {
		return false // No existing profile, not rate limited
	}
	return time.Since(profile.LastUpdatedAt) < profileRateLimitMin*time.Minute
}

// generateProfile triggers LLM reflection to produce an EntityProfile
// narrative for the given agent/entity combination.
//
// It loads the top-K observations (by importance) for the entity direction,
// formats them as evidence, and asks the LLM to synthesize a fresh narrative
// (no prior narrative is fed). MD5 of the evidence text is compared with the
// existing profile's input_md5 — if unchanged, generation is skipped.
func generateProfile(ctx context.Context, personID int64, entityType model.EntityType, entityID int64) {
	applogger.Info("Generating EntityProfile",
		"person_id", personID,
		"entity_type", entityType,
		"entity_id", entityID,
	)

	// Resolve entity label for the prompt
	entityName, ok := entityLabel(entityType, entityID)
	if !ok {
		applogger.Error("EntityProfile: failed to resolve entity label",
			"entity_type", entityType, "entity_id", entityID)
		return
	}

	// Resolve agent config for self-referencing in the prompt
	ac := getAgentConfigByPersonID(personID)
	if ac == nil {
		applogger.Error("EntityProfile: agent config not found", "person_id", personID)
		return
	}

	// Load agent name from the persons table
	var agentPerson model.Person
	if err := database.DB.First(&agentPerson, personID).Error; err != nil {
		applogger.Error("EntityProfile: failed to load agent person",
			"person_id", personID, "error", err)
		return
	}
	agentName := agentPerson.Name

	// Load relevant observations with event content
	evidences := loadProfileEvidences(personID, entityType, entityID, agentName)

	// Build the LLM prompt.
	// When the agent is reflecting on itself, use "yourself" instead of the agent
	// name as the entity label to prevent the LLM from slipping into third-person.
	entityDesc := entityName
	extraDirective := ""
	if entityType == model.EntityTypePerson && entityID == ac.PersonID {
		entityDesc = "yourself"
		extraDirective = " Write in first-person (use 'I', 'me', 'my')."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are %s, reflecting on your accumulated observations about %s.
Below are the key observations you've recorded (your messages are labeled with your name).
Based on these, write a concise narrative describing your understanding and impression of %s.

Focus on patterns, traits, preferences, or notable characteristics. Be honest about what
you know and what remains uncertain. Do not fabricate observations.%s

Key observations:
`, agentName, entityDesc, entityDesc, extraDirective))

	for i, ev := range evidences {
		sb.WriteString(fmt.Sprintf("- [Observation %d] %s\n", i+1, ev))
	}

	sb.WriteString("\nOutput: a single paragraph of 2-6 sentences.")

	promptText := sb.String()

	// Compute MD5 of the evidence text to detect unchanged input
	inputHash := fmt.Sprintf("%x", md5.Sum([]byte(promptText)))

	var existingProfile model.EntityProfile
	if err := database.DB.Where("person_id = ? AND entity_type = ? AND entity_id = ?",
		personID, entityType, entityID).First(&existingProfile).Error; err != nil {
		// record not found is the normal "first-time generation" case —
		// log as WARN (not ERROR) so logs aren't polluted by expected flow.
		// Any other error is a real problem and stays at ERROR level.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			applogger.Warn("EntityProfile: no existing profile found, first-time generation",
				"person_id", personID, "entity_type", entityType, "entity_id", entityID)
		} else {
			applogger.Error("EntityProfile: failed to check existing profile before generation",
				"person_id", personID, "entity_type", entityType, "entity_id", entityID, "error", err)
		}
	}

	if existingProfile.ID != 0 && existingProfile.InputMD5 == inputHash && existingProfile.InputMD5 != "" {
		applogger.Info("EntityProfile input unchanged, skipping regeneration",
			"person_id", personID,
			"entity_type", entityType,
			"entity_id", entityID,
			"md5", inputHash,
		)
		return
	}

	llmConfig := getLLMConfig(ac.LLMConfigID)
	if llmConfig == nil {
		applogger.Error("EntityProfile: LLM config not found", "person_id", personID)
		return
	}

	chatModel := llm.NewChatModelWithTemperature(
		llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic,
	)

	narrative, err := chatModel.Chat(ctx, []llm.Message{
		{Role: "user", Content: promptText},
	})

	if err != nil {
		applogger.Error("EntityProfile LLM call failed",
			"person_id", personID,
			"entity_type", entityType,
			"entity_id", entityID,
			"error", err,
		)
		return
	}

	narrative = strings.TrimSpace(narrative)
	if narrative == "" {
		applogger.Error("EntityProfile LLM returned empty narrative",
			"person_id", personID,
		)
		return
	}

	// Upsert profile
	evidenceCount := len(evidences)
	if existingProfile.ID != 0 {
		if err := database.DB.Model(&existingProfile).Updates(map[string]interface{}{
			"narrative":       narrative,
			"evidence_count":  evidenceCount,
			"input_md5":       inputHash,
			"last_updated_at": time.Now(),
		}).Error; err != nil {
			applogger.Error("EntityProfile: failed to update profile",
				"person_id", personID, "entity_type", entityType, "error", err)
			return
		}
	} else {
		profile := model.EntityProfile{
			PersonID:      personID,
			EntityType:    entityType,
			EntityID:      entityID,
			Narrative:     narrative,
			EvidenceCount: evidenceCount,
			InputMD5:      inputHash,
		}
		if err := database.DB.Create(&profile).Error; err != nil {
			applogger.Error("EntityProfile: failed to create profile", "person_id", personID, "entity_type", entityType, "error", err)
			return
		}
	}

	applogger.Info("EntityProfile generated",
		"person_id", personID,
		"entity_type", entityType,
		"entity_id", entityID,
		"evidence_count", evidenceCount,
		"narrative_len", len(narrative),
	)
}

// entityLabel returns a human-readable label for the entity type/ID combination.
// Returns (label, true) on success, ("", false) if the entity name cannot be resolved.
func entityLabel(entityType model.EntityType, entityID int64) (string, bool) {
	switch entityType {
	case model.EntityTypePerson:
		var person model.Person
		if err := database.DB.Where("id = ?", entityID).Select("name").First(&person).Error; err != nil {
			applogger.Error("entityLabel: failed to load person name", "entity_id", entityID, "error", err)
			return "", false
		}
		return person.Name, true
	case model.EntityTypeSession:
		return fmt.Sprintf("session #%d", entityID), true
	default:
		return fmt.Sprintf("entity (type=%d, id=%d)", entityType, entityID), true
	}
}

// loadProfileEvidences loads message content for observations relevant to the
// given entity direction, formatted for inclusion in the LLM reflection prompt.
//
// Selection: top profileTopK observations sorted by importance DESC (id DESC as
// tiebreaker for equal importance). No survival_count gate.
func loadProfileEvidences(personID int64, entityType model.EntityType, entityID int64, agentName string) []string {
	// Load all observations ordered by importance DESC, then id DESC for recency tiebreaking.
	var observations []model.AgentObservation
	if err := database.DB.Where("person_id = ?", personID).
		Order("importance DESC, id DESC").
		Find(&observations).Error; err != nil {
		applogger.Error("loadProfileEvidences: failed to load observations",
			"person_id", personID, "error", err)
		return nil
	}

	if len(observations) == 0 {
		return nil
	}

	// Collect event IDs
	eventIDs := make([]int64, 0, len(observations))
	for _, o := range observations {
		eventIDs = append(eventIDs, o.EventID)
	}

	// Load events (only message-type)
	var events []model.Event
	if err := database.DB.Where("id IN ? AND event_type = ?", eventIDs, model.EventTypeMessage).Find(&events).Error; err != nil {
		applogger.Error("loadProfileEvidences: failed to load events", "error", err)
	}
	eventMap := make(map[int64]model.Event)
	refIDs := make([]int64, 0, len(events))
	for _, e := range events {
		eventMap[e.ID] = e
		refIDs = append(refIDs, e.RefID)
	}
	if len(refIDs) == 0 {
		return nil
	}

	// Load messages
	var messages []model.Message
	if err := database.DB.Where("id IN ?", refIDs).Find(&messages).Error; err != nil {
		applogger.Error("loadProfileEvidences: failed to load messages", "error", err)
		return nil
	}
	msgMap := make(map[int64]model.Message)
	sessionIDs := make(map[int64]bool)
	for _, m := range messages {
		msgMap[m.ID] = m
		sessionIDs[m.SessionID] = true
	}

	sids := make([]int64, 0, len(sessionIDs))
	for id := range sessionIDs {
		sids = append(sids, id)
	}

	// Load sessions
	var sessions []model.Session
	if len(sids) > 0 {
		if err := database.DB.Where("id IN ?", sids).Find(&sessions).Error; err != nil {
			applogger.Error("loadProfileEvidences: failed to load sessions", "error", err)
		}
	}
	sessionMap := make(map[int64]model.Session)
	for _, s := range sessions {
		sessionMap[s.ID] = s
	}

	// Map person_id → person name for resolving labels in evidence.
	// Batch-load all persons referenced by messages.
	personIDs := make(map[int64]bool)
	for _, m := range messages {
		personIDs[m.PersonID] = true
	}
	personNameMap := make(map[int64]string)
	if len(personIDs) > 0 {
		var persons []model.Person
		pids := make([]int64, 0, len(personIDs))
		for pid := range personIDs {
			pids = append(pids, pid)
		}
		if err := database.DB.Where("id IN ?", pids).Select("id, name").Find(&persons).Error; err != nil {
			applogger.Error("loadProfileEvidences: failed to load person names", "error", err)
		}
		for _, p := range persons {
			personNameMap[p.ID] = p.Name
		}
	}

	// Walk observations in importance order, collect top K matching the entity direction.
	var evidences []string
	for _, o := range observations {
		ev, ok := eventMap[o.EventID]
		if !ok {
			continue
		}
		msg, ok := msgMap[ev.RefID]
		if !ok {
			continue
		}

		matches := false
		switch entityType {
		case model.EntityTypeSession:
			matches = msg.SessionID == entityID
		case model.EntityTypePerson:
			matches = msg.PersonID == entityID
		}

		if !matches {
			continue
		}

		// Resolve the label for this message based on who sent it.
		roleLabel := agentName // default: this agent's message
		if personName, ok := personNameMap[msg.PersonID]; ok && personName != agentName {
			roleLabel = personName
		}
		evidences = append(evidences, fmt.Sprintf("[%s] %s", roleLabel, msg.Content))

		if len(evidences) >= profileTopK {
			break
		}
	}

	return evidences
}

// getAgentConfigByPersonID retrieves the agent config model by person_id.
func getAgentConfigByPersonID(personID int64) *model.AgentConfig {
	var ac model.AgentConfig
	if err := database.DB.Where("person_id = ?", personID).First(&ac).Error; err != nil {
		return nil
	}
	return &ac
}

// getLLMConfig retrieves the LLM config by ID.
func getLLMConfig(configID int64) *model.LLMConfig {
	var cfg model.LLMConfig
	if err := database.DB.First(&cfg, configID).Error; err != nil {
		return nil
	}
	return &cfg
}

// LoadProfileForEntity returns the narrative from an agent's EntityProfile for
// a specific entity (user/agent/session). Returns empty string if no profile exists.
func LoadProfileForEntity(personID int64, entityType model.EntityType, entityID int64) string {
	var profile model.EntityProfile
	err := database.DB.Where("person_id = ? AND entity_type = ? AND entity_id = ?",
		personID, entityType, entityID).First(&profile).Error
	if err != nil {
		return ""
	}
	return profile.Narrative
}
