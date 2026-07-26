package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/constants"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/kb"

	"qingqiu-world-server/internal/api/response"

	"github.com/gin-gonic/gin"
)

// CreateKnowledgeBase handles creating a new knowledge base.
func (h *Handler) CreateKnowledgeBase(c *gin.Context) {
	var req schema.KnowledgeBaseCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	entity := model.KnowledgeBase{
		Name:        req.Name,
		Description: req.Description,
	}

	if err := kb.CreateKnowledgeBase(&entity); err != nil {
		response.InternalError(c, err.Error())
		return
	}

	response.Success(c, entity)
}

// ListKnowledgeBases handles listing all knowledge bases.
func (h *Handler) ListKnowledgeBases(c *gin.Context) {
	skip, limit := getPagination(c)
	entities, err := dops.GetMulti[model.KnowledgeBase](skip, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}

	type kbWithStats struct {
		model.KnowledgeBase
		DocumentCount int64 `json:"document_count"`
	}

	results := make([]kbWithStats, 0)
	for _, entity := range entities {
		count, err := dops.CountDocumentsInKB(entity.ID)
		if err != nil {
			applogger.Error("failed to count documents for KB list", "kb_id", entity.ID, "error", err)
		}
		results = append(results, kbWithStats{
			KnowledgeBase: entity,
			DocumentCount: count,
		})
	}

	response.Success(c, results)
}

// GetKnowledgeBase handles retrieving a single knowledge base by ID.
func (h *Handler) GetKnowledgeBase(c *gin.Context) {
	entity, err := dops.Get[model.KnowledgeBase](getPathID(c))
	if err != nil {
		response.NotFound(c, err.Error())
		return
	}
	response.Success(c, entity)
}

// UpdateKnowledgeBase handles updating an existing knowledge base.
func (h *Handler) UpdateKnowledgeBase(c *gin.Context) {
	var req schema.KnowledgeBaseUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	id := getPathID(c)
	entity, err := dops.Get[model.KnowledgeBase](id)
	if err != nil {
		response.NotFound(c, err.Error())
		return
	}

	updates := req.BuildUpdates()
	if len(updates) > 0 {
		if err := dops.Update(entity, updates); err != nil {
			response.InternalError(c, err.Error())
			return
		}
	}

	response.Success(c, entity)
}

// DeleteKnowledgeBase handles deleting a knowledge base and its resources.
func (h *Handler) DeleteKnowledgeBase(c *gin.Context) {
	id := getPathID(c)
	if err := kb.DeleteKnowledgeBase(id); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, nil)
}

// ListDocuments handles listing documents in a knowledge base.
func (h *Handler) ListDocuments(c *gin.Context) {
	kbID := getPathID(c)
	documents, err := dops.ListDocumentsInKB(kbID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, documents)
}

// UploadDocument handles uploading a document to a knowledge base.
func (h *Handler) UploadDocument(c *gin.Context) {
	kbID := getPathID(c)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.BadRequest(c, "No file provided")
		return
	}
	defer file.Close()

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !constants.IsAllowedFileExtension(ext) {
		response.BadRequest(c, fmt.Sprintf("Unsupported file type: %s. Allowed types: .txt, .md, .pdf", ext))
		return
	}

	kbDir := config.Get().GetKBDir()
	docDir := filepath.Join(kbDir, fmt.Sprintf("%d", kbID), "files")
	if err := os.MkdirAll(docDir, 0755); err != nil {
		response.InternalError(c, err.Error())
		return
	}

	filePath := filepath.Join(docDir, header.Filename)
	dst, err := os.Create(filePath)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	defer dst.Close()

	if _, err := dst.ReadFrom(file); err != nil {
		response.InternalError(c, err.Error())
		return
	}

	doc := model.Document{
		KnowledgeBaseID: kbID,
		Title:           header.Filename,
		FilePath:        filePath,
		Status:          model.DocumentStatusPending,
	}
	if err := dops.Create(&doc); err != nil {
		response.InternalError(c, err.Error())
		return
	}

	kb.SubmitDocument(doc.ID)

	response.Success(c, doc)
}

// GetDocument handles retrieving a single document by ID.
func (h *Handler) GetDocument(c *gin.Context) {
	docID := getPathIDByParam(c, "doc_id")
	entity, err := dops.Get[model.Document](docID)
	if err != nil {
		response.NotFound(c, err.Error())
		return
	}
	response.Success(c, entity)
}

// DeleteDocument handles deleting a document and its chunks.
func (h *Handler) DeleteDocument(c *gin.Context) {
	docID := getPathIDByParam(c, "doc_id")
	doc, err := dops.Get[model.Document](docID)
	if err != nil {
		response.NotFound(c, err.Error())
		return
	}

	if doc.FilePath != "" {
		os.Remove(doc.FilePath)
	}

	var chunkCount int64
	if err := database.DB.Model(&model.DocumentChunk{}).Where("document_id = ? AND deleted = 0", docID).Count(&chunkCount).Error; err != nil {
		applogger.Error("failed to count chunks for document soft-delete", "doc_id", docID, "error", err)
		response.InternalError(c, "Failed to delete document")
		return
	}
	if chunkCount > 0 {
		if err := dops.MarkDocumentChunksDeleted(docID); err != nil {
			applogger.Error("failed to soft-delete document chunks", "doc_id", docID, "error", err)
		}
		if err := dops.AddDeletedDocumentChunksCountOfKB(doc.KnowledgeBaseID, chunkCount); err != nil {
			applogger.Error("failed to update KB deleted_count after document delete", "kb_id", doc.KnowledgeBaseID, "error", err)
		}
	}

	if err := dops.Delete[model.Document](doc.ID); err != nil {
		applogger.Error("failed to delete document", "doc_id", docID, "error", err)
		response.InternalError(c, "Failed to delete document")
		return
	}

	response.Success(c, nil)
}

// SearchKB handles searching within a knowledge base.
func (h *Handler) SearchKB(c *gin.Context) {
	kbID := getPathID(c)
	var req schema.SearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	results, err := kb.SearchKB(c.Request.Context(), kbID, req.Query, req.TopK)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, results)
}

// SearchMultiKB handles searching across multiple knowledge bases.
func (h *Handler) SearchMultiKB(c *gin.Context) {
	var req schema.MultiKBSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	results, err := kb.SearchMultiKB(c.Request.Context(), req.KBIDs, req.Query, req.TopK)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, results)
}

// isImageFile checks if the file extension indicates an image.
func isImageFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	imageExts := []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}
	for _, e := range imageExts {
		if ext == e {
			return true
		}
	}
	return false
}
