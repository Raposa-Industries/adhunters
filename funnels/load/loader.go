package load

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/shared/spool"
)

// Marker is the suffix of the empty file that says a raw file is archived.
const Marker = ".archived"

// maxAttempts is how often a file may fail to load before it is set aside.
const maxAttempts = 3

// Config is one loader.
type Config struct {
	DB    *pgxpool.Pool
	Store archive.Store
	// Spool is the edge's spool on this box. Empty when another box ships.
	Spool string
	// KeepFor is how long an archived file stays in the spool.
	KeepFor time.Duration
	// CloseAfter is how long after an hour ends its journeys are counted:
	// long enough for most of them to have ended.
	CloseAfter time.Duration
	Log        *slog.Logger
	Metrics    *Metrics
	Now        func() time.Time
}

// Metrics are the loader's live signals.
type Metrics struct {
	Files       *prometheus.CounterVec // by outcome: archived, loaded, failed, quarantined
	Events      prometheus.Counter
	Beacons     *prometheus.CounterVec // by outcome: parsed, bad
	Hours       prometheus.Counter
	Pending     prometheus.Gauge
	Quarantined prometheus.Gauge
	Waiting     prometheus.Gauge // sealed files in the spool not archived yet
}

// NewMetrics registers the loader's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Files: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "funnels_loader_files_total", Help: "Raw files handled, by outcome (archived, loaded, failed, quarantined).",
		}, []string{"outcome"}),
		Events: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "funnels_loader_events_total", Help: "Page events loaded.",
		}),
		Beacons: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "funnels_loader_beacons_total", Help: "Beacons loaded, by outcome (parsed, bad); bad ones stay in the archive.",
		}, []string{"outcome"}),
		Hours: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "funnels_loader_hours_closed_total", Help: "Hours whose journeys and counts were computed.",
		}),
		Pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "funnels_loader_files_pending", Help: "Archived raw files not loaded yet.",
		}),
		Quarantined: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "funnels_loader_files_quarantined", Help: "Raw files set aside until a fix and a replay.",
		}),
		Waiting: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "funnels_loader_files_waiting", Help: "Sealed raw files in the spool not archived yet.",
		}),
	}
	reg.MustRegister(m.Files, m.Events, m.Beacons, m.Hours, m.Pending, m.Quarantined, m.Waiting)
	return m
}

// Loader does the work; each step is safe to repeat.
type Loader struct{ c Config }

// New makes a loader.
func New(c Config) *Loader {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.KeepFor == 0 {
		c.KeepFor = 48 * time.Hour
	}
	if c.CloseAfter == 0 {
		c.CloseAfter = time.Hour
	}
	return &Loader{c: c}
}

