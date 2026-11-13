// Package observability provides logging and metrics wiring.
package observability

import (
	"io"
	"log/slog"
)

// NewLogger builds a structured logger. JSON is the default because logs are
// meant to be shipped; text is for local development.
func NewLogger(level slog.Level, format string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}
