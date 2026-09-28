package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/raposa/internal/files"
	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
)

// The keeper keeps every version of every page a deep investigation reached
// whole, once.
//
// A visit stores the HTML it saw and records its steps against the version
// of the page it met (pagever). A new version waits with capture_state
// 'queued'. The keeper takes them one at a time and has the runner open each
// once (browser/keep.js): the stored HTML stands in for the page, so the
// cloaker decides nothing and the copy is of exactly the version the visit
// saw, while every file it loads (stylesheets and what they name, scripts,
// images, fonts, a stream stitched into one mp4) comes in through a line
// that is not metered. A video that is one whole file is downloaded here,
// streamed to disk while it is hashed, so the runner never holds it. Every
// file goes to the file store under its md5.

// Caps of one keep.
const (
	keepMaxFileBytes  = 26214400          // 25 MB, one file that is not a video
	keepMaxVideoBytes = 400 * 1024 * 1024 // one video
	keepMaxSegments   = 3000
	keepMaxVideos     = 3
	keepDwellMs       = 12000
	maxKeepAttempts   = 3
	// The runner gives a page 10 minutes and gives up at 13.
	keepTimeout = 15 * time.Minute
	keepLease   = 20 * time.Minute
)

// KeepRequest is the body POSTed to the runner's /keep.
type KeepRequest struct {
	URL           string       `json:"url"`
	HTML          string       `json:"html"`
	Referer       string       `json:"referer"`
	Proxy         *ProxyConfig `json:"proxy"`
	Device        string       `json:"device"`
	Timezone      string       `json:"timezone"`
	Dir           string       `json:"dir"`
	DwellMs       int          `json:"dwellMs"`
	MaxFileBytes  int          `json:"maxFileBytes"`
	MaxVideoBytes int          `json:"maxVideoBytes"`
	MaxSegments   int          `json:"maxSegments"`
	MaxVideos     int          `json:"maxVideos"`
}

// KeepAnswer is what the runner kept.
type KeepAnswer struct {
	OK           bool        `json:"ok"`
	Error        string      `json:"error"`
	Bytes        int64       `json:"bytes"`
	DurationMs   int         `json:"durationMs"`
	Title        string      `json:"title"`
	RenderedHTML string      `json:"renderedHtml"`
	Files        []KeptFile  `json:"files"`
	Videos       []KeptVideo `json:"videos"`
	Notes        []string    `json:"notes"`
}

// KeptFile is one file the page loaded, written to Path.
type KeptFile struct {
	URL           string `json:"url"`
	Role          string `json:"role"`
	MediaType     string `json:"mediaType"`
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	SkippedReason string `json:"skippedReason"`
}

