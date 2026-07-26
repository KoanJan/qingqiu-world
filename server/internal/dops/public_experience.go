package dops

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ListPublicExperiences lists PublicExperiences in pagination
func ListPublicExperiences(offset, limit int) ([]model.PublicExperience, error) {
	var entities []model.PublicExperience
	if err := database.DB.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// DeletePublicExperienceVectors deletes all the vectors of the PublicExperience
func DeletePublicExperienceVectors(docID int64) error {
	return database.DB.Where("experience_id = ?", docID).Delete(&model.PublicExperienceVector{}).Error
}
