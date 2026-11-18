package world

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata"

	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

var worldTimezone atomic.Pointer[time.Location]
var uninitializedTimezoneWarning sync.Once

// InitTime loads the persisted world timezone after checking whether a world
// already has persons. A missing or invalid file in such a world is an error.
func InitTime(db *gorm.DB, dataRoot string) error {
	loc, err := EnsureTimezone(db, dataRoot)
	if err != nil {
		return err
	}
	worldTimezone.Store(loc)
	return nil
}

// EnsureTimezone validates the staged or active data set without relying on
// process-local timezone state. Development migrations call it on their staged
// copy before any file or database conversion.
func EnsureTimezone(db *gorm.DB, dataRoot string) (*time.Location, error) {
	var tableCount int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'persons'").Scan(&tableCount).Error; err != nil {
		return nil, fmt.Errorf("inspect world persons table: %w", err)
	}
	var personCount int64
	if tableCount > 0 {
		if err := db.Raw("SELECT count(*) FROM persons").Scan(&personCount).Error; err != nil {
			return nil, fmt.Errorf("count world persons: %w", err)
		}
	}
	path := filepath.Join(dataRoot, "tz.txt")
	data, err := os.ReadFile(path)
	if err == nil {
		loc, loadErr := loadTimezone(string(data))
		if loadErr == nil {
			return loc, nil
		}
		if personCount > 0 {
			return nil, fmt.Errorf("existing world has invalid tz.txt: %w", loadErr)
		}
		applogger.Warn("invalid timezone file in world without persons; replacing it", "path", path, "error", loadErr)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read tz.txt: %w", err)
	} else if personCount > 0 {
		return nil, fmt.Errorf("existing world is missing tz.txt: %s", path)
	}

	name, err := systemTimezoneName()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(name), 0644); err != nil {
		return nil, fmt.Errorf("write tz.txt: %w", err)
	}
	readback, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read back tz.txt: %w", err)
	}
	if strings.TrimSpace(string(readback)) != name {
		return nil, fmt.Errorf("tz.txt readback differs from written timezone %q", name)
	}
	return loadTimezone(string(readback))
}

func loadTimezone(raw string) (*time.Location, error) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "Local" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return nil, fmt.Errorf("invalid timezone name %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", name, err)
	}
	return loc, nil
}

// systemTimezoneName reads the OS timezone configuration, never the TZ
// environment variable. Windows has no portable IANA system name here.
func systemTimezoneName() (string, error) {
	if runtime.GOOS == "windows" {
		return "UTC", nil
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if index := strings.Index(link, "zoneinfo/"); index >= 0 {
			name := link[index+len("zoneinfo/"):]
			if _, err := loadTimezone(name); err == nil {
				return name, nil
			}
		}
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		name := strings.TrimSpace(string(data))
		if _, err := loadTimezone(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot determine an IANA system timezone")
}

// Timezone returns the persisted world location. Before startup initialization,
// it warns and uses UTC so isolated unit tests can construct world timestamps.
func Timezone() *time.Location {
	if loc := worldTimezone.Load(); loc != nil {
		return loc
	}
	uninitializedTimezoneWarning.Do(func() {
		applogger.Warn("world timezone is not initialized; using UTC")
	})
	return time.UTC
}

// Now returns the current instant expressed in the world timezone.
func Now() time.Time { return time.Now().In(Timezone()) }

// Today returns the world's current calendar date.
func Today() string { return Now().Format("2006-01-02") }

// FormatTime shows an instant in the world timezone with its UTC offset.
func FormatTime(at time.Time) string { return at.In(Timezone()).Format(time.RFC3339) }
