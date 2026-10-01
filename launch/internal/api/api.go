// Package api is Launch's HTTP API under /launch/api/, for its own pages.
// Answers are JSON; an error is {"error": "<one pt-BR line>"} with 400 for
// what was refused before reaching the network, 404 for what is not here,
// 503 when the network is not connected and 502 when it refused or failed.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Raposa-Industries/adhunters/launch/internal/library"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Raposa-Industries/adhunters/launch/internal/actions"
	"github.com/Raposa-Industries/adhunters/launch/internal/images"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

// Classify gives an error its HTTP status and pt-BR line.
type Classify func(error) (int, string)

// API serves Launch's pages.
type API struct {
	l        *actions.Launch
	img      *images.Store
	log      *slog.Logger
	classify Classify
	// base is the context sends run under, so they end with the server.
	base context.Context
	// Limits are shown on the page (the network's ceilings).
	Limits map[string]any
	// Library is the team's library, for a new pair's pictures and
	// headlines; nil when there is none.
	Library *library.Client

	mu   sync.Mutex
	jobs map[string]*job
	keys map[string]string // send key -> job id, so one send is never made twice
	// shown keeps lists the pages read often for a short while (views.go).
	shown *lists
}

// New returns the API. base ends when the server stops.
func New(base context.Context, l *actions.Launch, img *images.Store, log *slog.Logger, classify Classify) *API {
	return &API{l: l, img: img, log: log, classify: classify, base: base, jobs: map[string]*job{}, keys: map[string]string{}, shown: newLists()}
}

// Who is the signed-in person: Cloudflare Access puts their email in this
// header on every request that reaches the server.
func Who(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Cf-Access-Authenticated-User-Email"))
}

// asker is who else asked for a change: "intel:311" when a person opened
// one of Intel's one-tap actions, "desk:42" for a Desk request.
var asker = regexp.MustCompile(`^(intel|desk):[A-Za-z0-9_:-]{1,60}$`)

func who(r *http.Request) actions.Who {
	w := actions.Who{Person: Who(r)}
	if from := strings.TrimSpace(r.URL.Query().Get("from")); asker.MatchString(from) {
		w.AskedBy = from
	}
	return w
}

// Handler routes /launch/api/.
func (a *API) Handler() http.Handler {
	m := http.NewServeMux()
	p := "/launch/api/"
	m.HandleFunc("GET "+p+"status", a.status)
	m.HandleFunc("GET "+p+"search", a.search)
	m.HandleFunc("GET "+p+"accounts/{net}", a.accounts)
	m.HandleFunc("GET "+p+"{net}/{account}/tree", a.tree)
	m.HandleFunc("GET "+p+"{net}/{account}/next", a.next)
	m.HandleFunc("GET "+p+"{net}/{account}/ads", a.ads)
	m.HandleFunc("GET "+p+"numbers", a.numbers)
	m.HandleFunc("GET "+p+"{net}/{account}/campaigns/{id}", a.campaign)
	m.HandleFunc("POST "+p+"{net}/{account}/groups", a.newGroup)
	m.HandleFunc("POST "+p+"{net}/{account}/move", a.move)
	m.HandleFunc("POST "+p+"{net}/{account}/duplicate", a.duplicate)
	m.HandleFunc("POST "+p+"{net}/{account}/pause", a.pause)
	m.HandleFunc("POST "+p+"{net}/{account}/change", a.change)
	m.HandleFunc("POST "+p+"{net}/{account}/add-ads", a.addAds)
	m.HandleFunc("POST "+p+"{net}/{account}/campaigns/{id}/pause-ads", a.pauseAds)
	m.HandleFunc("POST "+p+"moves/{id}/cancel", a.cancelMove)
	m.HandleFunc("POST "+p+"pairs", a.newPair)
	m.HandleFunc("GET "+p+"jobs/{id}", a.job)
	m.HandleFunc("GET "+p+"presets", a.presets)
	m.HandleFunc("POST "+p+"presets", a.savePreset)
	m.HandleFunc("PUT "+p+"presets/{id}", a.savePreset)
	m.HandleFunc("DELETE "+p+"presets/{id}", a.deletePreset)
	m.HandleFunc("GET "+p+"history", a.history)
	m.HandleFunc("GET "+p+"drafts", a.drafts)
	m.HandleFunc("GET "+p+"drafts/{id}", a.draft)
	m.HandleFunc("POST "+p+"drafts", a.saveDraft)
	m.HandleFunc("PUT "+p+"drafts/{id}", a.saveDraft)
	m.HandleFunc("DELETE "+p+"drafts/{id}", a.deleteDraft)
	m.HandleFunc("GET "+p+"requests", a.requests)
	m.HandleFunc("GET "+p+"requests/{id}", a.request)
	m.HandleFunc("POST "+p+"requests/{id}/confirm", a.decide)
	m.HandleFunc("POST "+p+"requests/{id}/refuse", a.decide)
	m.HandleFunc("POST "+p+"images", a.putImage)
	m.HandleFunc("GET "+p+"images/{sha}", a.getImage)
	// One set, one thumbnail and "use this creative" take ?id=: a third
	// path segment would clash with {net}/{account}/….
	m.HandleFunc("GET "+p+"library/{what}", a.libraryList)
	m.HandleFunc("POST "+p+"library/use", a.libraryUse)
	m.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { say(w, http.StatusNotFound, "endereço desconhecido") })
	return a.forgetOnWrite(m)
}

