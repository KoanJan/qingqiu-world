package model

import "time"

// DailyMemoryGroupType identifies the resource whose contact-day activity one
// dated record summarizes. The values are intentionally independent from
// EventType so impossible combinations cannot be represented. Zero is not a
// valid group type.
type DailyMemoryGroupType int

const (
	DailyMemoryGroupHeartbeat DailyMemoryGroupType = iota + 1
	DailyMemoryGroupChat
	DailyMemoryGroupJinshu
	DailyMemoryGroupReminder
	DailyMemoryGroupSystemNotice
	DailyMemoryGroupWork
	DailyMemoryGroupBiography
	DailyMemoryGroupPSDigest
)

// DailyMemory is an observer-owned, append-only account of one contact-day
// group. A completed day is never rewritten: later days cannot change what was
// already remembered. The observation and action rows remain the source.
type DailyMemory struct {
	ID        int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID  int64  `gorm:"not null;uniqueIndex:idx_daily_owner_day_shard;uniqueIndex:idx_daily_owner_group;index:idx_daily_owner_time" json:"person_id"`
	LocalDate string `gorm:"type:text;not null;uniqueIndex:idx_daily_owner_day_shard;uniqueIndex:idx_daily_owner_group" json:"local_date"`
	Shard     int    `gorm:"not null;uniqueIndex:idx_daily_owner_day_shard" json:"shard"` // Stable local slot, not a fixed-size partition.
	// GroupType and GroupID identify one resource group within LocalDate.
	// Heartbeat, Jinshu, Reminder, SystemNotice, and Biography are day-level
	// singletons and use zero; Chat uses a Session ID, Work a Work ID, and
	// PSDigest a digest ID.
	GroupType DailyMemoryGroupType `gorm:"not null;default:0;uniqueIndex:idx_daily_owner_group" json:"group_type"`
	GroupID   int64                `gorm:"not null;default:0;uniqueIndex:idx_daily_owner_group" json:"group_id"`
	Summary   string               `gorm:"type:text;not null" json:"summary"`
	StartAt   time.Time            `gorm:"not null;index:idx_daily_owner_time" json:"start_at"`
	EndAt     time.Time            `gorm:"not null" json:"end_at"`
	Embedding []byte               `gorm:"type:blob;not null" json:"-"`
	CreatedAt time.Time            `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time            `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the stable daily index table name.
func (DailyMemory) TableName() string { return "daily_memories" }

// CognitiveMemory identifies an enduring concern, separate from its revisions.
// UpdatedAt means the last completed review, including a review with no new version.
type CognitiveMemory struct {
	ID                  int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID            int64     `gorm:"not null;index:idx_cognitive_owner_updated" json:"person_id"`
	Description         string    `gorm:"type:text;not null" json:"description"`
	Embedding           []byte    `gorm:"type:blob;not null" json:"-"`            // Vector of the enduring concern, used for identity candidates.
	ImpressionEmbedding []byte    `gorm:"type:blob;not null;default:''" json:"-"` // Vector of the current impression, used for recall.
	CreatedAt           time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt           time.Time `gorm:"not null;autoUpdateTime;index:idx_cognitive_owner_updated" json:"updated_at"`
}

// TableName returns the stable cognitive identity table name.
func (CognitiveMemory) TableName() string { return "cognitive_memories" }

// CognitiveMemoryVersion preserves one impression and understanding formed by
// a process. Impression is the subject's coarse, first-person view of what the
// concern currently is and where it stands; Understanding is the interpretation
// and what it implies. Neither is a verified world fact.
type CognitiveMemoryVersion struct {
	ID            int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	MemoryID      int64     `gorm:"not null;uniqueIndex:idx_cognitive_revision" json:"memory_id"`
	Revision      int       `gorm:"not null;uniqueIndex:idx_cognitive_revision" json:"revision"`
	Impression    string    `gorm:"type:text;not null;default:''" json:"impression"`
	Understanding string    `gorm:"type:text;not null" json:"understanding"`
	ProcessID     int64     `gorm:"not null;uniqueIndex" json:"process_id"`
	CreatedAt     time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the immutable revision table name.
func (CognitiveMemoryVersion) TableName() string { return "cognitive_memory_versions" }

// CognitiveProcessStatus describes the lifecycle of a background cognition.
type CognitiveProcessStatus int

const (
	CognitiveProcessRunning CognitiveProcessStatus = iota + 1
	CognitiveProcessPaused
	CognitiveProcessEnded
)

// CognitiveProcessKind distinguishes a system-routed review from a voluntary
// discovery. A process always records its kind when it is created.
type CognitiveProcessKind int

const (
	// CognitiveProcessKindUpdate reviews one memory the router already selected.
	CognitiveProcessKindUpdate CognitiveProcessKind = 1
	// CognitiveProcessKindDiscovery lets the subject name a concern and choose a target.
	CognitiveProcessKindDiscovery CognitiveProcessKind = 2
)

// CognitiveProcessOutcome records how a process ended. Zero means no terminal
// result was recorded, which covers retry exhaustion, so such a process never
// advances a review watermark.
type CognitiveProcessOutcome int

const (
	CognitiveProcessOutcomeUnrecorded CognitiveProcessOutcome = 0
	CognitiveProcessOutcomeWritten    CognitiveProcessOutcome = 1
	CognitiveProcessOutcomeConfirmed  CognitiveProcessOutcome = 2
	CognitiveProcessOutcomeNoResult   CognitiveProcessOutcome = 3
)

// CognitiveProcess owns a single checkpoint file, not a Decision or Action.
// MemoryID is the identity selected by choose_cognitive_memory, or the identity
// fixed by a system-routed update, or zero for a new concern that has not
// selected one yet. It is stored so the daily quota can attribute a process
// that ends without writing a version. MemoryRevision is the version the update
// process reviewed, so the final transaction can reject a changed version.
// Failures counts failed model attempts and RetryAt is the Unix time of the next
// allowed attempt; a failed process stays paused so the same conversation can be
// retried. StorageFailures counts embedding or database write failures
// separately, because those must not consume the model retry budget.
type CognitiveProcess struct {
	ID              int64                   `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID        int64                   `gorm:"not null;index:idx_cognitive_process_owner" json:"person_id"`
	Status          CognitiveProcessStatus  `gorm:"not null;index:idx_cognitive_process_owner" json:"status"`
	Kind            CognitiveProcessKind    `gorm:"not null;default:0" json:"kind"`
	Outcome         CognitiveProcessOutcome `gorm:"not null;default:0" json:"outcome"`
	MemoryID        int64                   `gorm:"not null;default:0" json:"memory_id"`
	MemoryRevision  int                     `gorm:"not null;default:0" json:"memory_revision"`
	Failures        int                     `gorm:"not null;default:0" json:"failures"`
	StorageFailures int                     `gorm:"not null;default:0" json:"storage_failures"`
	RetryAt         int64                   `gorm:"not null;default:0" json:"retry_at"`
	Epoch           int64                   `gorm:"not null" json:"epoch"`
	CreatedAt       time.Time               `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time               `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the process lifecycle table name.
func (CognitiveProcess) TableName() string { return "cognitive_processes" }
