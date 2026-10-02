package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Raposa-Industries/adhunters/launch/internal/images"
	"github.com/Raposa-Industries/adhunters/launch/internal/library"
)

// The library's lists Launch passes on, and the filters each may carry.
// Hidden creatives and headlines are never asked for.
var libraryLists = map[string][]string{
	"status":    nil,
	"verticals": nil,
	// folders is the library as Create shows it: each vertical with its
	// platforms' folders and its sets (Novos anúncios' folder picker).
	"folders":   {"fresh"},
	"sets":      {"vertical", "limit", "before"},
	"creatives": {"vertical", "set", "angle", "origin", "ai_label", "q", "platform", "limit", "before"},
	"headlines": {"vertical", "set", "angle", "origin", "ai_label", "q", "platform", "limit", "before"},
}

var pictureExt = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}

func (a *API) libraryFail(w http.ResponseWriter, err error) {
	var le *library.Error
	switch {
	case a.Library == nil:
		say(w, http.StatusServiceUnavailable, "O Launch não está ligado à biblioteca.")
	case errors.Is(err, library.ErrDown):
		a.log.Warn("library not answering", "err", err)
		say(w, http.StatusServiceUnavailable, "A biblioteca não responde agora. Tente de novo em instantes.")
	case errors.As(err, &le) && le.Status == http.StatusNotFound:
		say(w, http.StatusNotFound, "Isso não está na biblioteca.")
	case errors.As(err, &le) && le.Status == http.StatusRequestEntityTooLarge:
		say(w, http.StatusRequestEntityTooLarge, "Essa imagem passa de 10 MB; diminua antes de usar.")
	case errors.As(err, &le) && le.Status/100 == 4:
		say(w, http.StatusBadRequest, "A biblioteca recusou: "+le.Message)
	default:
		a.log.Warn("library error", "err", err)
		say(w, http.StatusBadGateway, "A biblioteca falhou. Tente de novo em instantes.")
	}
}

// pass copies the library's answer to the page.
func (a *API) pass(w http.ResponseWriter, r *http.Request, path string, q url.Values) {
	if a.Library == nil {
		a.libraryFail(w, nil)
		return
	}
	res, err := a.Library.Get(r.Context(), path, q)
	if err != nil {
		a.libraryFail(w, err)
		return
	}
	defer res.Body.Close()
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := res.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (a *API) libraryList(w http.ResponseWriter, r *http.Request) {
	what := r.PathValue("what")
	if what == "set" || what == "thumb" {
		a.libraryOne(w, r, what)
		return
	}
	keys, ok := libraryLists[what]
	if !ok {
		say(w, http.StatusNotFound, "endereço desconhecido")
		return
	}
	q := url.Values{}
	for _, k := range keys {
		if v := strings.TrimSpace(r.URL.Query().Get(k)); v != "" {
			q.Set(k, v)
		}
	}
	a.pass(w, r, "/api/"+what, q)
}

func libraryID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	return id, err == nil && id > 0
}

// libraryOne is one set, or one creative's thumbnail.
func (a *API) libraryOne(w http.ResponseWriter, r *http.Request, what string) {
	id, ok := libraryID(r)
	if !ok {
		say(w, http.StatusBadRequest, "id inválido")
		return
	}
	path := "/api/sets/"
	if what == "thumb" {
		path = "/thumbs/"
	}
	a.pass(w, r, path+strconv.FormatInt(id, 10), nil)
}

// libraryUse brings a creative's picture into Launch's own pictures, as an
// upload would, so a pair can use it.
func (a *API) libraryUse(w http.ResponseWriter, r *http.Request) {
	id, ok := libraryID(r)
	if !ok {
		say(w, http.StatusBadRequest, "id inválido")
		return
	}
	if a.Library == nil {
		a.libraryFail(w, nil)
		return
	}
	c, data, err := a.Library.File(r.Context(), id, images.MaxBytes)
	if err != nil {
		a.libraryFail(w, err)
		return
	}
	name := c.Name
	if name == "" {
		name = "biblioteca-" + strconv.FormatInt(id, 10)
	}
	if ext, ok := pictureExt[http.DetectContentType(data)]; ok && !strings.HasSuffix(strings.ToLower(name), ext) {
		name += ext
	}
	info, err := a.img.Put(name, data)
	if err != nil {
		say(w, http.StatusBadRequest, err.Error())
		return
	}
	if c.SHA256 != "" && c.SHA256 != info.SHA {
		a.log.Error("library picture changed on the way", "creative", id, "library_sha", c.SHA256, "sha", info.SHA)
		say(w, http.StatusBadGateway, "A imagem chegou diferente da biblioteca. Tente de novo.")
		return
	}
	send(w, http.StatusOK, map[string]any{"image": info, "creative": c})
}

// fingerprint is a creative's in our ad ids: 10 hex characters.
var fingerprint = regexp.MustCompile(`^[0-9a-f]{10}$`)

// used counts the ads Launch made with each picture, by its fingerprint
// (?creatives=<first 10 hex of the SHA-256>,…; at most 200): {used: {fp: n}}.
// Novos anúncios shows it under the library's pictures ("no Launch: 2
// anúncios").
func (a *API) used(w http.ResponseWriter, r *http.Request) {
	var fps []string
	for _, f := range strings.Split(r.URL.Query().Get("creatives"), ",") {
		if f = strings.ToLower(strings.TrimSpace(f)); fingerprint.MatchString(f) && len(fps) < 200 {
			fps = append(fps, f)
		}
	}
	n, err := a.l.Store().ItemsByCreative(r.Context(), fps)
	if err != nil {
		a.log.Error("ads by creative", "err", err)
		say(w, http.StatusInternalServerError, "Não consegui contar os anúncios agora.")
		return
	}
	send(w, http.StatusOK, map[string]any{"used": n})
}
