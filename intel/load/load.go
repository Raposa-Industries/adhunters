// Package load reads the answers intel-collect kept (intel.answer) into
// Intel's tables. It can run over any range again: a newer answer replaces
// what an older one said, never the other way round, so loading answers
// twice or out of order ends in the same tables.
package load

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Loader loads answers.
type Loader struct {
	DB  *pgxpool.Pool
	Log *slog.Logger
}

// answer is one row of intel.answer.
type answer struct {
	ID        int64
	Source    string
	Login     string
	Account   string
	Kind      string
	Params    map[string]any
	Status    int
	FetchedAt time.Time
	Body      []byte
}

// Pending loads every answer not loaded yet, oldest first, in batches. It
// returns how many it read.
func (l *Loader) Pending(ctx context.Context) (int, error) {
	total := 0
	for {
		rows, err := l.DB.Query(ctx, `
			SELECT id, source, login, account, kind, params, status, fetched_at, body
			FROM intel.answer WHERE loaded_at IS NULL ORDER BY fetched_at, id LIMIT 100`)
		if err != nil {
			return total, err
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (answer, error) {
			var a answer
			err := r.Scan(&a.ID, &a.Source, &a.Login, &a.Account, &a.Kind, &a.Params, &a.Status, &a.FetchedAt, &a.Body)
			return a, err
		})
		if err != nil {
			return total, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		for _, a := range batch {
			if err := l.one(ctx, a); err != nil {
				return total, err
			}
			total++
		}
	}
}

// Reload marks the answers fetched in [from, to) as not loaded, then loads
// them again.
func (l *Loader) Reload(ctx context.Context, from, to time.Time) (int, error) {
	if _, err := l.DB.Exec(ctx, `UPDATE intel.answer SET loaded_at = NULL, load_error = NULL
		WHERE fetched_at >= $1 AND fetched_at < $2`, from, to); err != nil {
		return 0, err
	}
	return l.Pending(ctx)
}

// one loads an answer in a transaction. A body that cannot be read is
// recorded as the answer's load error; the answer is kept and a fixed
// parser can reload it.
func (l *Loader) one(ctx context.Context, a answer) error {
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	loadErr := ""
	if a.Status >= 200 && a.Status < 300 {
		body, perr := gunzip(a.Body)
		if perr == nil {
			perr = parse(ctx, tx, a, body)
		}
		if perr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Undo what this answer wrote, keep the error.
			if err := tx.Rollback(ctx); err != nil {
				return err
			}
			if tx, err = l.DB.Begin(ctx); err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			loadErr = perr.Error()
			l.Log.Warn("answer not loaded", "answer", a.ID, "kind", a.Kind, "err", loadErr)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE intel.answer SET loaded_at = now(), load_error = NULLIF($2, '') WHERE id = $1`, a.ID, loadErr); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func parse(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	switch a.Kind {
	case "taboola.accounts":
		return tbAccounts(ctx, tx, a, body)
	case "taboola.campaigns":
		return tbCampaigns(ctx, tx, a, body)
	case "taboola.groups":
		return tbGroups(ctx, tx, a, body)
	case "taboola.items":
		return tbItems(ctx, tx, a, body)
	case "taboola.campaign_day":
		return tbCampaignDay(ctx, tx, a, body)
	case "taboola.site_day":
		return tbSiteDay(ctx, tx, a, body)
	case "taboola.item_day":
		return tbItemDay(ctx, tx, a, body)
	case "taboola.bucket":
		return tbBucket(ctx, tx, a, body)
	case "redtrack.item_day", "redtrack.site_day", "redtrack.campaign_hour":
		return rtReport(ctx, tx, a, body)
	case "redtrack.conversions":
		return rtConversions(ctx, tx, a, body)
	case "taboola.history":
		// Kept raw for now: Launch keeps the history of what it does, and
		// the dashboard's own changes are read from here when the trial
		// starts.
		return nil
	}
	return fmt.Errorf("no parser for kind %q", a.Kind)
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// row is one JSON object with numbers kept as written.
type row map[string]any

func decodeRows(body []byte, key string) ([]row, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if key == "" {
		var rows []row
		// RedTrack sends a bare array, or null when there are none.
		if err := dec.Decode(&rows); err != nil {
			return nil, err
		}
		return rows, nil
	}
	var wrapped map[string]json.RawMessage
	if err := dec.Decode(&wrapped); err != nil {
		return nil, err
	}
	raw, ok := wrapped[key]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	d2 := json.NewDecoder(bytes.NewReader(raw))
	d2.UseNumber()
	var rows []row
	if err := d2.Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// str reads a field as text; numbers as written, null and missing as "".
func (r row) str(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// num reads a field as a number; a missing, null or odd one is 0 (strings
// holding numbers are read too: RedTrack writes some that way).
func (r row) num(k string) float64 {
	s := r.str(k)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func (r row) int(k string) int64 { return int64(r.num(k) + 0.5*sign(r.num(k))) }

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// id reads a numeric id; ok is false when there is none.
func (r row) id(k string) (int64, bool) {
	s := strings.TrimSpace(r.str(k))
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func (r row) bool(k string) bool {
	v, _ := r[k].(bool)
	return v
}

// param reads one of the answer's params as text.
func (a answer) param(k string) string {
	switch v := a.Params[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// day reads a day param (2006-01-02).
func (a answer) day(k string) (time.Time, error) {
	return time.Parse(time.DateOnly, a.param(k))
}

// reportDay reads Taboola's "2026-09-12 00:00:00.0" as its day.
func reportDay(s string) (time.Time, error) {
	if len(s) < 10 {
		return time.Time{}, fmt.Errorf("bad date %q", s)
	}
	return time.Parse(time.DateOnly, s[:10])
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
