// Package logger provides application-wide structured logging using slog.
//
// Logs are written to both stdout (JSON format) and a daily rotating log file.
// Log level is controlled by the LOG_LEVEL configuration setting.
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"qingqiu-world-server/internal/config"
)

// L is the global logger instance.
var globalInstance *slog.Logger

// Init initializes the global logger with JSON output to stdout and a daily log file.
func Init() {
	settings := config.Get()

	logDir := settings.LogDir
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create log directory %s: %v\n", logDir, err)
	}

	logFile := filepath.Join(logDir, fmt.Sprintf("app_%s.log", time.Now().Format("20060102")))
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open log file: %v\n", err)
		globalInstance = slog.Default()
		return
	}

	var (
		level     slog.Level
		addSource bool
	)
	switch settings.LogLevel {
	case "DEBUG":
		level = slog.LevelDebug
		addSource = true
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(
		os.Stdout,
		&slog.HandlerOptions{
			AddSource: addSource,
			Level:     level,
		},
	)

	fileHandler := slog.NewJSONHandler(
		f,
		&slog.HandlerOptions{
			AddSource: addSource,
			Level:     level,
		},
	)

	mh := newMultiHandler(handler, fileHandler)
	globalInstance = slog.New(mh)

	// init functions
	Debug = globalInstance.Debug
	Info = globalInstance.Info
	Warn = globalInstance.Warn
	Error = globalInstance.Error

	slog.SetDefault(globalInstance)
}

type multiHandler struct {
	handlers []slog.Handler
}

func newMultiHandler(handlers ...slog.Handler) *multiHandler {
	return &multiHandler{handlers: handlers}
}

// Enabled reports whether any of the underlying handlers are enabled for the given level.
func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches the log record to all underlying handlers that are enabled for the record's level.
func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

// WithAttrs returns a new multiHandler that includes the given attributes.
func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return newMultiHandler(handlers...)
}

// WithGroup returns a new multiHandler that wraps attributes under the given group name.
func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return newMultiHandler(handlers...)
}
