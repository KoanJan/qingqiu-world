package kb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"qingqiu-world-server/internal/constants"
	applogger "qingqiu-world-server/internal/logger"

	"github.com/ledongthuc/pdf"
)

const (
	// sourceRenderingVersionLegacy identifies the unnormalized rendition used
	// by documents created before source locators were introduced.
	sourceRenderingVersionLegacy = 1
	// sourceRenderingVersionCurrent identifies the canonical rendition used by
	// current document processing and all newly created chunk offsets.
	sourceRenderingVersionCurrent = 2
)

// extractedDocument is the canonical local rendition used by both chunking and
// evidence provenance. Offsets are byte offsets into Text, matching persisted
// DocumentChunk start_offset/end_offset fields.
type extractedDocument struct {
	Text     string
	FileType string
	pages    []extractedPage
}

// extractedPage records the exact range contributed by one PDF page. Plain
// text sources deliberately have no synthetic page numbers.
type extractedPage struct {
	Number int
	Start  int
	End    int
}

// evidenceLocator is stable, complete local-upload provenance. Every field is
// present so consumers never have to infer a missing part of the location.
type evidenceLocator struct {
	SourceKind   int    `json:"source_kind"`
	FileType     string `json:"file_type"`
	ChunkIndex   int    `json:"chunk_index"`
	CharStart    int    `json:"char_start"`
	CharEnd      int    `json:"char_end"`
	LineStart    int    `json:"line_start"`
	LineEnd      int    `json:"line_end"`
	PageStart    int    `json:"page_start"`
	PageEnd      int    `json:"page_end"`
	SelfStart    int    `json:"self_start"`
	SelfEnd      int    `json:"self_end"`
	SubtreeStart int    `json:"subtree_start"`
	SubtreeEnd   int    `json:"subtree_end"`
}

// contentNodeLocator describes a structural node's source extent. Unlike a
// retrieval-unit locator it deliberately has no chunk_index: one leaf can
// produce several chunks, so no single chunk index belongs to the node.
type contentNodeLocator struct {
	SourceKind   int    `json:"source_kind"`
	FileType     string `json:"file_type"`
	CharStart    int    `json:"char_start"`
	CharEnd      int    `json:"char_end"`
	LineStart    int    `json:"line_start"`
	LineEnd      int    `json:"line_end"`
	PageStart    int    `json:"page_start"`
	PageEnd      int    `json:"page_end"`
	SelfStart    int    `json:"self_start"`
	SelfEnd      int    `json:"self_end"`
	SubtreeStart int    `json:"subtree_start"`
	SubtreeEnd   int    `json:"subtree_end"`
}

// Extract reads a file into its canonical text rendition. New processing code
// should use ExtractDocument so chunk nodes retain source provenance.
func Extract(filePath string) (string, error) {
	document, err := ExtractDocument(filePath)
	if err != nil {
		return "", err
	}
	return document.Text, nil
}

// ExtractDocument reads a local upload and preserves page boundaries when the
// source is a PDF. No external source kinds are accepted.
func ExtractDocument(filePath string) (extractedDocument, error) {
	return extractDocumentForRendering(filePath, sourceRenderingVersionCurrent)
}

// extractDocumentForRendering keeps a document's stored chunk offsets tied to
// the exact rendition that produced them. It exists for migration only; new
// processing always uses sourceRenderingVersionCurrent.
func extractDocumentForRendering(filePath string, renderingVersion int) (extractedDocument, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if !constants.IsAllowedFileExtension(ext) {
		return extractedDocument{}, fmt.Errorf("unsupported file type: %s", ext)
	}

	switch ext {
	case ".txt", ".md":
		if renderingVersion == sourceRenderingVersionLegacy {
			return extractLegacyPlainTextDocument(filePath, strings.TrimPrefix(ext, "."))
		}
		return extractPlainTextDocument(filePath, strings.TrimPrefix(ext, "."))
	case ".pdf":
		if renderingVersion == sourceRenderingVersionLegacy {
			return extractLegacyPDFDocument(filePath)
		}
		return extractPDFDocument(filePath)
	default:
		return extractedDocument{}, fmt.Errorf("unsupported file type: %s", ext)
	}
}

