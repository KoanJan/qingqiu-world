package kb

import (
	"strings"

	applogger "qingqiu-world-server/internal/logger"

	"github.com/pkoukk/tiktoken-go"
)

// textSplitter builds deterministic retrieval units from structural boundaries
// and profile-specific token budgets. Token count constrains a unit; it does
// not override headings or code-fence boundaries.
// Uses tiktoken for token counting (cl100k_base encoding, compatible with
// OpenAI models). Token counts for non-OpenAI models may have minor deviations.
type textSplitter struct {
	chunkSize    int
	chunkOverlap int
	minchunkSize int
	tp           *tiktoken.Tiktoken
	initErr      error
}

// newTextSplitter creates a textSplitter with the given chunk size, overlap, and minchunkSize.
// chunks smaller than minchunkSize tokens are merged into the previous chunk.
func newTextSplitter(chunkSize, chunkOverlap, minchunkSize int) *textSplitter {
	tp, err := tiktoken.EncodingForModel("text-embedding-3-small")
	if err != nil {
		applogger.Error("failed to get tiktoken encoding for model, falling back to cl100k_base", "error", err)
		tp, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			applogger.Error("failed to initialize fallback tiktoken encoding", "error", err)
		}
	}
	return &textSplitter{
		chunkSize:    chunkSize,
		chunkOverlap: chunkOverlap,
		minchunkSize: minchunkSize,
		tp:           tp,
		initErr:      err,
	}
}

// Err returns tokenizer initialization failure so callers can fail a revision
// explicitly instead of dereferencing a nil tokenizer while processing text.
func (s *textSplitter) Err() error {
	return s.initErr
}

// chunk represents a text segment with position information.
type chunk struct {
	Content     string
	chunkIndex  int
	StartOffset int
	EndOffset   int
}

// textParagraph preserves a lightweight structural profile from local text.
type textParagraph struct {
	content string
	heading bool
	code    bool
	list    bool
	table   bool
}

// Split splits text into chunks that respect token limits with overlap.
func (s *textSplitter) Split(text string) []chunk {
	if s.initErr != nil || s.tp == nil {
		applogger.Error("text splitter cannot run without tokenizer", "error", s.initErr)
		return nil
	}
	// Chunk offsets belong to the canonical rendition, so normalize line endings
	// before both structural parsing and exact offset alignment.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}

	paragraphs := s.splitParagraphs(text)
	if len(paragraphs) == 0 {
		return nil
	}

	var chunks []chunk
	var currentParts []string
	currentTokens := 0
	chunkIndex := 0
	startOffset := 0

	flush := func() {
		if len(currentParts) == 0 {
			return
		}
		content := strings.Join(currentParts, "\n\n")
		chunks = append(chunks, chunk{
			Content:     content,
			chunkIndex:  chunkIndex,
			StartOffset: startOffset,
			EndOffset:   startOffset + len(content),
		})
		chunkIndex++
		startOffset += len(content) - s.overlapCharCount(currentParts)
		currentParts = nil
		currentTokens = 0
	}

	for _, para := range paragraphs {
		paraTokens := len(s.tp.Encode(para.content, nil, nil))
		targetSize := s.targetSize(para)
		// A heading starts a new structural unit so its following content keeps
		// the correct section context in search_text and citations.
		if para.heading && len(currentParts) > 0 {
			flush()
		}

		if currentTokens+paraTokens > targetSize && len(currentParts) > 0 {
			flush()
		}

		if paraTokens > targetSize {
			if len(currentParts) > 0 {
				flush()
			}
			subchunks := s.splitLargeParagraph(para.content, chunkIndex, startOffset, targetSize)
			for _, sc := range subchunks {
				sc.chunkIndex = chunkIndex
				chunks = append(chunks, sc)
				chunkIndex++
			}
			if len(subchunks) > 0 {
				last := subchunks[len(subchunks)-1]
				startOffset = last.EndOffset
			}
			continue
		}

		currentParts = append(currentParts, para.content)
		currentTokens += paraTokens
	}

	flush()

	chunks = s.mergeSmallTailchunks(chunks)
	return s.alignChunkOffsets(text, chunks)
}

// alignChunkOffsets reconciles reconstructed chunk text with the canonical
// rendition. The splitter may rebuild paragraph separators or overlap text;
// locating each exact unit prevents those implementation details from leaking
// into evidence provenance. A failed match retains the best available offset
// and is logged for diagnosis instead of silently fabricating a location.
func (s *textSplitter) alignChunkOffsets(text string, chunks []chunk) []chunk {
	searchText := normalizeLocatorSearchText(text)
	searchStart := 0
	for index := range chunks {
		content := chunks[index].Content
		if content == "" {
			applogger.Warn("KB splitter produced empty chunk while aligning offsets", "chunk_index", chunks[index].chunkIndex)
			continue
		}
		start, end, found := searchText.find(content, searchStart)
		if !found {
			applogger.Warn("KB splitter could not align chunk to canonical text", "chunk_index", chunks[index].chunkIndex, "content_bytes", len(content), "fallback_start_offset", chunks[index].StartOffset, "fallback_end_offset", chunks[index].EndOffset)
			continue
		}
		chunks[index].StartOffset = start
		chunks[index].EndOffset = end
		// Do not advance to EndOffset: overlapping retrieval units may begin in
		// the previous unit's suffix.
		searchStart = start
	}
	return chunks
}

