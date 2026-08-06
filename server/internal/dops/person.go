package dops

import (
	"encoding/json"
	"fmt"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// GetPerson retrieves a person by ID.
func GetPerson(personID int64) (*model.Person, error) {
	var person model.Person
	if err := database.DB.First(&person, personID).Error; err != nil {
		return nil, fmt.Errorf("person %d: %w", personID, err)
	}
	return &person, nil
}

// GetPersonByName retrieves a person by name.
func GetPersonByName(name string) (*model.Person, error) {
	var person model.Person
	if err := database.DB.Where("name = ?", name).First(&person).Error; err != nil {
		return nil, fmt.Errorf("person %q: %w", name, err)
	}
	return &person, nil
}

// GetCurrentUserPerson returns the current human user's Person record.
// In the current single-user design, this returns the unique type=2 person.
func GetCurrentUserPerson() (*model.Person, error) {
	var person model.Person
	if err := database.DB.Where("type = ?", model.PersonTypeHuman).First(&person).Error; err != nil {
		return nil, err
	}
	return &person, nil
}

// GetCurrentUserPersonID returns the current human user's PersonID.
func GetCurrentUserPersonID() (int64, error) {
	person, err := GetCurrentUserPerson()
	if err != nil {
		return 0, err
	}
	return person.ID, nil
}

// GetUserName returns the primary human user's name.
func GetUserName() string {
	person, err := GetCurrentUserPerson()
	if err != nil {
		applogger.Error("failed to load current user person", "error", err)
		return ""
	}
	return person.Name
}

// GetAIPersons returns all persons with type=AI (agent identities).
func GetAIPersons() ([]model.Person, error) {
	var persons []model.Person
	if err := database.DB.Where("type = ?", model.PersonTypeAI).Find(&persons).Error; err != nil {
		return nil, fmt.Errorf("query ai persons: %w", err)
	}
	return persons, nil
}

// CreateAIPerson creates a Person (type=AI) and an AgentConfig in a single transaction.
// Returns the created AgentConfig and Person.
func CreateAIPerson(name, bio string, characterSettings string, llmConfigID int64, avatar string, knowledgeBaseIDs []int64) (*model.AgentConfig, *model.Person, error) {
	var agent *model.AgentConfig
	var person *model.Person

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		p := model.Person{
			Name:   name,
			Bio:    bio,
			Avatar: avatar,
			Type:   model.PersonTypeAI,
		}
		if err := tx.Select("Name", "Bio", "Avatar", "Type").Create(&p).Error; err != nil {
			return fmt.Errorf("create person: %w", err)
		}

		kbIDsJSON := "[]"
		if len(knowledgeBaseIDs) > 0 {
			data, _ := json.Marshal(knowledgeBaseIDs)
			kbIDsJSON = string(data)
		}

		a := model.AgentConfig{
			PersonID:          p.ID,
			CharacterSettings: characterSettings,
			LLMConfigID:       llmConfigID,
			KnowledgeBaseIDs:  kbIDsJSON,
		}
		if err := tx.Select("PersonID", "CharacterSettings", "LLMConfigID", "KnowledgeBaseIDs").Create(&a).Error; err != nil {
			return fmt.Errorf("create agent: %w", err)
		}

		agent = &a
		person = &p
		return nil
	})

	return agent, person, err
}

