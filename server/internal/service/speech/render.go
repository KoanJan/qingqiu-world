package speech

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	renderercore "qingqiu-world-server/internal/service/speech/renderer"
)

var renderWorker = struct {
	wake chan struct{}
	once sync.Once
}{wake: make(chan struct{}, 1)}

// Start starts the recoverable single-flight speech worker.
func Start(ctx context.Context) {
	renderWorker.once.Do(func() {
		if err := recoverInterruptedRenders(); err != nil {
			applogger.Error("speech worker: recover interrupted renders failed", "error", err)
			panic(err)
		}
		go runWorker(ctx)
		Wake()
	})
}

// recoverInterruptedRenders requeues work left in rendering by a previous
// process. The speech worker is intentionally single-process and serial, so no
// live owner can remain after a service restart.
func recoverInterruptedRenders() error {
	result := database.DB.Model(&model.SpeechRenderHistory{}).
		Where("status = ?", model.SpeechRenderStatusRendering).
		Update("status", model.SpeechRenderStatusPending)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		applogger.Warn("speech worker: recovered interrupted renders", "count", result.RowsAffected)
	}
	return nil
}

// Wake requests prompt processing without queuing duplicate work items.
func Wake() {
	select {
	case renderWorker.wake <- struct{}{}:
	default:
	}
}

// EnsureRender returns a success record or atomically queues one pending render.
func EnsureRender(messageID int64) (*model.SpeechRenderHistory, error) {
	message, err := dops.Get[model.Message](messageID)
	if err != nil {
		return nil, err
	}
	person, err := dops.GetPerson(message.PersonID)
	if err != nil {
		return nil, err
	}
	if person.Type != model.PersonTypeAI {
		return nil, fmt.Errorf("message %d is not an Agent message", messageID)
	}
	tx := database.DB.Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var history model.SpeechRenderHistory
	err = tx.Where("message_id = ?", messageID).First(&history).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		history = model.SpeechRenderHistory{
			MessageID: messageID,
			Status:    model.SpeechRenderStatusPending,
		}
		if err := tx.Create(&history).Error; err != nil {
			// The unique message_id index is the final arbiter when simultaneous
			// play requests both observe an absent history. Reuse the winner.
			if isUniqueConstraintError(err) {
				if rollbackErr := tx.Rollback().Error; rollbackErr != nil {
					return nil, fmt.Errorf("rollback concurrent speech history create: %w", rollbackErr)
				}
				// Benign race: simultaneous play requests both observed no history.
				applogger.Debug("speech history create raced; reusing the committed winner", "message_id", messageID)
				return dops.GetSpeechRenderHistoryByMessageID(messageID)
			}
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if history.Status == model.SpeechRenderStatusFailed {
		if err := tx.Model(&history).Updates(map[string]interface{}{
			"status":        model.SpeechRenderStatusPending,
			"error_message": "",
		}).Error; err != nil {
			return nil, err
		}
		history.Status = model.SpeechRenderStatusPending
		history.ErrorMessage = ""
	} else if history.Status != model.SpeechRenderStatusSuccess && history.Status != model.SpeechRenderStatusPending && history.Status != model.SpeechRenderStatusRendering {
		return nil, fmt.Errorf("message %d has unexpected speech render status %d", messageID, history.Status)
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	if history.Status != model.SpeechRenderStatusSuccess {
		Wake()
	}
	return &history, nil
}

// WaitForRender waits only for a terminal state; it never cancels the worker.
func WaitForRender(ctx context.Context, messageID int64) (*model.SpeechRenderHistory, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		history, err := dops.GetSpeechRenderHistoryByMessageID(messageID)
		if err != nil {
			return nil, err
		}
		if history.Status == model.SpeechRenderStatusSuccess || history.Status == model.SpeechRenderStatusFailed {
			return history, nil
		}
		select {
		case <-ctx.Done():
			return history, ctx.Err()
		case <-ticker.C:
		}
	}
}

func runWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renderWorker.wake:
		case <-ticker.C:
		}
		// Go terminates the whole process on an unrecovered panic in any
		// goroutine, so the drain is contained here to keep the worker alive for
		// later renders. A panic raised inside one render is normally recovered
		// earlier by renderGuarded, which also marks that render failed.
		func() {
			defer func() {
				if r := recover(); r != nil {
					applogger.Error("speech worker recovered from panic while draining renders", "panic", r)
				}
			}()
			for processNext(ctx) {
			}
		}()
	}
}