// extractLegacyPlainTextDocument preserves the raw bytes that old chunks were
// split from. It is used only while upgrading pre-provenance documents.
func extractLegacyPlainTextDocument(filePath, fileType string) (extractedDocument, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return extractedDocument{}, fmt.Errorf("failed to read file: %w", err)
	}
	return extractedDocument{Text: string(data), FileType: fileType}, nil
}

// extractPlainText reads a plain text or markdown file.
func extractPlainText(filePath string) (string, error) {
	document, err := extractPlainTextDocument(filePath, strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), "."))
	if err != nil {
		return "", err
	}
	return document.Text, nil
}

func extractPlainTextDocument(filePath, fileType string) (extractedDocument, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return extractedDocument{}, fmt.Errorf("failed to read file: %w", err)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return extractedDocument{Text: text, FileType: fileType}, nil
}

// extractPDF extracts text from a PDF file using the ledongthuc/pdf library.
// Handles compressed content streams (FlateDecode) and multi-page documents.
func extractPDF(filePath string) (string, error) {
	document, err := extractPDFDocument(filePath)
	if err != nil {
		return "", err
	}
	return document.Text, nil
}

// extractPDFDocument preserves page ranges while normalizing only safe layout
// artifacts. Page indices are sourced from the local uploaded PDF itself.
func extractPDFDocument(filePath string) (extractedDocument, error) {
	f, r, err := pdf.Open(filePath)
	if err != nil {
		return extractedDocument{}, fmt.Errorf("failed to open PDF: %w", err)
	}
	defer f.Close()

	fonts := make(map[string]*pdf.Font)
	var textBuilder strings.Builder
	pages := make([]extractedPage, 0, r.NumPage())
	rawBytes := 0
	for pageNumber := 1; pageNumber <= r.NumPage(); pageNumber++ {
		page := r.Page(pageNumber)
		for _, name := range page.Fonts() {
			if _, exists := fonts[name]; exists {
				continue
			}
			font := page.Font(name)
			fonts[name] = &font
		}
		rawPageText, err := page.GetPlainText(fonts)
		if err != nil {
			return extractedDocument{}, fmt.Errorf("extract PDF page %d: %w", pageNumber, err)
		}
		rawBytes += len(rawPageText)
		pageText := normalizeExtractedText(rawPageText)
		if textBuilder.Len() > 0 && pageText != "" {
			textBuilder.WriteString("\n\n")
		}
		start := textBuilder.Len()
		textBuilder.WriteString(pageText)
		pages = append(pages, extractedPage{Number: pageNumber, Start: start, End: textBuilder.Len()})
	}

	text := textBuilder.String()
	if strings.TrimSpace(text) == "" {
		return extractedDocument{}, fmt.Errorf("no text content extracted from PDF")
	}
	applogger.Debug("PDF text extracted and normalized", "path", filePath, "raw_bytes", rawBytes, "normalized_bytes", len(text), "page_count", len(pages), "normalized_lines", strings.Count(text, "\n")+1)
	if hasSuspiciousPDFWordRuns(text) {
		applogger.Warn("PDF extraction contains suspiciously long word runs; source may lack recoverable glyph spacing", "path", filePath)
	}

	return extractedDocument{Text: text, FileType: "pdf", pages: pages}, nil
}

// extractLegacyPDFDocument recreates the exact page concatenation used by the
// former Extract implementation. The PDF library's Reader.GetPlainText uses
// this same traversal, making it safe for historical chunk offsets.
func extractLegacyPDFDocument(filePath string) (extractedDocument, error) {
	f, r, err := pdf.Open(filePath)
	if err != nil {
		return extractedDocument{}, fmt.Errorf("failed to open PDF: %w", err)
	}
	defer f.Close()

	fonts := make(map[string]*pdf.Font)
	var textBuilder strings.Builder
	pages := make([]extractedPage, 0, r.NumPage())
	for pageNumber := 1; pageNumber <= r.NumPage(); pageNumber++ {
		page := r.Page(pageNumber)
		for _, name := range page.Fonts() {
			if _, exists := fonts[name]; exists {
				continue
			}
			font := page.Font(name)
			fonts[name] = &font
		}
		pageText, err := page.GetPlainText(fonts)
		if err != nil {
			return extractedDocument{}, fmt.Errorf("extract legacy PDF page %d: %w", pageNumber, err)
		}
		start := textBuilder.Len()
		textBuilder.WriteString(pageText)
		pages = append(pages, extractedPage{Number: pageNumber, Start: start, End: textBuilder.Len()})
	}
	text := textBuilder.String()
	if strings.TrimSpace(text) == "" {
		return extractedDocument{}, fmt.Errorf("no text content extracted from PDF")
	}
	return extractedDocument{Text: text, FileType: "pdf", pages: pages}, nil
}

