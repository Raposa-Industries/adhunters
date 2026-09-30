// Package collect is intel-collect: it asks Taboola and RedTrack for
// numbers on a schedule and keeps every answer as received before anything
// reads it (decision 0003).
//
// An answer goes to the spool on local disk first, so collection carries on
// while the database restarts or a deploy runs elsewhere; Drain moves spool
// files into intel.answer and deletes each once its row is committed.
// Nothing here parses numbers: intel-numbers does, and can parse any range
// again.
package collect

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Answer is one answer as received, with what was asked.
type Answer struct {
	SpoolID   string         `json:"spool_id"`
	Source    string         `json:"source"` // taboola, redtrack
	Login     string         `json:"login"`
	Account   string         `json:"account,omitempty"`
	Kind      string         `json:"kind"`
	Path      string         `json:"path"`
	Params    map[string]any `json:"params,omitempty"`
	Status    int            `json:"status"`
	FetchedAt time.Time      `json:"fetched_at"`
	Body      []byte         `json:"-"`
}

// Spool is a folder of answers not yet in the database. Each file is gzip:
// one JSON line with the answer's fields, then the body as received.
type Spool struct{ Dir string }

const spoolExt = ".answer.gz"

// Put writes a to the spool and syncs it, giving it a spool id. Answers
// sort by name in the order they were put.
func (s Spool) Put(a *Answer) error {
	if a.SpoolID == "" {
		b := make([]byte, 5)
		_, _ = rand.Read(b)
		a.SpoolID = a.FetchedAt.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(b)
	}
	head, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(head)
	zw.Write([]byte{'\n'})
	zw.Write(a.Body)
	if err := zw.Close(); err != nil {
		return err
	}
	final := filepath.Join(s.Dir, a.SpoolID+spoolExt)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Read opens one spool file.
func Read(path string) (*Answer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	all, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	i := bytes.IndexByte(all, '\n')
	if i < 0 {
		return nil, errors.New("spool file has no header line")
	}
	var a Answer
	if err := json.Unmarshal(all[:i], &a); err != nil {
		return nil, err
	}
	a.Body = all[i+1:]
	return &a, nil
}

// Pending lists the spool's answers, oldest first.
func (s Spool) Pending() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), spoolExt) {
			out = append(out, filepath.Join(s.Dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Drain moves every spool file into intel.answer, deleting each once its
// row is committed. A file that is already in (a crash between the commit
// and the delete) is only deleted. It returns how many it moved; a file it
// cannot read is left in place and reported.
func (s Spool) Drain(ctx context.Context, db *pgxpool.Pool) (int, error) {
	files, err := s.Pending()
	if err != nil {
		return 0, err
	}
	n := 0
	var bad []string
	for _, path := range files {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		a, err := Read(path)
		if err != nil {
			bad = append(bad, filepath.Base(path)+": "+err.Error())
			continue
		}
		if err := Store(ctx, db, a); err != nil {
			return n, err
		}
		if err := os.Remove(path); err != nil {
			return n, err
		}
		n++
	}
	if len(bad) > 0 {
		return n, fmt.Errorf("unreadable spool files left in place: %s", strings.Join(bad, "; "))
	}
	return n, nil
}

// Store inserts one answer, body gzipped; a spool id already in is skipped.
func Store(ctx context.Context, db *pgxpool.Pool, a *Answer) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(a.Body)
	if err := zw.Close(); err != nil {
		return err
	}
	params := a.Params
	if params == nil {
		params = map[string]any{}
	}
	_, err := db.Exec(ctx, `
		INSERT INTO intel.answer (spool_id, source, login, account, kind, path, params, status, fetched_at, body, body_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (spool_id) DO NOTHING`,
		a.SpoolID, a.Source, a.Login, a.Account, a.Kind, a.Path, params, a.Status, a.FetchedAt, buf.Bytes(), len(a.Body))
	return err
}
