// Package errs sends a binary's errors to Sentry.
//
// Nothing needs to call it directly: logx.New turns it on when SENTRY_DSN is
// set, every log line at error level then becomes a Sentry event, and
// run.Main reports a panic and flushes before the process exits. With
// SENTRY_DSN empty (tests, a laptop) it does nothing.
//
// Events are grouped by service and log message, not by the error text, so
// "load raw file" failing for a thousand different files is one issue with a
// thousand events, and a new message is a new issue (a chat alert).
package errs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
)

var on atomic.Bool

// Init starts Sentry for service when SENTRY_DSN is set, and reports whether
// it did. SENTRY_ENVIRONMENT defaults to production. The release is
// service@version, so Sentry ties each new issue to the build that brought it.
func Init(service, version string) (bool, error) {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return false, nil
	}
	env := os.Getenv("SENTRY_ENVIRONMENT")
	if env == "" {
		env = "production"
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:         dsn,
		Environment: env,
		Release:     service + "@" + version,
		// No request bodies, cookies or user IPs: errors only.
		SendDefaultPII: false,
		Tags:           map[string]string{"service": service},
	})
	if err != nil {
		return false, fmt.Errorf("sentry: %w", err)
	}
	on.Store(true)
	return true, nil
}

// Enabled reports whether Init turned Sentry on.
func Enabled() bool { return on.Load() }

// Flush waits up to timeout for queued events to go out.
func Flush(timeout time.Duration) {
	if on.Load() {
		sentry.Flush(timeout)
	}
}

// Panic reports a recovered panic value and waits briefly for it to be sent.
// The caller re-panics or exits afterwards.
func Panic(v any) {
	if !on.Load() {
		return
	}
	sentry.CurrentHub().Recover(v)
	sentry.Flush(2 * time.Second)
}

// Handler wraps next so that records at error level and above also go to
// Sentry. The record's attributes become the event's "log" context; an attribute
// holding an error (conventionally "err") becomes its exception.
func Handler(next slog.Handler) slog.Handler {
	return &handler{next: next}
}

type handler struct {
	next  slog.Handler
	attrs []slog.Attr // from With, groups already folded into the keys
	group string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError && on.Load() {
		capture(h.event(r))
	}
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(as)
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), prefixed(h.group, as)...)
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	c := *h
	c.next = h.next.WithGroup(name)
	c.group = join(h.group, name)
	return &c
}

// capture is replaced in tests.
var capture = func(ev *sentry.Event) { sentry.CaptureEvent(ev) }

func (h *handler) event(r slog.Record) *sentry.Event {
	ev := sentry.NewEvent()
	ev.Level = sentry.LevelError
	if r.Level > slog.LevelError {
		ev.Level = sentry.LevelFatal
	}
	ev.Timestamp = r.Time
	ev.Message = r.Message
	ev.Logger = "slog"
	fields := sentry.Context{}
	ev.Contexts = map[string]sentry.Context{"log": fields}

	var errAttr error
	var service string
	add := func(a slog.Attr) {
		v := a.Value.Resolve()
		if e, ok := v.Any().(error); ok && errAttr == nil {
			errAttr = e
		}
		switch a.Key {
		case "service":
			service = v.String() // already the service tag
		case "version":
			// Already in the release.
		default:
			fields[a.Key] = v.String()
		}
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		for _, f := range prefixed(h.group, []slog.Attr{a}) {
			add(f)
		}
		return true
	})

	if errAttr != nil {
		ev.Message = r.Message + ": " + errAttr.Error()
		ev.Exception = []sentry.Exception{{
			Type:  typeName(errAttr),
			Value: errAttr.Error(),
		}}
	}
	// One issue per log message, however the error text varies.
	ev.Fingerprint = []string{service, r.Message}
	return ev
}

// typeName names the innermost wrapped error's type, which is what tells two
// failures apart (a timeout from a refused connection).
func typeName(err error) string {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return fmt.Sprintf("%T", err)
		}
		err = u
	}
}

// prefixed flattens groups into dotted keys, as the JSON log line shows them.
func prefixed(group string, as []slog.Attr) []slog.Attr {
	var out []slog.Attr
	for _, a := range as {
		if a.Value.Kind() == slog.KindGroup {
			out = append(out, prefixed(join(group, a.Key), a.Value.Group())...)
			continue
		}
		if group != "" {
			a.Key = group + "." + a.Key
		}
		out = append(out, a)
	}
	return out
}

func join(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "." + b
}