// locatorJSON returns complete source metadata for a chunk or document node.
// Invalid persisted offsets are clamped and logged instead of silently creating
// a locator outside the canonical rendition.
func (d extractedDocument) locatorJSON(chunkIndex, start, end int) string {
	if start < 0 || end < start || end > len(d.Text) {
		applogger.Warn("KB source locator received invalid range", "file_type", d.FileType, "chunk_index", chunkIndex, "start_offset", start, "end_offset", end, "text_bytes", len(d.Text))
		if start < 0 {
			start = 0
		}
		if start > len(d.Text) {
			start = len(d.Text)
		}
		if end < start {
			end = start
		}
		if end > len(d.Text) {
			end = len(d.Text)
		}
	}
	pageStart, pageEnd := d.pageRange(start, end)
	locator := evidenceLocator{
		SourceKind:   0,
		FileType:     d.FileType,
		ChunkIndex:   chunkIndex,
		CharStart:    start,
		CharEnd:      end,
		LineStart:    lineAtOffset(d.Text, start),
		LineEnd:      lineAtOffset(d.Text, end),
		PageStart:    pageStart,
		PageEnd:      pageEnd,
		SelfStart:    start,
		SelfEnd:      end,
		SubtreeStart: start,
		SubtreeEnd:   end,
	}
	encoded, err := json.Marshal(locator)
	if err != nil {
		applogger.Error("KB source locator serialization failed", "file_type", d.FileType, "chunk_index", chunkIndex, "error", err)
		return "{}"
	}
	return string(encoded)
}

// nodeLocatorJSON records both a node's own text and its full descendant
// extent. Chunk locators continue to use locatorJSON because one leaf may map
// to several linear chunks.
func (d extractedDocument) nodeLocatorJSON(node *contentTreeNode) string {
	if node.SelfRange.Start < 0 || node.SelfRange.End < node.SelfRange.Start || node.SelfRange.End > len(d.Text) || node.SubtreeRange.Start < 0 || node.SubtreeRange.End < node.SubtreeRange.Start || node.SubtreeRange.End > len(d.Text) {
		applogger.Error("KB content node locator received invalid range", "file_type", d.FileType, "self_start", node.SelfRange.Start, "self_end", node.SelfRange.End, "subtree_start", node.SubtreeRange.Start, "subtree_end", node.SubtreeRange.End, "text_bytes", len(d.Text))
		return "{}"
	}
	pageStart, pageEnd := d.pageRange(node.SelfRange.Start, node.SelfRange.End)
	locator := contentNodeLocator{
		SourceKind:   0,
		FileType:     d.FileType,
		CharStart:    node.SelfRange.Start,
		CharEnd:      node.SelfRange.End,
		LineStart:    lineAtOffset(d.Text, node.SelfRange.Start),
		LineEnd:      lineAtOffset(d.Text, node.SelfRange.End),
		PageStart:    pageStart,
		PageEnd:      pageEnd,
		SelfStart:    node.SelfRange.Start,
		SelfEnd:      node.SelfRange.End,
		SubtreeStart: node.SubtreeRange.Start,
		SubtreeEnd:   node.SubtreeRange.End,
	}
	encoded, err := json.Marshal(locator)
	if err != nil {
		applogger.Error("KB node locator serialization failed", "file_type", d.FileType, "error", err)
		return "{}"
	}
	return string(encoded)
}

