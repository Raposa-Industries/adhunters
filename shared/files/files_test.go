package files

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDirPutAndGet(t *testing.T) {
	ctx := context.Background()
	store, err := Open("file://" + t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(src, []byte("not really a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := Sum(src)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.Put(ctx, sum, "video/mp4", src, size)
	if err != nil {
		t.Fatal(err)
	}
	if key != "files/"+sum {
		t.Fatalf("key = %s", key)
	}
	// The same bytes again are harmless.
	if _, err := store.Put(ctx, sum, "video/mp4", src, size); err != nil {
		t.Fatal(err)
	}
	r, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	if string(b) != "not really a video" {
		t.Fatalf("read back %q", b)
	}
	if _, err := store.Get(ctx, "files/00000000000000000000000000000000"); err != ErrNotFound {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := store.Get(ctx, "../etc/passwd"); err == nil {
		t.Fatal("a bad key was read")
	}
}

func TestDirRefusesChangedBytes(t *testing.T) {
	store := &Dir{Root: t.TempDir()}
	src := filepath.Join(t.TempDir(), "a")
	if err := os.WriteFile(src, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "900150983cd24fb0d6963f7d28e17f72", "", src, 3); err != nil {
		t.Fatalf("right md5 refused: %v", err)
	}
	if _, err := store.Put(context.Background(), "00000000000000000000000000000000", "", src, 3); err == nil {
		t.Fatal("wrong md5 stored")
	}
}
