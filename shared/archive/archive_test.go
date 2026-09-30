package archive

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func sum(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) }

func TestDirPutGetStat(t *testing.T) {
	ctx := context.Background()
	s, err := Open("file://" + t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "taboola/2026/09/28/14/capture-a-1403.ndjson.zst"
	data := []byte("sealed bytes")
	if _, err := s.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat before put: %v", err)
	}
	if err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), sum(data)); err != nil {
		t.Fatal(err)
	}
	// The same bytes again are fine; other bytes are refused.
	if err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), sum(data)); err != nil {
		t.Fatalf("second put of the same bytes: %v", err)
	}
	other := []byte("other bytes!")
	if err := s.Put(ctx, key, bytes.NewReader(other), int64(len(other)), sum(other)); !errors.Is(err, ErrDifferent) {
		t.Fatalf("put of other bytes: %v", err)
	}
	o, err := s.Stat(ctx, key)
	if err != nil || o.Size != int64(len(data)) || o.MD5 != sum(data) {
		t.Fatalf("stat %+v %v", o, err)
	}
	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q", got)
	}
}

func TestDirRefusesShortWrites(t *testing.T) {
	s := &Dir{Root: t.TempDir()}
	data := []byte("abc")
	err := s.Put(context.Background(), "x/y.zst", bytes.NewReader(data), 4, sum(data))
	if err == nil {
		t.Fatal("a short body was stored")
	}
	if _, err := s.Stat(context.Background(), "x/y.zst"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a failed put left a file: %v", err)
	}
}

func TestBadKeys(t *testing.T) {
	s := &Dir{Root: t.TempDir()}
	for _, k := range []string{"", "/abs", "a/../b", "a//b"} {
		if err := s.Put(context.Background(), k, bytes.NewReader(nil), 0, sum(nil)); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}
