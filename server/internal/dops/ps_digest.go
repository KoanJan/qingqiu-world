package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreatePSDigest persists a private-space session digest for the given agent
// person and returns the created record.
func CreatePSDigest(personID int64, digest string) (*model.PSDigest, error) {
	record := &model.PSDigest{
		PersonID: personID,
		Digest:   digest,
	}
	if err := database.DB.Create(record).Error; err != nil {
		return nil, fmt.Errorf("create ps digest: %w", err)
	}
	return record, nil
}
