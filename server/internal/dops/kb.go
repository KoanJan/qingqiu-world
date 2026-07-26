package dops

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// CountDocumentsInKB counts the documents in the KB
func CountDocumentsInKB(kbID int64) (int64, error) {
	var count int64
	if err := database.DB.Model(&model.Document{}).Where("knowledge_base_id = ?", kbID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// ListDocumentsInKB list documents in the KB
func ListDocumentsInKB(kbID int64) ([]model.Document, error) {
	var documents []model.Document
	if err := database.DB.Where("knowledge_base_id = ?", kbID).Order("created_at DESC").Find(&documents).Error; err != nil {
		return nil, err
	}
	return documents, nil
}

// MarkDocumentChunksDeleted marks all chunks of the document as deleted
func MarkDocumentChunksDeleted(docID int64) error {
	return database.DB.Model(&model.DocumentChunk{}).Where("document_id = ? AND deleted = 0", docID).Update("deleted", 1).Error
}

// AddDeletedDocumentChunksCountOfKB adds the count of new-deleted chunks of the kb on old count
func AddDeletedDocumentChunksCountOfKB(kbID, newDeletedChunksCount int64) error {
	return database.DB.Model(&model.KnowledgeBase{}).Where("id = ?", kbID).
		Update("deleted_count", gorm.Expr("deleted_count + ?", newDeletedChunksCount)).Error
}
