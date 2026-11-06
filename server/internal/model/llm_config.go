package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// ProbeStatus describes the current compatibility check for an LLM connection.
// Zero also represents configurations created before probe support.
type ProbeStatus int

const (
	ProbeStatusUnverified ProbeStatus = iota
	ProbeStatusPassed
	ProbeStatusFailed
)

// LLMConfig stores the configuration for connecting to an LLM API provider.
// Includes model ID, base URL, and API key for the OpenAI-compatible API.
type LLMConfig struct {
	ID                         int64       `gorm:"primaryKey;autoIncrement;type:INTEGER PRIMARY KEY AUTOINCREMENT" json:"id"`
	Name                       string      `gorm:"type:varchar(100);not null" json:"name"`
	ModelID                    string      `gorm:"type:varchar(100);not null;column:model_id" json:"model_id"`
	BaseURL                    string      `gorm:"type:varchar(255);not null;column:base_url" json:"base_url"`
	APIKey                     string      `gorm:"type:varchar(255);not null;column:api_key" json:"api_key"`
	Description                string      `gorm:"type:text;not null;default:''" json:"description"`
	ProbeStatus                ProbeStatus `gorm:"not null;default:0" json:"-"`
	ProbeConfigHash            string      `gorm:"type:varchar(64);not null;default:''" json:"-"`
	ProbeRequiredToolsOK       bool        `gorm:"not null;default:false" json:"-"`
	ProbeRequiredToolsError    string      `gorm:"type:text;not null;default:''" json:"-"`
	ProbeStructuredOutputOK    bool        `gorm:"not null;default:false" json:"-"`
	ProbeStructuredOutputError string      `gorm:"type:text;not null;default:''" json:"-"`
	ProbeCheckedAt             int64       `gorm:"not null;default:0" json:"-"` // Unix milliseconds; zero means never checked.
	CreatedAt                  time.Time   `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt                  time.Time   `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for LLMConfig.
func (LLMConfig) TableName() string { return "llm_configs" }

// ProbeFingerprint binds a probe result to the exact connection settings.
// Only the digest is stored; the API key is never returned as part of a probe.
func (c *LLMConfig) ProbeFingerprint() string {
	sum := sha256.Sum256([]byte(c.ModelID + "\x00" + c.BaseURL + "\x00" + c.APIKey))
	return hex.EncodeToString(sum[:])
}

// ProbePassed requires a passing check for the current connection settings.
func (c *LLMConfig) ProbePassed() bool {
	return c.ProbeStatus == ProbeStatusPassed && c.ProbeRequiredToolsOK && c.ProbeStructuredOutputOK &&
		c.ProbeCheckedAt > 0 && c.ProbeConfigHash != "" && c.ProbeConfigHash == c.ProbeFingerprint()
}
