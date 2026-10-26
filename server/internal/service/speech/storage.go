package speech

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"qingqiu-world-server/internal/config"
)

// resolveSpeechDataPath confines a relative storage path beneath DATA_ROOT/speech.
func resolveSpeechDataPath(relativePath string) (string, error) {
	clean := filepath.Clean(relativePath)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid speech relative path")
	}
	root := filepath.Join(config.Get().DataRoot, "speech")
	path := filepath.Join(root, clean)
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("speech path escapes data root")
	}
	return path, nil
}

// ResolveStoredAudio resolves a persisted History audio relative path safely.
func ResolveStoredAudio(relativePath string) (string, error) {
	return resolveSpeechDataPath(relativePath)
}

// loadVoiceSampleAudio reads one immutable voice sample and verifies its raw
// bytes against the internal content digest recorded in the database.
func loadVoiceSampleAudio(relativePath, expectedSHA256 string) ([]byte, error) {
	path, err := resolveSpeechDataPath(relativePath)
	if err != nil {
		return nil, err
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read voice sample audio: %w", err)
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("voice sample audio is empty")
	}
	if computed := voiceSampleAudioSHA256(audio); computed != expectedSHA256 {
		return nil, fmt.Errorf("voice sample audio SHA-256 verification failed")
	}
	return audio, nil
}

// voiceSampleAudioSHA256 identifies only the immutable audio file. Transcript
// and locale are independently editable metadata and therefore cannot be part
// of the file identity.
func voiceSampleAudioSHA256(audio []byte) string {
	digest := sha256.Sum256(audio)
	return hex.EncodeToString(digest[:])
}
