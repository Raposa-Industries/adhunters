// Package rawtest writes sealed raw files for tests, through capture's own
// spool writer, so tests read exactly what capture writes.
package rawtest

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
)

// Quiet is a logger that drops everything.
func Quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Seal writes recs into dir as instance's files for the minute they were
// written in (at), and seals them.
func Seal(t testing.TB, dir, instance string, at time.Time, recs ...*spool.Record) {
	t.Helper()
	w, err := spool.Open(dir, instance, Quiet(), spool.Options{Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Instance == "" {
			r.Instance = instance
		}
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
}