func processNext(ctx context.Context) bool {
	history, err := claimNextRender()
	if err != nil {
		applogger.Error("speech worker: claim render failed", "error", err)
		return false
	}
	if history == nil {
		return false
	}
	if err := renderGuarded(ctx, history); err != nil {
		applogger.Error("speech worker: render failed", "history_id", history.ID, "message_id", history.MessageID, "attempt", history.AttemptCount, "error", err)
		if updateErr := markRenderFailed(history.ID, err.Error()); updateErr != nil {
			applogger.Error("speech worker: mark render failed", "history_id", history.ID, "error", updateErr)
		}
	}
	return true
}

// renderGuarded contains a panic raised while rendering one history. The panic is
// logged and returned as an error so the caller records the render as failed,
// instead of the history being stuck in rendering (or the process being torn
// down). Adapter code is third-party-facing, so it is the likeliest panic source.
func renderGuarded(ctx context.Context, history *model.SpeechRenderHistory) (err error) {
	defer func() {
		if r := recover(); r != nil {
			applogger.Error("speech worker recovered from panic while rendering",
				"history_id", history.ID, "message_id", history.MessageID, "panic", r)
			err = fmt.Errorf("panic while rendering: %v", r)
		}
	}()
	return render(ctx, history)
}

func claimNextRender() (*model.SpeechRenderHistory, error) {
	var candidate model.SpeechRenderHistory
	err := database.DB.Where("status = ?", model.SpeechRenderStatusPending).
		Order("id ASC").First(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	nextAttempt := candidate.AttemptCount + 1
	result := database.DB.Model(&model.SpeechRenderHistory{}).
		Where("id = ? AND status = ?", candidate.ID, model.SpeechRenderStatusPending).
		Updates(map[string]interface{}{
			"status":        model.SpeechRenderStatusRendering,
			"attempt_count": nextAttempt,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil
	}
	candidate.Status = model.SpeechRenderStatusRendering
	candidate.AttemptCount = nextAttempt
	return &candidate, nil
}

func render(ctx context.Context, history *model.SpeechRenderHistory) error {
	message, err := dops.Get[model.Message](history.MessageID)
	if err != nil {
		return fmt.Errorf("load message: %w", err)
	}
	voice, err := dops.GetAgentVoiceByPersonID(message.PersonID)
	if err != nil {
		return err
	}
	if voice.TTSRendererID == 0 || voice.SampleAudioSHA256 == "" {
		return fmt.Errorf("agent voice %d for person %d is not fully configured (renderer=%d has_sample=%t)", voice.ID, message.PersonID, voice.TTSRendererID, voice.SampleAudioSHA256 != "")
	}
	renderer, err := dops.GetTTSRenderer(voice.TTSRendererID)
	if err != nil {
		return err
	}
	adapter, err := renderercore.Get(renderer.Provider)
	if err != nil {
		return err
	}
	if err := adapter.Validate(ctx, *renderer); err != nil {
		return err
	}
	sampleAudio, err := loadVoiceSampleAudio(voice.SampleAudioRelativePath, voice.SampleAudioSHA256)
	if err != nil {
		return err
	}
	sample := renderercore.VoiceSample{
		Audio:       sampleAudio,
		Extension:   voice.SampleAudioExtension,
		AudioSHA256: voice.SampleAudioSHA256,
		Transcript:  voice.SampleTranscript,
		Locale:      voice.SampleLocale,
	}
	voiceRef := voice.RendererVoiceRef
	if voiceRef == "" {
		voiceRef, err = adapter.PrepareVoice(ctx, *renderer, sample)
		if err != nil {
			return err
		}
		// A direct-reference adapter intentionally returns an empty ref and
		// receives the canonical package in renderer.Request. Only registration-based
		// adapters populate the renderer-local cache.
		if voiceRef != "" {
			updated := database.DB.Model(&model.AgentVoice{}).
				Where("id = ? AND renderer_voice_ref = ?", voice.ID, "").
				Update("renderer_voice_ref", voiceRef)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 0 {
				stored, loadErr := dops.Get[model.AgentVoice](voice.ID)
				if loadErr != nil {
					return fmt.Errorf("reload agent voice %d after provider reference race: %w", voice.ID, loadErr)
				}
				if stored.RendererVoiceRef == "" {
					return fmt.Errorf("agent voice %d provider reference was not persisted", voice.ID)
				}
				voiceRef = stored.RendererVoiceRef
			}
		}
	}
	result, err := adapter.Synthesize(ctx, renderercore.Request{
		Content:               message.Content,
		ExpressionInstruction: message.ExpressionInstruction,
		Sample:                sample,
		RendererVoiceRef:      voiceRef,
		Renderer:              *renderer,
	})
	if err != nil {
		return err
	}
	if err := renderercore.ValidateResult(result); err != nil {
		return fmt.Errorf("validate adapter result: %w", err)
	}
	requestSnapshot, err := renderercore.ValidateRequestSnapshot(result.AdapterRequestSnapshot)
	if err != nil {
		return fmt.Errorf("validate adapter request snapshot: %w", err)
	}
	relativePath, mimeType, err := persistAudio(history.ID, history.MessageID, result)
	if err != nil {
		return err
	}
	updated := database.DB.Model(&model.SpeechRenderHistory{}).
		Where("id = ? AND status = ?", history.ID, model.SpeechRenderStatusRendering).
		Updates(map[string]interface{}{
			"agent_voice_id":           voice.ID,
			"adapter_request_snapshot": requestSnapshot,
			"status":                   model.SpeechRenderStatusSuccess,
			"relative_path":            relativePath,
			"mime_type":                mimeType,
			"error_message":            "",
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("speech render was no longer active before success")
	}
	return nil
}

func markRenderFailed(historyID int64, message string) error {
	result := database.DB.Model(&model.SpeechRenderHistory{}).
		Where("id = ? AND status = ?", historyID, model.SpeechRenderStatusRendering).
		Updates(map[string]interface{}{
			"status":        model.SpeechRenderStatusFailed,
			"error_message": message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("speech render was no longer active before failure")
	}
	return nil
}

// persistAudio writes one synthesized artifact under DATA_ROOT/speech/messages
// and returns its relative path plus the MIME type to serve it with. The file
// extension encodes the audio format, so no separate format value is returned.
func persistAudio(historyID, messageID int64, result renderercore.Result) (string, string, error) {
	format := strings.ToLower(result.Format)
	if !isSupportedPlaybackFormat(format) {
		return "", "", fmt.Errorf("unsupported upstream audio format %q; audio normalization is required", format)
	}
	mimeType := result.MIMEType
	if mimeType == "" {
		mimeType = mimeTypeForFormat(format)
	}
	// Keep message delivery assets distinct from canonical voice samples.
	relativePath := filepath.Join("messages", fmt.Sprintf("%d", messageID), fmt.Sprintf("%d.%s", historyID, format))
	path, err := resolveSpeechDataPath(relativePath)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Join(config.Get().DataRoot, "speech", ".tmp"), 0755); err != nil {
		return "", "", err
	}
	temporaryPath := filepath.Join(config.Get().DataRoot, "speech", ".tmp", uuid.NewString()+"."+format)
	if err := os.WriteFile(temporaryPath, result.Bytes, 0600); err != nil {
		return "", "", err
	}
	defer os.Remove(temporaryPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", "", err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", "", err
	}
	return relativePath, mimeType, nil
}

func isSupportedPlaybackFormat(format string) bool {
	switch format {
	case "mp3", "wav", "ogg", "opus", "m4a", "aac":
		return true
	default:
		return false
	}
}

func mimeTypeForFormat(format string) string {
	switch format {
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "ogg":
		return "audio/ogg"
	case "opus":
		return "audio/opus"
	case "m4a":
		return "audio/mp4"
	case "aac":
		return "audio/aac"
	default:
		return "application/octet-stream"
	}
}

// isUniqueConstraintError keeps concurrent request handling portable across
// the SQLite and future SQL drivers used by the application.
func isUniqueConstraintError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint") ||
		strings.Contains(strings.ToLower(err.Error()), "duplicate key")
}
