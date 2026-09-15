package kb

import (
	"os"
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestDeleteKnowledgeBaseCascadesDerivedData(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("DATA_ROOT", dataRoot)
	config.Init()

	oldDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(dataRoot, "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		database.DB = oldDB
	})

	if err := db.AutoMigrate(
		&model.KnowledgeBase{},
		&model.KBAccess{},
		&model.Document{},
		&model.DocumentRevision{},
		&model.ContentNode{},
		&model.DocumentChunk{},
		&model.DocumentChunkNode{},
		&model.KBUsageTrace{},
		&model.KBEntity{},
		&model.KBRelation{},
		&model.KBRelationEvidence{},
		&model.KBRelationJob{},
	); err != nil {
		t.Fatalf("auto-migrate test schema: %v", err)
	}

	const kbID int64 = 101
	const docID int64 = 201
	const revisionID int64 = 301
	const nodeID int64 = 401
	const chunkID int64 = 501
	const workID int64 = 601
	const relationID int64 = 701
	const subjectID int64 = 801
	const objectID int64 = 802

	if err := db.Create(&model.KnowledgeBase{ID: kbID, Name: "delete me"}).Error; err != nil {
		t.Fatalf("create KB: %v", err)
	}
	if err := db.Create(&model.KBAccess{KBID: kbID, PersonID: 1}).Error; err != nil {
		t.Fatalf("create KB access: %v", err)
	}
	if err := db.Create(&model.Document{ID: docID, KnowledgeBaseID: kbID, Title: "doc", Status: model.DocumentStatusReady, ActiveRevisionID: revisionID}).Error; err != nil {
		t.Fatalf("create document: %v", err)
	}
	if err := db.Create(&model.DocumentRevision{ID: revisionID, DocumentID: docID, ContentHash: "hash", Status: model.DocumentRevisionStatusReady}).Error; err != nil {
		t.Fatalf("create revision: %v", err)
	}
	if err := db.Create(&model.ContentNode{ID: nodeID, DocumentID: docID, RevisionID: revisionID, Text: "evidence text", ContentHash: "node-hash"}).Error; err != nil {
		t.Fatalf("create content node: %v", err)
	}
	if err := db.Create(&model.DocumentChunk{ID: chunkID, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ChunkIndex: 0, Content: "evidence text"}).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	if err := db.Create(&model.DocumentChunkNode{ChunkID: chunkID, NodeID: nodeID}).Error; err != nil {
		t.Fatalf("create chunk-node mapping: %v", err)
	}
	if err := db.Create(&model.KBUsageTrace{
		ID:                  901,
		WorkID:              workID,
		RequestedKBID:       kbID,
		AuthorizedKBIDsJSON: "[101]",
		EvidenceHandlesJSON: `[{"kb_id":101,"chunk_id":501}]`,
	}).Error; err != nil {
		t.Fatalf("create usage trace: %v", err)
	}
	if err := db.Create(&model.KBEntity{ID: subjectID, ScopeHash: "scope", ScopeKBIDsJSON: "[101]", NormalizedLabel: "subject", DisplayLabel: "Subject"}).Error; err != nil {
		t.Fatalf("create subject entity: %v", err)
	}
	if err := db.Create(&model.KBEntity{ID: objectID, ScopeHash: "scope", ScopeKBIDsJSON: "[101]", NormalizedLabel: "object", DisplayLabel: "Object"}).Error; err != nil {
		t.Fatalf("create object entity: %v", err)
	}
	if err := db.Create(&model.KBRelation{
		ID:              relationID,
		ScopeHash:       "scope",
		ScopeKBIDsJSON:  "[101]",
		SubjectEntityID: subjectID,
		Predicate:       "mentions",
		ObjectEntityID:  objectID,
		State:           model.KBRelationStateGrounded,
		IdempotencyKey:  "relation-key",
	}).Error; err != nil {
		t.Fatalf("create relation: %v", err)
	}
	if err := db.Create(&model.KBRelationEvidence{RelationID: relationID, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ContentNodeID: nodeID, ChunkID: chunkID, EvidenceFingerprint: "evidence-key"}).Error; err != nil {
		t.Fatalf("create relation evidence: %v", err)
	}
	if err := db.Create(&model.KBRelationJob{JobType: model.KBRelationJobTypeAnalyzeFocus, State: model.KBRelationJobStatePending, SourceWorkID: workID, IdempotencyKey: "focus-job"}).Error; err != nil {
		t.Fatalf("create focus relation job: %v", err)
	}
	if err := db.Create(&model.KBRelationJob{JobType: model.KBRelationJobTypeRevalidateRelation, State: model.KBRelationJobStatePending, RelationID: relationID, IdempotencyKey: "relation-job"}).Error; err != nil {
		t.Fatalf("create relation revalidation job: %v", err)
	}

	kbDir := filepath.Join(config.Get().GetKBDir(), "101")
	if err := os.MkdirAll(kbDir, 0755); err != nil {
		t.Fatalf("create KB dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "vectors.db"), []byte("vector-store"), 0644); err != nil {
		t.Fatalf("create KB file: %v", err)
	}

	if err := DeleteKnowledgeBase(kbID); err != nil {
		t.Fatalf("delete KB: %v", err)
	}

	assertRowCount(t, &model.KnowledgeBase{}, "id = ?", 0, kbID)
	assertRowCount(t, &model.KBAccess{}, "kb_id = ?", 0, kbID)
	assertRowCount(t, &model.Document{}, "knowledge_base_id = ?", 0, kbID)
	assertRowCount(t, &model.DocumentRevision{}, "document_id = ?", 0, docID)
	assertRowCount(t, &model.ContentNode{}, "document_id = ?", 0, docID)
	assertRowCount(t, &model.DocumentChunk{}, "knowledge_base_id = ?", 0, kbID)
	assertRowCount(t, &model.DocumentChunkNode{}, "chunk_id = ?", 0, chunkID)
	assertRowCount(t, &model.KBUsageTrace{}, "id = ?", 0, int64(901))
	assertRowCount(t, &model.KBRelationEvidence{}, "knowledge_base_id = ?", 0, kbID)
	assertRowCount(t, &model.KBRelation{}, "id = ?", 0, relationID)
	assertRowCount(t, &model.KBEntity{}, "scope_hash = ?", 0, "scope")
	assertRowCount(t, &model.KBRelationJob{}, "source_work_id = ? OR relation_id = ?", 0, workID, relationID)
	if _, err := os.Stat(kbDir); !os.IsNotExist(err) {
		t.Fatalf("KB dir should be removed, stat err=%v", err)
	}
}

func assertRowCount(t *testing.T, modelValue any, query string, want int64, args ...any) {
	t.Helper()
	var got int64
	if err := database.DB.Model(modelValue).Where(query, args...).Count(&got).Error; err != nil {
		t.Fatalf("count rows for %T: %v", modelValue, err)
	}
	if got != want {
		t.Fatalf("unexpected row count for %T: got=%d want=%d", modelValue, got, want)
	}
}
