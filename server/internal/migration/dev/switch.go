package dev

import (
	"fmt"
	"os"
	"path/filepath"
)

type switchRecord struct {
	StagedData string `json:"staged_data"`
	Quarantine string `json:"quarantine"`
}

func (m *manager) switchJournal() string { return filepath.Join(m.store, "switch.json") }

// switchActive uses a durable journal because replacing an existing directory
// takes two renames. Startup completes or rolls back an interrupted switch
// before opening the active database. The reason (seed, upgrade, promote)
// labels the retained copy for manual diagnosis.
func (m *manager) switchActive(stagedData, reason string) error {
	if !inside(m.store, stagedData) {
		return fmt.Errorf("staged data is outside the developer store")
	}
	if err := validateActiveCopy(stagedData); err != nil {
		return fmt.Errorf("staged active data is incomplete: %w", err)
	}
	quarantineRoot := filepath.Join(m.store, "quarantine")
	if err := os.MkdirAll(quarantineRoot, 0700); err != nil {
		return err
	}
	quarantine := filepath.Join(quarantineRoot, quarantineName(m.quarantineLabel(reason)))
	record := switchRecord{StagedData: stagedData, Quarantine: quarantine}
	if err := writeJSONAtomic(m.switchJournal(), record); err != nil {
		return err
	}
	if _, err := os.Stat(m.active); err == nil {
		if err := os.Rename(m.active, quarantine); err != nil {
			return fmt.Errorf("quarantine previous active data: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stagedData, m.active); err != nil {
		if _, oldErr := os.Stat(quarantine); oldErr == nil {
			if restoreErr := os.Rename(quarantine, m.active); restoreErr != nil {
				return fmt.Errorf("activate staged developer data: %w; restore previous active data: %v", err, restoreErr)
			}
		}
		return fmt.Errorf("activate staged developer data: %w", err)
	}
	return os.Remove(m.switchJournal())
}

func validateActiveCopy(path string) error {
	state, err := loadState(path)
	if err != nil {
		return err
	}
	if state.FormatVersion != stateFormat || state.Status != statusComplete {
		return fmt.Errorf("active state is not complete")
	}
	version, err := inspectDataDB(path)
	if err != nil {
		return err
	}
	if version != state.BaseRelease {
		return fmt.Errorf("database release %s differs from state base %s", version, state.BaseRelease)
	}
	return nil
}

func (m *manager) recoverSwitch() error {
	var record switchRecord
	if err := readJSON(m.switchJournal(), &record); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read interrupted data switch: %w", err)
	}
	if !inside(m.store, record.StagedData) || !inside(filepath.Join(m.store, "quarantine"), record.Quarantine) {
		return fmt.Errorf("interrupted data switch has unsafe paths")
	}
	if _, err := os.Stat(m.active); err == nil {
		if validateActiveCopy(m.active) == nil {
			return os.Remove(m.switchJournal())
		}
		if _, err := os.Stat(record.Quarantine); err != nil {
			return fmt.Errorf("active data is incomplete and previous copy is unavailable: %w", err)
		}
		failed := filepath.Join(m.store, "quarantine", quarantineName(reasonFailed))
		if err := os.Rename(m.active, failed); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if validateActiveCopy(record.StagedData) == nil {
		if err := os.Rename(record.StagedData, m.active); err != nil {
			return err
		}
		return os.Remove(m.switchJournal())
	}
	if _, err := os.Stat(record.Quarantine); err == nil {
		if err := os.Rename(record.Quarantine, m.active); err != nil {
			return err
		}
		return os.Remove(m.switchJournal())
	}
	return fmt.Errorf("interrupted switch has neither complete staged data nor previous active data")
}
