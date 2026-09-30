// Package web is raposa-web: plain pages to ask for investigations, follow
// them, read what they found and set watches. No design yet, on purpose; the
// app's look comes later.
//
// Stored pages are other people's HTML. They are served with a sandbox and a
// policy that loads nothing from outside, so opening one runs none of its
// scripts and tells the operator's servers nothing.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/shared/files"
)

//go:embed templates/*.html
var templateFiles embed.FS

// storedPolicy is the header every stored page and file goes out with: a
// sandbox (no scripts, a unique origin) and nothing loaded from anywhere.
const storedPolicy = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:"

// Server serves the pages.
type Server struct {
	db    *pgxpool.Pool
	files files.Store
	log   *slog.Logger
	tmpl  *template.Template
}

// New builds the pages over the raposa schema.
func New(db *pgxpool.Pool, fs files.Store, log *slog.Logger) (*Server, error) {
	t, err := template.New("").Funcs(template.FuncMap{
		"when":  when,
		"bytes": humanBytes,
		"deref": deref,
	}).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{db: db, files: fs, log: log, tmpl: t}, nil
}

// Handler routes the pages.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.list)
	mux.HandleFunc("POST /request", s.request)
	mux.HandleFunc("GET /i/{id}", s.investigation)
	mux.HandleFunc("POST /i/{id}/stop", s.stop)
	mux.HandleFunc("POST /i/{id}/watch", s.watch)
	mux.HandleFunc("POST /watch/{id}/end", s.endWatch)
	mux.HandleFunc("GET /p/{id}", s.page)
	mux.HandleFunc("GET /p/{id}/html", s.pageHTML)
	mux.HandleFunc("GET /p/{id}/kept", s.pageHTML)
	mux.HandleFunc("GET /f/{hash}", s.file)
	mux.HandleFunc("GET /burns", s.burns)
	return sameOrigin(mux)
}

// sameOrigin refuses a form posted from another site: the pages sit on a
// private address, but a browser that can reach them would carry such a post.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "a form from another site", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render", "page", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, nil)
		return
	}
	s.log.Error("page failed", "err", err)
	http.Error(w, "something went wrong: "+err.Error(), http.StatusInternalServerError)
}

// ---- investigations -----------------------------------------------------

// Row is one investigation in the list.
type Row struct {
	ID            int64
	CreativeID    int32
	AdID          *int32
	Mode, Origin  string
	Status, Stage string
	StageNote     string
	VisitsDone    int32
	VisitsTarget  int32
	Variants      int16
	Cloaked       bool
	Confidence    float64
	BreachRung    *int16
	RequestedBy   string
	RequestedAt   time.Time
	CompletedAt   *time.Time
}

const rowColumns = `id, creative_id, ad_id, mode, origin, status, stage, stage_note, visits_done, visits_target,
	variants_count, is_cloaked, cloaked_confidence::float8, breach_rung, requested_by, requested_at, completed_at`

