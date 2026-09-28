// Package run implements the platform's stop rule for a binary's main loop.
//
// SIGTERM or SIGINT cancels the context passed to the service. The service
// must stop taking new work, finish or checkpoint what is running, and return.
// If it has not returned within the grace period, Main gives up and returns
// ErrGraceExceeded so the process exits non-zero. systemd waits 45 s, so the
// default 30 s grace leaves room for the exit itself.
package run

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// DefaultGrace is how long a service gets to drain after a stop signal.
const DefaultGrace = 30 * time.Second

// ErrGraceExceeded means the service did not return in time after a stop signal.
var ErrGraceExceeded = errors.New("run: service did not stop within the grace period")

// Main runs fn until it returns or a stop signal arrives and the grace period
// passes. It returns fn's error, or ErrGraceExceeded.
func Main(log *slog.Logger, grace time.Duration, fn func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	return Until(ctx, log, grace, fn)
}

// Until is Main with the stop signal given as a context, for tests and for
// services that embed several loops.
func Until(stopCtx context.Context, log *slog.Logger, grace time.Duration, fn func(ctx context.Context) error) error {
	done := make(chan error, 1)
	go func() { done <- fn(stopCtx) }()

	select {
	case err := <-done:
		return err
	case <-stopCtx.Done():
		log.Info("stop requested, draining", "grace", grace.String())
	}

	select {
	case err := <-done:
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		log.Info("drained")
		return err
	case <-time.After(grace):
		log.Error("drain timed out", "grace", grace.String())
		return ErrGraceExceeded
	}
}
