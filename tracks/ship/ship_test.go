package ship

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/internal/rawtest"
	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
)

func rec(id string) *spool.Record {
	r := &spool.Record{ID: id, At: time.Now(), Network: "taboola", Status: 200}
	r.SetBody([]byte("TRC.callbacks.mute()"))
	return r
}

func TestPassShipsRecordsAndCleansUp(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	dir, arch := t.TempDir(), t.TempDir()
	m1 := time.Date(2026, 9, 28, 14, 3, 10, 0, time.UTC)
	rawtest.Seal(t, dir, "a", m1, rec("1"), rec("2"))
	rawtest.Seal(t, dir, "a", m1.Add(time.Minute), rec("3"))
	// Not a sealed raw file: left alone.
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o640)

	now := time.Date(2026, 9, 28, 14, 5, 30, 0, time.UTC)
	c := Config{
		Spool: dir, Store: &archive.Dir{Root: arch}, DB: db, Box: "worker", KeepFor: 48 * time.Hour,
		Log: rawtest.Quiet(), Metrics: NewMetrics(prometheus.NewRegistry()), Now: func() time.Time { return now },
	}
	n, err := Pass(ctx, ctx, c)
	if err != nil || n != 2 {
		t.Fatalf("shipped %d, err %v", n, err)
	}
	key := "taboola/2026/09/28/14/capture-a-1403.ndjson.zst"
	if _, err := c.Store.Stat(ctx, key); err != nil {
		t.Fatalf("not archived: %v", err)
	}
	var rows int
	var minute time.Time
	var inst string
	if err := db.QueryRow(ctx, `SELECT rows, minute, instance FROM tracks.raw_file WHERE key = $1`, key).Scan(&rows, &minute, &inst); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || inst != "a" || !minute.Equal(time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC)) {
		t.Fatalf("row: rows %d instance %q minute %s", rows, inst, minute)
	}

	// A second pass finds nothing; a lost marker only redoes the same file.
	if n, err := Pass(ctx, ctx, c); err != nil || n != 0 {
		t.Fatalf("second pass shipped %d, err %v", n, err)
	}
	_ = os.Remove(filepath.Join(dir, key+Marker))
	if n, err := Pass(ctx, ctx, c); err != nil || n != 1 {
		t.Fatalf("redo shipped %d, err %v", n, err)
	}
	var count int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM tracks.raw_file`).Scan(&count)
	if count != 2 {
		t.Fatalf("%d raw_file rows, want 2", count)
	}

	// 48 hours after shipping, the local copies go.
	old := time.Now().Add(-49 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, key+Marker), old, old)
	now = time.Now()
	if _, err := Pass(ctx, ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, key)); !os.IsNotExist(err) {
		t.Fatal("shipped file kept past 48 hours")
	}
	if _, err := os.Stat(filepath.Join(dir, "taboola/2026/09/28/14/capture-a-1404.ndjson.zst")); err != nil {
		t.Fatal("a newer shipped file was deleted")
	}
}

func TestPassRefusesAChangedFile(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	at := time.Date(2026, 9, 28, 14, 3, 10, 0, time.UTC)
	rawtest.Seal(t, dir, "a", at, rec("1"))
	c := Config{Spool: dir, Store: &archive.Dir{Root: t.TempDir()}, DB: db, Box: "worker", Log: rawtest.Quiet()}
	if _, err := Pass(ctx, ctx, c); err != nil {
		t.Fatal(err)
	}
	// The same key recorded with other bytes (another box shipped it): the
	// file stays unmarked and the pass fails, so an alert fires.
	key := "taboola/2026/09/28/14/capture-a-1403.ndjson.zst"
	if _, err := db.Exec(ctx, `UPDATE tracks.raw_file SET sha256 = 'other' WHERE key = $1`, key); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, key+Marker))
	if _, err := Pass(ctx, ctx, c); err == nil {
		t.Fatal("a file recorded with other bytes was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, key+Marker)); !os.IsNotExist(err) {
		t.Fatal("marked as shipped")
	}
}