func send(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func say(w http.ResponseWriter, code int, msg string) { send(w, code, map[string]string{"error": msg}) }

func (a *API) fail(w http.ResponseWriter, err error) {
	code, msg := a.classify(err)
	if code >= 500 {
		a.log.Warn("launch api error", "status", code, "err", err)
	}
	say(w, code, msg)
}

// read decodes a JSON body of at most 4 MB.
func read(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		say(w, http.StatusBadRequest, "pedido inválido: "+err.Error())
		return false
	}
	return true
}

func (a *API) net(w http.ResponseWriter, r *http.Request) (network.Network, bool) {
	n, err := a.l.Net(r.PathValue("net"))
	if err != nil {
		a.fail(w, err)
		return nil, false
	}
	return n, true
}

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	var nets []map[string]any
	for _, n := range a.l.Networks() {
		ok, why := n.Available()
		nets = append(nets, map[string]any{"name": n.Name(), "connected": ok, "reason": why})
	}
	send(w, http.StatusOK, map[string]any{"user": Who(r), "networks": nets, "limits": a.Limits})
}

func (a *API) accounts(w http.ResponseWriter, r *http.Request) {
	n, ok := a.net(w, r)
	if !ok {
		return
	}
	list, err := n.Accounts(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"accounts": list})
}

// tree is everything the account and group pages show: the groups, every
// campaign with its group, and the pairs Launch made.
func (a *API) tree(w http.ResponseWriter, r *http.Request) {
	n, ok := a.net(w, r)
	if !ok {
		return
	}
	acct := r.PathValue("account")
	t, err := a.treeLists(r.Context(), n, acct)
	if err != nil {
		a.fail(w, err)
		return
	}
	groups, camps := t.groups, t.camps
	pairs, err := a.l.Store().Pairs(r.Context(), n.Name(), acct)
	if err != nil {
		a.fail(w, err)
		return
	}
	moves, _ := a.l.Store().Waiting(r.Context())
	var waiting []store.Move
	for _, m := range moves {
		if m.Network == n.Name() && m.Account == acct {
			waiting = append(waiting, m)
		}
	}
	send(w, http.StatusOK, map[string]any{"groups": nonNil(groups), "campaigns": nonNil(camps), "pairs": nonNil(pairs), "moves": nonNil(waiting)})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (a *API) campaign(w http.ResponseWriter, r *http.Request) {
	n, ok := a.net(w, r)
	if !ok {
		return
	}
	acct, id := r.PathValue("account"), r.PathValue("id")
	c, err := n.Campaign(r.Context(), acct, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	ads, err := n.Ads(r.Context(), acct, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	hist, _ := a.l.Store().History(r.Context(), store.Filter{Network: n.Name(), Account: acct, Campaign: id, Limit: 20})
	var pair *store.Pair
	var twin *network.Campaign
	pairs, _ := a.l.Store().Pairs(r.Context(), n.Name(), acct)
	for i, p := range pairs {
		if p.DesktopID == id || p.MobileID == id {
			pair = &pairs[i]
			other := p.MobileID
			if p.MobileID == id {
				other = p.DesktopID
			}
			if other != "" {
				if oc, err := n.Campaign(r.Context(), acct, other); err == nil {
					twin = &oc
				}
			}
			break
		}
	}
	send(w, http.StatusOK, map[string]any{"campaign": c, "ads": ads, "pair": pair, "twin": twin, "history": nonNil(hist)})
}

func (a *API) newGroup(w http.ResponseWriter, r *http.Request) {
	var g network.NewGroup
	if !read(w, r, &g) {
		return
	}
	made, err := a.l.NewGroup(r.Context(), who(r), r.PathValue("net"), r.PathValue("account"), g)
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, made)
}

type many struct {
	Campaigns []string        `json:"campaigns"`
	ToGroup   string          `json:"to_group,omitempty"`
	Originals string          `json:"originals,omitempty"`
	Change    *network.Change `json:"change,omitempty"`
	Ads       []string        `json:"ads,omitempty"`
	NewAds    []network.NewAd `json:"new_ads,omitempty"`
}

func (a *API) doMany(w http.ResponseWriter, r *http.Request, run func(many) ([]actions.Done, error)) {
	var b many
	if !read(w, r, &b) {
		return
	}
	done, err := run(b)
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"done": done})
}

