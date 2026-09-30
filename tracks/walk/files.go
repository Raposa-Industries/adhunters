package walk

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
)

// Archive uploads the sealed raw walk files under dir to the archive, lists
// each in tracks.walk_file, and then removes the local copy. A file that
// fails stays for the next pass. It returns how many it archived.
func Archive(ctx context.Context, db *pgxpool.Pool, store archive.Store, dir string) (int, error) {
	var keys []string
	root := filepath.Join(dir, Network)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".ndjson.zst") {
			rel, _ := filepath.Rel(dir, path)
			keys = append(keys, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range keys {
		if err := archiveOne(ctx, db, store, dir, key); err != nil {
			return n, fmt.Errorf("%s: %w", key, err)
		}
		n++
	}
	return n, nil
}

func archiveOne(ctx context.Context, db *pgxpool.Pool, store archive.Store, dir, key string) error {
	k, err := spool.ParseKey(key)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, filepath.FromSlash(key))
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rows := 0
	if err := spool.ReadLines(bytes.NewReader(data), func(int, []byte) error { rows++; return nil }); err != nil {
		return err
	}
	m := md5.Sum(data)
	s := sha256.Sum256(data)
	if err := store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), hex.EncodeToString(m[:])); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO tracks.walk_file (key, minute, rows, bytes, sha256) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (key) DO NOTHING`, key, k.Minute, rows, len(data), hex.EncodeToString(s[:])); err != nil {
		return err
	}
	return os.Remove(path)
}

// Replayed is what a replay did.
type Replayed struct {
	Files int
	Walks int
}

// Replay parses the archived walk files of minutes [from, to) again and
// saves every walk they hold, replacing what it stored before. The walk
// state is left alone: a replay reads the past, it walks nothing.
func Replay(ctx context.Context, db *pgxpool.Pool, store archive.Store, from, to time.Time) (Replayed, error) {
	var r Replayed
	rows, err := db.Query(ctx, `SELECT key FROM tracks.walk_file WHERE minute >= $1 AND minute < $2 ORDER BY minute, id`, from, to)
	if err != nil {
		return r, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return r, err
	}
	for _, key := range keys {
		n, err := ReplayFile(ctx, db, store, key)
		if err != nil {
			return r, fmt.Errorf("%s: %w", key, err)
		}
		r.Files++
		r.Walks += n
	}
	return r, nil
}

// ReplayFile parses and saves one archived walk file.
func ReplayFile(ctx context.Context, db *pgxpool.Pool, store archive.Store, key string) (int, error) {
	body, err := store.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = body.Close() }()
	n := 0
	err = spool.ReadLines(body, func(line int, b []byte) error {
		var rec Record
		if err := json.Unmarshal(b, &rec); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if err := Save(ctx, db, &rec, Parse(&rec)); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		n++
		return nil
	})
	return n, err
}