// Archive uploads each sealed file in the spool not archived yet, verifies
// it, records it in funnels.raw_file and leaves a marker beside it. Files
// archived longer ago than KeepFor are deleted. It returns how many it
// archived.
func (l *Loader) Archive(ctx context.Context) (int, error) {
	if l.c.Spool == "" {
		return 0, nil
	}
	var sealed []string
	now := l.c.Now()
	err := filepath.WalkDir(l.c.Spool, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(p, ".ndjson.zst"):
			if _, err := os.Stat(p + Marker); err != nil {
				sealed = append(sealed, p)
			}
		case strings.HasSuffix(p, ".ndjson.zst"+Marker):
			if st, err := d.Info(); err == nil && now.Sub(st.ModTime()) > l.c.KeepFor {
				_ = os.Remove(strings.TrimSuffix(p, Marker))
				_ = os.Remove(p)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Strings(sealed)
	if l.c.Metrics != nil {
		l.c.Metrics.Waiting.Set(float64(len(sealed)))
	}
	n := 0
	for _, p := range sealed {
		if err := l.archiveOne(ctx, p); err != nil {
			l.count("failed")
			return n, fmt.Errorf("archiving %s: %w", p, err)
		}
		n++
		l.count("archived")
		if l.c.Metrics != nil {
			l.c.Metrics.Waiting.Dec()
		}
	}
	return n, nil
}

func (l *Loader) archiveOne(ctx context.Context, p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(l.c.Spool, p)
	if err != nil {
		return err
	}
	key := filepath.ToSlash(rel)
	minute, err := minuteOf(key)
	if err != nil {
		return err
	}
	rows, err := countLines(b)
	if err != nil {
		return err
	}
	m := md5.Sum(b)
	s := sha256.Sum256(b)
	if err := l.c.Store.Put(ctx, key, bytes.NewReader(b), int64(len(b)), hex.EncodeToString(m[:])); err != nil {
		return err
	}
	_, err = l.c.DB.Exec(ctx, `
		INSERT INTO funnels.raw_file (key, minute, rows, bytes, sha256) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (key) DO NOTHING`, key, minute, rows, len(b), hex.EncodeToString(s[:]))
	if err != nil {
		return err
	}
	return os.WriteFile(p+Marker, nil, 0o640)
}

// minuteOf reads the minute from a key: events/2026/09/30/14/edge-a-1407[-2].ndjson.zst.
func minuteOf(key string) (time.Time, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 6 || parts[0] != edge.Stream {
		return time.Time{}, fmt.Errorf("unexpected raw file key %q", key)
	}
	name := strings.TrimSuffix(parts[5], ".ndjson.zst")
	fields := strings.Split(name, "-")
	if len(fields) < 3 {
		return time.Time{}, fmt.Errorf("unexpected raw file name %q", parts[5])
	}
	hhmm := fields[2]
	return time.Parse("2006/01/02/1504", strings.Join(parts[1:4], "/")+"/"+hhmm)
}

func countLines(zst []byte) (int, error) {
	dec, err := zstd.NewReader(bytes.NewReader(zst))
	if err != nil {
		return 0, err
	}
	defer dec.Close()
	n := 0
	buf := make([]byte, 64<<10)
	for {
		k, err := dec.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// LoadOne loads the oldest pending raw file. It returns false when none
// waits.
func (l *Loader) LoadOne(ctx context.Context) (bool, error) {
	tx, err := l.c.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var key, sum string
	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT key, sha256, attempts FROM funnels.raw_file
		WHERE loaded_at IS NULL AND quarantined_at IS NULL
		ORDER BY minute, key LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&key, &sum, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	events, beacons, bad, err := l.read(ctx, key, sum)
	if err == nil {
		err = l.write(ctx, tx, key, events, bad)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return true, l.failed(ctx, key, attempts, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return true, err
	}
	l.count("loaded")
	if l.c.Metrics != nil {
		l.c.Metrics.Events.Add(float64(len(events)))
		l.c.Metrics.Beacons.WithLabelValues("parsed").Add(float64(beacons - bad))
		l.c.Metrics.Beacons.WithLabelValues("bad").Add(float64(bad))
	}
	return true, nil
}

// errForever marks a failure loading again cannot fix.
type errForever struct{ error }

// read returns a file's events, how many beacons it held and how many of
// them did not parse.
func (l *Loader) read(ctx context.Context, key, sum string) ([]Event, int, int, error) {
	rc, err := l.c.Store.Get(ctx, key)
	if err != nil {
		return nil, 0, 0, err
	}
	b, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return nil, 0, 0, err
	}
	if s := sha256.Sum256(b); hex.EncodeToString(s[:]) != sum {
		return nil, 0, 0, errForever{fmt.Errorf("sha256 does not match the one recorded")}
	}
	var out []Event
	bad, n := 0, 0
	err = spool.ReadLines(bytes.NewReader(b), func(line int, text []byte) error {
		n = line
		evs, err := Parse(text, line)
		if err != nil {
			bad++
			return nil
		}
		out = append(out, evs...)
		return nil
	})
	if err != nil {
		return nil, 0, 0, errForever{err}
	}
	return out, n, bad, nil
}

func (l *Loader) write(ctx context.Context, tx pgx.Tx, key string, events []Event, bad int) error {
	// Journeys of the file's earlier load and of this one: their hours change.
	var journeys []string
	rows, err := tx.Query(ctx, `SELECT DISTINCT journey FROM funnels.event WHERE file_key = $1`, key)
	if err != nil {
		return err
	}
	for rows.Next() {
		var j string
		if err := rows.Scan(&j); err != nil {
			return err
		}
		journeys = append(journeys, j)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM funnels.event WHERE file_key = $1`, key); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, j := range journeys {
		seen[j] = true
	}
	src := make([][]any, 0, len(events))
	for _, e := range events {
		if !seen[e.Journey] {
			seen[e.Journey] = true
			journeys = append(journeys, e.Journey)
		}
		subs, _ := json.Marshal(e.Subs)
		src = append(src, []any{key, e.Line, e.N, e.ReceivedAt, e.SentAt, e.Journey, e.Site, e.LP, e.URL, e.Kind,
			e.ClickID, subs, e.UA, e.Country, e.IPHash, e.Net, e.Webdriver, e.HadInput, e.ScreenW, []byte(e.Data)})
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"funnels", "event"},
		[]string{"file_key", "line", "n", "received_at", "sent_at", "journey", "site", "lp", "url", "kind",
			"clickid", "subs", "ua", "country", "ip_hash", "net", "webdriver", "had_input", "screen_w", "data"},
		pgx.CopyFromRows(src))
	if err != nil {
		return err
	}
	// A journey belongs to the hour of its first event; a journey whose
	// events are all gone leaves its old hour, which must close again too.
	_, err = tx.Exec(ctx, `
		INSERT INTO funnels.hour_state (hour, dirty_since)
		SELECT DISTINCT h, now() FROM (
		    SELECT date_trunc('hour', min(received_at)) AS h FROM funnels.event
		    WHERE journey = ANY($1) GROUP BY journey
		    UNION
		    SELECT hour FROM funnels.journey WHERE id = ANY($1)
		) x
		ON CONFLICT (hour) DO UPDATE SET dirty_since = COALESCE(funnels.hour_state.dirty_since, now())`, journeys)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE funnels.raw_file SET loaded_at = now(), events = $2, bad_beacons = $3, last_error = NULL
		WHERE key = $1`, key, len(events), bad)
	return err
}

func (l *Loader) failed(ctx context.Context, key string, attempts int, cause error) error {
	var forever errForever
	quarantine := errors.As(cause, &forever) || attempts+1 >= maxAttempts
	_, err := l.c.DB.Exec(ctx, `
		UPDATE funnels.raw_file SET attempts = attempts + 1, last_error = $2,
		       quarantined_at = CASE WHEN $3 THEN now() END
		WHERE key = $1`, key, cause.Error(), quarantine)
	if quarantine {
		l.count("quarantined")
		l.c.Log.Error("raw file quarantined; fix the cause, then replay its range", "key", key, "err", cause)
	} else {
		l.count("failed")
		l.c.Log.Warn("raw file failed to load; it will be tried again", "key", key, "attempt", attempts+1, "err", cause)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("loading %s: %w", key, cause)
}

// CloseDue closes every dirty hour that is due: CloseAfter past its end,
// with every raw file that can hold its journeys' events loaded. It returns
// how many it closed.
func (l *Loader) CloseDue(ctx context.Context) (int, error) {
	rows, err := l.c.DB.Query(ctx, `
		SELECT h.hour FROM funnels.hour_state h
		WHERE h.dirty_since IS NOT NULL AND h.hour + interval '1 hour' + $1::interval <= $2
		  AND NOT EXISTS (
		      SELECT 1 FROM funnels.raw_file f
		      WHERE f.loaded_at IS NULL AND f.quarantined_at IS NULL AND f.minute >= h.hour
		        AND f.minute < h.hour + interval '1 hour' + $1::interval)
		ORDER BY h.hour`, l.c.CloseAfter, l.c.Now())
	if err != nil {
		return 0, err
	}
	hours, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return 0, err
	}
	for i, h := range hours {
		if err := l.CloseHour(ctx, h); err != nil {
			return i, fmt.Errorf("closing %s: %w", h.UTC().Format(time.RFC3339), err)
		}
	}
	return len(hours), nil
}

// CloseHour rebuilds the journeys that started in hour and its counts.
func (l *Loader) CloseHour(ctx context.Context, hour time.Time) error {
	hour = hour.UTC()
	tx, err := l.c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The row lock holds back a load that would make the hour dirty until
	// this commits; that load then makes it dirty again.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM funnels.hour_state WHERE hour = $1 FOR UPDATE`, hour); err != nil {
		return err
	}
	journeys, err := l.journeysOf(ctx, tx, hour)
	if err != nil {
		return err
	}
	// A journey whose first event moved to this hour (a late file) is still
	// stored under its old hour until then; that hour is dirty too.
	ids := make([]string, len(journeys))
	for i, j := range journeys {
		ids[i] = j.ID
	}
	if _, err := tx.Exec(ctx, `DELETE FROM funnels.journey WHERE hour = $1 OR id = ANY($2)`, hour, ids); err != nil {
		return err
	}
	if err := copyJourneys(ctx, tx, journeys); err != nil {
		return err
	}
	for _, t := range []string{"journey_hourly", "step_hourly", "video_hourly", "video_second_hourly"} {
		if _, err := tx.Exec(ctx, `DELETE FROM funnels.`+t+` WHERE hour = $1`, hour); err != nil {
			return err
		}
	}
	for _, q := range countQueries {
		if _, err := tx.Exec(ctx, q, hour); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO funnels.hour_state (hour, closed_at, journeys) VALUES ($1, now(), $2)
		ON CONFLICT (hour) DO UPDATE SET closed_at = now(), journeys = $2, dirty_since = NULL`, hour, len(journeys))
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if l.c.Metrics != nil {
		l.c.Metrics.Hours.Inc()
	}
	l.c.Log.Info("hour closed", "hour", hour.Format(time.RFC3339), "journeys", len(journeys))
	return nil
}

// journeysOf rebuilds the journeys whose first event is in hour, from every
// event they have, whenever it arrived.
func (l *Loader) journeysOf(ctx context.Context, tx pgx.Tx, hour time.Time) ([]Journey, error) {
	rows, err := tx.Query(ctx, `
		WITH c AS (
		    SELECT DISTINCT journey FROM funnels.event
		    WHERE received_at >= $1 AND received_at < $1 + interval '1 hour'),
		j AS (
		    SELECT journey FROM c WHERE NOT EXISTS (
		        SELECT 1 FROM funnels.event b WHERE b.journey = c.journey AND b.received_at < $1))
		SELECT e.line, e.n, e.received_at, e.sent_at, e.journey, e.site, e.lp, e.url, e.kind, e.clickid, e.subs,
		       e.ua, e.country, e.ip_hash, e.net, e.webdriver, e.had_input, e.screen_w, e.data
		FROM funnels.event e JOIN j USING (journey)
		ORDER BY e.journey, e.received_at, e.file_key, e.line, e.n`, hour)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Journey
	var cur []Event
	for rows.Next() {
		var e Event
		var subs []byte
		if err := rows.Scan(&e.Line, &e.N, &e.ReceivedAt, &e.SentAt, &e.Journey, &e.Site, &e.LP, &e.URL, &e.Kind,
			&e.ClickID, &subs, &e.UA, &e.Country, &e.IPHash, &e.Net, &e.Webdriver, &e.HadInput, &e.ScreenW, &e.Data); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(subs, &e.Subs)
		e.ReceivedAt = e.ReceivedAt.UTC()
		if len(cur) > 0 && cur[0].Journey != e.Journey {
			out = append(out, Build(cur))
			cur = nil
		}
		cur = append(cur, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cur) > 0 {
		out = append(out, Build(cur))
	}
	return out, nil
}

func copyJourneys(ctx context.Context, tx pgx.Tx, js []Journey) error {
	var jr, sr, vr [][]any
	for _, j := range js {
		subs, _ := json.Marshal(j.Subs)
		jr = append(jr, []any{j.ID, j.Hour, j.StartedAt, j.LastAt, j.Site, j.FirstLP, j.LastLP, j.LastStep, j.LPs,
			j.ClickID, j.Subs["sub1"], j.Subs["sub4"], j.Subs["sub8"], subs, j.Device, j.Country, j.VisibleMS,
			j.MaxScroll, j.HadInput, j.BotSuspect, j.BotReason})
		for _, s := range j.Steps {
			sr = append(sr, []any{j.ID, s.Seq, s.LP, s.Step, s.At})
		}
		for _, v := range j.Videos {
			mr := make(pgtype.Multirange[pgtype.Range[pgtype.Int4]], 0, len(v.Watched))
			for _, r := range v.Watched {
				mr = append(mr, pgtype.Range[pgtype.Int4]{
					Lower: pgtype.Int4{Int32: int32(r[0]), Valid: true}, Upper: pgtype.Int4{Int32: int32(r[1]), Valid: true},
					LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true,
				})
			}
			vr = append(vr, []any{j.ID, v.Video, v.Arm, v.LP, v.LenS, v.PitchS, v.Autoplayed, v.Played, mr,
				v.WatchedS, v.LastS, v.ReachedPitch})
		}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"funnels", "journey"},
		[]string{"id", "hour", "started_at", "last_at", "site", "first_lp", "last_lp", "last_step", "lps", "clickid",
			"sub1", "sub4", "sub8", "subs", "device", "country", "visible_ms", "max_scroll", "had_input", "bot_suspect", "bot_reason"},
		pgx.CopyFromRows(jr)); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"funnels", "journey_step"},
		[]string{"journey", "seq", "lp", "step", "at"}, pgx.CopyFromRows(sr)); err != nil {
		return err
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"funnels", "journey_video"},
		[]string{"journey", "video", "arm", "lp", "len_s", "pitch_s", "autoplayed", "played", "watched", "watched_s",
			"last_s", "reached_pitch"}, pgx.CopyFromRows(vr))
	return err
}

// countQueries compute one closed hour's counts from its journeys ($1).
var countQueries = []string{
	`INSERT INTO funnels.journey_hourly (hour, site, first_lp, sub1, sub4, sub8, device, country, journeys,
	     with_click_id, with_input, visible_ms, bots)
	 SELECT hour, site, first_lp, sub1, sub4, sub8, device, country,
	        count(*) FILTER (WHERE NOT bot_suspect),
	        count(*) FILTER (WHERE NOT bot_suspect AND clickid <> ''),
	        count(*) FILTER (WHERE NOT bot_suspect AND had_input),
	        COALESCE(sum(visible_ms) FILTER (WHERE NOT bot_suspect), 0),
	        count(*) FILTER (WHERE bot_suspect)
	 FROM funnels.journey WHERE hour = $1
	 GROUP BY hour, site, first_lp, sub1, sub4, sub8, device, country`,

	`INSERT INTO funnels.step_hourly (hour, site, lp, step, sub1, sub4, sub8, device, reached, stopped, bots)
	 SELECT j.hour, j.site, s.lp, s.step, j.sub1, j.sub4, j.sub8, j.device,
	        count(*) FILTER (WHERE NOT j.bot_suspect),
	        count(*) FILTER (WHERE NOT j.bot_suspect AND s.seq = last.seq),
	        count(*) FILTER (WHERE j.bot_suspect)
	 FROM funnels.journey j
	 JOIN funnels.journey_step s ON s.journey = j.id
	 JOIN LATERAL (SELECT max(seq) AS seq FROM funnels.journey_step WHERE journey = j.id) last ON true
	 WHERE j.hour = $1
	 GROUP BY j.hour, j.site, s.lp, s.step, j.sub1, j.sub4, j.sub8, j.device`,

	`INSERT INTO funnels.video_hourly (hour, site, video, arm, sub1, sub4, sub8, device, loads, autoplays, plays,
	     watched_s, len_s, reached_pitch)
	 SELECT j.hour, j.site, v.video, v.arm, j.sub1, j.sub4, j.sub8, j.device,
	        count(*), count(*) FILTER (WHERE v.autoplayed), count(*) FILTER (WHERE v.played),
	        COALESCE(sum(v.watched_s), 0), max(v.len_s), count(*) FILTER (WHERE v.reached_pitch)
	 FROM funnels.journey j JOIN funnels.journey_video v ON v.journey = j.id
	 WHERE j.hour = $1 AND NOT j.bot_suspect
	 GROUP BY j.hour, j.site, v.video, v.arm, j.sub1, j.sub4, j.sub8, j.device`,

	`INSERT INTO funnels.video_second_hourly (hour, video, arm, device, second, watching)
	 SELECT j.hour, v.video, v.arm, j.device, s, count(*)
	 FROM funnels.journey j
	 JOIN funnels.journey_video v ON v.journey = j.id
	 CROSS JOIN LATERAL generate_series(0, v.last_s) s
	 WHERE j.hour = $1 AND NOT j.bot_suspect AND v.played AND v.last_s >= 0 AND v.watched @> s
	 GROUP BY j.hour, v.video, v.arm, j.device, s`,
}

// Replay marks the raw files of a range pending again, so the running
// loader loads them again and their hours close again. Quarantined files
// in the range get another chance. It returns how many files it marked.
func (l *Loader) Replay(ctx context.Context, from, to time.Time) (int64, error) {
	tag, err := l.c.DB.Exec(ctx, `
		UPDATE funnels.raw_file SET loaded_at = NULL, quarantined_at = NULL, attempts = 0, last_error = NULL
		WHERE minute >= $1 AND minute < $2`, from, to)
	return tag.RowsAffected(), err
}

// Status is where loading stands.
type Status struct {
	Pending, Quarantined int
	DirtyHours           int
	LastClosedHour       *time.Time
}

// Status reads where loading stands, and sets the pending and quarantined
// gauges.
func (l *Loader) Status(ctx context.Context) (Status, error) {
	var s Status
	err := l.c.DB.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE loaded_at IS NULL AND quarantined_at IS NULL),
		       count(*) FILTER (WHERE quarantined_at IS NOT NULL),
		       (SELECT count(*) FROM funnels.hour_state WHERE dirty_since IS NOT NULL),
		       (SELECT max(hour) FROM funnels.hour_state WHERE closed_at IS NOT NULL AND dirty_since IS NULL)
		FROM funnels.raw_file`).Scan(&s.Pending, &s.Quarantined, &s.DirtyHours, &s.LastClosedHour)
	if err == nil && l.c.Metrics != nil {
		l.c.Metrics.Pending.Set(float64(s.Pending))
		l.c.Metrics.Quarantined.Set(float64(s.Quarantined))
	}
	return s, err
}

func (l *Loader) count(outcome string) {
	if l.c.Metrics != nil {
		l.c.Metrics.Files.WithLabelValues(outcome).Inc()
	}
}