func (a *API) move(w http.ResponseWriter, r *http.Request) {
	a.doMany(w, r, func(b many) ([]actions.Done, error) {
		return a.l.Move(r.Context(), who(r), actions.MoveRequest{Network: r.PathValue("net"), Account: r.PathValue("account"), Campaigns: b.Campaigns, ToGroup: b.ToGroup, Originals: b.Originals})
	})
}

func (a *API) duplicate(w http.ResponseWriter, r *http.Request) {
	a.doMany(w, r, func(b many) ([]actions.Done, error) {
		return a.l.Duplicate(r.Context(), who(r), r.PathValue("net"), r.PathValue("account"), b.Campaigns)
	})
}

func (a *API) pause(w http.ResponseWriter, r *http.Request) {
	a.doMany(w, r, func(b many) ([]actions.Done, error) {
		return a.l.Pause(r.Context(), who(r), r.PathValue("net"), r.PathValue("account"), b.Campaigns)
	})
}

// next is the names the next group and campaign get in the account.
func (a *API) next(w http.ResponseWriter, r *http.Request) {
	n, err := a.l.Next(r.Context(), r.PathValue("net"), r.PathValue("account"))
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, n)
}

func (a *API) addAds(w http.ResponseWriter, r *http.Request) {
	a.doMany(w, r, func(b many) ([]actions.Done, error) {
		return a.l.AddAds(r.Context(), who(r), r.PathValue("net"), r.PathValue("account"), b.Campaigns, b.NewAds)
	})
}

func (a *API) pauseAds(w http.ResponseWriter, r *http.Request) {
	a.doMany(w, r, func(b many) ([]actions.Done, error) {
		return a.l.PauseAds(r.Context(), who(r), r.PathValue("net"), r.PathValue("account"), r.PathValue("id"), b.Ads)
	})
}

func (a *API) change(w http.ResponseWriter, r *http.Request) {
	a.doMany(w, r, func(b many) ([]actions.Done, error) {
		if b.Change == nil {
			return nil, &network.Refused{Message: "diga o que mudar"}
		}
		return a.l.Change(r.Context(), who(r), r.PathValue("net"), r.PathValue("account"), b.Campaigns, *b.Change)
	})
}

func (a *API) cancelMove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		say(w, http.StatusBadRequest, "id inválido")
		return
	}
	if err := a.l.CancelMove(r.Context(), who(r), id); err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}

// job is one send of a new pair, running on after the request that
// started it returns, so the page can show each step and a person may leave.
type job struct {
	ID      string               `json:"id"`
	Started time.Time            `json:"started"`
	Who     string               `json:"who"`
	Steps   []actions.Step       `json:"steps"`
	Done    bool                 `json:"done"`
	Result  *actions.PairResult  `json:"result,omitempty"`
	Error   string               `json:"error,omitempty"`
	Request *actions.PairRequest `json:"-"`
}

