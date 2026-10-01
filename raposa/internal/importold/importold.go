// Package importold copies the investigations the collector's Raposa made
// (spy.raposa_job and the tables under it, adhunters-collector e20148c and
// its migrations 043-045) into the raposa schema, with their visits, steps,
// variants, pages, the files those pages kept, the screenshots of their dark
// verdicts and their log. What raposa has no column for (the verdict rule
// and its evidence, the rounds of continuous sampling, a visit's mismatch)
// goes into the investigation's log as it was, so nothing in a job is lost.
//
// It only reads the collector's database. Each job is copied in one
// transaction that also records it in raposa.imported_investigation, so a
// job is copied whole or not at all, and running the import again copies
// only what is missing. A job is copied only once Tracks knows its creative
// (by creative_key); the others are counted and wait for a later run.
package importold

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/raposa/internal/engine"
	"github.com/Raposa-Industries/adhunters/raposa/internal/pagever"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// Config is what one import needs.
type Config struct {
	Old      *pgxpool.Pool // the collector's database; only read
	New      *pgxpool.Pool // the database raposa lives in
	OldFiles files.Store   // the collector's object storage (raposa/files/<md5>); nil when it had none
	Files    files.Store   // where Raposa keeps files now
	TmpDir   string        // where a file waits between the two stores
	DryRun   bool          // read and check everything, write nothing
	Log      *slog.Logger
}

// Report is what one import did.
type Report struct {
	Jobs            int // jobs in the collector's database
	AlreadyCopied   int
	Copied          int
	UnknownCreative int      // Tracks does not know the creative yet
	UnknownKeys     []string // the first of their creative keys
	Failed          int
	Errors          []string // one line per failed job
	Visits          int
	PagesNew        int // pages copied; the others were in raposa already
	Files           int // files copied into the files store
	FileBytes       int64
	SettingsDiffer  []string // settings whose value differs, for a person to look at
}

// job is one spy.raposa_job row.
type job struct {
	id                                     int32
	uid                                    *[16]byte
	creativeKey                            string
	headline                               *string
	status, mode, origin                   string
	clickURL, referer, device, stage, note string
	stopRequested, cloaked                 bool
	rungReached                            int16
	breachRung                             *int16
	whitePage                              *int32
	visitsTarget, visitsDone               int32
	variants                               int16
	bytesUsed                              int64
	confidence                             float64
	reviewerData, raposaData, checkoutData []byte
	retryOf                                *int32
	attempt                                int16
	requestedAt                            time.Time
	startedAt, completedAt                 *time.Time
	creativeID                             int32
	adID                                   *int32
}

// Run copies every job not copied yet.
func Run(ctx context.Context, cfg Config) (Report, error) {
	var r Report
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := settingsDiffer(ctx, cfg, &r); err != nil {
		return r, err
	}
	done := map[int32]bool{}
	if err := collect(ctx, cfg.New, `SELECT old_id FROM raposa.imported_investigation`, func(rows pgx.Rows) error {
		var id int32
		err := rows.Scan(&id)
		done[id] = true
		return err
	}); err != nil {
		return r, err
	}
	jobs, err := readJobs(ctx, cfg.Old)
	if err != nil {
		return r, err
	}
	r.Jobs = len(jobs)
	creatives, err := creativeIDs(ctx, cfg.New, jobs)
	if err != nil {
		return r, err
	}
	disguises, codes, err := disguiseMap(ctx, cfg)
	if err != nil {
		return r, err
	}
	// The collector's migrations 043 and 045 added these; an older
	// collector has neither.
	var hasWindows, hasShots bool
	if err := cfg.Old.QueryRow(ctx, `SELECT to_regclass('spy.raposa_window') IS NOT NULL,
		to_regclass('spy.raposa_shot') IS NOT NULL`).Scan(&hasWindows, &hasShots); err != nil {
		return r, err
	}
	cfg.Log.Info("import: jobs read", "jobs", len(jobs), "copied before", len(done))

	for _, j := range jobs {
		if done[j.id] {
			r.AlreadyCopied++
			continue
		}
		id, ok := creatives[j.creativeKey]
		if !ok {
			r.UnknownCreative++
			if len(r.UnknownKeys) < 20 {
				r.UnknownKeys = append(r.UnknownKeys, j.creativeKey)
			}
			continue
		}
		j.creativeID = id
		c := &copier{cfg: cfg, r: &r, disguises: disguises, codes: codes, hasWindows: hasWindows, hasShots: hasShots,
			pages: map[int32]int32{}, visitIDs: map[int32]visitRef{}}
		if err := c.copyJob(ctx, j); err != nil {
			if ctx.Err() != nil {
				return r, ctx.Err()
			}
			r.Failed++
			r.Errors = append(r.Errors, fmt.Sprintf("job %d: %v", j.id, err))
			cfg.Log.Error("import: job not copied", "job", j.id, "err", err)
			continue
		}
		r.Copied++
		if r.Copied%100 == 0 {
			cfg.Log.Info("import: progress", "copied", r.Copied, "of", len(jobs))
		}
	}
	return r, nil
}

