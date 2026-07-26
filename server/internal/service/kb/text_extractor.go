package kb

import (
	"fmt"
	"io"
	"os"
	"strings"

	"qingqiu-world-server/internal/constants"

	"github.com/ledongthuc/pdf"
)

// Extract reads a file and extracts its text content based on file extension.
func Extract(filePath string) (string, error) {
	ext := strings.ToLower(filePath[strings.LastIndex(filePath, "."):])
	if !constants.IsAllowedFileExtension(ext) {
		return "", fmt.Errorf("unsupported file type: %s", ext)
	}

	switch ext {
	case ".txt", ".md":
		return extractPlainText(filePath)
	case ".pdf":
		return extractPDF(filePath)
	default:
		return "", fmt.Errorf("unsupported file type: %s", ext)
	}
}

// extractPlainText reads a plain text or markdown file.
func extractPlainText(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	return string(data), nil
}

// extractPDF extracts text from a PDF file using the ledongthuc/pdf library.
// Handles compressed content streams (FlateDecode) and multi-page documents.
func extractPDF(filePath string) (string, error) {
	f, r, err := pdf.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open PDF: %w", err)
	}
	defer f.Close()

	reader, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("failed to extract PDF text: %w", err)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read PDF text: %w", err)
	}

	text := string(data)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("no text content extracted from PDF")
	}

	return text, nil
}
