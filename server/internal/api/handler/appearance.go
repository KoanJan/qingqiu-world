package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// maxBackgroundFileSize is the maximum allowed background image file size (10MB).
const maxBackgroundFileSize = 10 * 1024 * 1024

// allowedBackgroundExtensions defines the allowed image file extensions for backgrounds.
var allowedBackgroundExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// AppearanceSettings is the JSON schema stored in data/settings/config.json.
type AppearanceSettings struct {
	Language      string   `json:"language"`
	BgMode        string   `json:"bgMode"`
	BgColor       string   `json:"bgColor"`
	BgImage       string   `json:"bgImage"`
	BgImageSource string   `json:"bgImageSource"` // "preset" | "upload"
	CustomColors  []string `json:"customColors"`
	GlassOpacity  float64  `json:"glassOpacity"`
	GlassBlur     int      `json:"glassBlur"`
}

// defaultAppearanceSettings returns sensible defaults.
func defaultAppearanceSettings() AppearanceSettings {
	return AppearanceSettings{
		Language:      "zh",
		BgMode:        "color",
		BgColor:       "#ffffff",
		BgImage:       "",
		BgImageSource: "",
		CustomColors:  []string{},
		GlassOpacity:  0.65,
		GlassBlur:     10,
	}
}

// settingsFilePath returns the path to data/settings/config.json.
func settingsFilePath() string {
	return filepath.Join(config.Get().GetSettingsDir(), "config.json")
}

// readAppearanceSettings reads and parses config.json. Returns defaults if the
// file does not exist or is corrupted.
func readAppearanceSettings() AppearanceSettings {
	defaults := defaultAppearanceSettings()

	data, err := os.ReadFile(settingsFilePath())
	if err != nil {
		return defaults
	}

	var s AppearanceSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return defaults
	}

	// Validate mode
	if s.BgMode != "color" && s.BgMode != "image" {
		s.BgMode = defaults.BgMode
	}
	if s.Language != "zh" && s.Language != "en" {
		s.Language = defaults.Language
	}
	if s.BgColor == "" {
		s.BgColor = defaults.BgColor
	}
	if s.GlassOpacity < 0 || s.GlassOpacity > 1 {
		s.GlassOpacity = defaults.GlassOpacity
	}
	if s.GlassBlur < 0 || s.GlassBlur > 20 {
		s.GlassBlur = defaults.GlassBlur
	}
	if s.CustomColors == nil {
		s.CustomColors = defaults.CustomColors
	}
	if s.BgImageSource != "preset" && s.BgImageSource != "upload" {
		s.BgImageSource = defaults.BgImageSource
	}

	return s
}

// writeAppearanceSettings atomically writes settings to config.json.
func writeAppearanceSettings(s AppearanceSettings) error {
	dir := config.Get().GetSettingsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	// Write to a temp file first, then rename for atomicity.
	tmpPath := settingsFilePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}
	return os.Rename(tmpPath, settingsFilePath())
}

// GetSettings returns the current appearance settings from config.json.
func (h *Handler) GetSettings(c *gin.Context) {
	s := readAppearanceSettings()
	response.Success(c, s)
}

// UpdateSettings replaces the appearance settings in config.json.
// Accepts a full or partial AppearanceSettings JSON body and merges
// with existing defaults for missing fields.
func (h *Handler) UpdateSettings(c *gin.Context) {
	current := readAppearanceSettings()

	var body map[string]interface{}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "Invalid JSON body")
		return
	}

	if v, ok := body["bgMode"]; ok {
		if s, ok := v.(string); ok {
			current.BgMode = s
		}
	}
	if v, ok := body["language"]; ok {
		if s, ok := v.(string); ok {
			current.Language = s
		}
	}
	if v, ok := body["bgColor"]; ok {
		if s, ok := v.(string); ok {
			current.BgColor = s
		}
	}
	if v, ok := body["bgImage"]; ok {
		if s, ok := v.(string); ok {
			current.BgImage = s
		} else if v == nil {
			current.BgImage = ""
		}
	}
	if v, ok := body["bgImageSource"]; ok {
		if s, ok := v.(string); ok {
			current.BgImageSource = s
		} else if v == nil {
			current.BgImageSource = ""
		}
	}
	if v, ok := body["customColors"]; ok {
		if arr, ok := v.([]interface{}); ok {
			colors := make([]string, 0, len(arr))
			for _, item := range arr {
				if s, ok := item.(string); ok {
					colors = append(colors, s)
				}
			}
			current.CustomColors = colors
		} else if v == nil {
			current.CustomColors = []string{}
		}
	}
	if v, ok := body["glassOpacity"]; ok {
		if f, ok := v.(float64); ok {
			current.GlassOpacity = f
		}
	}
	if v, ok := body["glassBlur"]; ok {
		if f, ok := v.(float64); ok {
			current.GlassBlur = int(f)
		}
	}

	if err := writeAppearanceSettings(current); err != nil {
		response.InternalError(c, "Failed to save settings")
		return
	}

	response.Success(c, current)
}

