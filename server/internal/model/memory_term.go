package model

import "time"

// MemorySourceKind identifies the authoritative row represented by a term.
type MemorySourceKind int

const (
	// MemorySourceEvent points to an observed occurrence.
	MemorySourceEvent MemorySourceKind = iota + 1
	// MemorySourceAction points to a top-level behavior selected by Decide.
	MemorySourceAction
	// MemorySourceWork points to a persistent Focus work record.
	MemorySourceWork
	// MemorySourceFocusHandoff points to a Focus result or continuity record.
	MemorySourceFocusHandoff
)

// MemoryTerm is a rebuildable search index, never a second factual record.
// Authorization and source validity are checked against the original tables.
type MemoryTerm struct {
	// ID identifies one derived index row, not a remembered fact.
	ID int64 `gorm:"primaryKey;autoIncrement"`
	// Term is a normalized lexical key extracted from source text.
	Term string `gorm:"type:text;not null;uniqueIndex:idx_memory_term_source;index:idx_memory_term_lookup"`
	// SourceKind and SourceID locate the authoritative source row.
	SourceKind MemorySourceKind `gorm:"not null;uniqueIndex:idx_memory_term_source;index:idx_memory_term_lookup"`
	SourceID   int64            `gorm:"not null;uniqueIndex:idx_memory_term_source"`
	// OwnerPersonID is zero for Events; other sources retain their owner.
	OwnerPersonID int64 `gorm:"not null;index:idx_memory_term_lookup"`
	// SourceCreatedAt copies the source time as rebuildable index metadata.
	SourceCreatedAt time.Time `gorm:"not null;index:idx_memory_term_lookup"`
	// SourceVersion detects when the derived terms need replacement.
	SourceVersion int64 `gorm:"not null"`
}

// TableName returns the rebuildable term index table name.
func (MemoryTerm) TableName() string { return "memory_terms" }
