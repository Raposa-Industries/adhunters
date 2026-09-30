// Package site is Create's pages and their API, under /create/ on the
// shared shell (shared/frame). The pages are plain files (pages/); every
// change goes through the API, which writes the same rows create_api
// exposes, and the worker does the paid work.
//
// Errors are {"error": "<one line in pt-BR>"}: 400 bad input, 404 not
// found, 413 too big, 500 anything else.
package site

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Raposa-Industries/adhunters/create/internal/briefs"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/shared/frame"
)

//go:embed pages
var pagesFS embed.FS

// Angles are the angles the brief page offers, the team's words (the
// design canvas, 2026-09-29); a person can type others.
var Angles = []string{"Variação próxima", "Colher", "Canudo", "Copinho", "Garrafa", "Antes de tomar", "Reação depois de tomar"}

// Status is what the pages are told about the service.
type Status interface {
	// OpenAIWhy is why making is off, "" when it is on.
	OpenAIWhy() string
}

// Site serves Create.
type Site struct {
	st      *briefs.Store
	browse  http.Handler
	status  Status
	log     *slog.Logger
	version string
}

// New returns the site. browse serves the library's reads (library.Client.Browse).
func New(st *briefs.Store, browse http.Handler, status Status, log *slog.Logger, version string) *Site {
	return &Site{st: st, browse: browse, status: status, log: log, version: version}
}

// Handler routes everything under /create/.
func (s *Site) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/create/_frame/", http.StripPrefix("/create/_frame", frame.Handler()))
	mux.Handle("GET /create/library-api/", http.StripPrefix("/create/library-api", s.browse))

	pages, err := fs.Sub(pagesFS, "pages")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/create/", http.FileServerFS(pages))
	mux.Handle("GET /create/static/", files)
	index := func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(pages, "index.html")
		if err != nil {
			http.Error(w, "missing page", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
	for _, p := range []string{"GET /create/{$}", "GET /create/new", "GET /create/briefs/{id}", "GET /create/library", "GET /create/rules"} {
		mux.HandleFunc(p, index)
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/create/", http.StatusFound) })
	mux.HandleFunc("GET /create", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/create/", http.StatusFound) })

	mux.HandleFunc("GET /create/api/status", s.getStatus)
	mux.HandleFunc("GET /create/api/rules", s.rules)
	mux.HandleFunc("GET /create/api/briefs", s.list)
	mux.HandleFunc("POST /create/api/briefs", s.newBrief)
	mux.HandleFunc("GET /create/api/briefs/{id}", s.detail)
	mux.HandleFunc("PATCH /create/api/briefs/{id}", s.change)
	mux.HandleFunc("POST /create/api/briefs/{id}/references", s.addReference)
	mux.HandleFunc("DELETE /create/api/references/{id}", s.removeReference)
	mux.HandleFunc("POST /create/api/briefs/{id}/read", s.read)
	mux.HandleFunc("POST /create/api/briefs/{id}/make", s.make)
	mux.HandleFunc("POST /create/api/briefs/{id}/save", s.save)
	mux.HandleFunc("PATCH /create/api/options/{id}", s.mark)
	mux.HandleFunc("POST /create/api/options/{id}/again", s.again)
	mux.HandleFunc("GET /create/api/saves/{id}", s.getSave)
	mux.HandleFunc("GET /create/files/options/{id}", s.optionFile)
	mux.HandleFunc("GET /create/files/references/{id}", s.referenceFile)
	return sameSite(mux)
}

// sameSite refuses a change sent from another site's page: Cloudflare
// Access signs people in with a cookie, which a browser would send along.
func sameSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
				writeError(w, http.StatusForbidden, "pedido de outro site")
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "pedido de outro site")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// who is the person asking: Cloudflare Access names them.
func who(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Cf-Access-Authenticated-User-Email"))
}

func (s *Site) getStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": who(r), "openai_why": s.status.OpenAIWhy(), "version": s.version})
}

func (s *Site) rules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"blocked": openai.BlockedWords, "angles": Angles,
		"max_images": openai.MaxImages, "max_headlines": openai.MaxHeadlines,
		"example_verticals": openai.ExampleVerticals(),
	})
}

func (s *Site) list(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.st.Briefs(r.Context(), before, limit)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"briefs": list})
}

// newBriefBody is a new brief, with its references by id ("spy:ad:123",
// "library:creative:45"), as the ?ref= of a link from Spy or the library.
type newBriefBody struct {
	briefs.Input
	Refs []string `json:"refs"`
}