func collect(ctx context.Context, db *pgxpool.Pool, sql string, each func(pgx.Rows) error, args ...any) error {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// settingsDiffer lists the settings the collector had at another value, so a
// person can decide whether to carry them over. Nothing is changed.
func settingsDiffer(ctx context.Context, cfg Config, r *Report) error {
	now := map[string]string{}
	if err := collect(ctx, cfg.New, `SELECT key, value FROM raposa.setting`, func(rows pgx.Rows) error {
		var k, v string
		err := rows.Scan(&k, &v)
		now[k] = v
		return err
	}); err != nil {
		return err
	}
	return collect(ctx, cfg.Old, `SELECT key, value FROM spy.raposa_setting ORDER BY key`, func(rows pgx.Rows) error {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if cur, ok := now[k]; ok && cur != v {
			r.SettingsDiffer = append(r.SettingsDiffer, fmt.Sprintf("%s: collector %q, raposa %q", k, v, cur))
		}
		return nil
	})
}

func readJobs(ctx context.Context, db *pgxpool.Pool) ([]job, error) {
	var out []job
	err := collect(ctx, db, `
		SELECT j.id, j.uid, c.creative_key, a.headline, j.status, j.mode, j.origin,
		       j.target_click_url, j.publisher_referer, j.target_device, j.stage, j.stage_note,
		       j.stop_requested, j.is_cloaked, j.rung_reached, j.breach_rung, j.white_page_id,
		       j.visits_target, j.visits_done, j.variants_count, j.bytes_used, j.cloaked_confidence::float8,
		       j.reviewer_data, j.raposa_data, j.checkout_data, j.retry_of, j.attempt,
		       j.requested_at, j.started_at, j.completed_at
		FROM spy.raposa_job j
		JOIN spy.creative c ON c.id = j.creative_id
		LEFT JOIN spy.ad a ON a.id = j.ad_id
		ORDER BY j.id`, func(rows pgx.Rows) error {
		var j job
		var uid *[16]byte
		err := rows.Scan(&j.id, &uid, &j.creativeKey, &j.headline, &j.status, &j.mode, &j.origin,
			&j.clickURL, &j.referer, &j.device, &j.stage, &j.note,
			&j.stopRequested, &j.cloaked, &j.rungReached, &j.breachRung, &j.whitePage,
			&j.visitsTarget, &j.visitsDone, &j.variants, &j.bytesUsed, &j.confidence,
			&j.reviewerData, &j.raposaData, &j.checkoutData, &j.retryOf, &j.attempt,
			&j.requestedAt, &j.startedAt, &j.completedAt)
		j.uid = uid
		out = append(out, j)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read the collector's jobs: %w", err)
	}
	return out, nil
}

// creativeIDs maps the jobs' creative keys to Tracks' creative ids.
func creativeIDs(ctx context.Context, db *pgxpool.Pool, jobs []job) (map[string]int32, error) {
	seen := map[string]bool{}
	var keys []string
	for _, j := range jobs {
		if !seen[j.creativeKey] {
			seen[j.creativeKey] = true
			keys = append(keys, j.creativeKey)
		}
	}
	out := map[string]int32{}
	err := collect(ctx, db, `SELECT creative_key, id FROM tracks_api.creative_v1 WHERE creative_key = ANY($1)`, func(rows pgx.Rows) error {
		var k string
		var id int32
		err := rows.Scan(&k, &id)
		out[k] = id
		return err
	}, keys)
	if err != nil {
		return nil, fmt.Errorf("read the creatives from Tracks: %w", err)
	}
	return out, nil
}

// disguiseMap maps the collector's disguise ids to raposa's, by code, and
// returns the collector's codes too.
func disguiseMap(ctx context.Context, cfg Config) (map[int16]int16, map[int16]string, error) {
	byCode := map[string]int16{}
	if err := collect(ctx, cfg.New, `SELECT code, id FROM raposa.disguise`, func(rows pgx.Rows) error {
		var code string
		var id int16
		err := rows.Scan(&code, &id)
		byCode[code] = id
		return err
	}); err != nil {
		return nil, nil, err
	}
	out, codes := map[int16]int16{}, map[int16]string{}
	err := collect(ctx, cfg.Old, `SELECT id, code FROM spy.raposa_disguise`, func(rows pgx.Rows) error {
		var id int16
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return err
		}
		codes[id] = code
		if n, ok := byCode[code]; ok {
			out[id] = n
		}
		return nil
	})
	return out, codes, err
}

// status maps the collector's status to raposa's, with the stage and note a
// job that did not end gets. A job the collector was running is stopped: its
// state was in the collector's memory, not in the row.
func status(j job) (st, stage, note string, ended bool) {
	switch j.status {
	case "COMPLETED":
		return "completed", j.stage, j.note, true
	case "FAILED":
		return "failed", j.stage, j.note, true
	case "STOPPED":
		return "stopped", j.stage, j.note, true
	case "PENDING":
		if j.stopRequested {
			return "stopped", "done", "stopped before it started", true
		}
		return "waiting", "queued", j.note, false
	case "CLAIMED", "RUNNING":
		return "stopped", "done", "stopped by the move from the collector: it was running there", true
	}
	return "failed", "done", fmt.Sprintf("the collector left it %s", j.status), true
}

type copier struct {
	cfg                  Config
	r                    *Report
	disguises            map[int16]int16
	codes                map[int16]string // the collector's disguise codes
	hasWindows, hasShots bool             // the collector has spy.raposa_window, spy.raposa_shot
	pages                map[int32]int32  // the collector's page id to raposa's
	visitIDs             map[int32]visitRef
	white                *int32    // the job's white page here
	notes                []logLine // what raposa has no column for, for the log
	tx                   pgx.Tx
	visits               int
	pagesNew             int
	files                int
	fileBytes            int64
}

// visitRef is one copied visit: its id here and the page it landed on here.
type visitRef struct {
	id     int64
	landed *int32
}

type logLine struct {
	at   time.Time
	line string
}

func (c *copier) note(at time.Time, line string) {
	c.notes = append(c.notes, logLine{at, line})
}

func (c *copier) copyJob(ctx context.Context, j job) error {
	tx, err := c.cfg.New.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	c.tx = tx

	if j.headline != nil {
		var ad int32
		err := tx.QueryRow(ctx, `SELECT id FROM tracks_api.ad_v1 WHERE creative_id = $1 AND headline = $2 ORDER BY id LIMIT 1`,
			j.creativeID, *j.headline).Scan(&ad)
		if err == nil {
			j.adID = &ad
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read the ad from Tracks: %w", err)
		}
	}
	var retryOf *int64
	if j.retryOf != nil {
		var id int64
		err := tx.QueryRow(ctx, `SELECT investigation_id FROM raposa.imported_investigation WHERE old_id = $1`, *j.retryOf).Scan(&id)
		if err == nil {
			retryOf = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var white *int32
	if j.whitePage != nil {
		id, err := c.page(ctx, *j.whitePage)
		if err != nil {
			return err
		}
		white = &id
	}
	c.white = white

	st, stage, note, ended := status(j)
	completed := j.completedAt
	if ended && completed == nil {
		t := time.Now()
		completed = &t
	}
	next := j.requestedAt // a paced retry waits until then
	if next.Before(time.Now()) {
		next = time.Now()
	}
	var host string
	if u := strings.TrimSpace(j.clickURL); u != "" {
		host = hostOf(u)
	}
	var inv int64
	err = tx.QueryRow(ctx, `
		INSERT INTO raposa.investigation (creative_id, ad_id, mode, origin, requested_by, requested_at, status,
		    stop_requested, target_click_url, publisher_referer, target_device, burn_scope, stage, stage_note,
		    rung_reached, breach_rung, white_page_id, visits_target, visits_done, variants_count, bytes_used,
		    is_cloaked, cloaked_confidence, reviewer_data, raposa_data, checkout_data, retry_of, attempt,
		    next_visit_at, started_at, completed_at)
		VALUES ($1, $2, $3, $4, 'collector', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
		    $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30)
		RETURNING id`,
		j.creativeID, j.adID, j.mode, j.origin, j.requestedAt, st,
		j.stopRequested, j.clickURL, j.referer, device(j.device), engine.SiteScope(host), stage, note,
		j.rungReached, j.breachRung, white, j.visitsTarget, j.visitsDone, j.variants, j.bytesUsed,
		j.cloaked, j.confidence, jsonb(j.reviewerData), jsonb(j.raposaData), jsonb(j.checkoutData), retryOf, max(j.attempt, 1),
		next, j.startedAt, completed).Scan(&inv)
	if err != nil {
		return fmt.Errorf("insert the investigation: %w", err)
	}
	if err := c.copyVisits(ctx, j, inv); err != nil {
		return err
	}
	if err := c.copyVariants(ctx, j, inv); err != nil {
		return err
	}
	if err := c.copyWindows(ctx, j); err != nil {
		return err
	}
	if err := c.copyShots(ctx, j); err != nil {
		return err
	}
	if err := c.copyLog(ctx, j, inv); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO raposa.imported_investigation (old_id, old_uid, investigation_id) VALUES ($1, $2, $3)`,
		j.id, j.uid, inv); err != nil {
		return err
	}
	if c.cfg.DryRun {
		return c.count()
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return c.count()
}

func (c *copier) count() error {
	c.r.Visits += c.visits
	c.r.PagesNew += c.pagesNew
	c.r.Files += c.files
	c.r.FileBytes += c.fileBytes
	return nil
}

// visitOutcome maps the collector's visit outcomes onto raposa's three
// (white, dark, error). Besides white, dark and error the collector wrote:
// blocked, a bot defence served instead of the site (raposa's error);
// candidate (043), a page the reviewer never got that was not yet called dark
// (raposa's dark: the visit did not get the reviewer's page; the verdict comes
// from the job itself; the log names these visits); and unfinished (044), a
// page that never finished loading (raposa's error: nothing to judge). The
// error text starts with the collector's word.
func visitOutcome(outcome string, errText *string) (string, *string) {
	note := ""
	switch outcome {
	case "white", "dark", "error":
		return outcome, errText
	case "candidate":
		return "dark", errText
	case "blocked", "unfinished":
		note = outcome
	default:
		note = "the collector's outcome " + outcome
	}
	if errText != nil && *errText != "" {
		note += ": " + *errText
	}
	return "error", &note
}

// visitCopied, jobCopied and shotCopied are the collector's columns that have
// a place here; kept() gathers the rest.
var (
	visitCopied = []string{"id", "job_id", "purpose", "rung", "attempt", "steps_count", "disguise_id", "line_key", "place",
		"device", "engine", "link_kind", "target_url", "referer", "exit_ip", "error", "outcome", "landed_page_id",
		"status_code", "redirect_hops", "bytes_used", "duration_ms", "started_at", "window_id"}
	jobCopied = []string{"id", "uid", "creative_id", "ad_id", "status", "mode", "origin", "target_click_url",
		"publisher_referer", "target_device", "stage", "stage_note", "stop_requested", "is_cloaked", "rung_reached",
		"breach_rung", "white_page_id", "visits_target", "visits_done", "variants_count", "bytes_used",
		"cloaked_confidence", "reviewer_data", "raposa_data", "checkout_data", "retry_of", "attempt", "requested_at",
		"started_at", "completed_at", "logs"}
	shotCopied = []string{"id", "visit_id", "side", "page_id", "content_hash", "taken_at"}
)

// keptJSON is SQL for the columns of row that the import has no place for,
// as one JSON object without the empty ones (null, "", [] and {}), or NULL
// when none is left. drop is the parameter holding the columns it copies. It
// reads whatever columns the collector's table has, so a column added there
// later is kept too.
func keptJSON(row, drop string) string {
	return `(SELECT jsonb_object_agg(key, value) FROM jsonb_each(to_jsonb(` + row + `) - ` + drop + `::text[])
		WHERE value NOT IN ('null', '""', '[]', '{}'))`
}

func kept(row, drop string) string { return keptJSON(row, drop) + "::text" }

// ranges writes ascending ids as "3-5, 9, 12-14".
func ranges(ids []int64) string {
	if len(ids) == 0 {
		return "none"
	}
	var b strings.Builder
	for i := 0; i < len(ids); {
		k := i
		for k+1 < len(ids) && ids[k+1] == ids[k]+1 {
			k++
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		if k == i {
			fmt.Fprintf(&b, "%d", ids[i])
		} else {
			fmt.Fprintf(&b, "%d-%d", ids[i], ids[k])
		}
		i = k + 1
	}
	return b.String()
}

func uuidText(h [16]byte) string {
	s := hex.EncodeToString(h[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func (c *copier) copyVisits(ctx context.Context, j job, inv int64) error {
	type visit struct {
		id                                                  int32
		purpose                                             string
		rung, attempt, steps                                int16
		disguise                                            *int16
		line, place, device, eng, linkKind, target, referer string
		exitIP, errText                                     *string
		outcome                                             string
		landed                                              *int32
		status                                              *int16
		hops                                                []byte
		bytes, ms                                           int32
		at                                                  time.Time
		kept                                                *string
	}
	var vs []visit
	err := collect(ctx, c.cfg.Old, `
		SELECT id, purpose, rung, attempt, steps_count, disguise_id, line_key, place, device, engine, link_kind,
		       target_url, referer, exit_ip, error, outcome, landed_page_id, status_code, redirect_hops,
		       bytes_used, duration_ms, started_at, `+kept("v", "$2")+`
		FROM spy.raposa_visit v WHERE job_id = $1 ORDER BY id`, func(rows pgx.Rows) error {
		var v visit
		err := rows.Scan(&v.id, &v.purpose, &v.rung, &v.attempt, &v.steps, &v.disguise, &v.line, &v.place, &v.device,
			&v.eng, &v.linkKind, &v.target, &v.referer, &v.exitIP, &v.errText, &v.outcome, &v.landed, &v.status,
			&v.hops, &v.bytes, &v.ms, &v.at, &v.kept)
		vs = append(vs, v)
		return err
	}, j.id, visitCopied)
	if err != nil {
		return fmt.Errorf("read the visits: %w", err)
	}
	// Per job, not per visit, so a long sample does not bury the log.
	var candidates []int64
	unknownDisguise := map[string][]int64{}
	defer func() {
		if len(candidates) > 0 {
			c.note(j.requestedAt, "visits the collector called candidate (a page the reviewer never got, not yet called dark), dark here: "+ranges(candidates))
		}
		for _, code := range slices.Sorted(maps.Keys(unknownDisguise)) {
			c.note(j.requestedAt, fmt.Sprintf("visits on the collector's disguise %s, which raposa does not have: %s", code, ranges(unknownDisguise[code])))
		}
	}()
	for _, v := range vs {
		var disguise *int16
		if v.disguise != nil {
			if d, ok := c.disguises[*v.disguise]; ok {
				disguise = &d
			}
		}
		outcome, errText := visitOutcome(v.outcome, v.errText)
		var landed *int32
		if v.landed != nil {
			id, err := c.page(ctx, *v.landed)
			if err != nil {
				return err
			}
			landed = &id
		}
		var id int64
		err := c.tx.QueryRow(ctx, `
			INSERT INTO raposa.visit (investigation_id, purpose, rung, disguise_id, attempt, line_key, place, exit_ip,
			    device, engine, link_kind, target_url, referer, outcome, landed_page_id, steps_count, status_code,
			    redirect_hops, bytes_used, duration_ms, error, started_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
			RETURNING id`,
			inv, v.purpose, v.rung, disguise, v.attempt, v.line, v.place, v.exitIP, v.device, v.eng, v.linkKind,
			v.target, v.referer, outcome, landed, v.steps, v.status, jsonbArray(v.hops), v.bytes, v.ms, errText, v.at).Scan(&id)
		if err != nil {
			return fmt.Errorf("insert visit %d: %w", v.id, err)
		}
		c.visits++
		c.visitIDs[v.id] = visitRef{id: id, landed: landed}
		if v.outcome == "candidate" {
			candidates = append(candidates, id)
		}
		if v.disguise != nil && disguise == nil {
			code := c.codes[*v.disguise]
			if code == "" {
				code = fmt.Sprintf("id %d", *v.disguise)
			}
			unknownDisguise[code] = append(unknownDisguise[code], id)
		}
		if v.kept != nil {
			c.note(v.at, fmt.Sprintf("visit %d (the collector's %d), kept as it was: %s", id, v.id, *v.kept))
		}

		type step struct {
			no              int16
			page            int32
			by              string
			text, clickedTo *string
		}
		var steps []step
		if err := collect(ctx, c.cfg.Old, `
			SELECT step_no, page_id, reached_by, clicked_text, clicked_url
			FROM spy.raposa_step WHERE visit_id = $1 ORDER BY step_no`, func(rows pgx.Rows) error {
			var s step
			err := rows.Scan(&s.no, &s.page, &s.by, &s.text, &s.clickedTo)
			steps = append(steps, s)
			return err
		}, v.id); err != nil {
			return fmt.Errorf("read the steps of visit %d: %w", v.id, err)
		}
		for _, s := range steps {
			page, err := c.page(ctx, s.page)
			if err != nil {
				return err
			}
			if _, err := c.tx.Exec(ctx, `
				INSERT INTO raposa.step (visit_id, step_no, page_id, reached_by, clicked_text, clicked_url)
				VALUES ($1, $2, $3, $4, $5, $6)`, id, s.no, page, s.by, s.text, s.clickedTo); err != nil {
				return fmt.Errorf("insert a step of visit %d: %w", v.id, err)
			}
		}
	}
	return nil
}

func (c *copier) copyVariants(ctx context.Context, j job, inv int64) error {
	type variant struct {
		hash         [16]byte
		pages        []int32
		first        *int32
		label        string
		visits       int32
		share        float64
		first_, last time.Time
	}
	var vs []variant
	if err := collect(ctx, c.cfg.Old, `
		SELECT path_hash, page_ids, first_page_id, label, visits, share_pct::float8, first_seen_at, last_seen_at
		FROM spy.raposa_variant WHERE job_id = $1 ORDER BY id`, func(rows pgx.Rows) error {
		var v variant
		err := rows.Scan(&v.hash, &v.pages, &v.first, &v.label, &v.visits, &v.share, &v.first_, &v.last)
		vs = append(vs, v)
		return err
	}, j.id); err != nil {
		return fmt.Errorf("read the variants: %w", err)
	}
	for _, v := range vs {
		pages := make([]int32, 0, len(v.pages))
		for _, p := range v.pages {
			id, err := c.page(ctx, p)
			if err != nil {
				return err
			}
			pages = append(pages, id)
		}
		var first *int32
		if v.first != nil {
			id, err := c.page(ctx, *v.first)
			if err != nil {
				return err
			}
			first = &id
		}
		if _, err := c.tx.Exec(ctx, `
			INSERT INTO raposa.variant (investigation_id, path_hash, page_ids, first_page_id, label, visits, share_pct,
			    first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			inv, v.hash, pages, first, v.label, v.visits, v.share, v.first_, v.last); err != nil {
			return fmt.Errorf("insert a variant: %w", err)
		}
	}
	return nil
}

// copyLog writes the investigation's log: the notes on what raposa has no
// column for, then the collector's log, one text, as one row per line, then
// the job's own columns raposa has no place for. The screens show the newest
// lines first.
func (c *copier) copyLog(ctx context.Context, j job, inv int64) error {
	var headline *string // when Tracks has no ad with it
	if j.adID == nil {
		headline = j.headline
	}
	var text string
	var rest *string
	if err := c.cfg.Old.QueryRow(ctx, `
		SELECT logs, NULLIF(COALESCE(`+keptJSON("j", "$2")+`, '{}')
		       || jsonb_strip_nulls(jsonb_build_object('headline', $3::text)), '{}')::text
		FROM spy.raposa_job j WHERE id = $1`, j.id, jobCopied, headline).Scan(&text, &rest); err != nil {
		return fmt.Errorf("read the log: %w", err)
	}
	at := j.requestedAt
	if j.startedAt != nil {
		at = *j.startedAt
	}
	lines := c.notes
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			lines = append(lines, logLine{at, l})
		}
	}
	if rest != nil {
		lines = append(lines, logLine{at, "the collector's job, kept as it was: " + *rest})
	}
	lines = append(lines, logLine{at, fmt.Sprintf("copied from the collector's job %d", j.id)})
	ats := make([]time.Time, len(lines))
	texts := make([]string, len(lines))
	for i, l := range lines {
		ats[i], texts[i] = l.at, l.line
	}
	_, err := c.tx.Exec(ctx, `
		INSERT INTO raposa.log (investigation_id, at, line)
		SELECT $1, a, l FROM unnest($2::timestamptz[], $3::text[]) WITH ORDINALITY AS t(a, l, n) ORDER BY n`, inv, ats, texts)
	return err
}

// copyWindows notes each round of the collector's continuous sampling (045)
// in the log, with the visits it made.
func (c *copier) copyWindows(ctx context.Context, j job) error {
	if !c.hasWindows {
		return nil
	}
	type window struct {
		no     int32
		at     time.Time
		row    string
		visits []int32
	}
	var ws []window
	if err := collect(ctx, c.cfg.Old, `
		SELECT w.window_no, w.started_at, COALESCE(`+kept("w", "$2")+`, '{}'),
		       ARRAY(SELECT v.id FROM spy.raposa_visit v WHERE v.window_id = w.id ORDER BY v.id)
		FROM spy.raposa_window w WHERE w.job_id = $1 ORDER BY w.window_no`, func(rows pgx.Rows) error {
		var w window
		err := rows.Scan(&w.no, &w.at, &w.row, &w.visits)
		ws = append(ws, w)
		return err
	}, j.id, []string{"id", "job_id", "window_no"}); err != nil {
		return fmt.Errorf("read the windows: %w", err)
	}
	for _, w := range ws {
		ids := make([]int64, 0, len(w.visits))
		for _, v := range w.visits {
			if ref, ok := c.visitIDs[v]; ok {
				ids = append(ids, ref.id)
			}
		}
		c.note(w.at, fmt.Sprintf("the collector's sampling round %d (spy.raposa_window), as it kept it: %s; its visits: %s", w.no, w.row, ranges(ids)))
	}
	return nil
}

// copyShots copies the screenshots the collector took when it judged a visit
// dark (043): the dark page the visit landed on and the white page beside
// it. Each becomes a file of that page here, role screenshot, and a line in
// the log with how it was taken.
func (c *copier) copyShots(ctx context.Context, j job) error {
	if !c.hasShots {
		return nil
	}
	type shot struct {
		visit int32
		side  string
		page  *int32
		file  oldAsset
		at    time.Time
		rest  *string
	}
	var shots []shot
	if err := collect(ctx, c.cfg.Old, `
		SELECT s.visit_id, s.side, s.page_id, a.content_hash, a.media_type, a.size_bytes, a.object_key,
		       a.skipped_reason, a.bytes IS NOT NULL, s.taken_at, `+kept("s", "$2")+`
		FROM spy.raposa_shot s
		JOIN spy.raposa_visit v ON v.id = s.visit_id
		JOIN spy.raposa_asset a ON a.content_hash = s.content_hash
		WHERE v.job_id = $1 ORDER BY s.id`, func(rows pgx.Rows) error {
		var s shot
		err := rows.Scan(&s.visit, &s.side, &s.page, &s.file.hash, &s.file.media, &s.file.size, &s.file.objectKey,
			&s.file.skipped, &s.file.hasBytes, &s.at, &s.rest)
		shots = append(shots, s)
		return err
	}, j.id, shotCopied); err != nil {
		return fmt.Errorf("read the screenshots: %w", err)
	}
	for _, s := range shots {
		visit := c.visitIDs[s.visit]
		var page *int32
		switch {
		case s.page != nil:
			id, err := c.page(ctx, *s.page)
			if err != nil {
				return err
			}
			page = &id
		case s.side == "dark":
			page = visit.landed
		default:
			page = c.white
		}
		if err := c.ensureAsset(ctx, s.file); err != nil {
			return fmt.Errorf("screenshot of visit %d: %w", s.visit, err)
		}
		where := "kept with no page"
		if page != nil {
			if _, err := c.tx.Exec(ctx, `
				INSERT INTO raposa.page_asset (page_id, content_hash, role, source_url)
				VALUES ($1, $2, 'screenshot', $3) ON CONFLICT DO NOTHING`,
				*page, s.file.hash, fmt.Sprintf("screenshot:visit-%d:%s", visit.id, s.side)); err != nil {
				return err
			}
			where = fmt.Sprintf("a file of page %d", *page)
		}
		line := fmt.Sprintf("screenshot of the %s page at visit %d: file %s, %s", s.side, visit.id, uuidText(s.file.hash), where)
		if s.rest != nil {
			line += "; " + *s.rest
		}
		c.note(s.at, line)
	}
	return nil
}

// page returns raposa's id for the collector's page, copying the page and
// its files when raposa does not have its content yet.
func (c *copier) page(ctx context.Context, old int32) (int32, error) {
	if id, ok := c.pages[old]; ok {
		return id, nil
	}
	var p struct {
		hash                                         [16]byte
		key, digest, title, kind, html, text         *string
		url, host, path                              string
		words, htmlBytes, seen                       int32
		headings, meta, pixels, outbound             []byte
		platform, merchant, state, capNote, rendered *string
		capLine                                      *string
		dark                                         bool
		attempts                                     int16
		firstSeen, lastSeen                          time.Time
		capturedAt                                   *time.Time
		capBytes                                     int64
	}
	err := c.cfg.Old.QueryRow(ctx, `
		SELECT content_hash, page_key, text_digest, url, host, path, title, page_kind, word_count, html, body_text,
		       html_bytes, headings, meta_tags, pixels, checkout_platform, checkout_merchant_id, outbound_links,
		       is_dark, first_seen_at, last_seen_at, times_seen, capture_state, capture_note, capture_attempts,
		       captured_at, rendered_html, capture_line, capture_bytes
		FROM spy.raposa_page WHERE id = $1`, old).Scan(
		&p.hash, &p.key, &p.digest, &p.url, &p.host, &p.path, &p.title, &p.kind, &p.words, &p.html, &p.text,
		&p.htmlBytes, &p.headings, &p.meta, &p.pixels, &p.platform, &p.merchant, &p.outbound,
		&p.dark, &p.firstSeen, &p.lastSeen, &p.seen, &p.state, &p.capNote, &p.attempts,
		&p.capturedAt, &p.rendered, &p.capLine, &p.capBytes)
	if err != nil {
		return 0, fmt.Errorf("read page %d: %w", old, err)
	}
	var id int32
	err = c.tx.QueryRow(ctx, `SELECT id FROM raposa.page WHERE content_hash = $1`, p.hash).Scan(&id)
	if err == nil {
		c.pages[old] = id
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}

	key := deref(p.key)
	if key == "" {
		key = pagever.Key(p.host, p.path)
	}
	digest := deref(p.digest)
	if digest == "" {
		digest = pagever.TextDigest(deref(p.text))
	}
	state := deref(p.state)
	switch state {
	case "html", "complete", "failed":
	case "queued", "capturing":
		state = "queued" // the keeper here takes it over
	default:
		state = "html"
	}
	err = c.tx.QueryRow(ctx, `
		INSERT INTO raposa.page (content_hash, page_key, text_digest, url, host, path, title, page_kind, word_count,
		    html, body_text, html_bytes, headings, meta_tags, pixels, checkout_platform, checkout_merchant_id,
		    outbound_links, is_dark, first_seen_at, last_seen_at, times_seen, capture_state, capture_note,
		    capture_attempts, captured_at, rendered_html, capture_line, capture_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
		    $23, $24, $25, $26, $27, $28, $29)
		RETURNING id`,
		p.hash, key, digest, p.url, p.host, p.path, p.title, p.kind, p.words, p.html, p.text, p.htmlBytes,
		jsonb(p.headings), jsonb(p.meta), jsonb(p.pixels), p.platform, p.merchant, jsonbArray(p.outbound), p.dark,
		p.firstSeen, p.lastSeen, max(p.seen, 1), state, p.capNote, p.attempts, p.capturedAt, p.rendered, p.capLine,
		p.capBytes).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert page %d: %w", old, err)
	}
	c.pagesNew++
	c.pages[old] = id
	if err := c.copyFiles(ctx, old, id); err != nil {
		return 0, fmt.Errorf("files of page %d: %w", old, err)
	}
	return id, nil
}