func scanRow(r pgx.Row, x *Row, more ...any) error {
	return r.Scan(append([]any{&x.ID, &x.CreativeID, &x.AdID, &x.Mode, &x.Origin, &x.Status, &x.Stage, &x.StageNote,
		&x.VisitsDone, &x.VisitsTarget, &x.Variants, &x.Cloaked, &x.Confidence, &x.BreachRung, &x.RequestedBy,
		&x.RequestedAt, &x.CompletedAt}, more...)...)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT `+rowColumns+` FROM raposa.investigation ORDER BY id DESC LIMIT 200`)
	if err != nil {
		s.fail(w, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var x Row
		return x, scanRow(r, &x)
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "list.html", map[string]any{"Rows": list, "Asked": r.URL.Query().Get("asked")})
}

func (s *Server) request(w http.ResponseWriter, r *http.Request) {
	creative, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("creative")), 10, 32)
	if err != nil || creative <= 0 {
		http.Error(w, "the creative id is a number", http.StatusBadRequest)
		return
	}
	var adID *int32
	if a := strings.TrimSpace(r.FormValue("ad")); a != "" {
		n, err := strconv.ParseInt(a, 10, 32)
		if err != nil || n <= 0 {
			http.Error(w, "the ad id is a number", http.StatusBadRequest)
			return
		}
		v := int32(n)
		adID = &v
	}
	mode := r.FormValue("mode")
	if mode != "quick" {
		mode = "deep"
	}
	var id int64
	if err := s.db.QueryRow(r.Context(), `SELECT raposa_api.request_investigation_v1($1, $2, $3, $4)`,
		int32(creative), mode, adID, who(r)).Scan(&id); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/i/%d", id), http.StatusSeeOther)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `SELECT raposa_api.stop_investigation_v1($1)`, id); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/i/%d", id), http.StatusSeeOther)
}

// Detail is one investigation's page.
type Detail struct {
	Row
	TargetURL, Referer string
	Device, Scope      string
	RungReached        int16
	BytesUsed          int64
	Attempt            int16
	RetryOf            *int64
	NextVisitAt        time.Time
	ClaimedBy          *string
	StopRequested      bool
	WhitePageID        *int32
	Variants           []Variant
	Evidence           []Evidence
	Visits             []Visit
	Log                []LogLine
	Watches            []Watch
	Running            bool
}

// Variant is one dark funnel the sample met.
type Variant struct {
	Label   string
	Visits  int32
	Share   float64
	PageIDs []int32
}

// Evidence is one landing page as proof of who runs the ad.
type Evidence struct {
	Outcome, FinalURL, Domain, Kind, Title string
	Checkout, Merchant, Seller, SellerAcct string
	Account                                *int32
	Campaign, Device                       string
	PageID                                 int32
}

// Visit is one visit.
type Visit struct {
	ID                   int64
	At                   time.Time
	Purpose              string
	Rung                 int16
	Device, Engine, Link string
	Line, Place          string
	ExitIP               *string
	Outcome              string
	PageID               *int32
	Steps                int16
	Status               *int16
	Bytes, Ms            int32
	Error                *string
}

// LogLine is one line of the investigation's log.
type LogLine struct {
	At   time.Time
	Line string
}

// Watch is one watch that covers the investigation.
type Watch struct {
	ID           int64
	Notification string
	Kinds        []string
	Creative     bool
	By           string
	Sent, Failed int
	Skipped      int
}

func (s *Server) investigation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var d Detail
	err := scanRow(s.db.QueryRow(ctx, `SELECT `+rowColumns+`, target_click_url, publisher_referer, target_device,
			burn_scope, rung_reached, bytes_used, attempt, retry_of, next_visit_at, claimed_by, stop_requested, white_page_id
		FROM raposa.investigation WHERE id = $1`, id), &d.Row,
		&d.TargetURL, &d.Referer, &d.Device, &d.Scope, &d.RungReached, &d.BytesUsed, &d.Attempt, &d.RetryOf,
		&d.NextVisitAt, &d.ClaimedBy, &d.StopRequested, &d.WhitePageID)
	if err != nil {
		s.fail(w, err)
		return
	}
	d.Running = d.Status == "waiting" || d.Status == "running"
	if err := s.details(ctx, &d); err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "investigation.html", d)
}

func (s *Server) details(ctx context.Context, d *Detail) error {
	var err error
	rows, _ := s.db.Query(ctx, `SELECT label, visits, share_pct::float8, page_ids FROM raposa.variant
		WHERE investigation_id = $1 ORDER BY visits DESC, id`, d.ID)
	if d.Variants, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Variant, error) {
		var v Variant
		return v, r.Scan(&v.Label, &v.Visits, &v.Share, &v.PageIDs)
	}); err != nil {
		return err
	}
	rows, _ = s.db.Query(ctx, `SELECT outcome, final_url, domain, page_kind, title, checkout_platform, checkout_merchant_id,
			seller_platform, seller_account, account_id, campaign_external_id, device, landing_page_id
		FROM raposa.evidence WHERE investigation_id = $1 ORDER BY outcome, id`, d.ID)
	if d.Evidence, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Evidence, error) {
		var e Evidence
		return e, r.Scan(&e.Outcome, &e.FinalURL, &e.Domain, &e.Kind, &e.Title, &e.Checkout, &e.Merchant,
			&e.Seller, &e.SellerAcct, &e.Account, &e.Campaign, &e.Device, &e.PageID)
	}); err != nil {
		return err
	}
	rows, _ = s.db.Query(ctx, `SELECT id, started_at, purpose, rung, device, engine, link_kind, line_key, place, exit_ip,
			outcome, landed_page_id, steps_count, status_code, bytes_used, duration_ms, error
		FROM raposa.visit WHERE investigation_id = $1 ORDER BY id DESC LIMIT 300`, d.ID)
	if d.Visits, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Visit, error) {
		var v Visit
		return v, r.Scan(&v.ID, &v.At, &v.Purpose, &v.Rung, &v.Device, &v.Engine, &v.Link, &v.Line, &v.Place, &v.ExitIP,
			&v.Outcome, &v.PageID, &v.Steps, &v.Status, &v.Bytes, &v.Ms, &v.Error)
	}); err != nil {
		return err
	}
	rows, _ = s.db.Query(ctx, `SELECT at, line FROM raposa.log WHERE investigation_id = $1 ORDER BY id DESC LIMIT 300`, d.ID)
	if d.Log, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (LogLine, error) {
		var l LogLine
		return l, r.Scan(&l.At, &l.Line)
	}); err != nil {
		return err
	}
	rows, _ = s.db.Query(ctx, `
		SELECT w.id, w.pushcut_notification, w.kinds, w.investigation_id IS NULL, w.created_by,
		       count(*) FILTER (WHERE dl.status = 'sent')::int, count(*) FILTER (WHERE dl.status = 'failed')::int,
		       count(*) FILTER (WHERE dl.status = 'skipped')::int
		FROM raposa.watch w
		LEFT JOIN raposa.delivery dl ON dl.watch_id = w.id
		WHERE w.ended_at IS NULL
		  AND (w.investigation_id IN ($1, $2) OR w.creative_id = $3 OR (w.investigation_id IS NULL AND w.creative_id IS NULL))
		GROUP BY w.id ORDER BY w.id`, d.ID, d.RetryOf, d.CreativeID)
	d.Watches, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Watch, error) {
		var x Watch
		return x, r.Scan(&x.ID, &x.Notification, &x.Kinds, &x.Creative, &x.By, &x.Sent, &x.Failed, &x.Skipped)
	})
	return err
}

// ---- watches ------------------------------------------------------------

var watchKinds = map[string]bool{"started": true, "dark_found": true, "finished": true}

func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("notification"))
	if name == "" {
		http.Error(w, "name the Pushcut notification to send to", http.StatusBadRequest)
		return
	}
	var kinds []string
	for _, k := range r.Form["kind"] {
		if watchKinds[k] {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		http.Error(w, "pick at least one event", http.StatusBadRequest)
		return
	}
	// A watch on the creative covers every investigation of it, the retries
	// and the ones asked for later.
	var err error
	if r.FormValue("scope") == "creative" {
		_, err = s.db.Exec(r.Context(), `
			INSERT INTO raposa.watch (creative_id, kinds, pushcut_notification, created_by)
			SELECT creative_id, $2, $3, $4 FROM raposa.investigation WHERE id = $1`, id, kinds, name, who(r))
	} else {
		_, err = s.db.Exec(r.Context(), `
			INSERT INTO raposa.watch (investigation_id, kinds, pushcut_notification, created_by)
			VALUES ($1, $2, $3, $4)`, id, kinds, name, who(r))
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/i/%d", id), http.StatusSeeOther)
}

func (s *Server) endWatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE raposa.watch SET ended_at = now() WHERE id = $1 AND ended_at IS NULL`, id); err != nil {
		s.fail(w, err)
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/i/") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// ---- pages and files ----------------------------------------------------

// PageInfo is one stored version of a page.
type PageInfo struct {
	ID                 int32
	URL, Host, Path    string
	Title, Kind        *string
	Words              int32
	Dark               bool
	Checkout, Merchant *string
	State              string
	Note               *string
	Seen               int32
	First, Last        time.Time
	HTMLBytes          int32
	HasKept            bool
	KeptBytes          int64
	Files              []File
}

// File is one file of a kept page.
type File struct {
	Role, URL, MediaType string
	Size                 int64
	Hash                 string
	Stored               bool
	Skipped              *string
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var p PageInfo
	err := s.db.QueryRow(ctx, `SELECT id, url, host, path, title, page_kind, word_count, is_dark, checkout_platform,
			checkout_merchant_id, capture_state, capture_note, times_seen, first_seen_at, last_seen_at, html_bytes,
			rendered_html IS NOT NULL, capture_bytes
		FROM raposa.page WHERE id = $1`, id).Scan(&p.ID, &p.URL, &p.Host, &p.Path, &p.Title, &p.Kind, &p.Words, &p.Dark,
		&p.Checkout, &p.Merchant, &p.State, &p.Note, &p.Seen, &p.First, &p.Last, &p.HTMLBytes, &p.HasKept, &p.KeptBytes)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows, _ := s.db.Query(ctx, `SELECT pa.role, pa.source_url, a.media_type, a.size_bytes, a.content_hash::text,
			a.object_key IS NOT NULL, a.skipped_reason
		FROM raposa.page_asset pa JOIN raposa.asset a USING (content_hash)
		WHERE pa.page_id = $1 ORDER BY pa.role, pa.source_url`, id)
	if p.Files, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (File, error) {
		var f File
		return f, r.Scan(&f.Role, &f.URL, &f.MediaType, &f.Size, &f.Hash, &f.Stored, &f.Skipped)
	}); err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "page.html", p)
}

