package dops

import (
	"fmt"

	"gorm.io/gorm"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreatePSDigest persists a private-space session digest for the given agent
// person and returns the created record.
func CreatePSDigest(personID int64, digest string, actionIDs ...int64) (*model.PSDigest, error) {
	record := &model.PSDigest{
		PersonID: personID,
		Digest:   digest,
	}
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		for _, actionID := range actionIDs {
			var source model.Action
			if err := tx.Table("actions AS a").Select("a.*").Joins("JOIN decisions AS d ON d.id = a.decision_id").Where("a.id = ? AND a.type = ? AND d.person_id = ?", actionID, model.ActionTypeEnterPrivateSpace, personID).Take(&source).Error; err != nil {
				return fmt.Errorf("digest participant Action %d: %w", actionID, err)
			}
			if err := tx.Create(&model.ActionEffect{ActionID: actionID, EffectType: model.ActionEffectPSDigest, EffectID: record.ID}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("create ps digest: %w", err)
	}
	return record, nil
}