// KeptVideo is the video of the page: stitched from a stream, a whole file
// for the engine to download (Download), or only its address (a player that
// serves behind signed addresses).
type KeptVideo struct {
	URL          string `json:"url"`
	Kind         string `json:"kind"`
	Download     bool   `json:"download"`
	Variant      string `json:"variant"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	MediaType    string `json:"mediaType"`
	Note         string `json:"note"`
	Cut          string `json:"cut"`
	Pieces       int    `json:"pieces"`
	PiecesTotal  int    `json:"piecesTotal"`
	PiecesFailed int    `json:"piecesFailed"`
	Seconds      int    `json:"seconds"`
}

// Keep asks the runner to keep one version whole.
func (b *BrowserClient) Keep(ctx context.Context, req KeepRequest) (*KeepAnswer, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := b.post(ctx, "/keep", body, &http.Client{Timeout: keepTimeout})
	if err != nil {
		return nil, err
	}
	var out KeepAnswer
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode keep answer: %w", err)
	}
	return &out, nil
}

// keepTask is one version the keeper opens once.
type keepTask struct {
	PageID   int32
	URL      string
	HTML     string
	IsDark   bool
	Attempts int16
	// From the latest visit that reached it: the device it claimed, the line
	// it took, what sent it there (the page before, or the visit's referer on
	// the first step), and the site whose burned lines it skips.
	Device  string
	LineKey string
	Referer string
	Scope   string
}

// keptRow is one file of a kept page.
type keptRow struct {
	ContentHash   uuid.UUID
	MediaType     string
	SizeBytes     int64
	ObjectKey     string
	SkippedReason string
	Role          string
	SourceURL     string
}

// keepResult is what one keep stored.
type keepResult struct {
	Complete     bool
	Note         string
	RenderedHTML string
	LineKey      string
	Bytes        int64
	Rows         []keptRow
	// The page did not get a try: the runner was down, or this node is
	// stopping. The attempt is given back.
	GiveBack bool
}

// releaseKeeps puts back the pages this node was keeping when it stopped.
func (e *Engine) releaseKeeps(ctx context.Context) (int64, error) {
	tag, err := e.store.pool.Exec(ctx, `
		UPDATE raposa.page
		SET capture_state = 'queued', capture_claimed_by = NULL, capture_claimed_until = NULL,
		    capture_attempts = GREATEST(capture_attempts - 1, 0)
		WHERE capture_state = 'capturing' AND capture_claimed_by = $1`, e.cfg.Node)
	if err != nil {
		return 0, fmt.Errorf("put back this node's keeps: %w", err)
	}
	return tag.RowsAffected(), nil
}

// keep keeps queued versions until ctx ends.
func (e *Engine) keep(ctx context.Context) {
	for ctx.Err() == nil {
		done, err := e.KeepOne(ctx)
		if err != nil && ctx.Err() == nil {
			e.log.Error("keeper", "err", err)
		}
		if !done || err != nil {
			sleep(ctx, 10*time.Second)
		}
	}
}

// KeepOne keeps the next queued version. It reports false when none waits.
func (e *Engine) KeepOne(ctx context.Context) (bool, error) {
	t, err := e.claimKeep(ctx)
	if err != nil || t == nil {
		return false, err
	}
	res := e.keepPage(ctx, t)
	if ctx.Err() != nil {
		res = keepResult{GiveBack: true, Note: "the engine stopped during the keep"}
	}
	if err := e.finishKeep(context.WithoutCancel(ctx), t, res); err != nil {
		return true, err
	}
	switch {
	case res.GiveBack:
		e.m.keeps.WithLabelValues("runner_down").Inc()
		// Every page claimed now would fail the same way: wait.
		return false, nil
	case res.Complete:
		e.m.keeps.WithLabelValues("complete").Inc()
	case t.Attempts >= maxKeepAttempts:
		e.m.keeps.WithLabelValues("failed").Inc()
	default:
		e.m.keeps.WithLabelValues("retry").Inc()
	}
	e.log.Info("keep", "page", t.PageID, "url", t.URL, "complete", res.Complete, "note", res.Note, "bytes", res.Bytes, "line", res.LineKey)
	return true, nil
}

// claimKeep takes the oldest queued version, dark pages first. A keep whose
// node died is taken again once its lease has run out.
func (e *Engine) claimKeep(ctx context.Context) (*keepTask, error) {
	var t keepTask
	err := e.store.pool.QueryRow(ctx, `
		UPDATE raposa.page
		SET capture_state = 'capturing', capture_attempts = capture_attempts + 1,
		    capture_claimed_by = $1, capture_claimed_until = now() + $2::interval
		WHERE id = (
			SELECT id FROM raposa.page
			WHERE (capture_state = 'queued' OR (capture_state = 'capturing' AND capture_claimed_until < now()))
			  AND html IS NOT NULL
			ORDER BY is_dark DESC, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, url, html, is_dark, capture_attempts`, e.cfg.Node, interval(keepLease)).
		Scan(&t.PageID, &t.URL, &t.HTML, &t.IsDark, &t.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim a page to keep: %w", err)
	}
	var device, lineKey, referer, scope *string
	err = e.store.pool.QueryRow(ctx, `
		SELECT v.device, v.line_key,
		       COALESCE((SELECT p2.url FROM raposa.step s2
		                 JOIN raposa.page p2 ON p2.id = s2.page_id
		                 WHERE s2.visit_id = s.visit_id AND s2.step_no = s.step_no - 1), v.referer),
		       i.burn_scope
		FROM raposa.step s
		JOIN raposa.visit v ON v.id = s.visit_id
		JOIN raposa.investigation i ON i.id = v.investigation_id
		WHERE s.page_id = $1
		ORDER BY v.started_at DESC
		LIMIT 1`, t.PageID).Scan(&device, &lineKey, &referer, &scope)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read how page %d was reached: %w", t.PageID, err)
	}
	t.Device, t.LineKey, t.Referer, t.Scope = deref(device), deref(lineKey), deref(referer), deref(scope)
	return &t, nil
}

// finishKeep closes one keep: its files, and the page's state. A keep that
// failed goes back to the queue until its third attempt.
func (e *Engine) finishKeep(ctx context.Context, t *keepTask, k keepResult) error {
	state := "complete"
	if !k.Complete {
		state = "queued"
		if t.Attempts >= maxKeepAttempts && !k.GiveBack {
			state = "failed"
		}
	}
	giveBack := 0
	if k.GiveBack {
		giveBack = 1
	}
	tx, err := e.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, a := range k.Rows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO raposa.asset (content_hash, media_type, size_bytes, object_key, skipped_reason)
			VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''))
			ON CONFLICT (content_hash) DO UPDATE SET
				object_key = COALESCE(raposa.asset.object_key, EXCLUDED.object_key),
				size_bytes = GREATEST(raposa.asset.size_bytes, EXCLUDED.size_bytes),
				skipped_reason = CASE WHEN COALESCE(raposa.asset.object_key, EXCLUDED.object_key) IS NOT NULL
				                      THEN NULL ELSE EXCLUDED.skipped_reason END`,
			a.ContentHash, a.MediaType, a.SizeBytes, a.ObjectKey, clean(a.SkippedReason)); err != nil {
			return fmt.Errorf("store kept file %s: %w", a.SourceURL, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO raposa.page_asset (page_id, content_hash, role, source_url)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (page_id, md5(source_url)) DO UPDATE SET content_hash = EXCLUDED.content_hash, role = EXCLUDED.role`,
			t.PageID, a.ContentHash, a.Role, clean(a.SourceURL)); err != nil {
			return fmt.Errorf("join kept file %s to page %d: %w", a.SourceURL, t.PageID, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE raposa.page
		SET capture_state = $2,
		    capture_note = NULLIF($3, ''),
		    captured_at = CASE WHEN $2 = 'complete' THEN now() ELSE captured_at END,
		    rendered_html = COALESCE(NULLIF($4, ''), rendered_html),
		    capture_line = COALESCE(NULLIF($5, ''), capture_line),
		    capture_bytes = capture_bytes + $6,
		    capture_attempts = GREATEST(capture_attempts - $7, 0),
		    capture_claimed_by = NULL, capture_claimed_until = NULL
		WHERE id = $1 AND capture_claimed_by = $8`,
		t.PageID, state, clean(k.Note), clean(k.RenderedHTML), k.LineKey, k.Bytes, giveBack, e.cfg.Node); err != nil {
		return fmt.Errorf("finish keeping page %d: %w", t.PageID, err)
	}
	return tx.Commit(ctx)
}

func (e *Engine) keepPage(ctx context.Context, t *keepTask) keepResult {
	var res keepResult
	line := e.keepLine(ctx, t)
	if line != nil {
		res.LineKey = line.Key
	}
	release, err := e.browser.AcquireKeep(ctx)
	defer release()
	if err != nil {
		res.GiveBack = true
		res.Note = "stopped while waiting for the browser"
		return res
	}
	device := "desktop"
	if t.Device == "phone" {
		device = "phone"
	}
	dir := filepath.Join(e.cfg.KeepDir, fmt.Sprintf("page-%d-%d", t.PageID, time.Now().UnixNano()))
	defer os.RemoveAll(dir)

	ans, err := e.browser.Keep(ctx, KeepRequest{
		URL:           browserURL(t.URL),
		HTML:          t.HTML,
		Referer:       t.Referer,
		Proxy:         proxyConfig(line),
		Device:        device,
		Timezone:      timezoneFor("", res.LineKey),
		Dir:           dir,
		DwellMs:       keepDwellMs,
		MaxFileBytes:  keepMaxFileBytes,
		MaxVideoBytes: keepMaxVideoBytes,
		MaxSegments:   keepMaxSegments,
		MaxVideos:     keepMaxVideos,
	})
	if err != nil {
		res.Note = err.Error()
		res.GiveBack = errors.Is(err, errRunnerBusy)
		return res
	}
	res.Bytes = ans.Bytes
	if !ans.OK {
		res.Note = "the browser could not open the page: " + ans.Error
		return res
	}
	res.RenderedHTML = ans.RenderedHTML

	kept, skipped, stored := 0, 0, int64(0)
	for _, f := range ans.Files {
		row, err := e.storeFile(ctx, f.URL, keptRole(f.Role, f.URL, f.MediaType), f.MediaType, f.Path, f.Size, f.SkippedReason)
		if err != nil {
			res.Note = err.Error()
			return res
		}
		if row.SkippedReason != "" {
			skipped++
		} else {
			kept++
			stored += row.SizeBytes
		}
		res.Rows = append(res.Rows, row)
	}
	var videoNotes []string
	for _, v := range ans.Videos {
		if v.Download {
			path, size, mediaType, note := e.downloadVideo(ctx, line, device, v.URL, t.URL, dir)
			v.Path, v.Size, v.Note = path, size, note
			if mediaType != "" {
				v.MediaType = mediaType
			}
			res.Bytes += size
		}
		skippedReason := ""
		if v.Path == "" {
			skippedReason = orDefault(v.Note, "no bytes")
		}
		row, err := e.storeFile(ctx, v.URL, "video", orDefault(v.MediaType, "video/mp4"), v.Path, v.Size, skippedReason)
		if err != nil {
			res.Note = err.Error()
			return res
		}
		res.Rows = append(res.Rows, row)
		videoNotes = append(videoNotes, describeVideo(v, row))
		if row.SkippedReason == "" {
			stored += row.SizeBytes
		}
	}
	res.Complete = true
	res.Note = fmt.Sprintf("%d files kept (%d bytes), %d not kept", kept, stored, skipped)
	if len(videoNotes) > 0 {
		res.Note += "; " + strings.Join(videoNotes, "; ")
	} else {
		res.Note += "; no video on the page"
	}
	for _, n := range ans.Notes {
		res.Note += "; " + n
	}
	return res
}

func describeVideo(v KeptVideo, row keptRow) string {
	desc := fmt.Sprintf("%s video %s", v.Kind, v.URL)
	if row.SkippedReason == "" {
		desc += fmt.Sprintf(": %d bytes", row.SizeBytes)
		if v.PiecesTotal > 0 {
			desc += fmt.Sprintf(", %d of %d pieces", v.Pieces, v.PiecesTotal)
		}
		if v.Seconds > 0 {
			desc += fmt.Sprintf(", %d min", (v.Seconds+30)/60)
		}
		if v.Variant != "" {
			desc += ", variant " + v.Variant
		}
	}
	if v.Cut != "" {
		desc += ", " + v.Cut
	}
	if v.Note != "" {
		desc += " (" + v.Note + ")"
	} else if row.SkippedReason != "" {
		desc += " (" + row.SkippedReason + ")"
	}
	return desc
}

// storeFile puts one file on disk into the file store and returns its row.
// A file with no bytes keeps a row keyed on the md5 of its address.
func (e *Engine) storeFile(ctx context.Context, source, role, mediaType, path string, size int64, skipped string) (keptRow, error) {
	row := keptRow{Role: role, SourceURL: source, MediaType: orDefault(mediaType, "application/octet-stream"), SkippedReason: skipped}
	if path == "" || skipped != "" {
		if row.SkippedReason == "" {
			row.SkippedReason = "no bytes returned"
		}
		row.ContentHash = uuid.UUID(md5.Sum([]byte(source)))
		return row, nil
	}
	// The runner's directory is this node's, so a path outside it is refused.
	if rel, err := filepath.Rel(e.cfg.KeepDir, path); err != nil || strings.HasPrefix(rel, "..") {
		row.SkippedReason = "the runner wrote the file outside the keep folder"
		row.ContentHash = uuid.UUID(md5.Sum([]byte(source)))
		return row, nil
	}
	sum, n, err := files.Sum(path)
	if err != nil {
		return row, fmt.Errorf("read kept file %s: %w", source, err)
	}
	raw, err := hex.DecodeString(sum)
	if err != nil || len(raw) != 16 {
		return row, fmt.Errorf("md5 of %s: %q", source, sum)
	}
	row.ContentHash = uuid.UUID(raw)
	row.SizeBytes = n
	key, err := e.files.Put(ctx, sum, row.MediaType, path, n)
	if err != nil {
		row.SkippedReason = "upload failed: " + err.Error()
		return row, nil
	}
	row.ObjectKey = key
	return row, nil
}

// downloadVideo fetches one whole-file video through the line the page was
// kept through, straight to disk, and returns its path, size and type, or a
// note saying why there is none.
func (e *Engine) downloadVideo(ctx context.Context, line *lines.Line, device, videoURL, pageURL, dir string) (string, int64, string, string) {
	transport := lines.Transport(line)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, browserURL(videoURL), nil)
	if err != nil {
		return "", 0, "", "not a link that loads"
	}
	req.Header.Set("User-Agent", userAgent(device))
	req.Header.Set("Referer", pageURL)
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, "", "could not be downloaded"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, "", fmt.Sprintf("the server answered %d", resp.StatusCode)
	}
	if resp.ContentLength > keepMaxVideoBytes {
		return "", 0, "", fmt.Sprintf("larger than the %d byte limit (%d bytes)", keepMaxVideoBytes, resp.ContentLength)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", 0, "", "could not be written: " + err.Error()
	}
	f, err := os.CreateTemp(dir, "video-*")
	if err != nil {
		return "", 0, "", "could not be written: " + err.Error()
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, keepMaxVideoBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, "", "the download broke off: " + err.Error()
	}
	if n > keepMaxVideoBytes {
		return "", 0, "", fmt.Sprintf("larger than the %d byte limit", keepMaxVideoBytes)
	}
	mediaType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	return f.Name(), n, mediaType, ""
}

