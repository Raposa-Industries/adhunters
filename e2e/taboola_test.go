package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeTaboola is a Backstage stand-in that records every request it gets
// and answers like the real API does for the calls Launch makes (shapes from
// shared/taboola/write and research/taboola-api). It never turns anything
// on: what it is sent is what it keeps, so the rules are checked on what
// Launch sent.
type fakeTaboola struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	reqs     []tbRequest
	next     int
	accounts []string
	groups   map[string][]map[string]any // account -> groups
	camps    map[string][]map[string]any // account -> campaigns
	items    map[string][]map[string]any // campaign id -> items
}

// tbRequest is one request as it arrived. Path is relative to
// /backstage/api/1.0/; Body is the decoded JSON (nil for an image upload or
// a read).
type tbRequest struct {
	Method string
	Path   string
	Body   any
	Raw    string
}

const tbPrefix = "/backstage/api/1.0/"

func newFakeTaboola(t *testing.T, accounts ...string) *fakeTaboola {
	f := &fakeTaboola{t: t, next: 46000000, accounts: accounts,
		groups: map[string][]map[string]any{}, camps: map[string][]map[string]any{}, items: map[string][]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTaboola) URL() string { return f.srv.URL }

// requests returns a copy of what was sent so far.
func (f *fakeTaboola) requests() []tbRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tbRequest(nil), f.reqs...)
}

func (f *fakeTaboola) id() string {
	f.next++
	return strconv.Itoa(f.next)
}

func (f *fakeTaboola) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/backstage/oauth/token" {
		answer(w, map[string]any{"access_token": "fake-token", "token_type": "bearer", "expires_in": 43200})
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake-token" {
		f.t.Errorf("taboola: %s %s without the token", r.Method, r.URL.Path)
	}
	raw, _ := io.ReadAll(r.Body)
	path, ok := strings.CutPrefix(r.URL.Path, tbPrefix)
	if !ok {
		f.t.Errorf("taboola: unexpected address %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	req := tbRequest{Method: r.Method, Path: path, Raw: string(raw)}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && len(raw) > 0 {
		if err := json.Unmarshal(raw, &req.Body); err != nil {
			f.t.Errorf("taboola: %s %s: body is not JSON: %v", r.Method, path, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	body, _ := req.Body.(map[string]any)
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")

	switch {
	case r.Method == http.MethodGet && path == "users/current/allowed-accounts/":
		var list []map[string]any
		for i, a := range f.accounts {
			list = append(list, map[string]any{"id": 1000 + i, "account_id": a, "name": "ZoltaGroup " + strconv.Itoa(i+1), "type": "PARTNER", "currency": "USD"})
		}
		answer(w, map[string]any{"results": list})
	case r.Method == http.MethodPost && path == "operations/upload-image":
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			f.t.Errorf("taboola: image upload sent as %q", r.Header.Get("Content-Type"))
		}
		answer(w, map[string]any{"value": "https://cdn.taboola.test/libtrc/static/thumbnails/" + f.id() + ".jpg"})
	case len(parts) == 2 && parts[1] == "campaigns_group" && r.Method == http.MethodGet:
		answer(w, map[string]any{"results": nonNil(f.groups[parts[0]])})
	case len(parts) == 2 && parts[1] == "campaigns_group" && r.Method == http.MethodPost:
		g := copyMap(body)
		g["id"], g["status"] = f.id(), status(g)
		f.groups[parts[0]] = append(f.groups[parts[0]], g)
		answer(w, g)
	case len(parts) == 2 && parts[1] == "campaigns" && r.Method == http.MethodGet:
		answer(w, map[string]any{"results": nonNil(f.camps[parts[0]])})
	case len(parts) == 2 && parts[1] == "campaigns" && r.Method == http.MethodPost:
		c := copyMap(body)
		c["id"], c["status"], c["advertiser_id"] = f.id(), status(c), parts[0]
		f.camps[parts[0]] = append(f.camps[parts[0]], c)
		answer(w, c)
	case len(parts) == 3 && parts[1] == "campaigns":
		c := f.campaign(parts[0], parts[2])
		if c == nil {
			http.Error(w, `{"http_status":404,"message":"campaign not found"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost {
			for k, v := range body {
				c[k] = v
			}
		}
		answer(w, c)
	case len(parts) == 4 && parts[1] == "campaigns" && parts[3] == "items" && r.Method == http.MethodGet:
		answer(w, map[string]any{"results": nonNil(f.items[parts[2]])})
	case len(parts) == 5 && parts[1] == "campaigns" && parts[3] == "items" && parts[4] == "mass" && r.Method == http.MethodPost:
		coll, _ := body["collection"].([]any)
		var made []map[string]any
		for _, x := range coll {
			it := copyMap(x.(map[string]any))
			it["id"], it["campaign_id"], it["status"], it["approval_state"] = f.id(), parts[2], "PENDING_APPROVAL", "PENDING"
			f.items[parts[2]] = append(f.items[parts[2]], it)
			made = append(made, it)
		}
		answer(w, map[string]any{"results": made})
	default:
		f.t.Errorf("taboola: no fake answer for %s %s", r.Method, path)
		http.Error(w, `{"http_status":404,"message":"not in the fake"}`, http.StatusNotFound)
	}
}

func (f *fakeTaboola) campaign(account, id string) map[string]any {
	for _, c := range f.camps[account] {
		if c["id"] == id {
			return c
		}
	}
	return nil
}

func answer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func nonNil(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

// turnsOn reports where in v something is switched on: an is_active that is
// not false, at any depth.
func turnsOn(v any, at string) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == "is_active" && e != false {
				out = append(out, fmt.Sprintf("%s.is_active = %v", at, e))
			}
			out = append(out, turnsOn(e, at+"."+k)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, turnsOn(e, fmt.Sprintf("%s[%d]", at, i))...)
		}
	}
	return out
}

// status is Taboola's status for something just made: RUNNING when it was
// made active, PAUSED otherwise.
func status(m map[string]any) string {
	if m["is_active"] == true {
		return "RUNNING"
	}
	return "PAUSED"
}
