// Package logx builds the JSON logger every AdHunters binary uses.
//
// Every line carries the service and its version, so one search in Grafana
// follows a piece of work across services. Put correlation ids (capture file,
// job, investigation, request) on the logger with With, not in the message.
package logx

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON logger writing to stdout. The level comes from LOG_LEVEL
// (debug, info, warn, error) and defaults to info.
func New(service, version string) *slog.Logger {
	return NewTo(os.Stdout, service, version, os.Getenv("LOG_LEVEL"))
}

// NewTo is New with an explicit writer and level, for tests.
func NewTo(w io.Writer, service, version, level string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(h).With("service", service, "version", version)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
