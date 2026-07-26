package kb

import (
	"strings"

	applogger "qingqiu-world-server/internal/logger"

	"github.com/pkoukk/tiktoken-go"
)

// textSplitter splits text into chunks based on token count with overlap.
// Uses tiktoken for token counting (cl100k_base encoding, compatible with
// OpenAI models). Token counts for non-OpenAI models may have minor deviations.
type textSplitter struct {
	chunkSize    int
	chunkOverlap int
	minchunkSize int
	tp           *tiktoken.Tiktoken
}

// newTextSplitter creates a textSplitter with the given chunk size, overlap, and minchunkSize.
// chunks smaller than minchunkSize tokens are merged into the previous chunk.
func newTextSplitter(chunkSize, chunkOverlap, minchunkSize int) *textSplitter {
	tp, err := tiktoken.EncodingForModel("text-embedding-3-small")
	if err != nil {
		applogger.Error("failed to get tiktoken encoding for model, falling back to cl100k_base", "error", err)
		tp, _ = tiktoken.GetEncoding("cl100k_base")
	}
	return &textSplitter{
		chunkSize:    chunkSize,
		chunkOverlap: chunkOverlap,
		minchunkSize: minchunkSize,
		tp:           tp,
	}
}

// chunk represents a text segment with position information.
type chunk struct {
	Content     string
	chunkIndex  int
	StartOffset int
	EndOffset   int
}

// Split splits text into chunks that respect token limits with overlap.
func (s *textSplitter) Split(text string) []chunk {
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
		paraTokens := len(s.tp.Encode(para, nil, nil))

		if currentTokens+paraTokens > s.chunkSize && len(currentParts) > 0 {
			flush()
		}

		if paraTokens > s.chunkSize {
			if len(currentParts) > 0 {
				flush()
			}
			subchunks := s.splitLargeParagraph(para, chunkIndex, startOffset)
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

		currentParts = append(currentParts, para)
		currentTokens += paraTokens
	}

	flush()

	chunks = s.mergeSmallTailchunks(chunks)
	return chunks
}

func (s *textSplitter) splitParagraphs(text string) []string {
	lines := strings.Split(text, "\n")
	var paragraphs []string
	var current []string

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				paragraphs = append(paragraphs, strings.Join(current, "\n"))
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		paragraphs = append(paragraphs, strings.Join(current, "\n"))
	}
	return paragraphs
}

func (s *textSplitter) splitLargeParagraph(para string, chunkIndex, startOffset int) []chunk {
	words := strings.Fields(para)
	var chunks []chunk
	var currentWords []string
	currentTokens := 0
	offset := startOffset

	for _, word := range words {
		wordTokens := len(s.tp.Encode(word, nil, nil))
		if currentTokens+wordTokens > s.chunkSize && len(currentWords) > 0 {
			content := strings.Join(currentWords, " ")
			chunks = append(chunks, chunk{
				Content:     content,
				chunkIndex:  chunkIndex,
				StartOffset: offset,
				EndOffset:   offset + len(content),
			})
			offset += len(content)
			currentWords = nil
			currentTokens = 0
		}
		currentWords = append(currentWords, word)
		currentTokens += wordTokens
	}

	if len(currentWords) > 0 {
		content := strings.Join(currentWords, " ")
		chunks = append(chunks, chunk{
			Content:     content,
			chunkIndex:  chunkIndex,
			StartOffset: offset,
			EndOffset:   offset + len(content),
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