// UploadBackground handles background image upload.
// Validates file type and size, saves to the backgrounds directory, returns the filename.
func (h *Handler) UploadBackground(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		response.BadRequest(c, "No file uploaded")
		return
	}

	if file.Filename == "" {
		response.BadRequest(c, "No filename provided")
		return
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	if !allowedBackgroundExtensions[ext] {
		response.BadRequest(c, "Invalid file type. Allowed: .jpg, .jpeg, .png, .webp")
		return
	}

	if file.Size > maxBackgroundFileSize {
		response.BadRequest(c, fmt.Sprintf("File too large. Max size: %dMB", maxBackgroundFileSize/(1024*1024)))
		return
	}

	dir := config.Get().GetBackgroundsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		response.InternalError(c, "Failed to create backgrounds directory")
		return
	}

	filename := fmt.Sprintf("bg-%s%s", uuid.New().String(), ext)
	savePath := filepath.Join(dir, filename)

	if err := c.SaveUploadedFile(file, savePath); err != nil {
		response.InternalError(c, "Failed to save file")
		return
	}

	response.Success(c, gin.H{"filename": filename})
}

// DeleteBackground handles deleting a background image by filename.
func (h *Handler) DeleteBackground(c *gin.Context) {
	filename := c.Param("filename")
	if filename == "" {
		response.BadRequest(c, "Filename is required")
		return
	}

	// Prevent path traversal.
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		response.BadRequest(c, "Invalid filename")
		return
	}

	filePath := filepath.Join(config.Get().GetBackgroundsDir(), filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		response.NotFound(c, "File not found")
		return
	}

	if err := os.Remove(filePath); err != nil {
		response.InternalError(c, "Failed to delete file")
		return
	}

	response.SuccessMessage(c, "Background deleted", nil)
}

// backgroundFileInfo carries lightweight metadata for a single background file.
type backgroundFileInfo struct {
	Filename string `json:"filename"`
}

// ListBackgrounds returns all user-uploaded background images sorted by
// modification time (newest first).
func (h *Handler) ListBackgrounds(c *gin.Context) {
	dir := config.Get().GetBackgroundsDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			response.Success(c, []backgroundFileInfo{})
			return
		}
		response.InternalError(c, "Failed to list backgrounds")
		return
	}

	var files []backgroundFileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !allowedBackgroundExtensions[ext] {
			continue
		}
		files = append(files, backgroundFileInfo{Filename: entry.Name()})
	}

	// Sort by modification time, newest first.
	sort.Slice(files, func(i, j int) bool {
		infoI, errI := os.Stat(filepath.Join(dir, files[i].Filename))
		infoJ, errJ := os.Stat(filepath.Join(dir, files[j].Filename))
		if errI != nil || errJ != nil {
			return false
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})

	response.Success(c, files)
}

// ServeBackground serves a user-uploaded background image file.
// GET /api/appearance/backgrounds/:filename
func (h *Handler) ServeBackground(c *gin.Context) {
	filename := c.Param("filename")
	if strings.Contains(filename, "..") {
		c.Status(http.StatusForbidden)
		return
	}
	dir := config.Get().GetBackgroundsDir()
	filePath := filepath.Join(dir, filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(filePath)
}
