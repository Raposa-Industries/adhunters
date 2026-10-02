// Package site is Create's pages and their API, under /create/ on the
// shared shell (shared/frame). The page is a chat (pages/): a session in a
// vertical, turns sent with a prompt and the items picked, and the pictures
// and headlines each turn makes. Every change goes through the API, which
// calls the same create_api functions Desk does, and the worker does the
// paid work.
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

	"github.com/Raposa-Industries/adhunters/create/internal/library"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/create/internal/sessions"
	"github.com/Raposa-Industries/adhunters/create/internal/spyad"
	"github.com/Raposa-Industries/adhunters/shared/frame"
	"github.com/Raposa-Industries/adhunters/shared/verticals"
)

//go:embed pages
var pagesFS embed.FS

// Status is what the pages are told about the service.
type Status interface {
	// OpenAIWhy is why making is off, "" when it is on.
	OpenAIWhy() string
	// HeadlineModels are the other headline models that are on (decision
	// 0023); OpenAI's is always there and not listed.
	HeadlineModels() []HeadlineModel
}

// HeadlineModel is a text model that may write a turn's headlines.
type HeadlineModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Library is what the site needs of the library beyond its reads: a
// creative's bytes, to add it to a session.
type Library interface {
	File(ctx context.Context, id string) ([]byte, error)
	RenameSet(ctx context.Context, id int64, name string) error
}

// Spy reads a Spy ad and downloads its picture (spyad.Reader).
type Spy interface {
	Ad(ctx context.Context, id int64) (spyad.Ad, error)
	Picture(ctx context.Context, url string) ([]byte, error)
}

// Site serves Create.
type Site struct {
	st      *sessions.Store
	lib     Library
	spy     Spy
	browse  http.Handler
	status  Status
	log     *slog.Logger
	version string
	vert    []verticalGroup
}

// verticalGroup is one category of the verticals list, as the page shows it.
type verticalGroup struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Verticals []verticalItem `json:"verticals"`
}

type verticalItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// New returns the site. browse serves the library's reads (library.Client.Browse).
func New(st *sessions.Store, lib Library, spy Spy, browse http.Handler, status Status, log *slog.Logger, version string) (*Site, error) {
	list, err := verticals.Load()
	if err != nil {
		return nil, err
	}
	var groups []verticalGroup
	for _, c := range list.Categories {
		g := verticalGroup{ID: c.ID, Name: c.Name}
		for _, v := range c.Verticals {
			g.Verticals = append(g.Verticals, verticalItem{ID: v.ID, Name: v.Name})
		}
		groups = append(groups, g)
	}
	return &Site{st: st, lib: lib, spy: spy, browse: browse, status: status, log: log, version: version, vert: groups}, nil
}

// verticalName is the name of a vertical of the list, "" when it is not one.
func (s *Site) verticalName(id string) string {
	for _, g := range s.vert {
		for _, v := range g.Verticals {
			if v.ID == id {
				return v.Name
			}
		}
	}
	return ""
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
	for _, p := range []string{"GET /create/{$}", "GET /create/s/{id}", "GET /create/library", "GET /create/rules"} {
		mux.HandleFunc(p, index)
	}
	home := func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/create/", http.StatusFound) }
	// The brief pages' old addresses land on the chat.
	for _, p := range []string{"GET /{$}", "GET /create", "GET /create/new", "GET /create/briefs/{id}"} {
		mux.HandleFunc(p, home)
	}

	mux.HandleFunc("GET /create/api/status", s.getStatus)
	mux.HandleFunc("GET /create/api/rules", s.rules)
	mux.HandleFunc("GET /create/api/verticals", s.verticals)
	mux.HandleFunc("GET /create/api/sessions", s.list)
	mux.HandleFunc("POST /create/api/sessions", s.newSession)
	mux.HandleFunc("GET /create/api/sessions/{id}", s.detail)
	mux.HandleFunc("PATCH /create/api/sessions/{id}", s.rename)
	mux.HandleFunc("GET /create/api/spy/{id}", s.spyAd)
	mux.HandleFunc("POST /create/api/spy/{id}/session", s.spySession)
	mux.HandleFunc("POST /create/api/sessions/{id}/turns", s.send)
	mux.HandleFunc("POST /create/api/sessions/{id}/items", s.addItem)
	mux.HandleFunc("POST /create/api/sessions/{id}/saves", s.save)
	mux.HandleFunc("PATCH /create/api/items/{id}", s.editItem)
	mux.HandleFunc("POST /create/api/items/{id}/retry", s.retry)
	mux.HandleFunc("POST /create/api/turns/{id}/interrupt", s.interrupt)
	mux.HandleFunc("GET /create/api/saves/{id}", s.getSave)
	mux.HandleFunc("GET /create/files/items/{id}", s.itemFile)
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

// headlineModels is OpenAI's, then the others that are on.
func (s *Site) headlineModels() []HeadlineModel {
	return append([]HeadlineModel{{ID: "openai", Name: "OpenAI"}}, s.status.HeadlineModels()...)
}

func (s *Site) getStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": who(r), "openai_why": s.status.OpenAIWhy(), "version": s.version,
		"headline_models": s.headlineModels(), "sizes": openai.Sizes, "platforms": []map[string]string{
			{"id": "taboola", "name": "Taboola"}, {"id": "newsbreak", "name": "NewsBreak"}}})
}

