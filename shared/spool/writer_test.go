package spool

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteLineNamesFilesByPrefixAndStream(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 30, 20, 7, 30, 0, time.UTC)
	var sealed []Sealed
	w, err := Open(dir, "edge", "a", slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Now:      func() time.Time { return at },
		OnSealed: func(s Sealed) { sealed = append(sealed, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteLine("events", []byte("{\"a\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteLine("events", []byte("no newline")); err == nil {
		t.Error("a line without a newline was taken")
	}
	if err := w.WriteLine("../up", []byte("x\n")); err == nil {
		t.Error("a stream with a path in it was taken")
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	want := filepath.Join(dir, "events", "2026", "09", "30", "20", "edge-a-2007.ndjson.zst")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("sealed file not where expected: %v", err)
	}
	if len(sealed) != 1 || sealed[0].Stream != "events" || sealed[0].Rows != 1 {
		t.Errorf("sealed = %+v", sealed)
	}
}