type pairBody struct {
	actions.PairRequest
	// Key is the page's id for this send: the same key twice is one send.
	Key string `json:"key"`
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *API) newPair(w http.ResponseWriter, r *http.Request) {
	var b pairBody
	if !read(w, r, &b) {
		return
	}
	if err := b.Check(); err != nil {
		a.fail(w, err)
		return
	}
	if _, err := a.l.Net(b.Network); err != nil {
		a.fail(w, err)
		return
	}
	a.mu.Lock()
	if id, ok := a.keys[b.Key]; ok && b.Key != "" {
		j := a.jobs[id]
		a.mu.Unlock()
		send(w, http.StatusOK, a.snapshot(j))
		return
	}
	j := &job{ID: newID(), Started: time.Now().UTC(), Who: Who(r), Request: &b.PairRequest}
	a.jobs[j.ID] = j
	if b.Key != "" {
		a.keys[b.Key] = j.ID
	}
	a.forgetOld()
	a.mu.Unlock()

	wh := who(r)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(a.base), 15*time.Minute)
		defer cancel()
		res, err := a.l.NewPair(ctx, wh, b.PairRequest, func(s []actions.Step) {
			a.mu.Lock()
			j.Steps = s
			a.mu.Unlock()
		})
		a.mu.Lock()
		defer a.mu.Unlock()
		j.Done, j.Result = true, &res
		if err != nil {
			_, j.Error = a.classify(err)
		}
	}()
	send(w, http.StatusAccepted, a.snapshot(j))
}

// forgetOld drops sends finished over a day ago. Call with mu held.
func (a *API) forgetOld() {
	for id, j := range a.jobs {
		if j.Done && time.Since(j.Started) > 24*time.Hour {
			delete(a.jobs, id)
			for k, v := range a.keys {
				if v == id {
					delete(a.keys, k)
				}
			}
		}
	}
}

func (a *API) snapshot(j *job) job {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := *j
	c.Steps = append([]actions.Step(nil), j.Steps...)
	return c
}

func (a *API) job(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	j, ok := a.jobs[r.PathValue("id")]
	a.mu.Unlock()
	if !ok {
		say(w, http.StatusNotFound, "esse envio não está mais aqui; confira no Histórico")
		return
	}
	send(w, http.StatusOK, a.snapshot(j))
}

func (a *API) presets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	net := q.Get("network")
	if net == "" {
		net = "taboola"
	}
	list, err := a.l.Store().Presets(r.Context(), net, q.Get("account"))
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"presets": nonNil(list)})
}

func (a *API) savePreset(w http.ResponseWriter, r *http.Request) {
	var p store.Preset
	if !read(w, r, &p) {
		return
	}
	if s := r.PathValue("id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			say(w, http.StatusBadRequest, "id inválido")
			return
		}
		p.ID = id
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Network == "" {
		p.Network = "taboola"
	}
	switch {
	case p.Name == "":
		say(w, http.StatusBadRequest, "dê um nome ao preset")
		return
	case p.Level != "group" && p.Level != "campaign":
		say(w, http.StatusBadRequest, "um preset é de grupo ou de campanha")
		return
	case len(p.Fields) == 0 || p.Fields[0] != '{':
		say(w, http.StatusBadRequest, "o preset não tem campos")
		return
	}
	id, err := a.l.Store().SavePreset(r.Context(), p, Who(r))
	switch {
	case errors.Is(err, store.ErrNameTaken):
		say(w, http.StatusConflict, "já existe um preset com esse nome")
	case errors.Is(err, store.ErrNotFound):
		say(w, http.StatusNotFound, "esse preset não existe mais")
	case err != nil:
		a.fail(w, err)
	default:
		send(w, http.StatusOK, map[string]int64{"id": id})
	}
}

func (a *API) deletePreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		say(w, http.StatusBadRequest, "id inválido")
		return
	}
	if err := a.l.Store().DeletePreset(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		say(w, http.StatusNotFound, "esse preset não existe mais")
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) history(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := a.l.Store().History(r.Context(), store.Filter{Network: q.Get("network"), Account: q.Get("account"), Campaign: q.Get("campaign"), Limit: limit})
	if err != nil {
		a.fail(w, err)
		return
	}
	moves, _ := a.l.Store().Waiting(r.Context())
	send(w, http.StatusOK, map[string]any{"history": nonNil(list), "moves": nonNil(moves)})
}

func (a *API) drafts(w http.ResponseWriter, r *http.Request) {
	list, err := a.l.Store().Drafts(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"drafts": nonNil(list)})
}

func draftID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		say(w, http.StatusBadRequest, "id inválido")
		return 0, false
	}
	return id, true
}

func (a *API) draft(w http.ResponseWriter, r *http.Request) {
	id, ok := draftID(w, r)
	if !ok {
		return
	}
	d, err := a.l.Store().Draft(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		say(w, http.StatusNotFound, "esse rascunho não existe mais")
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, d)
}

