// Package web is the library's HTTP API, for the apps on the same box
// (Create, Launch). It listens on localhost only and is never reached by a
// browser directly: each app calls it from its own server, and serves the
// pictures to its pages itself. Lists are also in the library_api views.
//
// Errors are {"error": "<one line>"}: 400 bad input, 404 no such thing,
// 413 too big, 500 anything else.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/library/internal/picture"
	"github.com/Raposa-Industries/adhunters/library/internal/store"
)

// Drive is what the API knows of the Drive sync.
type Drive interface {
	// Kick asks for a pass now; it does not wait for it.
	Kick()
	// Folder is the library folder's id, "" when Drive is off.
	Folder() string
	// Why is the reason Drive is off, "" when it is on.
	Why() string
}

// API serves the library.
type API struct {
	st    *store.Store
	drive Drive
	log   *slog.Logger
}

// New returns the API.
func New(st *store.Store, d Drive, log *slog.Logger) *API {
	return &API{st: st, drive: d, log: log}
}

// Handler routes the API.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("POST /api/drive/sync", a.kick)
	mux.HandleFunc("GET /api/verticals", a.verticals)
	mux.HandleFunc("PATCH /api/verticals/{id}", a.changeVertical)
	mux.HandleFunc("GET /api/sets", a.sets)
	mux.HandleFunc("POST /api/sets", a.addSet)
	mux.HandleFunc("GET /api/sets/{id}", a.set)
	mux.HandleFunc("GET /api/creatives", a.creatives)
	mux.HandleFunc("POST /api/creatives", a.addCreative)
	mux.HandleFunc("GET /api/creatives/{id}", a.creative)
	mux.HandleFunc("PATCH /api/creatives/{id}", a.changeCreative)
	mux.HandleFunc("GET /api/headlines", a.headlines)
	mux.HandleFunc("POST /api/headlines", a.addHeadlines)
	mux.HandleFunc("PATCH /api/headlines/{id}", a.changeHeadline)
	mux.HandleFunc("GET /files/{id}", func(w http.ResponseWriter, r *http.Request) { a.file(w, r, false) })
	mux.HandleFunc("GET /thumbs/{id}", func(w http.ResponseWriter, r *http.Request) { a.file(w, r, true) })
	return onlyServers(mux)
}

// onlyServers refuses browsers: a request with an Origin header came from a
// page, and no page may call the library directly.
func onlyServers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "the library is called by the apps' servers, not by pages")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail answers an error from the store.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var bad store.BadInput
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrGone):
		writeError(w, http.StatusGone, "this picture's file was deleted from Drive")
	case errors.Is(err, store.ErrNoDrive):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		a.log.Error("library request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "the library failed; see its log")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad id")
		return 0, false
	}
	return id, true
}

// ---- status ------------------------------------------------------------------