// pageHTML serves the HTML a visit stored (/html) or the keeper's copy after
// the page's scripts ran (/kept), sandboxed.
func (s *Server) pageHTML(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	col := "html"
	if strings.HasSuffix(r.URL.Path, "/kept") {
		col = "rendered_html"
	}
	var html *string
	if err := s.db.QueryRow(r.Context(), `SELECT `+col+` FROM raposa.page WHERE id = $1`, id).Scan(&html); err != nil {
		s.fail(w, err)
		return
	}
	if html == nil {
		http.NotFound(w, r)
		return
	}
	stored(w, "text/html; charset=utf-8")
	_, _ = io.WriteString(w, *html)
}

var hashRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// file serves one kept file from the files store, sandboxed like the pages.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !hashRe.MatchString(hash) {
		http.NotFound(w, r)
		return
	}
	var key *string
	var mediaType string
	if err := s.db.QueryRow(r.Context(), `SELECT object_key, media_type FROM raposa.asset WHERE content_hash = $1`, hash).
		Scan(&key, &mediaType); err != nil {
		s.fail(w, err)
		return
	}
	if key == nil {
		http.NotFound(w, r)
		return
	}
	body, err := s.files.Get(r.Context(), *key)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer body.Close()
	stored(w, mediaType)
	_, _ = io.Copy(w, body)
}

