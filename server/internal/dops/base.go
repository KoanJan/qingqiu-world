package dops

import (
	"qingqiu-world-server/internal/database"
)

// Get retrieves an entity by ID.
func Get[T any](id int64) (*T, error) {
	var entity T
	if err := database.DB.First(&entity, id).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

// GetMulti retrieves multiple entities with pagination.
func GetMulti[T any](skip, limit int) ([]T, error) {
	var entities []T
	if err := database.DB.Offset(skip).Limit(limit).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// Create inserts a new entity.
func Create[T any](entity *T) error {
	return database.DB.Create(entity).Error
}

// Update applies partial updates to an entity.
func Update[T any](entity *T, updates map[string]interface{}) error {
	return database.DB.Model(entity).Updates(updates).Error
}

// Delete removes an entity by ID from the database.
func Delete[T any](id int64) error {
	var entity T
	return database.DB.Delete(&entity, id).Error
}