func (s *Site) rules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"blocked": openai.BlockedWords, "max_images": sessions.MaxImages, "max_headlines": sessions.MaxHeadlines,
		"max_picked": sessions.MaxPicked,
	})
}

func (s *Site) verticals(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"categories": s.vert})
}

func (s *Site) list(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.st.Sessions(r.Context(), r.URL.Query().Get("vertical"), limit)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Site) newSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		VerticalID string `json:"vertical_id"`
		// Platform is taboola (the default) or newsbreak.
		Platform string `json:"platform"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := s.verticalName(in.VerticalID)
	if name == "" {
		writeError(w, http.StatusBadRequest, "escolha uma vertical da lista")
		return
	}
	v, err := s.st.NewSession(r.Context(), in.Name, in.VerticalID, name, in.Platform, who(r))
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
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

func (s *Site) rename(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	old, err := s.st.Session(ctx, id)
	if s.fail(w, err) {
		return
	}
	v, err := s.st.Rename(ctx, id, in.Name)
	if s.fail(w, err) {
		return
	}
	// Saved already: its library set and Drive folder take the new name.
	if v.LibrarySetID != nil && v.Name != old.Name {
		if err := s.lib.RenameSet(ctx, *v.LibrarySetID, v.Name); err != nil {
			if _, rerr := s.st.Rename(context.WithoutCancel(ctx), id, old.Name); rerr != nil {
				s.log.Error("session name not put back", "session", id, "err", rerr)
			}
			var le *library.Error
			if errors.As(err, &le) && le.Status == http.StatusBadRequest {
				writeError(w, http.StatusBadRequest, "a biblioteca já tem uma pasta com esse nome nesta vertical")
				return
			}
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// spyRef is how an item from a Spy ad points at it.
func spyRef(id int64) string { return "spy:creative:" + strconv.FormatInt(id, 10) }

// spyName is a session's name for a Spy ad: "Spy 123 · Brand".
func spyName(a spyad.Ad) string {
	name := "Spy " + strconv.FormatInt(a.CreativeID, 10)
	brand := strings.NewReplacer("/", " ", "\\", " ").Replace(openai.CleanLine(a.Brand))
	if brand != "" {
		name += " · " + brand
	}
	if r := []rune(name); len(r) > 120 {
		name = strings.TrimSpace(string(r[:120]))
	}
	return name
}

// spyAd is what the page shows before opening a session from a Spy ad.
func (s *Site) spyAd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := s.spy.Ad(r.Context(), id)
	if errors.Is(err, spyad.ErrNotFound) {
		writeError(w, http.StatusNotFound, "o Spy não tem esse anúncio")
		return
	}
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ad": a, "vertical_name": s.verticalName(a.VerticalID), "name": spyName(a)})
}

// spySession opens a session for a Spy ad (decisions/0022): in the ad's
// vertical, or the one the person chose, with the ad's picture and headline
// added. The same ad again returns that session and the items it has.
func (s *Site) spySession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		VerticalID string `json:"vertical_id"`
		Name       string `json:"name"`
		Platform   string `json:"platform"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	a, err := s.spy.Ad(ctx, id)
	if errors.Is(err, spyad.ErrNotFound) {
		writeError(w, http.StatusNotFound, "o Spy não tem esse anúncio")
		return
	}
	if s.fail(w, err) {
		return
	}
	vert := in.VerticalID
	if vert == "" {
		vert = a.VerticalID
	}
	vname := s.verticalName(vert)
	if vname == "" {
		writeError(w, http.StatusBadRequest, "escolha uma vertical da lista")
		return
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		name = spyName(a)
	}
	v, err := s.st.NewSession(ctx, name, vert, vname, in.Platform, who(r))
	if s.fail(w, err) {
		return
	}
	ref := spyRef(id)
	have, err := s.st.ItemsFrom(ctx, v.ID, ref)
	if s.fail(w, err) {
		return
	}
	var picture, headline bool
	picked := []int64{}
	for _, it := range have {
		picture = picture || it.Kind == "image"
		headline = headline || it.Kind == "headline"
		picked = append(picked, it.ID)
	}
	warn := ""
	if !picture {
		b, err := s.spy.Picture(ctx, a.ImageURL)
		var it sessions.Item
		if err == nil {
			it, err = s.st.AddPicture(ctx, v.ID, "spy", ref, b)
		}
		if err != nil {
			s.log.Warn("spy picture not added", "creative", id, "err", err)
			warn = "não deu para trazer a imagem do anúncio; envie-a do computador"
		} else {
			picked = append(picked, it.ID)
		}
	}
	if !headline && strings.TrimSpace(a.Headline) != "" {
		it, err := s.st.AddHeadline(ctx, v.ID, "spy", ref, a.Headline)
		if err != nil {
			s.log.Warn("spy headline not added", "creative", id, "err", err)
		} else {
			picked = append(picked, it.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": v, "picked": picked, "warning": warn})
}

func (s *Site) send(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in sessions.Send
	if !readJSON(w, r, &in) {
		return
	}
	if in.Model != "" {
		on := false
		for _, m := range s.headlineModels() {
			on = on || m.ID == in.Model
		}
		if !on {
			writeError(w, http.StatusBadRequest, "esse modelo de headlines não está ligado")
			return
		}
	}
	t, err := s.st.Send(r.Context(), id, in, who(r))
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

// addItem adds a picture from the person's computer (multipart, field
// file), or JSON: {"headline": text} typed, {"library_creative": id}, or
// {"library_headline": id, "headline": text} from the library.
func (s *Site) addItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, sessions.MaxUpload+1<<20)
		f, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "envie a imagem no campo file")
			return
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, sessions.MaxUpload+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "a imagem não chegou inteira")
			return
		}
		it, err := s.st.AddPicture(r.Context(), id, "upload", "", b)
		if s.fail(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, it)
		return
	}
	var in struct {
		Headline        string `json:"headline"`
		LibraryCreative int64  `json:"library_creative"`
		LibraryHeadline int64  `json:"library_headline"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	var it sessions.Item
	var err error
	switch {
	case in.LibraryCreative > 0:
		ref := strconv.FormatInt(in.LibraryCreative, 10)
		b, ferr := s.lib.File(r.Context(), ref)
		if errors.Is(ferr, library.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "esse criativo não está na biblioteca")
			return
		}
		if ferr != nil {
			s.log.Error("create library file", "creative", ref, "err", ferr)
			writeError(w, http.StatusBadGateway, "a biblioteca não respondeu; tente de novo")
			return
		}
		it, err = s.st.AddPicture(r.Context(), id, "library", ref, b)
	case in.LibraryHeadline > 0:
		it, err = s.st.AddHeadline(r.Context(), id, "library", strconv.FormatInt(in.LibraryHeadline, 10), in.Headline)
	default:
		it, err = s.st.AddHeadline(r.Context(), id, "typed", "", in.Headline)
	}
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

func (s *Site) editItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	it, err := s.st.EditHeadline(r.Context(), id, in.Text)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// retry makes one failed picture again.
func (s *Site) retry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	it, err := s.st.Retry(r.Context(), id)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, it)
}

// interrupt stops a turn: what has not started is not made.
func (s *Site) interrupt(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := s.st.Interrupt(r.Context(), id)
	if s.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Site) save(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ItemIDs []int64 `json:"item_ids"`
		AILabel string  `json:"ai_label"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v, err := s.st.Save(r.Context(), id, in.ItemIDs, in.AILabel, who(r))
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

func (s *Site) itemFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rc, mediaType, err := s.st.ItemFile(r.Context(), id)
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
	var bad sessions.BadInput
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, string(bad))
	case errors.Is(err, sessions.ErrNotFound):
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
