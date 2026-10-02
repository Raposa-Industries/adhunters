package walk

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	pathpkg "path"
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
//
// A walker restarted inside a minute writes a second file with the same
// name as the one it archived on the way out (the local copy is gone, so
// the spool does not know to add -2). The archive never replaces a key, so
// that file goes under the next free name of its minute, -2, -3, …, and both
// are kept; log says so.
func Archive(ctx context.Context, db *pgxpool.Pool, store archive.Store, dir string, log *slog.Logger) (int, error) {
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
		stored, err := archiveOne(ctx, db, store, dir, key)
		if err != nil {
			return n, fmt.Errorf("%s: %w", key, err)
		}
		if stored != key && log != nil {
			log.Warn("raw walk file archived under another name: the archive holds other bytes under its own",
				"file", key, "archived_as", stored)
		}
		n++
	}
	return n, nil
}

// maxSameMinute is how many files of one minute the archive may hold.
const maxSameMinute = 50

// archiveOne archives one file and returns the key it went under.
func archiveOne(ctx context.Context, db *pgxpool.Pool, store archive.Store, dir, key string) (string, error) {
	k, err := spool.ParseKey(key)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filepath.FromSlash(key))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	rows := 0
	if err := spool.ReadLines(bytes.NewReader(data), func(int, []byte) error { rows++; return nil }); err != nil {
		return "", err
	}
	m := md5.Sum(data)
	s := sha256.Sum256(data)
	stored := key
	stem := pathpkg.Dir(key) + "/capture-" + k.Instance + "-" + k.Minute.Format("1504")
	for n := 1; ; n++ {
		if n > 1 {
			if stored = fmt.Sprintf("%s-%d.ndjson.zst", stem, n); stored == key {
				continue // the file's own name, tried first
			}
		}
		err := store.Put(ctx, stored, bytes.NewReader(data), int64(len(data)), hex.EncodeToString(m[:]))
		if err == nil {
			break
		}
		if !errors.Is(err, archive.ErrDifferent) || n >= maxSameMinute {
			return "", err
		}
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO tracks.walk_file (key, minute, rows, bytes, sha256) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (key) DO NOTHING`, stored, k.Minute, rows, len(data), hex.EncodeToString(s[:])); err != nil {
		return "", err
	}
	return stored, os.Remove(path)
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