func (d extractedDocument) pageRange(start, end int) (pageStart, pageEnd int) {
	for _, page := range d.pages {
		if end <= page.Start || start >= page.End {
			continue
		}
		if pageStart == 0 {
			pageStart = page.Number
		}
		pageEnd = page.Number
	}
	return pageStart, pageEnd
}

func lineAtOffset(text string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	return strings.Count(text[:offset], "\n") + 1
}

// localNodeMetadataJSON records the implemented source capability without
// introducing nullable or implicit metadata fields.
func localNodeMetadataJSON(fileType, role string) string {
	return localNodeMetadataJSONForRendering(fileType, role, sourceRenderingVersionCurrent)
}

// localNodeMetadataJSONForRendering persists the rendition version alongside
// source metadata, so migrations never reinterpret old chunk offsets.
func localNodeMetadataJSONForRendering(fileType, role string, renderingVersion int) string {
	payload := struct {
		SourceKind       int    `json:"source_kind"`
		FileType         string `json:"file_type"`
		Role             string `json:"role"`
		RenderingVersion int    `json:"source_rendering_version"`
	}{SourceKind: 0, FileType: fileType, Role: role, RenderingVersion: renderingVersion}
	encoded, err := json.Marshal(payload)
	if err != nil {
		applogger.Error("KB content-node metadata serialization failed", "file_type", fileType, "role", role, "rendering_version", renderingVersion, "error", err)
		return `{"source_kind":0,"file_type":"","role":"","source_rendering_version":0}`
	}
	return string(encoded)
}

// sourceRenderingVersionFromMetadata reports whether a node carries an
// explicit rendition version. Versionless metadata is a legacy migration case.
func sourceRenderingVersionFromMetadata(metadata string) (int, bool) {
	var payload struct {
		RenderingVersion int `json:"source_rendering_version"`
	}
	if err := json.Unmarshal([]byte(metadata), &payload); err != nil {
		applogger.Warn("KB content node has invalid metadata JSON", "metadata", metadata, "error", err)
		return 0, false
	}
	if payload.RenderingVersion == sourceRenderingVersionLegacy || payload.RenderingVersion == sourceRenderingVersionCurrent {
		return payload.RenderingVersion, true
	}
	if payload.RenderingVersion != 0 {
		applogger.Warn("KB content node has unsupported source rendering version", "rendering_version", payload.RenderingVersion)
	}
	return 0, false
}

// hasSuspiciousPDFWordRuns detects a likely glyph-spacing loss without trying
// to guess missing word boundaries. A warning makes degraded extraction
// observable while preserving source fidelity.
func hasSuspiciousPDFWordRuns(text string) bool {
	letters := 0
	asciiLetters := 0
	whitespace := 0
	totalRunes := 0
	for _, r := range text {
		totalRunes++
		if unicode.IsSpace(r) {
			whitespace++
		}
		if unicode.IsLetter(r) {
			letters++
			if r <= unicode.MaxASCII {
				asciiLetters++
			}
			if letters >= 80 {
				return true
			}
			continue
		}
		letters = 0
	}
	// English prose normally contains regular whitespace. A long ASCII-heavy
	// extraction with almost none usually means the PDF omitted glyph spacing.
	return totalRunes >= 200 && asciiLetters >= 160 && whitespace*100 < totalRunes*2
}

// normalizeExtractedText repairs safe layout artifacts introduced by PDF text
// extraction without inventing words or changing document meaning.
func normalizeExtractedText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\u00ad", "")

	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(result) > 0 && result[len(result)-1] != "" {
				result = append(result, "")
			}
			continue
		}
		if len(result) > 0 && result[len(result)-1] != "" && joinsHyphenatedLine(result[len(result)-1], line) {
			result[len(result)-1] = strings.TrimSuffix(result[len(result)-1], "-") + line
			continue
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

// joinsHyphenatedLine identifies only lexical word wraps such as "process-\ning".
func joinsHyphenatedLine(previous, next string) bool {
	previousRunes := []rune(previous)
	nextRunes := []rune(next)
	if len(previousRunes) < 2 || len(nextRunes) == 0 || previousRunes[len(previousRunes)-1] != '-' {
		return false
	}
	return unicode.IsLetter(previousRunes[len(previousRunes)-2]) && unicode.IsLower(nextRunes[0])
}
