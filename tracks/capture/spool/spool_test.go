package spool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func quiet() *slog.Logger            { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func rec(network, body string) *Record {
	r := &Record{ID: "x", Network: network, Status: 200}
	r.SetBody([]byte(body))
	return r
}

func readSealed(t *testing.T, path string) []Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec, _ := zstd.NewReader(f)
	defer dec.Close()
	var out []Record
	sc := bufio.NewScanner(dec)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestWriterSealsEachMinutePerNetwork(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 9, 28, 14, 3, 10, 0, time.UTC)}
	var sealed []Sealed
	var mu sync.Mutex
	w, err := Open(dir, "a", quiet(), Options{Now: c.now, OnSealed: func(s Sealed) { mu.Lock(); sealed = append(sealed, s); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	odd := string([]byte{0xff, 0xfe, 'x'}) // not UTF-8: must round-trip through base64
	for _, r := range []*Record{rec("taboola", `TRC.callbacks.mute()`), rec("taboola", odd), rec("newsbreak", `{"seatbid":[]}`)} {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	c.add(time.Minute)
	w.Write(rec("taboola", "next minute"))
	w.Close()

	a := filepath.Join(dir, "taboola/2026/09/28/14/capture-a-1403.ndjson.zst")
	got := readSealed(t, a)
	if len(got) != 2 {
		t.Fatalf("%d records in %s", len(got), a)
	}
	body, _ := got[1].BodyBytes()
	if !bytes.Equal(body, []byte(odd)) || got[1].BodyBase64 == "" {
		t.Fatalf("body did not round-trip: %q", body)
	}
	if len(readSealed(t, filepath.Join(dir, "newsbreak/2026/09/28/14/capture-a-1403.ndjson.zst"))) != 1 ||
		len(readSealed(t, filepath.Join(dir, "taboola/2026/09/28/14/capture-a-1404.ndjson.zst"))) != 1 {
		t.Fatal("missing sealed files")
	}
	if len(sealed) != 3 {
		t.Fatalf("sealed %d files", len(sealed))
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*/*/*/*/*/*.ndjson"))
	if len(matches) != 0 {
		t.Fatalf("plain files left: %v", matches)
	}

	totals, err := Summarize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 2 || totals[0].Network != "newsbreak" || totals[1].Scrapes != 3 || totals[1].Day != "2026-09-28" {
		t.Fatalf("totals %+v", totals)
	}
	var out strings.Builder
	Report(&out, totals, 14000)
	if !strings.Contains(out.String(), "GB a day") {
		t.Fatal(out.String())
	}
}

func TestTickClosesAnEndedMinuteWithoutNewWrites(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 9, 28, 14, 3, 59, 0, time.UTC)}
	w, _ := Open(dir, "a", quiet(), Options{Now: c.now})
	w.Write(rec("taboola", "x"))
	w.Tick()
	if _, err := os.Stat(filepath.Join(dir, "taboola/2026/09/28/14/capture-a-1403.ndjson")); err != nil {
		t.Fatal("file closed before its minute ended")
	}
	c.add(time.Second)
	w.Tick()
	w.Close()
	if len(readSealed(t, filepath.Join(dir, "taboola/2026/09/28/14/capture-a-1403.ndjson.zst"))) != 1 {
		t.Fatal("not sealed")
	}
}

func TestRestartSealsLeftoversAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 9, 28, 14, 3, 10, 0, time.UTC)}
	// A crashed run left this minute's plain file and a half-written .zst.
	sub := filepath.Join(dir, "taboola/2026/09/28/14")
	os.MkdirAll(sub, 0o750)
	line, _ := rec("taboola", "before crash").Marshal()
	os.WriteFile(filepath.Join(sub, "capture-a-1403.ndjson"), line, 0o640)
	os.WriteFile(filepath.Join(sub, "capture-a-1403.ndjson.zst.tmp"), []byte("junk"), 0o640)
	// Another instance's file is not ours to touch.
	os.WriteFile(filepath.Join(sub, "capture-b-1403.ndjson"), line, 0o640)

	w, err := Open(dir, "a", quiet(), Options{Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(rec("taboola", "after restart"))
	w.Close()

	first := readSealed(t, filepath.Join(sub, "capture-a-1403.ndjson.zst"))
	second := readSealed(t, filepath.Join(sub, "capture-a-1403-2.ndjson.zst"))
	if len(first) != 1 || *first[0].Body != "before crash" || len(second) != 1 || *second[0].Body != "after restart" {
		t.Fatalf("first %v second %v", first, second)
	}
	if _, err := os.Stat(filepath.Join(sub, "capture-b-1403.ndjson")); err != nil {
		t.Fatal("touched another instance's file")
	}
}
