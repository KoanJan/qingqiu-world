package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// ListAuthorizedKBs returns the knowledge bases granted to the given person
// (agent), ordered by KB ID. Used by the comprehension phase as the agent's
// decision input and retrieval enforcement set.
func ListAuthorizedKBs(personID int64) ([]model.KnowledgeBase, error) {
	var kbs []model.KnowledgeBase
	err := database.DB.
		Joins("JOIN kb_access ON kb_access.kb_id = knowledge_bases.id").
		Where("kb_access.person_id = ?", personID).
		Order("knowledge_bases.id ASC").
		Find(&kbs).Error
	if err != nil {
		return nil, fmt.Errorf("list authorized KBs for person %d: %w", personID, err)
	}
	return kbs, nil
}

// ListGrantedPersons returns the AI persons granted access to the given KB,
// ordered by person ID. Used by the KB access management API.
func ListGrantedPersons(kbID int64) ([]model.Person, error) {
	var persons []model.Person
	err := database.DB.
		Joins("JOIN kb_access ON kb_access.person_id = persons.id").
		Where("kb_access.kb_id = ?", kbID).
		Order("persons.id ASC").
		Find(&persons).Error
	if err != nil {
		return nil, fmt.Errorf("list granted persons for KB %d: %w", kbID, err)
	}
	return persons, nil
}

// GrantKBAccess grants an AI person access to a knowledge base.
// Both existence and person type are validated at the application layer
// (the project forbids cross-table FK constraints). Granting is idempotent:
// re-granting an existing pair is a no-op.
func GrantKBAccess(kbID, personID int64) error {
	if _, err := Get[model.KnowledgeBase](kbID); err != nil {
		return fmt.Errorf("knowledge base %d not found: %w", kbID, err)
	}
	var person model.Person
	if err := database.DB.Where("id = ? AND type = ?", personID, model.PersonTypeAI).First(&person).Error; err != nil {
		return fmt.Errorf("AI person %d not found: %w", personID, err)
	}

	access := model.KBAccess{PersonID: personID, KBID: kbID}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.KBAccess{}).
			Where("person_id = ? AND kb_id = ?", personID, kbID).
			Count(&count).Error; err != nil {
			return fmt.Errorf("check existing KB access: %w", err)
		}
		if count > 0 {
			return nil
		}
		if err := tx.Create(&access).Error; err != nil {
			return fmt.Errorf("create KB access: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	applogger.Info("KB access granted", "kb_id", kbID, "person_id", personID)
	return nil
}

// RevokeKBAccess removes a person's access to a knowledge base.
// Revoking a non-existent grant is a no-op (idempotent), logged per the
// no-silent-handling rule.
func RevokeKBAccess(kbID, personID int64) error {
	result := database.DB.
		Where("person_id = ? AND kb_id = ?", personID, kbID).
		Delete(&model.KBAccess{})
	if result.Error != nil {
		return fmt.Errorf("revoke KB access (kb %d, person %d): %w", kbID, personID, result.Error)
	}
	if result.RowsAffected == 0 {
		applogger.Info("KB access revoke hit no grant, no-op", "kb_id", kbID, "person_id", personID)
	} else {
		applogger.Info("KB access revoked", "kb_id", kbID, "person_id", personID)
	}
	return nil
}

// DeleteAccessByKB removes all grants referencing the given KB. Called when
// a KB is deleted (application-level cascade — no FK constraints allowed).
// Returns the number of removed rows.
func DeleteAccessByKB(kbID int64) (int64, error) {
	result := database.DB.Where("kb_id = ?", kbID).Delete(&model.KBAccess{})
	if result.Error != nil {
		return 0, fmt.Errorf("delete KB access by KB %d: %w", kbID, result.Error)
	}
	return result.RowsAffected, nil
}

// DeleteAccessByPerson removes all grants referencing the given person.
// Called when an AI person is deleted (application-level cascade).
// Accepts a transaction handle so it participates in the caller's deletion
// transaction. Returns the number of removed rows.
func DeleteAccessByPerson(tx *gorm.DB, personID int64) (int64, error) {
	result := tx.Where("person_id = ?", personID).Delete(&model.KBAccess{})
	if result.Error != nil {
		return 0, fmt.Errorf("delete KB access by person %d: %w", personID, result.Error)
	}
	return result.RowsAffected, nil
}
