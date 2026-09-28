package speech

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	renderercore "qingqiu-world-server/internal/service/speech/renderer"
)

const maxVoiceSampleAudioBytes = 32 << 20

var supportedVoiceLocales = map[string]struct{}{
	"zh-CN": {},
	"en-US": {},
	"ja-JP": {},
}

// VoiceSampleUpload is an optional replacement for the immutable audio file.
// Its transcript and locale are ordinary AgentVoice configuration fields.
type VoiceSampleUpload struct {
	Filename string
	Audio    io.Reader
}

// VoiceBindingUpdate is the declarative Agent voice form. Scalar fields always
// describe the desired version; a nil SampleAudio preserves the current file.
type VoiceBindingUpdate struct {
	RendererID       int64
	SampleTranscript string
	SampleLocale     string
	SampleAudio      *VoiceSampleUpload
}

// SaveAgentVoice inserts an immutable voice version when the effective sample
// or renderer binding changes. Re-submitting the current configuration is a
// no-op and returns the existing latest version.
func SaveAgentVoice(ctx context.Context, personID int64, update VoiceBindingUpdate) (*model.AgentVoice, error) {
	if update.RendererID < 0 {
		return nil, fmt.Errorf("tts_renderer_id must not be negative")
	}
	person, err := dops.GetPerson(personID)
	if err != nil {
		return nil, fmt.Errorf("load voice agent: %w", err)
	}
	if person.Type != model.PersonTypeAI {
		return nil, fmt.Errorf("person %d is not an AI agent", personID)
	}
	if update.RendererID > 0 {
		if err := validateVoiceRenderer(ctx, update.RendererID); err != nil {
			return nil, err
		}
	}
	var prepared *preparedVoiceSample
	if update.SampleAudio != nil {
		prepared, err = prepareVoiceSample(*update.SampleAudio)
		if err != nil {
			return nil, err
		}
	}
	var voice *model.AgentVoice
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		current, err := findLatestAgentVoice(tx, personID)
		if err != nil {
			return err
		}
		// Recheck existence inside the version-creation transaction so a
		// concurrent Renderer deletion cannot pass an earlier validation and
		// leave the new current Voice pointing at a missing connection.
		if update.RendererID > 0 {
			var renderer model.TTSRenderer
			if err := tx.First(&renderer, update.RendererID).Error; err != nil {
				return fmt.Errorf("tts renderer %d disappeared before voice save: %w", update.RendererID, err)
			}
		}
		next := model.AgentVoice{
			PersonID:         personID,
			TTSRendererID:    update.RendererID,
			SampleTranscript: strings.TrimSpace(update.SampleTranscript),
			SampleLocale:     update.SampleLocale,
		}
		if current != nil {
			next.SampleAudioSHA256 = current.SampleAudioSHA256
			next.SampleAudioExtension = current.SampleAudioExtension
			next.SampleAudioRelativePath = current.SampleAudioRelativePath
		}
		if prepared != nil {
			next.SampleAudioSHA256 = prepared.audioSHA256
			next.SampleAudioExtension = prepared.extension
			next.SampleAudioRelativePath = voiceSampleRelativePath(personID, prepared.extension, prepared.audioSHA256)
		}
		if err := validateVoiceSampleConfiguration(&next); err != nil {
			return err
		}
		if current != nil && sameVoiceConfiguration(current, &next) {
			voice = current
			return nil
		}
		if prepared != nil {
			if err := writeVoiceSampleAudio(next.SampleAudioRelativePath, prepared.audio); err != nil {
				return err
			}
		}
		if err := tx.Create(&next).Error; err != nil {
			return fmt.Errorf("create agent voice version: %w", err)
		}
		voice = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return voice, nil
}

// preparedVoiceSample is a validated sample ready to be persisted.
type preparedVoiceSample struct {
	audio       []byte
	extension   string
	audioSHA256 string
}

// prepareVoiceSample validates one uploaded file and derives its raw-byte
// content digest, so an invalid sample is rejected before any write happens.
func prepareVoiceSample(sample VoiceSampleUpload) (*preparedVoiceSample, error) {
	audioBytes, err := readVoiceSampleAudio(sample.Audio)
	if err != nil {
		return nil, err
	}
	extension, err := allowedVoiceSampleExtension(sample.Filename)
	if err != nil {
		return nil, err
	}
	if err := validateVoiceSampleAudio(extension, audioBytes); err != nil {
		return nil, err
	}
	return &preparedVoiceSample{
		audio:       audioBytes,
		extension:   extension,
		audioSHA256: voiceSampleAudioSHA256(audioBytes),
	}, nil
}

