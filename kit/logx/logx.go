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

	"github.com/Raposa-Industries/adhunters/kit/errs"
)

// New returns a JSON logger writing to stdout. The level comes from LOG_LEVEL
// (debug, info, warn, error) and defaults to info.
//
// When SENTRY_DSN is set, New also turns on Sentry (kit/errs), and every line
// at error level becomes a Sentry event as well.
func New(service, version string) *slog.Logger {
	log := NewTo(os.Stdout, service, version, os.Getenv("LOG_LEVEL"))
	on, err := errs.Init(service, version)
	switch {
	case err != nil:
		log.Warn("errors are not reaching Sentry", "err", err)
	case on:
		log = slog.New(errs.Handler(log.Handler()))
	}
	return log
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
