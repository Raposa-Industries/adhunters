// Package logx builds the JSON logger every AdHunters binary uses.
//
// Every line carries the service and its version, so one search in Grafana
// follows a piece of work across services. Put correlation ids (capture file,
// job, investigation, request) on the logger with With, not in the message.
//
// Every line at error level is also counted by message in Errors, which
// kit/ops serves on /metrics, so an alert sees a service failing the same way
// over and over, not only the first time.
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/kit/errs"
)

// Errors counts the lines logged at error level or above, by message, in
// every logger New and NewTo build: adhunters_log_errors_total{msg}. kit/ops
// registers it on every binary's /metrics.
var Errors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "adhunters_log_errors_total",
	Help: "Lines logged at error level or above, by message.",
}, []string{"msg"})

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
	return slog.New(counted{h}).With("service", service, "version", version)
}

// counted adds each error line to Errors.
type counted struct{ slog.Handler }

func (c counted) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		Errors.WithLabelValues(msgLabel(r.Message)).Inc()
	}
	return c.Handler.Handle(ctx, r)
}

func (c counted) WithAttrs(as []slog.Attr) slog.Handler { return counted{c.Handler.WithAttrs(as)} }
func (c counted) WithGroup(name string) slog.Handler    { return counted{c.Handler.WithGroup(name)} }

var digits = regexp.MustCompile(`[0-9]+`)

// msgLabel keeps the label's values few: messages are fixed text (what
// varies belongs in attributes), but a number in one would make a new series
// per value, so numbers read N, and a message is cut at 100 characters.
func msgLabel(msg string) string {
	msg = digits.ReplaceAllString(msg, "N")
	if len(msg) > 100 {
		msg = strings.ToValidUTF8(msg[:100], "")
	}
	return msg
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
