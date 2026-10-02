package walk

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
)

func md5hex(b []byte) string { m := md5.Sum(b); return hex.EncodeToString(m[:]) }

// A walker restarted inside a minute writes a second file under the name of
// the one it archived on the way out. The archive keeps both: the second
// goes under the next free name of its minute, and the files after it are
// archived as usual.
func TestArchiveSameMinuteTwice(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	store := &archive.Dir{Root: t.TempDir()}
	zst := func(line string) []byte {
		var b bytes.Buffer
		w, _ := zstd.NewWriter(&b)
		_, _ = w.Write([]byte(line + "\n"))
		_ = w.Close()
		return b.Bytes()
	}
	local := func(key, line string) []byte {
		t.Helper()
		b := zst(line)
		p := filepath.Join(dir, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o640); err != nil {
			t.Fatal(err)
		}
		return b
	}
	stored := func(key string) []byte {
		t.Helper()
		r, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		defer r.Close()
		b, _ := io.ReadAll(r)
		return b
	}
	const key = "walk/2026/10/01/21/capture-adhunters-worker-2155.ndjson.zst"
	first := zst(`{"id":"before the restart"}`)
	if err := store.Put(ctx, key, bytes.NewReader(first), int64(len(first)), md5hex(first)); err != nil {
		t.Fatal(err)
	}
	second := local(key, `{"id":"after the restart"}`)
	next := local("walk/2026/10/01/21/capture-adhunters-worker-2156.ndjson.zst", `{"id":"the next minute"}`)

	n, err := Archive(ctx, db, store, dir, nil)
	if err != nil || n != 2 {
		t.Fatalf("archived %d: %v", n, err)
	}
	if !bytes.Equal(stored(key), first) {
		t.Error("the first file was replaced")
	}
	if !bytes.Equal(stored("walk/2026/10/01/21/capture-adhunters-worker-2155-2.ndjson.zst"), second) {
		t.Error("the second file is not under -2")
	}
	if !bytes.Equal(stored("walk/2026/10/01/21/capture-adhunters-worker-2156.ndjson.zst"), next) {
		t.Error("the next minute's file was not archived")
	}

	// A third one of the same minute, and the spool's own -2 beside it.
	third := local(key, `{"id":"another restart"}`)
	own := local("walk/2026/10/01/21/capture-adhunters-worker-2155-2.ndjson.zst", `{"id":"the spool's own second file"}`)
	if n, err := Archive(ctx, db, store, dir, nil); err != nil || n != 2 {
		t.Fatalf("archived %d: %v", n, err)
	}
	got := map[string]bool{}
	for _, k := range []string{"-3", "-4"} {
		b := stored("walk/2026/10/01/21/capture-adhunters-worker-2155" + k + ".ndjson.zst")
		got[string(b)] = true
	}
	if !got[string(third)] || !got[string(own)] {
		t.Error("the third file and the spool's own -2 are not under -3 and -4")
	}

	var files int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM tracks.walk_file WHERE key LIKE 'walk/2026/10/01/21/capture-adhunters-worker-2155%'`).Scan(&files); err != nil || files != 3 {
		t.Errorf("%d files of 21:55 listed (the first was archived before), want 3: %v", files, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "walk", "2026", "10", "01", "21", "*"))
	if len(left) != 0 {
		t.Errorf("local files left: %v", left)
	}
}