// validateVoiceSampleConfiguration keeps metadata and file presence coherent.
// Renderer-only configurations are valid, but a stored sample is never allowed
// to lose the transcript or locale needed by provider adapters.
func validateVoiceSampleConfiguration(voice *model.AgentVoice) error {
	if voice.SampleAudioSHA256 == "" {
		if voice.SampleTranscript != "" || voice.SampleLocale != "" {
			return fmt.Errorf("sample audio is required before sample transcript or locale can be saved")
		}
		return nil
	}
	if voice.SampleTranscript == "" {
		return fmt.Errorf("sample transcript is required")
	}
	if _, supported := supportedVoiceLocales[voice.SampleLocale]; !supported {
		return fmt.Errorf("unsupported sample locale %q", voice.SampleLocale)
	}
	return nil
}

// validateVoiceRenderer rejects an unknown renderer or one whose
// provider-specific connection configuration is invalid.
func validateVoiceRenderer(ctx context.Context, rendererID int64) error {
	renderer, err := dops.GetTTSRenderer(rendererID)
	if err != nil {
		return err
	}
	return renderercore.Validate(ctx, *renderer)
}

// findLatestAgentVoice returns nil when the Agent has no voice version yet.
func findLatestAgentVoice(tx *gorm.DB, personID int64) (*model.AgentVoice, error) {
	var voice model.AgentVoice
	err := tx.Where("person_id = ?", personID).Order("id DESC").First(&voice).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load agent voice for person %d: %w", personID, err)
	}
	return &voice, nil
}

// sameVoiceConfiguration deliberately excludes RendererVoiceRef and timestamps:
// those fields are derived runtime state, not user-owned configuration.
func sameVoiceConfiguration(left, right *model.AgentVoice) bool {
	return left.PersonID == right.PersonID &&
		left.TTSRendererID == right.TTSRendererID &&
		left.SampleAudioSHA256 == right.SampleAudioSHA256 &&
		left.SampleTranscript == right.SampleTranscript &&
		left.SampleAudioExtension == right.SampleAudioExtension &&
		left.SampleAudioRelativePath == right.SampleAudioRelativePath &&
		left.SampleLocale == right.SampleLocale
}

func readVoiceSampleAudio(source io.Reader) ([]byte, error) {
	limited := io.LimitReader(source, maxVoiceSampleAudioBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read sample audio: %w", err)
	}
	if len(data) == 0 || len(data) > maxVoiceSampleAudioBytes {
		return nil, fmt.Errorf("sample audio must be between 1 byte and %d bytes", maxVoiceSampleAudioBytes)
	}
	return data, nil
}

func allowedVoiceSampleExtension(filename string) (string, error) {
	extension := strings.ToLower(filepath.Ext(filename))
	switch extension {
	case ".wav", ".mp3", ".flac":
		return extension, nil
	default:
		return "", fmt.Errorf("sample audio format %q is unsupported", extension)
	}
}

// voiceSampleRelativePath returns the content-addressed path shared by voice
// versions that bind the same Agent sample to different renderers.
func voiceSampleRelativePath(personID int64, extension, audioSHA256 string) string {
	return filepath.Join("samples", fmt.Sprintf("%d", personID), audioSHA256[:16]+extension)
}

// writeVoiceSampleAudio publishes an immutable sample. Existing identical
// bytes are reused when another voice version points at the same sample.
func writeVoiceSampleAudio(relativePath string, audio []byte) error {
	path, err := resolveSpeechDataPath(relativePath)
	if err != nil {
		return err
	}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Equal(existing, audio) {
			return fmt.Errorf("stored voice sample differs from content-addressed input")
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read stored voice sample: %w", readErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create voice sample directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".sample-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary voice sample file: %w", err)
	}
	temporaryPath := temporary.Name()
	if _, err := temporary.Write(audio); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return fmt.Errorf("write voice sample audio: %w", err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return fmt.Errorf("close voice sample audio: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish voice sample audio: %w", err)
	}
	return nil
}

// validateVoiceSampleAudio rejects arbitrary files renamed to an accepted suffix.
// Full decoding remains the provider's responsibility, but the voice sample
// must at least contain the advertised audio container before it is persisted.
func validateVoiceSampleAudio(extension string, audio []byte) error {
	valid := false
	switch extension {
	case ".wav":
		valid = len(audio) >= 12 && string(audio[:4]) == "RIFF" && string(audio[8:12]) == "WAVE"
	case ".flac":
		valid = len(audio) >= 4 && string(audio[:4]) == "fLaC"
	case ".mp3":
		valid = len(audio) >= 3 && string(audio[:3]) == "ID3"
		if !valid && len(audio) >= 2 {
			valid = audio[0] == 0xff && audio[1]&0xe0 == 0xe0
		}
	}
	if !valid {
		return fmt.Errorf("voice sample audio does not match %s format", extension)
	}
	return nil
}