func (s *textSplitter) splitParagraphs(text string) []textParagraph {
	lines := strings.Split(text, "\n")
	var paragraphs []textParagraph
	var current []string
	inCodeFence := false
	currentKind := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		content := strings.Join(current, "\n")
		paragraphs = append(paragraphs, textParagraph{
			content: content,
			heading: isMarkdownHeading(content),
			code:    inCodeFence || strings.HasPrefix(strings.TrimSpace(content), "```") || strings.HasPrefix(strings.TrimSpace(content), "    "),
			list:    isListBlock(content),
			table:   isTableBlock(content),
		})
		current = nil
		currentKind = 0
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if len(current) > 0 && !inCodeFence {
				flush()
			}
			current = append(current, line)
			inCodeFence = !inCodeFence
			if !inCodeFence {
				flush()
			}
			continue
		}
		if inCodeFence {
			current = append(current, line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		kind := paragraphKind(line)
		if len(current) > 0 && kind != currentKind {
			flush()
		}
		currentKind = kind
		current = append(current, line)
	}
	flush()
	return paragraphs
}

func (s *textSplitter) targetSize(paragraph textParagraph) int {
	if paragraph.heading {
		target := s.chunkSize / 2
		if target < s.minchunkSize {
			return s.minchunkSize
		}
		return target
	}
	if paragraph.code {
		return s.chunkSize * 2
	}
	if paragraph.table {
		return s.chunkSize * 2
	}
	if paragraph.list {
		return s.chunkSize / 2
	}
	return s.chunkSize
}

func isMarkdownHeading(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, "#") && len(trimmed) > 1
}

// paragraphKind groups structural Markdown blocks without normalizing their
// source text. It intentionally uses a small deterministic recognizer rather
// than an LLM or parser dependency because local uploads are only TXT/MD/PDF.
func paragraphKind(line string) int {
	trimmed := strings.TrimSpace(line)
	switch {
	case isMarkdownHeading(trimmed):
		return 1
	case isListLine(trimmed):
		return 2
	case strings.HasPrefix(trimmed, "|"):
		return 3
	default:
		return 4
	}
}

func isListLine(line string) bool {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return true
	}
	for index, r := range line {
		if r == '.' || r == ')' {
			return index > 0 && index+1 < len(line) && line[index+1] == ' '
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return false
}

func isListBlock(content string) bool {
	lines := strings.Split(content, "\n")
	return len(lines) > 0 && isListLine(strings.TrimSpace(lines[0]))
}

func isTableBlock(content string) bool {
	lines := strings.Split(content, "\n")
	return len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "|")
}

func (s *textSplitter) splitLargeParagraph(para string, chunkIndex, startOffset, targetSize int) []chunk {
	words := strings.Fields(para)
	var chunks []chunk
	currentTokens := 0
	currentStart := -1
	currentEnd := -1
	searchOffset := 0

	for _, word := range words {
		wordOffset := strings.Index(para[searchOffset:], word)
		if wordOffset < 0 {
			applogger.Error("KB splitter could not locate token inside large paragraph", "chunk_index", chunkIndex, "token_bytes", len(word))
			return nil
		}
		wordStart := searchOffset + wordOffset
		wordEnd := wordStart + len(word)
		wordTokens := len(s.tp.Encode(word, nil, nil))
		if currentTokens+wordTokens > targetSize && currentStart >= 0 {
			content := para[currentStart:currentEnd]
			chunks = append(chunks, chunk{
				Content:     content,
				chunkIndex:  chunkIndex,
				StartOffset: startOffset + currentStart,
				EndOffset:   startOffset + currentEnd,
			})
			currentTokens = 0
			chunkIndex++
			currentStart = -1
		}
		if currentStart < 0 {
			currentStart = wordStart
		}
		currentEnd = wordEnd
		currentTokens += wordTokens
		searchOffset = wordEnd
	}

	if currentStart >= 0 {
		content := para[currentStart:currentEnd]
		chunks = append(chunks, chunk{
			Content:     content,
			chunkIndex:  chunkIndex,
			StartOffset: startOffset + currentStart,
			EndOffset:   startOffset + currentEnd,
		})
	}

	return chunks
}

func (s *textSplitter) overlapCharCount(parts []string) int {
	if len(parts) == 0 || s.chunkOverlap == 0 {
		return 0
	}
	overlapTokens := 0
	overlapChars := 0
	for i := len(parts) - 1; i >= 0; i-- {
		tokens := len(s.tp.Encode(parts[i], nil, nil))
		if overlapTokens+tokens > s.chunkOverlap {
			break
		}
		overlapTokens += tokens
		overlapChars += len(parts[i]) + 2
	}
	return overlapChars
}

// mergeSmallTailchunks merges the last chunk into the previous one if its
// token count is below minchunkSize. This avoids producing tiny fragments
// that degrade retrieval quality. Re-indexes chunkIndex after merging.
func (s *textSplitter) mergeSmallTailchunks(chunks []chunk) []chunk {
	if s.minchunkSize <= 0 || len(chunks) <= 1 {
		return chunks
	}

	last := &chunks[len(chunks)-1]
	lastTokens := len(s.tp.Encode(last.Content, nil, nil))
	if lastTokens >= s.minchunkSize {
		return chunks
	}

	prev := &chunks[len(chunks)-2]
	prev.Content = prev.Content + "\n\n" + last.Content
	prev.EndOffset = last.EndOffset

	merged := chunks[:len(chunks)-1]
	for i := range merged {
		merged[i].chunkIndex = i
	}
	return merged
}
