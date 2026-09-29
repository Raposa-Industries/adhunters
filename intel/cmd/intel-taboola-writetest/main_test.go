package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/taboola/act"
)

// backstage is a small stateful stand-in: campaigns and items in maps, items
// crawl on the first read and are approved at once.
type backstage struct {
	mu        sync.Mutex
	next      int
	campaigns map[string]act.Obj
	items     map[string]act.Obj // id -> item, with "campaign_id"
	groups    map[string]act.Obj // AutoGen group per campaign, kept after it is deleted
	writes    []string
}

func (b *backstage) id() string { b.next++; return fmt.Sprint(1000 + b.next) }

// group gives a new campaign its own AutoGen group, as Taboola does.
func (b *backstage) group(c act.Obj) {
	g := b.id()
	b.groups[g] = act.Obj{"id": g, "name": "AutoGen - " + fmt.Sprint(c["name"])}
	c["campaign_group_id"] = g
}

func (b *backstage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.URL.Path == "/backstage/oauth/token" {
		w.Write([]byte(`{"access_token":"tok","expires_in":43200}`))
		return
	}
	if r.URL.Path == "/backstage/api/1.0/operations/upload-image" {
		w.Write([]byte(`{"name":"image_url","value":"http://cdn/test.png"}`))
		return
	}
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/backstage/api/1.0/acme-1-sc/"), "/")
	parts := strings.Split(p, "/")
	var body act.Obj
	json.NewDecoder(r.Body).Decode(&body)
	if r.Method != http.MethodGet {
		b.writes = append(b.writes, r.Method+" "+p)
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	notFound := func() { w.WriteHeader(404); w.Write([]byte(`{"http_status":404,"message":"Resource not found"}`)) }
	switch {
	case parts[0] == "campaigns_group":
		g, ok := b.groups[parts[1]]
		if !ok || g["status"] == "TERMINATED" {
			notFound()
			return
		}
		if r.Method == http.MethodDelete {
			g["status"] = "TERMINATED"
		}
		reply(g)
	case parts[0] == "reports":
		reply(act.Obj{"results": []any{}})
	case p == "campaigns" && r.Method == http.MethodGet:
		rows := []any{act.Obj{"id": "7", "name": "owner's", "status": "PAUSED", "campaign_group_id": "9"}}
		for _, c := range b.campaigns {
			if c["status"] != "TERMINATED" {
				rows = append(rows, c)
			}
		}
		reply(act.Obj{"results": rows})
	case p == "campaigns":
		body["id"] = b.id()
		body["status"], body["approval_state"] = "PAUSED", "APPROVED"
		b.group(body)
		b.campaigns[body["id"].(string)] = body
		reply(body)
	case len(parts) == 3 && parts[2] == "duplicate":
		src := b.campaigns[parts[1]]
		cp := act.Obj{}
		for k, v := range src {
			cp[k] = v
		}
		for k, v := range body {
			cp[k] = v
		}
		cp["id"] = b.id()
		b.group(cp)
		b.campaigns[cp["id"].(string)] = cp
		reply(cp)
	case len(parts) == 2:
		c, ok := b.campaigns[parts[1]]
		if !ok || c["status"] == "TERMINATED" {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodDelete:
			c["status"] = "TERMINATED"
		case http.MethodPost, http.MethodPatch:
			for k, v := range body {
				c[k] = v
			}
		}
		reply(c)
	case len(parts) == 3 && parts[2] == "items" && r.Method == http.MethodGet:
		var rows []any
		for _, it := range b.items {
			if it["campaign_id"] == parts[1] {
				rows = append(rows, it)
			}
		}
		reply(act.Obj{"results": rows})
	case len(parts) == 3 && parts[2] == "items":
		body["id"], body["campaign_id"], body["status"] = b.id(), parts[1], "CRAWLING"
		b.items[body["id"].(string)] = body
		reply(body)
	case len(parts) == 4 && parts[3] == "mass":
		var rows []any
		for _, raw := range body["collection"].([]any) {
			it := raw.(act.Obj)
			it["id"], it["campaign_id"], it["status"], it["approval_state"] = b.id(), parts[1], "PENDING", "APPROVED"
			b.items[it["id"].(string)] = it
			rows = append(rows, it)
		}
		reply(act.Obj{"results": rows})
	case len(parts) == 4:
		it, ok := b.items[parts[3]]
		if !ok || it["status"] == "TERMINATED" {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodGet:
			if it["status"] == "CRAWLING" {
				it["status"], it["approval_state"] = "PENDING", "APPROVED"
			}
		case http.MethodDelete:
			it["status"] = "TERMINATED"
		default:
			for k, v := range body {
				it[k] = v
			}
		}
		reply(it)
	default:
		notFound()
	}
}

func TestAllStepsTouchOnlyTheirOwnObjects(t *testing.T) {
	bs := &backstage{campaigns: map[string]act.Obj{}, items: map[string]act.Obj{}, groups: map[string]act.Obj{"9": {"id": "9", "name": "Group 1"}}}
	srv := httptest.NewServer(bs)
	defer srv.Close()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "raw"), 0o750)
	c, err := act.New(srv.URL, "id", "s", act.Guard{Account: "acme-1-sc", NamePrefix: prefix, MaxCPC: maxCPC, MaxMoney: maxMoney, StateFile: filepath.Join(dir, "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	photo := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(photo, testImage(), 0o640); err != nil {
		t.Fatal(err)
	}
	tt := &tester{c: c, dir: dir, account: "acme-1-sc", url: "https://example.com", sites: []string{"site-a", "site-b"}, wait: time.Millisecond,
		brand: "Acme", imagePath: photo, titles: []string{"Headline one", "Headline two"}}
	c.Record = tt.save
	ctx := context.Background()
	for _, step := range []func(context.Context) error{tt.paused, tt.liveStart, tt.liveOn, tt.liveCut, tt.liveEnd} {
		if err := step(ctx); err != nil {
			t.Fatalf("%v\n%s", err, strings.Join(tt.lines, "\n"))
		}
	}
	tt.writeResults("all")
	for _, w := range bs.writes {
		if strings.Contains(w, "campaigns/7") || strings.Contains(w, "campaigns_group/9") {
			t.Fatalf("touched the owner's campaign: %s", w)
		}
	}
	for id, g := range bs.groups {
		if (id == "9") == (g["status"] == "TERMINATED") {
			t.Errorf("group %s %v: want only the owner's group 9 left", id, g["status"])
		}
	}
	for id, camp := range bs.campaigns {
		if camp["status"] != "TERMINATED" {
			t.Errorf("campaign %s left %v", id, camp["status"])
		}
	}
	if on := len(c.State().Activated); on != 1 {
		t.Fatalf("%d campaigns turned on, want 1", len(c.State().Activated))
	}
	res, _ := os.ReadFile(filepath.Join(dir, "results.md"))
	for _, want := range []string{"| T1 | created campaign", "| T2 | traffic_allocation_mode EVEN", "custom_id", "| T9 | copy", "| T12 | campaign", "ON at", "cut item"} {
		if !strings.Contains(string(res), want) {
			t.Errorf("results lack %q:\n%s", want, res)
		}
	}
	raws, _ := os.ReadDir(filepath.Join(dir, "raw"))
	if len(raws) < 20 {
		t.Fatalf("only %d raw files", len(raws))
	}
}

func TestLiveStartRefusesPlaceholders(t *testing.T) {
	tt := &tester{url: "https://example.com"}
	if err := tt.liveStart(context.Background()); err == nil || !strings.Contains(err.Error(), "-brand") {
		t.Fatalf("want a refusal before any request, got %v", err)
	}
}