// DeleteAIPersonCascade deletes an agent config and all associated data in a single transaction.
// Workspace cleanup (filesystem) remains the caller's responsibility.
func DeleteAIPersonCascade(personID int64) (sessionIDs []int64, err error) {
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Collect session IDs via participant_sessions.
		if err := tx.Raw(`SELECT ps.session_id FROM participant_sessions ps
			WHERE ps.participant_id = ?`, personID).Pluck("session_id", &sessionIDs).Error; err != nil {
			return fmt.Errorf("pluck sessions via participant_sessions: %w", err)
		}

		if len(sessionIDs) > 0 {
			// NOTE: This logic assumes 1v1 (one agent per session).
			// In multi-agent/group chat, deleting one agent should NOT cascade delete the entire session.
			tables := []interface{}{
				&model.Work{}, &model.MessageDraft{}, &model.Interaction{},
				&model.AgentNarrative{}, &model.Summary{},
				&model.ParticipantSession{}, &model.Message{},
			}
			for _, table := range tables {
				if err := tx.Where("session_id IN ?", sessionIDs).Delete(table).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("id IN ?", sessionIDs).Delete(&model.Session{}).Error; err != nil {
				return err
			}
			if err := tx.Where("session_id IN ?", sessionIDs).Delete(&model.ScheduledEvent{}).Error; err != nil {
				return err
			}
		}

		// Agent-level memory and cognition — now keyed by person_id.
		if err := tx.Where("person_id = ?", personID).Delete(&model.AgentObservation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("person_id = ?", personID).Delete(&model.EntityProfile{}).Error; err != nil {
			return err
		}

		// Delete agent config and person
		if err := tx.Where("person_id = ?", personID).Delete(&model.AgentConfig{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.Person{}, personID).Error; err != nil {
			return err
		}

		return nil
	})

	return sessionIDs, err
}

// UpdateHumanPerson update Person of hunman type
func UpdateHumanPerson(personID int64, bio string) error {
	updates := map[string]interface{}{"bio": bio}
	return database.DB.Model(&model.Person{ID: personID, Type: model.PersonTypeHuman}).Updates(updates).Error
}

// CreateHumanPerson creates an new human person
func CreateHumanPerson(name, bio string) (*model.Person, error) {
	person := model.Person{
		Name: name,
		Bio:  bio,
		Type: model.PersonTypeHuman,
	}
	if err := database.DB.Create(&person).Error; err != nil {
		return nil, err
	}
	return &person, nil
}

// AIPersonUpdates holds optional fields for updating an AI person.
type AIPersonUpdates struct {
	PersonID          int64
	Bio               *string
	CharacterSettings *string
	LLMConfigID       *int64
	Avatar            *string
	KnowledgeBaseIDs  *[]int64
}

func (m *AIPersonUpdates) getAgentConfigUpdates() map[string]any {
	updates := make(map[string]interface{})
	if m.CharacterSettings != nil {
		updates["character_settings"] = *m.CharacterSettings
	}
	if m.LLMConfigID != nil {
		updates["llm_config_id"] = *m.LLMConfigID
	}
	if m.KnowledgeBaseIDs != nil {
		data, _ := json.Marshal(*m.KnowledgeBaseIDs)
		updates["knowledge_base_ids"] = string(data)
	}
	return updates
}

func (m *AIPersonUpdates) getPersonUpdates() map[string]any {
	updates := make(map[string]interface{})
	if m.Bio != nil {
		updates["bio"] = *m.Bio
	}
	if m.Avatar != nil {
		updates["avatar"] = *m.Avatar
	}
	return updates
}

// UpdateAIPerson updates a Person of AI type
func UpdateAIPerson(aiPersonUpdates *AIPersonUpdates) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		// Update agent-level fields
		acUpdates := aiPersonUpdates.getAgentConfigUpdates()
		if len(acUpdates) > 0 {
			if err := tx.Model(&model.AgentConfig{}).Where("person_id = ?", aiPersonUpdates.PersonID).Updates(acUpdates).Error; err != nil {
				return err
			}
		}

		// Update person-level fields (avatar and bio) if provided
		pUpdates := aiPersonUpdates.getPersonUpdates()
		if len(pUpdates) > 0 {
			if err := tx.Model(&model.Person{}).Where("id = ? AND type = ?", aiPersonUpdates.PersonID, model.PersonTypeAI).Updates(pUpdates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SessionMember
type SessionMember struct {
	PersonID int64
	Name     string
	Avatar   string
}

// GetAIPersonInSession returns ALL AI persons participating in the session.
// For 1v1 human-AI sessions this returns one entry; for A2A sessions it
// returns both AI participants. Used by the session API to render the
// participant avatar grid on the frontend.
func GetAIPersonInSession(sessionID int64) ([]SessionMember, error) {
	var members []SessionMember
	err := database.DB.Raw(`SELECT ps.participant_id AS person_id, p.name, p.avatar
		FROM participant_sessions ps
		JOIN persons p ON p.id = ps.participant_id AND p.type = ?
		WHERE ps.session_id = ?
		ORDER BY ps.participant_id ASC`, model.PersonTypeAI, sessionID).Scan(&members).Error
	return members, err
}

// GetAIPersonsInSessions returns a map of sessionID → []SessionMember for
// the given session IDs. Each session maps to ALL its AI participants
// (not just the first), so the frontend can render a multi-avatar grid
// for A2A sessions and future group chats.
func GetAIPersonsInSessions(sessionIDs []int64) (map[int64][]SessionMember, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	type row struct {
		SessionID int64
		PersonID  int64
		Name      string
		Avatar    string
	}
	var rows []row
	err := database.DB.Raw(`SELECT ps.session_id, ps.participant_id AS person_id,
		p.name, p.avatar
		FROM participant_sessions ps
		JOIN persons p ON p.id = ps.participant_id AND p.type = ?
		WHERE ps.session_id IN ?
		ORDER BY ps.session_id ASC, ps.participant_id ASC`, model.PersonTypeAI, sessionIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make(map[int64][]SessionMember, len(rows))
	for _, r := range rows {
		result[r.SessionID] = append(result[r.SessionID], SessionMember{
			PersonID: r.PersonID,
			Name:     r.Name,
			Avatar:   r.Avatar,
		})
	}
	return result, nil
}