func stored(w http.ResponseWriter, mediaType string) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Security-Policy", storedPolicy)
	w.Header().Set("Cache-Control", "private, max-age=3600")
}

// ---- burned lines -------------------------------------------------------

// Burn is one line one site shows the white page to.
type Burn struct {
	Scope, Line       string
	Rung              int16
	Visits, Dark      int32
	OtherV, OtherD    int32
	Detected, Checked time.Time
	Sites             int32
	General           bool
}

func (s *Server) burns(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.db.Query(r.Context(), `
		SELECT b.scope, b.line_key, b.rung, b.visits, b.dark, b.other_visits, b.other_dark, b.detected_at, b.checked_at,
		       COALESCE(ls.sites_burned, 0), COALESCE(ls.burned_in_general, false)
		FROM raposa.line_burn b LEFT JOIN raposa.line_status ls USING (line_key)
		WHERE b.ended_at IS NULL ORDER BY b.scope, b.line_key`)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Burn, error) {
		var b Burn
		return b, r.Scan(&b.Scope, &b.Line, &b.Rung, &b.Visits, &b.Dark, &b.OtherV, &b.OtherD, &b.Detected, &b.Checked,
			&b.Sites, &b.General)
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "burns.html", list)
}

// ---- helpers ------------------------------------------------------------

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

// who is the person asking: Cloudflare Access names them once it is in
// front; until then, whatever the form says.
func who(r *http.Request) string {
	if e := r.Header.Get("Cf-Access-Authenticated-User-Email"); e != "" {
		return e
	}
	return strings.TrimSpace(r.FormValue("by"))
}

func when(t any) string {
	switch v := t.(type) {
	case time.Time:
		return v.UTC().Format("Jan 2 15:04:05")
	case *time.Time:
		if v == nil {
			return ""
		}
		return v.UTC().Format("Jan 2 15:04:05")
	}
	return ""
}

func humanBytes(n any) string {
	var b float64
	switch v := n.(type) {
	case int64:
		b = float64(v)
	case int32:
		b = float64(v)
	case int:
		b = float64(v)
	}
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", b/(1<<10))
	}
	return fmt.Sprintf("%.0f B", b)
}

func deref(v any) any {
	switch p := v.(type) {
	case *string:
		if p == nil {
			return ""
		}
		return *p
	case *int32:
		if p == nil {
			return ""
		}
		return *p
	case *int16:
		if p == nil {
			return ""
		}
		return *p
	case *int64:
		if p == nil {
			return ""
		}
		return *p
	}
	return v
}
