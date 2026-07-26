package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/api/response"

	"github.com/gin-gonic/gin"
)

// maxAvatarFileSize is the maximum allowed avatar file size (20MB).
const maxAvatarFileSize = 20 * 1024 * 1024 // 20MB

// allowedAvatarExtensions defines the allowed image file extensions for avatars.
var allowedAvatarExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// UploadAvatar handles avatar image upload.
// Validates file type and size, saves to the avatars directory, returns the filename.
func (h *Handler) UploadAvatar(c *gin.Context) {
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
	if !allowedAvatarExtensions[ext] {
		response.BadRequest(c, "Invalid file type. Allowed: .jpg, .jpeg, .png, .webp")
		return
	}

	if file.Size > maxAvatarFileSize {
		response.BadRequest(c, fmt.Sprintf("File too large. Max size: %dMB", maxAvatarFileSize/(1024*1024)))
		return
	}

	avatarsDir := config.Get().GetAvatarsDir()
	if err := os.MkdirAll(avatarsDir, 0755); err != nil {
		response.InternalError(c, "Failed to create avatars directory")
		return
	}

	filename := fmt.Sprintf("%d%s", time.Now().UnixMilli(), ext)
	savePath := filepath.Join(avatarsDir, filename)

	if err := c.SaveUploadedFile(file, savePath); err != nil {
		response.InternalError(c, "Failed to save file")
		return
	}

	response.Success(c, gin.H{"filename": filename})
}
