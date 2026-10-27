package model

import "time"

// Entity type constants for entity_profiles.entity_type.
type EntityType int

const (
	// EntityTypePerson represents a person entity (AI or human).
	EntityTypePerson EntityType = iota + 1
	// EntityTypeSession represents a session entity.
	EntityTypeSession
)

// EntityProfile represents a directional narrative that one person has formed
// about a specific entity (person or session).
//
// Unlike observations (which are mechanical event recordings), EntityProfile
// is an LLM-generated reflective narrative: "What do I think about X?"
//
// Key design:
//   - Each (person_id, entity_type, entity_id) has exactly one profile row.
//     New reflections replace the old narrative (update, not append).
//   - Evidence selection: top K observations by importance, with newer IDs
//     breaking ties; there is no survival_count gate.
//   - input_md5 is the MD5 hash of the evidence text at generation time. It is
//     compared before re-generation to skip when input is unchanged.
//   - Each generation is fresh — no prior narrative is fed to the LLM.
//   - Rate limit: same profile at most once per six hours.
type EntityProfile struct {
	ID            int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID      int64      `gorm:"not null;uniqueIndex:idx_entity_profile;column:person_id" json:"person_id"`
	EntityType    EntityType `gorm:"not null;uniqueIndex:idx_entity_profile;column:entity_type" json:"entity_type"`
	EntityID      int64      `gorm:"not null;uniqueIndex:idx_entity_profile;column:entity_id" json:"entity_id"`
	Narrative     string     `gorm:"type:text;not null" json:"narrative"`
	EvidenceCount int        `gorm:"not null;default:0;column:evidence_count" json:"evidence_count"`
	InputMD5      string     `gorm:"not null;default:'';column:input_md5" json:"input_md5"`
	LastUpdatedAt time.Time  `gorm:"not null;autoUpdateTime;column:last_updated_at" json:"last_updated_at"`
	CreatedAt     time.Time  `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table name for EntityProfile.
func (EntityProfile) TableName() string { return "entity_profiles" }