func (s *Site) newBrief(w http.ResponseWriter, r *http.Request) {
	var in newBriefBody
	if !readJSON(w, r, &in) {
		return
	}
	var kinds, ids []string
	for _, ref := range in.Refs {
		kind, id, ok := ParseRef(ref)
		if !ok {
			writeError(w, http.StatusBadRequest, "referência inválida: "+ref)
			return
		}
		kinds, ids = append(kinds, kind), append(ids, id)
	}
	b, err := s.st.NewBrief(r.Context(), in.Input, who(r))
	if s.fail(w, err) {
		return
	}
	for i := range kinds {
		if _, err := s.st.AddReference(r.Context(), b.ID, kinds[i], ids[i]); s.fail(w, err) {
			return
		}
	}
	d, err := s.st.Detail(r.Context(), b.ID)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

// ParseRef reads a reference link: spy:ad:<id> or library:creative:<id>.
func ParseRef(ref string) (kind, id string, ok bool) {
	parts := strings.Split(strings.TrimSpace(ref), ":")
	if len(parts) != 3 || parts[2] == "" {
		return "", "", false
	}
	switch parts[0] + ":" + parts[1] {
	case "spy:ad":
		kind = briefs.RefSpyAd
	case "library:creative":
		kind = briefs.RefLibraryCreative
	default:
		return "", "", false
	}
	if _, err := strconv.ParseInt(parts[2], 10, 64); err != nil {
		return "", "", false
	}
	return kind, parts[2], true
}

func (s *Site) detail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.st.Detail(r.Context(), id)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Site) change(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in briefs.Input
	if !readJSON(w, r, &in) {
		return
	}
	b, err := s.st.Change(r.Context(), id, in)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Site) addReference(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, briefs.MaxReference+1<<20)
		f, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "envie a imagem no campo file")
			return
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, briefs.MaxReference+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "a imagem não chegou inteira")
			return
		}
		ref, err := s.st.AddUpload(r.Context(), id, b)
		if s.fail(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, ref)
		return
	}
	var in struct {
		Ref string `json:"ref"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	kind, refID, ok := ParseRef(in.Ref)
	if !ok {
		writeError(w, http.StatusBadRequest, "referência inválida: use spy:ad:<id> ou library:creative:<id>")
		return
	}
	ref, err := s.st.AddReference(r.Context(), id, kind, refID)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, ref)
}

func (s *Site) removeReference(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.fail(w, s.st.RemoveReference(r.Context(), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Site) read(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := s.st.Read(r.Context(), id)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, b)
}

func (s *Site) make(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var round *briefs.Round
	if r.ContentLength != 0 {
		round = &briefs.Round{}
		if !readJSON(w, r, round) {
			return
		}
	}
	b, err := s.st.Make(r.Context(), id, round)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, b)
}

func (s *Site) save(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		OptionIDs []int64 `json:"option_ids"`
		Name      string  `json:"name"`
		AILabel   string  `json:"ai_label"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v, err := s.st.Save(r.Context(), id, in.OptionIDs, in.Name, in.AILabel, who(r))
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

func (s *Site) getSave(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.st.SaveByID(r.Context(), id)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Site) mark(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var m briefs.Mark
	if !readJSON(w, r, &m) {
		return
	}
	o, err := s.st.MarkOption(r.Context(), id, m)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Site) again(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	o, err := s.st.Again(r.Context(), id, in.Note)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, o)
}

func (s *Site) optionFile(w http.ResponseWriter, r *http.Request) {
	s.file(w, r, s.st.OptionFile)
}

func (s *Site) referenceFile(w http.ResponseWriter, r *http.Request) {
	s.file(w, r, s.st.ReferenceFile)
}

func (s *Site) file(w http.ResponseWriter, r *http.Request, open func(context.Context, int64) (io.ReadCloser, string, error)) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rc, mediaType, err := open(r.Context(), id)
	if s.fail(w, err) {
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", mediaType)
	// An id's bytes never change.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = io.Copy(w, rc)
}

// ---- helpers ---------------------------------------------------------------

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "não encontrado")
		return 0, false
	}
	return id, true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusBadRequest, "envie JSON")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "pedido ilegível: "+err.Error())
		return false
	}
	return true
}

// fail answers an error, and reports whether there was one.
func (s *Site) fail(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var bad briefs.BadInput
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, string(bad))
	case errors.Is(err, briefs.ErrNotFound):
		writeError(w, http.StatusNotFound, "não encontrado")
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "arquivo grande demais")
	default:
		s.log.Error("create request", "err", err)
		writeError(w, http.StatusInternalServerError, "erro no servidor; tente de novo")
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
