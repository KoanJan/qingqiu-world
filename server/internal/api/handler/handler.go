package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/jinshu"
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

// jinshuFileEntry represents a file or directory in a jinshu file tree.
type jinshuFileEntry struct {
	Name      string            `json:"name"`
	Path      string            `json:"path"`
	LocalPath string            `json:"local_path,omitempty"`
	Size      int64             `json:"size"`
	IsDir     bool              `json:"is_dir"`
	Children  []jinshuFileEntry `json:"children"`
}

// jinshuEntry represents one jinshu record with its file tree and resolved
// sender/recipient names for display.
type jinshuEntry struct {
	ID           int64             `json:"id"`
	FromPersonID int64             `json:"from_person_id"`
	ToPersonID   int64             `json:"to_person_id"`
	FromName     string            `json:"from_name"`
	ToName       string            `json:"to_name"`
	Topic        string            `json:"topic"`
	Description  string            `json:"description"`
	CreatedAt    time.Time         `json:"created_at"`
	Files        []jinshuFileEntry `json:"files"`
}

// GetSentJinshus lists the current user's sent jinshu records.
func (h *Handler) GetSentJinshus(c *gin.Context) {
	h.listJinshus(c, "sent")
}

// GetReceivedJinshus lists the current user's received jinshu records.
func (h *Handler) GetReceivedJinshus(c *gin.Context) {
	h.listJinshus(c, "received")
}

// listJinshus returns jinshu records for the current user in the given
// direction ("sent" or "received"), each with its file tree and resolved names.
func (h *Handler) listJinshus(c *gin.Context, direction string) {
	userPerson, err := dops.GetCurrentUserPerson()
	if err != nil {
		response.BadRequest(c, "No user profile found")
		return
	}

	query := database.DB.Model(&model.Jinshu{})
	baseDir := ""
	switch direction {
	case "sent":
		query = query.Where("from_person_id = ?", userPerson.ID)
		baseDir = jinshu.SentDir(userPerson.ID)
	case "received":
		query = query.Where("to_person_id = ?", userPerson.ID)
		baseDir = jinshu.ReceivedDir(userPerson.ID)
	default:
		applogger.Error("listJinshus: unknown direction", "direction", direction)
		response.InternalError(c, "Invalid direction")
		return
	}

	var records []model.Jinshu
	if err := query.Order("id DESC").Find(&records).Error; err != nil {
		applogger.Error("failed to list jinshus", "direction", direction, "error", err)
		response.InternalError(c, "Failed to list jinshus")
		return
	}

	names := resolveJinshuPersonNames(records)

	entries := make([]jinshuEntry, 0, len(records))
	for _, r := range records {
		dir := filepath.Join(baseDir, strconv.FormatInt(r.ID, 10))
		entries = append(entries, jinshuEntry{
			ID:           r.ID,
			FromPersonID: r.FromPersonID,
			ToPersonID:   r.ToPersonID,
			FromName:     names[r.FromPersonID],
			ToName:       names[r.ToPersonID],
			Topic:        r.Topic,
			Description:  r.Description,
			CreatedAt:    r.CreatedAt,
			Files:        walkJinshuFiles(dir),
		})
	}

	response.Success(c, entries)
}

// resolveJinshuPersonNames resolves names for all sender/recipient persons
// referenced by the given jinshu records.
func resolveJinshuPersonNames(records []model.Jinshu) map[int64]string {
	idSet := make(map[int64]struct{})
	for _, r := range records {
		idSet[r.FromPersonID] = struct{}{}
		idSet[r.ToPersonID] = struct{}{}
	}
	ids := make([]int64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	names, err := dops.GetPersonNames(ids)
	if err != nil {
		applogger.Error("failed to resolve jinshu person names", "error", err)
		return map[int64]string{}
	}
	return names
}

// GetJinshuFile returns the content of a file within a jinshu directory.
// The requester must be either the sender or the recipient of the jinshu.
// Query param: path=xxx (required)
func (h *Handler) GetJinshuFile(c *gin.Context) {
	id := getPathID(c)
	filePath := c.Query("path")
	if filePath == "" {
		response.BadRequest(c, "path query parameter is required")
		return
	}

	userPerson, err := dops.GetCurrentUserPerson()
	if err != nil {
		response.BadRequest(c, "No user profile found")
		return
	}

	var record model.Jinshu
	if err := database.DB.First(&record, id).Error; err != nil {
		response.NotFound(c, "Jinshu not found")
		return
	}

	var baseDir string
	switch {
	case record.FromPersonID == userPerson.ID:
		baseDir = jinshu.SentDir(userPerson.ID)
	case record.ToPersonID == userPerson.ID:
		baseDir = jinshu.ReceivedDir(userPerson.ID)
	default:
		// Neither sender nor recipient — hide the record's existence.
		response.NotFound(c, "Jinshu not found")
		return
	}

	fullPath := filepath.Join(baseDir, strconv.FormatInt(id, 10), filepath.Clean(filePath))

	// Security: ensure the resolved path is within the jinshu base directory.
	if !strings.HasPrefix(fullPath, baseDir) {
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

// walkJinshuFiles builds a directory tree from a jinshu directory.
// The returned slice is always non-nil so it serializes to [] (never null).
func walkJinshuFiles(dirPath string) []jinshuFileEntry {
	root := []jinshuFileEntry{}

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
		node := jinshuFileEntry{
			Name:  info.Name(),
			Path:  filepath.ToSlash(relPath),
			IsDir: info.IsDir(),
		}
		if info.IsDir() {
			node.Children = []jinshuFileEntry{}
		} else {
			node.LocalPath = path
			node.Size = info.Size()
		}

		// Find or create parent directory nodes along the path.
		current := &root
		for i := 0; i < len(parts)-1; i++ {
			dirName := parts[i]
			found := false
			for j := range *current {
				if (*current)[j].Name == dirName && (*current)[j].IsDir {
					current = &(*current)[j].Children
					found = true
					break
				}
			}
			if !found {
				parentRelPath := strings.Join(parts[:i+1], "/")
				newDir := jinshuFileEntry{
					Name:     dirName,
					Path:     parentRelPath,
					IsDir:    true,
					Children: []jinshuFileEntry{},
				}
				*current = append(*current, newDir)
				current = &(*current)[len(*current)-1].Children
			}
		}

		// Add the node itself (file or leaf directory).
		if !info.IsDir() || len(parts) > 0 {
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