// keptRole settles on one of the words raposa.page_asset.role takes. A
// playlist is part of a video.
func keptRole(role, rawURL, mediaType string) string {
	if role == "playlist" {
		return "video"
	}
	return assetRole(role, rawURL, mediaType)
}

// keepLine picks the line a page is kept through. The page is opened again,
// so a cloaker in front of its files decides again: the line the visit that
// reached it took comes first, as long as it is not the metered residential
// line and not burned for the site. Otherwise an ISP line away from the
// places ad networks sit, as a ladder rung past the baseline picks it.
func (e *Engine) keepLine(ctx context.Context, t *keepTask) *lines.Line {
	burned, err := e.store.BurnedLines(ctx, t.Scope)
	if err != nil {
		e.log.Warn("keeper: burned lines", "err", err)
	}
	var usable []lines.Line
	for _, l := range e.lines.All() {
		if _, b := burned[l.Key]; !b && l.Role != "residential" {
			usable = append(usable, l)
		}
	}
	for _, l := range usable {
		if l.Key == t.LineKey {
			cpy := l
			return &cpy
		}
	}
	var avoid []string
	if set, err := e.store.LoadSettings(ctx); err == nil {
		avoid = set.AvoidPlaces
	}
	n := int(t.PageID)
	if l := chooseLine(usable, "isp", avoid, false, n); l != nil {
		return l
	}
	return chooseLine(usable, "dc", avoid, false, n)
}
