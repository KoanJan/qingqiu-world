package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/workspace"
)

// Handler handles core API HTTP requests.
type Handler struct{}

// NewHandler creates a new Handler instance.
func NewHandler() *Handler {
	return &Handler{}
}

// Root handles the API root endpoint.
func (h *Handler) Root(c *gin.Context) {
	response.SuccessMessage(c, "Qingqiu World API is running", nil)
}

// GetVersion handles retrieving the application version.
func (h *Handler) GetVersion(c *gin.Context) {
	response.Success(c, gin.H{"version": dops.GetVersion()})
}

// GetUserProfile returns the current user's person profile.
// Returns zero-value response if user hasn't been set up yet.
func (h *Handler) GetUserProfile(c *gin.Context) {
	person, err := dops.GetCurrentUserPerson()
	if err != nil {
		response.Success(c, gin.H{})
		return
	}
	response.Success(c, gin.H{
		"id":   person.ID,
		"name": person.Name,
		"bio":  person.Bio,
		"type": person.Type,
	})
}

// CreateOrUpdateUserProfile creates or updates the user's person profile.
// Name is immutable once set (controlled via UNIQUE constraint on name column).
// Bio can be updated at any time.
func (h *Handler) CreateOrUpdateUserProfile(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
		Bio  string `json:"bio"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// check if exists
	existing, _ := dops.GetCurrentUserPerson()

	// update
	if existing != nil {
		// Update bio only (name is immutable)
		if err := dops.UpdateHumanPerson(existing.ID, req.Bio); err != nil {
			response.InternalError(c, err.Error())
			return
		}
		refreshed, err := dops.GetPerson(existing.ID)
		if err != nil {
			applogger.Error("failed to refresh user profile after update", "id", existing.ID, "error", err)
		} else {
			existing = refreshed
		}
		response.Success(c, gin.H{
			"id":   existing.ID,
			"name": existing.Name,
			"bio":  existing.Bio,
			"type": existing.Type,
		})
		return
	}

	// create
	person, err := dops.CreateHumanPerson(req.Name, req.Bio)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			response.BadRequest(c, fmt.Sprintf("User name '%s' already exists", req.Name))
			return
		}
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, gin.H{
		"id":   person.ID,
		"name": person.Name,
		"bio":  person.Bio,
		"type": person.Type,
	})
}

// receivedFileEntry represents a file or directory in a delivery tree.
type receivedFileEntry struct {
	Name      string              `json:"name"`
	Path      string              `json:"path"`
	LocalPath string              `json:"local_path,omitempty"`
	Size      int64               `json:"size"`
	IsDir     bool                `json:"is_dir"`
	Children  []receivedFileEntry `json:"children"`
}

// receivedDeliveryEntry represents one delivery directory with its file tree.
type receivedDeliveryEntry struct {
	Name  string              `json:"name"`
	Files []receivedFileEntry `json:"files"`
}

// GetReceivedDeliveries lists all delivery directories and their file trees
// under the user's received/ directory for a given session.
func (h *Handler) GetReceivedDeliveries(c *gin.Context) {
	sessionID := getPathID(c)

	_, err := dops.GetSession(sessionID)
	if err != nil {
		response.NotFound(c, "Session not found")
		return
	}

	// Get current user's PersonID for received directory
	userPerson, err := dops.GetCurrentUserPerson()
	if err != nil {
		response.BadRequest(c, "No user profile found")
		return
	}

	receivedDir := workspace.GetReceivedDir(userPerson.ID, sessionID)

	var deliveries []receivedDeliveryEntry

	entries, err := os.ReadDir(receivedDir)
	if err != nil {
		// Directory doesn't exist yet — return empty list
		response.Success(c, []receivedDeliveryEntry{})
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		deliveryPath := filepath.Join(receivedDir, entry.Name())
		files := walkDeliveryFiles(deliveryPath)

		deliveries = append(deliveries, receivedDeliveryEntry{
			Name:  entry.Name(),
			Files: files,
		})
	}

	response.Success(c, deliveries)
}

// walkDeliveryFiles builds a directory tree from the delivery directory.
func walkDeliveryFiles(dirPath string) []receivedFileEntry {
	var root []receivedFileEntry

	filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip the root dir itself
		if path == dirPath {
			return nil
		}

		relPath, _ := filepath.Rel(dirPath, path)
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		node := receivedFileEntry{
			Name:  info.Name(),
			Path:  filepath.ToSlash(relPath),
			IsDir: info.IsDir(),
		}
		if info.IsDir() {
			node.Children = []receivedFileEntry{}
		} else {
			node.LocalPath = path
			node.Size = info.Size()
		}

		// Find or create parent directory nodes along the path
		current := &root
		for i := 0; i < len(parts)-1; i++ {
			dirName := parts[i]
			// Find existing dir or create one
			found := false
			for j := range *current {
				if (*current)[j].Name == dirName && (*current)[j].IsDir {
					current = &(*current)[j].Children
					found = true
					break
				}
			}
			if !found {
				dirPath := strings.Join(parts[:i+1], "/")
				newDir := receivedFileEntry{
					Name:     dirName,
					Path:     dirPath,
					IsDir:    true,
					Children: []receivedFileEntry{},
				}
				*current = append(*current, newDir)
				current = &(*current)[len(*current)-1].Children
			}
		}

		// Add the node itself (file or leaf directory)
		if !info.IsDir() || len(parts) > 0 {
			// Only append if not already added as intermediate dir
			alreadyAdded := false
			for _, c := range *current {
				if c.Name == node.Name && c.IsDir == node.IsDir {
					alreadyAdded = true
					break
				}
			}
			if !alreadyAdded {
				*current = append(*current, node)
			}
		}

		return nil
	})

	return root
}

// GetReceivedFile returns the content of a file in the user's received/ directory.
// Query params: delivery=N (required), path=xxx (required)
func (h *Handler) GetReceivedFile(c *gin.Context) {
	sessionID := getPathID(c)
	delivery := c.Query("delivery")
	filePath := c.Query("path")

	if delivery == "" || filePath == "" {
		response.BadRequest(c, "delivery and path query parameters are required")
		return
	}

	var session model.Session
	if err := database.DB.First(&session, sessionID).Error; err != nil {
		response.NotFound(c, "Session not found")
		return
	}

	userPerson, err := dops.GetCurrentUserPerson()
	if err != nil {
		response.BadRequest(c, "No user profile found")
		return
	}

	// Security: validate delivery name contains only safe characters
	for _, ch := range delivery {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			response.BadRequest(c, "invalid delivery name")
			return
		}
	}

	receivedDir := workspace.GetReceivedDir(userPerson.ID, sessionID)
	fullPath := filepath.Join(receivedDir, delivery, filepath.Clean(filePath))

	// Security: ensure the resolved path is within the received/ directory
	if !strings.HasPrefix(fullPath, receivedDir) {
		response.BadRequest(c, "invalid file path")
		return
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			response.NotFound(c, "File not found")
			return
		}
		response.InternalError(c, err.Error())
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(filePath)))
	c.Data(200, "application/octet-stream", data)
}

// CreateMessage handles creating a new message in a session.
func (h *Handler) CreateMessage(c *gin.Context) {
	sessionID := getPathID(c)
	_, err := dops.GetSession(sessionID)
	if err != nil {
		response.NotFound(c, "Session not found")
		return
	}
	var req schema.MessageCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		applogger.Error("CreateMessage: failed to get current user person ID", "error", err)
		response.InternalError(c, "Failed to identify current user")
		return
	}
	entity := model.Message{
		SessionID: sessionID,
		PersonID:  userPersonID,
		Content:   req.Content,
	}
	if err := dops.CreateMessage(&entity); err != nil {
		response.InternalError(c, err.Error())
		return
	}

	// Submit to the event vectorization service for embedding + observation.
	memory.SubmitVectorization(memory.VectorizationTask{
		MessageID: entity.ID,
		SessionID: entity.SessionID,
		Content:   entity.Content,
	})

	response.Success(c, schema.NewMessageResponse(&entity))
}

// ListMessages handles listing messages in a session.
func (h *Handler) ListMessages(c *gin.Context) {
	sessionID := getPathID(c)
	_, err := dops.GetSession(sessionID)
	if err != nil {
		response.NotFound(c, "Session not found")
		return
	}
	messages, err := dops.ListMessagesBySessionID(sessionID)
	if err != nil {
		applogger.Error("failed to list messages", "session_id", sessionID, "error", err)
		response.InternalError(c, "Failed to list messages")
		return
	}
	response.Success(c, schema.NewMessageResponseList(messages))
}

// GetSearchConfig handles retrieving the search configuration.
func (h *Handler) GetSearchConfig(c *gin.Context) {
	config := dops.GetSearchConfig()
	response.Success(c, schema.NewSearchConfigResponse(config))
}

// UpdateSearchConfig handles updating the search configuration.
func (h *Handler) UpdateSearchConfig(c *gin.Context) {
	var req schema.SearchConfigUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	config := dops.UpdateSearchConfig(req.Provider, req.APIKey, req.Description, req.IsActive)
	response.Success(c, schema.NewSearchConfigResponse(config))
}

// GetCurrentPerson returns the current user's person record.
// GET /api/persons/me
func (h *Handler) GetCurrentPerson(c *gin.Context) {
	person, err := dops.GetCurrentUserPerson()
	if err != nil {
		response.NotFound(c, "No user person record found. Please create a user profile first.")
		return
	}
	response.Success(c, gin.H{
		"id":   person.ID,
		"name": person.Name,
		"type": person.Type,
	})
}
