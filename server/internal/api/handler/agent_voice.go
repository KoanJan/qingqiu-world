package handler

import (
	"errors"
	"net/http"
	"strconv"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/speech"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UpsertAgentVoice applies the declarative voice binding for one agent. The
// scalar fields describe the complete desired configuration, while an omitted
// sample_audio preserves the currently stored audio bytes.
func (h *Handler) UpsertAgentVoice(c *gin.Context) {
	personID := getPathID(c)
	rendererID, err := strconv.ParseInt(c.PostForm("tts_renderer_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "tts_renderer_id must be an integer")
		return
	}
	if rendererID < 0 {
		response.BadRequest(c, "tts_renderer_id must not be negative")
		return
	}
	// A missing sample_audio means the caller does not want to replace the sample.
	var sampleAudio *speech.VoiceSampleUpload
	fileHeader, fileErr := c.FormFile("sample_audio")
	if fileErr == nil {
		file, err := fileHeader.Open()
		if err != nil {
			response.InternalError(c, err.Error())
			return
		}
		// The reader must stay open until SaveAgentVoice consumes it.
		defer file.Close()
		sampleAudio = &speech.VoiceSampleUpload{
			Filename: fileHeader.Filename,
			Audio:    file,
		}
	} else if !errors.Is(fileErr, http.ErrMissingFile) {
		applogger.Error("read AgentVoice sample upload", "person_id", personID, "error", fileErr)
		response.BadRequest(c, "sample_audio could not be read")
		return
	}
	requiresTerms, err := agentVoiceBindingRequiresTerms(personID, rendererID)
	if err != nil {
		applogger.Error("load AgentVoice before save", "person_id", personID, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	if requiresTerms && c.PostForm("terms_confirmed") != "true" {
		response.BadRequest(c, "terms_confirmed must be true before binding this renderer")
		return
	}
	voice, err := speech.SaveAgentVoice(c.Request.Context(), personID, speech.VoiceBindingUpdate{
		RendererID:       rendererID,
		SampleTranscript: c.PostForm("sample_transcript"),
		SampleLocale:     c.PostForm("sample_locale"),
		SampleAudio:      sampleAudio,
	})
	if err != nil {
		// A rejected save is a client-visible decision (invalid renderer, bad
		// sample, non-AI person); record it instead of returning a bare message.
		applogger.Warn("save Agent voice rejected",
			"person_id", personID,
			"tts_renderer_id", rendererID,
			"has_sample_audio", sampleAudio != nil,
			"error", err)
		response.BadRequest(c, err.Error())
		return
	}
	respondAgentVoice(c, voice)
}

// agentVoiceBindingRequiresTerms requires a confirmation only for a new
// renderer binding: a first binding or an explicit switch to another renderer.
// Saves that leave the binding alone, and explicit unbinding (renderer 0), ask
// for no acknowledgement.
func agentVoiceBindingRequiresTerms(personID, rendererID int64) (bool, error) {
	if rendererID == 0 {
		return false, nil
	}
	voice, err := dops.GetAgentVoiceByPersonID(personID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		return false, err
	}
	return voice.TTSRendererID != rendererID, nil
}

// respondAgentVoice returns one complete immutable voice version without
// exposing its internal sample storage path or provider-local cache.
func respondAgentVoice(c *gin.Context, voice *model.AgentVoice) {
	response.Success(c, schema.NewAgentVoiceResponse(voice))
}

// GetAgentVoice returns the currently configured voice for one agent.
func (h *Handler) GetAgentVoice(c *gin.Context) {
	personID := getPathID(c)
	voice, err := dops.GetAgentVoiceByPersonID(personID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			handleNotFound(c, "Agent voice", personID)
			return
		}
		applogger.Error("load AgentVoice failed", "person_id", personID, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	respondAgentVoice(c, voice)
}