type lastRun struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Written    int        `json:"written"`
	Listed     int        `json:"listed"`
	Added      int        `json:"added"`
	Gone       int        `json:"gone"`
	Error      string     `json:"error"`
}

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	db := a.st.DB()
	out := map[string]any{}
	var creatives, headlines, sets, waiting int
	if err := db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM library.creative WHERE hidden_at IS NULL),
		(SELECT count(*) FROM library.headline WHERE hidden_at IS NULL),
		(SELECT count(*) FROM library.set),
		(SELECT count(*) FROM library.creative WHERE drive_state = 'waiting')`).Scan(&creatives, &headlines, &sets, &waiting); err != nil {
		a.fail(w, r, err)
		return
	}
	out["creatives"], out["headlines"], out["sets"] = creatives, headlines, sets
	dr := map[string]any{"on": a.drive.Why() == "", "why_off": a.drive.Why(), "folder": a.drive.Folder(), "waiting": waiting}
	var account string
	if err := db.QueryRow(ctx, `SELECT account FROM library.drive_login`).Scan(&account); err == nil {
		dr["account"] = account
	}
	var lr lastRun
	err := db.QueryRow(ctx, `SELECT started_at, finished_at, written, listed, added, gone, error
		FROM library.drive_run ORDER BY id DESC LIMIT 1`).Scan(&lr.StartedAt, &lr.FinishedAt, &lr.Written, &lr.Listed, &lr.Added, &lr.Gone, &lr.Error)
	if err == nil {
		dr["last_run"] = lr
	} else if !errors.Is(err, pgx.ErrNoRows) {
		a.fail(w, r, err)
		return
	}
	out["drive"] = dr
	writeJSON(w, http.StatusOK, out)
}

func (a *API) kick(w http.ResponseWriter, _ *http.Request) {
	if why := a.drive.Why(); why != "" {
		writeError(w, http.StatusServiceUnavailable, why)
		return
	}
	a.drive.Kick()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// ---- verticals and sets ------------------------------------------------------

func (a *API) verticals(w http.ResponseWriter, r *http.Request) {
	vs, err := a.st.Verticals(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verticals": vs})
}

func (a *API) changeVertical(w http.ResponseWriter, r *http.Request) {
	var c store.VerticalChange
	if !decode(w, r, &c) {
		return
	}
	v, err := a.st.ChangeVertical(r.Context(), r.PathValue("id"), c)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *API) sets(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ss, err := a.st.Sets(r.Context(), r.URL.Query().Get("vertical"), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sets": ss})
}

func (a *API) addSet(w http.ResponseWriter, r *http.Request) {
	var n store.NewSet
	if !decode(w, r, &n) {
		return
	}
	s, err := a.st.AddSet(r.Context(), n)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

// set returns a set with its creatives and headlines, in order.
func (a *API) set(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	s, err := a.st.GetSet(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cs, err := a.st.Creatives(ctx, store.Filter{SetID: id, Limit: 500})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	hs, err := a.st.Headlines(ctx, store.Filter{SetID: id, Limit: 500})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"set": s, "creatives": cs, "headlines": hs})
}

// ---- creatives and headlines ---------------------------------------------------

func filter(r *http.Request) store.Filter {
	q := r.URL.Query()
	num := func(k string) int64 { n, _ := strconv.ParseInt(q.Get(k), 10, 64); return n }
	return store.Filter{
		VerticalID: q.Get("vertical"), SetID: num("set"), Angle: q.Get("angle"), Origin: q.Get("origin"),
		AILabel: q.Get("ai_label"), Search: q.Get("q"), Hidden: q.Get("hidden") == "1",
		Before: num("before"), Limit: int(num("limit")),
	}
}

func (a *API) creatives(w http.ResponseWriter, r *http.Request) {
	cs, err := a.st.Creatives(r.Context(), filter(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"creatives": cs})
}

func (a *API) creative(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := a.st.Creative(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// addCreative takes multipart: "file" (the picture) and "meta" (JSON, a
// store.NewCreative). 201 for a new creative, 200 when the bytes were kept
// already.
func (a *API) addCreative(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, picture.MaxBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "the picture is over the library's limit")
			return
		}
		writeError(w, http.StatusBadRequest, "send multipart with a file and meta")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	var n store.NewCreative
	if m := r.FormValue("meta"); m != "" {
		if err := json.Unmarshal([]byte(m), &n); err != nil {
			writeError(w, http.StatusBadRequest, "bad meta JSON: "+err.Error())
			return
		}
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "no file")
		return
	}
	b, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, "the file could not be read")
		return
	}
	c, created, err := a.st.AddCreative(r.Context(), n, b)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.drive.Kick()
	}
	writeJSON(w, status, c)
}

func (a *API) changeCreative(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var c store.Change
	if !decode(w, r, &c) {
		return
	}
	out, err := a.st.ChangeCreative(r.Context(), id, c)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) headlines(w http.ResponseWriter, r *http.Request) {
	hs, err := a.st.Headlines(r.Context(), filter(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"headlines": hs})
}

func (a *API) addHeadlines(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Headlines []store.NewHeadline `json:"headlines"`
	}
	if !decode(w, r, &body) {
		return
	}
	hs, err := a.st.AddHeadlines(r.Context(), body.Headlines)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.drive.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"headlines": hs})
}

func (a *API) changeHeadline(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var c store.Change
	if !decode(w, r, &c) {
		return
	}
	out, err := a.st.ChangeHeadline(r.Context(), id, c)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.drive.Kick()
	writeJSON(w, http.StatusOK, out)
}

// file serves a creative's picture (from Drive, once uploaded) or its
// thumbnail. The bytes of an id never change, so they may be cached for good.
func (a *API) file(w http.ResponseWriter, r *http.Request, thumb bool) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rc, mt, err := a.st.Open(r.Context(), id, thumb)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if _, err := io.Copy(w, rc); err != nil && !errors.Is(err, context.Canceled) {
		a.log.Warn("file not sent whole", "creative", id, "err", err)
	}
}
