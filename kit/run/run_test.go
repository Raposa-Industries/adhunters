package run

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestReturnsServiceError(t *testing.T) {
	want := errors.New("boom")
	err := Until(context.Background(), quiet, time.Second, func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestDrainsOnStop(t *testing.T) {
	stop, cancel := context.WithCancel(context.Background())
	drained := false
	go cancel()
	err := Until(stop, quiet, time.Second, func(ctx context.Context) error {
		<-ctx.Done()
		drained = true
		return ctx.Err()
	})
	if err != nil || !drained {
		t.Fatalf("err=%v drained=%v", err, drained)
	}
}

func TestGiveUpAfterGrace(t *testing.T) {
	stop, cancel := context.WithCancel(context.Background())
	cancel()
	block := make(chan struct{})
	defer close(block)
	err := Until(stop, quiet, 20*time.Millisecond, func(context.Context) error { <-block; return nil })
	if !errors.Is(err, ErrGraceExceeded) {
		t.Fatalf("got %v", err)
	}
}
