package scripts

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/world"

	"gorm.io/gorm"
)

// Convert2 updates instants and files on the isolated development copy. The
// manager discards that copy after any error, so a retry starts from complete
// data rather than from a partially copied directory.
func Convert2(tx *gorm.DB, dataRoot string) error {
	if _, err := world.EnsureTimezone(tx, dataRoot); err != nil {
		return err
	}
	var alarms []model.ScheduledEvent
	if err := tx.Select("id", "trigger_at").Find(&alarms).Error; err != nil {
		return fmt.Errorf("load historical alarm times: %w", err)
	}
	for _, alarm := range alarms {
		if err := tx.Model(&model.ScheduledEvent{}).Where("id = ?", alarm.ID).
			UpdateColumn("trigger_at", alarm.TriggerAt.UTC()).Error; err != nil {
			return fmt.Errorf("normalize alarm %d time: %w", alarm.ID, err)
		}
	}
	for _, root := range []struct{ key, name string }{
		{"AOS_ROOT", "aos"},
		{"AOS_META_ROOT", "aosmeta"},
		{"JINSHU_ROOT", "jinshu"},
	} {
		if err := copyLegacyRoot(root.key, root.name, dataRoot); err != nil {
			return err
		}
	}
	return nil
}

func copyLegacyRoot(key, name, stagedRoot string) error {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	stagedRoot, err := filepath.EvalSymlinks(stagedRoot)
	if err != nil {
		return fmt.Errorf("resolve staged data root for %s: %w", key, err)
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve %s home: %w", key, err)
		}
		raw = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(raw, "~"), "/"))
	}
	source, err := filepath.Abs(raw)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", key, err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(source); resolveErr == nil {
		source = resolved
	} else if !os.IsNotExist(resolveErr) {
		return fmt.Errorf("resolve %s source: %w", key, resolveErr)
	}
	activeRoot, err := filepath.Abs(config.Get().DataRoot)
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(activeRoot); resolveErr == nil {
		activeRoot = resolved
	} else if !os.IsNotExist(resolveErr) {
		return fmt.Errorf("resolve active data root for %s: %w", key, resolveErr)
	}
	// Paths inside the active collection refer to its staged counterpart. An
	// external legacy root remains an external, read-only copy source.
	if rel, err := filepath.Rel(activeRoot, source); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		source = filepath.Join(stagedRoot, rel)
	}
	source, err = filepath.EvalSymlinks(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve %s source: %w", key, err)
	}
	target := filepath.Join(stagedRoot, name)
	target = filepath.Clean(target)
	if source == target {
		return nil
	}
	if pathsOverlap(source, target) {
		return fmt.Errorf("%s source %s overlaps target %s", key, source, target)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect %s source: %w", key, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s source must be a regular directory: %s", key, source)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read %s source: %w", key, err)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := copyLegacyTree(source, target); err != nil {
		return fmt.Errorf("copy %s from %s: %w", key, source, err)
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func copyLegacyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in legacy root: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if entry.IsDir() {
			if info, err := os.Lstat(dest); err == nil && !info.IsDir() {
				return fmt.Errorf("directory conflicts with file: %s", dest)
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			return os.MkdirAll(dest, info.Mode().Perm())
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported legacy entry: %s", path)
		}
		if info, err := os.Lstat(dest); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("non-regular destination conflicts with legacy file: %s", dest)
			}
			equal, err := sameFileContent(path, dest)
			if err != nil {
				return err
			}
			if equal {
				return nil
			}
			return fmt.Errorf("different content already exists: %s", dest)
		} else if !os.IsNotExist(err) {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		inErr := in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if inErr != nil {
			return inErr
		}
		return closeErr
	})
}

func sameFileContent(a, b string) (bool, error) {
	digest := func(path string) ([sha256.Size]byte, error) {
		var sum [sha256.Size]byte
		file, err := os.Open(path)
		if err != nil {
			return sum, err
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return sum, err
		}
		copy(sum[:], hash.Sum(nil))
		return sum, nil
	}
	left, err := digest(a)
	if err != nil {
		return false, err
	}
	right, err := digest(b)
	if err != nil {
		return false, err
	}
	return left == right, nil
}