// copyFiles copies which files a page loaded, and each file's bytes into the
// files store when raposa does not have the file yet. The collector kept a
// small file's bytes in spy.raposa_asset.bytes and a large one in its object
// storage under raposa/files/<md5>.
func (c *copier) copyFiles(ctx context.Context, oldPage, page int32) error {
	type use struct {
		file         oldAsset
		role, source string
	}
	var uses []use
	if err := collect(ctx, c.cfg.Old, `
		SELECT pa.content_hash, pa.role, pa.source_url, a.media_type, a.size_bytes, a.object_key, a.skipped_reason,
		       a.bytes IS NOT NULL
		FROM spy.raposa_page_asset pa JOIN spy.raposa_asset a ON a.content_hash = pa.content_hash
		WHERE pa.page_id = $1`, func(rows pgx.Rows) error {
		var u use
		err := rows.Scan(&u.file.hash, &u.role, &u.source, &u.file.media, &u.file.size, &u.file.objectKey,
			&u.file.skipped, &u.file.hasBytes)
		uses = append(uses, u)
		return err
	}, oldPage); err != nil {
		return err
	}
	for _, u := range uses {
		if err := c.ensureAsset(ctx, u.file); err != nil {
			return err
		}
		if _, err := c.tx.Exec(ctx, `
			INSERT INTO raposa.page_asset (page_id, content_hash, role, source_url)
			VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, page, u.file.hash, u.role, u.source); err != nil {
			return err
		}
	}
	return nil
}

// oldAsset is one spy.raposa_asset row, without its bytes.
type oldAsset struct {
	hash               [16]byte
	media              string
	size               int64
	objectKey, skipped *string
	hasBytes           bool
}

// ensureAsset copies one of the collector's files into raposa.asset, and its
// bytes into the files store, when raposa does not have it yet.
func (c *copier) ensureAsset(ctx context.Context, a oldAsset) error {
	var have bool
	if err := c.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM raposa.asset WHERE content_hash = $1)`, a.hash).Scan(&have); err != nil {
		return err
	}
	if have {
		return nil
	}
	objectKey, err := c.copyFile(ctx, a.hash, a.media, a.hasBytes, deref(a.objectKey))
	if err != nil {
		return err
	}
	skipped := a.skipped
	if objectKey != nil {
		skipped = nil
	}
	_, err = c.tx.Exec(ctx, `
		INSERT INTO raposa.asset (content_hash, media_type, size_bytes, object_key, skipped_reason)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (content_hash) DO NOTHING`,
		a.hash, a.media, a.size, objectKey, skipped)
	return err
}

// copyFile puts one file of the collector's into the files store and
// returns its key there, or nil when the collector had no bytes for it.
func (c *copier) copyFile(ctx context.Context, hash [16]byte, media string, hasBytes bool, oldKey string) (*string, error) {
	sum := hex.EncodeToString(hash[:])
	if !hasBytes && oldKey == "" {
		return nil, nil
	}
	if c.cfg.DryRun {
		key := files.Key(sum)
		c.files++
		return &key, nil
	}
	f, err := os.CreateTemp(c.cfg.TmpDir, "import-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	if hasBytes {
		var b []byte
		if err := c.cfg.Old.QueryRow(ctx, `SELECT bytes FROM spy.raposa_asset WHERE content_hash = $1`, hash).Scan(&b); err != nil {
			return nil, err
		}
		if _, err := f.Write(b); err != nil {
			return nil, err
		}
	} else {
		if c.cfg.OldFiles == nil {
			return nil, fmt.Errorf("file %s is in the collector's object storage (%s), and no -old-files store was given", sum, oldKey)
		}
		// The collector's keys are raposa/files/<md5>; the old store is
		// opened at raposa/, so its key is files/<md5>.
		if path.Base(oldKey) != sum {
			return nil, fmt.Errorf("file %s is kept under %s, not under its md5", sum, oldKey)
		}
		rc, err := c.cfg.OldFiles.Get(ctx, files.Key(sum))
		if err != nil {
			return nil, fmt.Errorf("read %s from the collector's object storage: %w", oldKey, err)
		}
		_, err = io.Copy(f, rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s from the collector's object storage: %w", oldKey, err)
		}
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	got, size, err := files.Sum(f.Name())
	if err != nil {
		return nil, err
	}
	if got != sum {
		// The collector keyed a file it could not fetch on the md5 of its
		// address; one with bytes must match them.
		return nil, fmt.Errorf("file %s: its bytes have md5 %s", sum, got)
	}
	key, err := c.cfg.Files.Put(ctx, sum, media, f.Name(), size)
	if err != nil {
		return nil, err
	}
	c.files++
	c.fileBytes += size
	return &key, nil
}

func hostOf(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "]") {
		s = s[:i]
	}
	return strings.ToLower(s)
}

func device(d string) string {
	if d == "phone" {
		return "phone"
	}
	return "desktop"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// jsonb passes JSON through, and {} for none.
func jsonb(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

func jsonbArray(b []byte) string {
	if len(b) == 0 {
		return "[]"
	}
	return string(b)
}
