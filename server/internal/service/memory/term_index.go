package memory

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

const maxIndexedRunes = 4000
const maxTermsPerSource = 2048

var termIndexReady atomic.Bool

// searchTerms supports a provisional lexical candidate index, not a relevance
// retrieval system. Han unigrams, bigrams, and trigrams cover short Chinese
// phrases; other words are kept whole and lowercased. This costs one stored
// row per distinct term, plus database indexes and ongoing rebuild work.
// Paraphrases and different word choices cannot match, while terms found in
// separate parts of a source can still produce a candidate. Only the first
// maxIndexedRunes runes and maxTermsPerSource distinct terms are indexed, so
// matching text beyond either limit is invisible to keyword recall. Keep this
// implementation replaceable until its benefit over scoped text scans and
// other retrieval approaches has been measured.
func searchTerms(input string) []string {
	runes := []rune(strings.ToLower(input))
	if len(runes) > maxIndexedRunes {
		runes = runes[:maxIndexedRunes]
	}
	seen := make(map[string]struct{})
	var terms []string
	add := func(s string) {
		if s == "" {
			return
		}
		if len(terms) >= maxTermsPerSource {
			return
		}
		if _, exists := seen[s]; !exists {
			seen[s] = struct{}{}
			terms = append(terms, s)
		}
	}
	var word []rune
	flush := func() {
		if len(word) > 0 {
			add(string(word))
			word = word[:0]
		}
	}
	for i, r := range runes {
		if unicode.Is(unicode.Han, r) {
			flush()
			add(string(r))
			if i+1 < len(runes) && unicode.Is(unicode.Han, runes[i+1]) {
				add(string(runes[i : i+2]))
			}
			if i+2 < len(runes) && unicode.Is(unicode.Han, runes[i+1]) && unicode.Is(unicode.Han, runes[i+2]) {
				add(string(runes[i : i+3]))
			}
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

// sourceContentVersion changes when a referenced domain row changes the
// searchable text, even if the Event row itself remains untouched.
func sourceContentVersion(content string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(content))
	return int64(h.Sum64())
}

// IndexSource refreshes a derived lexical representation after a source write.
// Failure is logged by the caller and can be repaired by RebuildTermIndex.
func IndexSource(kind model.MemorySourceKind, sourceID, ownerID int64, createdAt time.Time, version int64, content string) error {
	if sourceID <= 0 || createdAt.IsZero() {
		return fmt.Errorf("invalid memory index source kind=%d id=%d", kind, sourceID)
	}
	var current model.MemoryTerm
	if err := database.DB.Where("source_kind = ? AND source_id = ?", kind, sourceID).Take(&current).Error; err == nil && current.SourceVersion == version {
		return nil
	}
	tx := database.DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	if err := tx.Where("source_kind = ? AND source_id = ?", kind, sourceID).Delete(&model.MemoryTerm{}).Error; err != nil {
		return err
	}
	terms := searchTerms(content)
	rows := make([]model.MemoryTerm, 0, len(terms))
	for _, term := range terms {
		rows = append(rows, model.MemoryTerm{Term: term, SourceKind: kind, SourceID: sourceID, OwnerPersonID: ownerID, SourceCreatedAt: createdAt, SourceVersion: version})
	}
	if len(rows) > 0 {
		if err := tx.CreateInBatches(&rows, 100).Error; err != nil {
			return err
		}
	}
	return tx.Commit().Error
}

// RebuildTermIndex scans authoritative sources in bounded ID batches. It can
// run repeatedly; unchanged source versions are skipped by IndexSource.
func RebuildTermIndex(ctx context.Context) error {
	termIndexReady.Store(false)
	const batchSize = 100
	for _, kind := range []model.MemorySourceKind{model.MemorySourceEvent, model.MemorySourceAction, model.MemorySourceWork, model.MemorySourceFocusHandoff} {
		lastID := int64(0)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			count, nextID, err := indexBatch(kind, lastID, batchSize)
			if err != nil {
				return err
			}
			if count == 0 {
				break
			}
			lastID = nextID
		}
	}
	// Remove entries whose source has since been deleted.
	for kind, table := range map[model.MemorySourceKind]string{
		model.MemorySourceEvent: "events", model.MemorySourceAction: "actions",
		model.MemorySourceWork: "works", model.MemorySourceFocusHandoff: "focus_handoffs",
	} {
		if err := database.DB.Exec("DELETE FROM memory_terms WHERE source_kind = ? AND NOT EXISTS (SELECT 1 FROM "+table+" WHERE "+table+".id = memory_terms.source_id)", kind).Error; err != nil {
			return err
		}
	}
	termIndexReady.Store(true)
	return nil
}

// RefreshSource indexes one committed row without making its original write
// depend on the derived index. Callers log errors and the sweeper repairs them.
func RefreshSource(kind model.MemorySourceKind, sourceID int64) error {
	count, nextID, err := indexBatch(kind, sourceID-1, 1)
	if err != nil {
		// A failed incremental write makes lexical coverage incomplete until a
		// successful full rebuild verifies every authoritative source again.
		termIndexReady.Store(false)
		return err
	}
	if count != 1 || nextID != sourceID {
		termIndexReady.Store(false)
		return fmt.Errorf("memory source kind=%d id=%d unavailable for indexing", kind, sourceID)
	}
	return nil
}

// indexBatch reads one source kind in ID order and refreshes its derived terms.
// It returns the number of scanned rows and the last ID for continuation;
// individual source failures are logged so a bad row cannot stall the sweep.
func indexBatch(kind model.MemorySourceKind, after int64, limit int) (int, int64, error) {
	switch kind {
	case model.MemorySourceEvent:
		var rows []model.Event
		if err := database.DB.Where("id > ?", after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
			return 0, 0, err
		}
		for _, row := range rows {
			content, occurred, err := eventSourceContent(row)
			if err != nil {
				applogger.Error("index event source failed", "event_id", row.ID, "error", err)
				continue
			}
			if err := IndexSource(kind, row.ID, 0, occurred, sourceContentVersion(content), content); err != nil {
				return 0, 0, err
			}
		}
		if len(rows) == 0 {
			return 0, 0, nil
		}
		return len(rows), rows[len(rows)-1].ID, nil
	case model.MemorySourceAction:
		var rows []model.Action
		if err := database.DB.Where("id > ?", after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
			return 0, 0, err
		}
		for _, row := range rows {
			var decision model.Decision
			if err := database.DB.First(&decision, row.DecisionID).Error; err != nil {
				applogger.Error("index action owner failed", "action_id", row.ID, "error", err)
				continue
			}
			content := row.Background + "\n" + row.Reason + "\n" + row.PlanJSON
			if err := IndexSource(kind, row.ID, decision.PersonID, row.CreatedAt, row.UpdatedAt.UnixNano(), content); err != nil {
				return 0, 0, err
			}
		}
		if len(rows) == 0 {
			return 0, 0, nil
		}
		return len(rows), rows[len(rows)-1].ID, nil
	case model.MemorySourceWork:
		var rows []model.Work
		if err := database.DB.Where("id > ?", after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
			return 0, 0, err
		}
		for _, row := range rows {
			if err := IndexSource(kind, row.ID, row.PersonID, row.CreatedAt, row.UpdatedAt.UnixNano(), row.Description); err != nil {
				return 0, 0, err
			}
		}
		if len(rows) == 0 {
			return 0, 0, nil
		}
		return len(rows), rows[len(rows)-1].ID, nil
	case model.MemorySourceFocusHandoff:
		var rows []model.FocusHandoff
		if err := database.DB.Where("id > ?", after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
			return 0, 0, err
		}
		for _, row := range rows {
			content := strings.Join([]string{row.Orientation, row.Summary, row.ConfirmedFindings, row.ArtifactReferences, row.Unresolved, row.NextStep}, "\n")
			if err := IndexSource(kind, row.ID, row.PersonID, row.CreatedAt, row.CreatedAt.UnixNano(), content); err != nil {
				return 0, 0, err
			}
		}
		if len(rows) == 0 {
			return 0, 0, nil
		}
		return len(rows), rows[len(rows)-1].ID, nil
	}
	return 0, 0, fmt.Errorf("unknown memory source kind %d", kind)
}

// startTermIndexMaintenance repairs missed incremental writes and periodically
// verifies the full derived index against its authoritative source tables.
// Search availability never blocks source persistence.
func startTermIndexMaintenance(ctx context.Context) {
	lastSweep := time.Now().UTC().Add(-time.Second)
	fullSweeps := 0
	for {
		started := time.Now().UTC()
		var err error
		if !termIndexReady.Load() || fullSweeps >= 288 {
			err = RebuildTermIndex(ctx)
			if err == nil {
				fullSweeps = 0
			}
		} else {
			err = refreshChangedSources(ctx, lastSweep)
			if err == nil {
				fullSweeps++
			}
		}
		if err != nil && ctx.Err() == nil {
			applogger.Error("memory term index maintenance failed", "error", err)
		} else if err == nil {
			// The next pass overlaps writes that occurred during this scan.
			lastSweep = started.Add(-time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// refreshChangedSources repairs missed incremental writes without rescanning
// the entire history on every maintenance tick. A full sweep also runs daily.
func refreshChangedSources(ctx context.Context, since time.Time) error {
	for _, spec := range []struct {
		kind         model.MemorySourceKind
		table, clock string
	}{
		{model.MemorySourceEvent, "events", "created_at"},
		{model.MemorySourceAction, "actions", "updated_at"},
		{model.MemorySourceWork, "works", "updated_at"},
		{model.MemorySourceFocusHandoff, "focus_handoffs", "created_at"},
	} {
		var after int64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var ids []int64
			if err := database.DB.Table(spec.table).Where(spec.clock+" >= ? AND id > ?", since.Local(), after).
				Order("id").Limit(100).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			for _, id := range ids {
				if err := RefreshSource(spec.kind, id); err != nil {
					return err
				}
			}
			after = ids[len(ids)-1]
		}
	}
	return nil
}