func (a *API) saveDraft(w http.ResponseWriter, r *http.Request) {
	var d store.Draft
	if !read(w, r, &d) {
		return
	}
	d.ID = 0
	if r.PathValue("id") != "" {
		id, ok := draftID(w, r)
		if !ok {
			return
		}
		d.ID = id
	}
	if d.Network == "" {
		d.Network = "taboola"
	}
	if len(d.Body) == 0 || d.Body[0] != '{' {
		say(w, http.StatusBadRequest, "rascunho vazio")
		return
	}
	d.MadeBy = Who(r)
	id, err := a.l.Store().SaveDraft(r.Context(), d)
	if errors.Is(err, store.ErrNotFound) {
		say(w, http.StatusNotFound, "esse rascunho não existe mais")
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]int64{"id": id})
}

func (a *API) deleteDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := draftID(w, r)
	if !ok {
		return
	}
	if err := a.l.Store().DeleteDraft(r.Context(), id); err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}

// putImage keeps one uploaded picture (multipart field "image").
func (a *API) putImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, images.MaxBytes+1<<20)
	f, h, err := r.FormFile("image")
	if err != nil {
		say(w, http.StatusBadRequest, "envie a imagem no campo image (até 10 MB)")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		say(w, http.StatusBadRequest, "imagem cortada no envio")
		return
	}
	info, err := a.img.Put(h.Filename, data)
	if err != nil {
		say(w, http.StatusBadRequest, err.Error())
		return
	}
	send(w, http.StatusOK, info)
}

func (a *API) getImage(w http.ResponseWriter, r *http.Request) {
	data, _, err := a.img.Get(r.PathValue("sha"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

// search finds campaigns and groups by name or id in every connected
// account, for ⌘K. Accounts are read at most once a minute.
func (a *API) search(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	type hit struct {
		Title string `json:"title"`
		Sub   string `json:"sub"`
		Href  string `json:"href"`
		rank  int
	}
	var hits []hit
	if q == "" {
		send(w, http.StatusOK, map[string]any{"results": []hit{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	for _, n := range a.l.Networks() {
		if ok, _ := n.Available(); !ok {
			continue
		}
		accts, err := n.Accounts(ctx)
		if err != nil {
			continue
		}
		for _, acct := range accts {
			idx, err := a.index(ctx, n, acct)
			if err != nil {
				continue
			}
			for _, e := range idx {
				name := strings.ToLower(e.Title)
				switch {
				case e.id == q:
					e.rank = 0
				case strings.HasPrefix(name, q):
					e.rank = 1
				case strings.Contains(name, q) || strings.Contains(e.id, q):
					e.rank = 2
				default:
					continue
				}
				hits = append(hits, hit{Title: e.Title, Sub: e.Sub, Href: e.Href, rank: e.rank})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	if len(hits) > 30 {
		hits = hits[:30]
	}
	send(w, http.StatusOK, map[string]any{"results": nonNil(hits)})
}

type entry struct {
	Title, Sub, Href, id string
	rank                 int
}

type cached struct {
	at   time.Time
	list []entry
}

var (
	indexMu sync.Mutex
	indexes = map[string]cached{}
)

func (a *API) index(ctx context.Context, n network.Network, acct network.Account) ([]entry, error) {
	key := n.Name() + "/" + acct.ID
	indexMu.Lock()
	c, ok := indexes[key]
	indexMu.Unlock()
	if ok && time.Since(c.at) < time.Minute {
		return c.list, nil
	}
	groups, err := n.Groups(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	camps, err := n.Campaigns(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	base := "/launch/" + n.Name() + "/" + acct.ID
	names := map[string]string{}
	var list []entry
	for _, g := range groups {
		names[g.ID] = g.Name
		list = append(list, entry{Title: g.Name, Sub: "grupo · " + acct.Name + " · " + g.ID, Href: base + "/g/" + g.ID, id: g.ID})
	}
	for _, c := range camps {
		g := c.GroupID
		if g == "" {
			g = "-"
		}
		list = append(list, entry{Title: c.Name, Sub: "campanha · " + acct.Name + " · " + names[c.GroupID] + " · " + c.ID, Href: base + "/g/" + g + "/c/" + c.ID, id: c.ID})
	}
	indexMu.Lock()
	indexes[key] = cached{at: time.Now(), list: list}
	indexMu.Unlock()
	return list, nil
}
