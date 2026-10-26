package logger

import "log/slog"

// Debug logs at DEBUG level.
// Info logs at INFO level.
// Warn logs at WARN level.
// Error logs at ERROR level.
// Default slog functions keep validation and unit tests observable before the
// application logger is initialized; Init replaces them with configured sinks.
var (
	Debug = slog.Debug
	Info  = slog.Info
	Warn  = slog.Warn
	Error = slog.Error
)
