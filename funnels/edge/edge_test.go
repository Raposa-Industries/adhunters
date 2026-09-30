package edge

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type memSpool struct {
	mu    sync.Mutex
	lines []string
	fail  bool
}

func (m *memSpool) WriteLine(stream string, line []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("disk full")
	}
	if stream != Stream {
		return errors.New("wrong stream " + stream)
	}
	m.lines = append(m.lines, string(line))
	return nil
}

func newEdge(t *testing.T, trustCF bool) (*Edge, *memSpool, *Sites) {
	t.Helper()
	sp := &memSpool{}
	sites := &Sites{Root: t.TempDir()}
	e := New(Config{
		Sites: sites, Spool: sp, Instance: "a", Version: "test", IPKey: []byte("0123456789abcdef"),
		TrustCloudflare: trustCF, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: NewMetrics(prometheus.NewRegistry()),
		Now:     func() time.Time { return time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC) },
	})
	return e, sp, sites
}

func TestCollectSavesTheBeaconAsReceived(t *testing.T) {
	e, sp, _ := newEdge(t, true)
	body := `{"v":1,"j":"abc","e":[{"k":"view","t":1}]}`
	req := httptest.NewRequest("POST", "https://lp.example.com/e", strings.NewReader(body))
	req.Header.Set("CF-Connecting-IP", "203.0.113.77")
	req.Header.Set("CF-IPCountry", "US")
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone)")
	rw := httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, req)
	if rw.Code != http.StatusNoContent || rw.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("answer %d %v", rw.Code, rw.Header())
	}
	if len(sp.lines) != 1 {
		t.Fatalf("lines = %d", len(sp.lines))
	}
	var rec Record
	if err := json.Unmarshal([]byte(sp.lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Body != body || rec.Host != "lp.example.com" || rec.Country != "US" || rec.Net != "203.0.113.0/24" || len(rec.IPHash) != 16 {
		t.Errorf("record = %+v", rec)
	}
	if strings.Contains(sp.lines[0], "203.0.113.77") {
		t.Error("the visitor's address was stored")
	}
}

func TestCollectIgnoresCloudflareHeadersUnlessTrusted(t *testing.T) {
	e, sp, _ := newEdge(t, false)
	req := httptest.NewRequest("POST", "/e", strings.NewReader("{}"))
	req.RemoteAddr = "198.51.100.9:5555"
	req.Header.Set("CF-Connecting-IP", "203.0.113.77")
	req.Header.Set("CF-IPCountry", "US")
	e.Handler().ServeHTTP(httptest.NewRecorder(), req)
	var rec Record
	_ = json.Unmarshal([]byte(sp.lines[0]), &rec)
	if rec.Net != "198.51.100.0/24" || rec.Country != "" {
		t.Errorf("record = %+v", rec)
	}
}

func TestCollectCutsLongBodiesAndRefusesOthers(t *testing.T) {
	e, sp, _ := newEdge(t, false)
	big := strings.Repeat("x", MaxBody+500)
	rw := httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, httptest.NewRequest("POST", "/e", strings.NewReader(big)))
	var rec Record
	_ = json.Unmarshal([]byte(sp.lines[0]), &rec)
	if !rec.Truncated || len(rec.Body) != MaxBody {
		t.Errorf("cut = %v, len %d", rec.Truncated, len(rec.Body))
	}
	rw = httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, httptest.NewRequest("GET", "/e", nil))
	if rw.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /e = %d", rw.Code)
	}
	rw = httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, httptest.NewRequest("OPTIONS", "/e", nil))
	if rw.Code != http.StatusNoContent {
		t.Errorf("OPTIONS /e = %d", rw.Code)
	}
	sp.fail = true
	rw = httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, httptest.NewRequest("POST", "/e", strings.NewReader("{}")))
	if rw.Code != http.StatusServiceUnavailable || !e.c.Metrics.SpoolFailing() {
		t.Errorf("failed write answered %d", rw.Code)
	}
}

func TestBinaryBodiesRoundTrip(t *testing.T) {
	var r Record
	r.SetBody([]byte{0xff, 0x00, 'a'})
	b, err := r.RawBody()
	if err != nil || string(b) != "\xff\x00a" || r.Body != "" {
		t.Errorf("body %q %v", b, err)
	}
}

func TestScriptIsServedWithAnETag(t *testing.T) {
	e, _, _ := newEdge(t, false)
	rw := httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, httptest.NewRequest("GET", "/ah.js", nil))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "AdHuntersFunnels") {
		t.Fatalf("GET /ah.js = %d", rw.Code)
	}
	req := httptest.NewRequest("GET", "/ah.js", nil)
	req.Header.Set("If-None-Match", rw.Header().Get("ETag"))
	rw = httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, req)
	if rw.Code != http.StatusNotModified {
		t.Errorf("repeat GET = %d", rw.Code)
	}
}

func writeSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func get(t *testing.T, e *Edge, url string) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	e.Handler().ServeHTTP(rw, httptest.NewRequest("GET", url, nil))
	return rw
}

func TestSitesServePublishedVersionsWithTheScript(t *testing.T) {
	e, _, sites := newEdge(t, false)
	src := writeSite(t, map[string]string{
		"index.html":       `<!doctype html><html><head><title>BP</title></head><body>Hi</body></html>`,
		"offer.html":       `<p>no head</p>`,
		"img/a.png":        "PNG",
		"vsl/index.html":   `<head></head>VSL`,
		"own/index.html":   `<head><script src="/ah.js"></script></head>`,
		"_notes/draft.txt": "private",
	})
	if err := sites.SetConfig("lp.example.com", SiteConfig{Clarity: "abc123xyz"}); err != nil {
		t.Fatal(err)
	}
	v1, err := sites.Publish("lp.example.com", src, time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	rw := get(t, e, "https://lp.example.com/?clickid=1")
	body := rw.Body.String()
	if rw.Code != 200 || !strings.Contains(body, `<title>BP</title><style>`) || !strings.Contains(body, `data-site="lp.example.com" data-clarity="abc123xyz"`) {
		t.Fatalf("index = %d %s", rw.Code, body)
	}
	if b := get(t, e, "https://lp.example.com/offer").Body.String(); !strings.HasPrefix(b, "<style>") || !strings.HasSuffix(b, "<p>no head</p>") {
		t.Errorf("offer = %s", b)
	}
	if b := get(t, e, "https://www.lp.example.com/vsl/").Body.String(); !strings.Contains(b, "/ah.js") {
		t.Errorf("www and folder index: %s", b)
	}
	if b := get(t, e, "https://lp.example.com/own/").Body.String(); strings.Count(b, "/ah.js") != 1 {
		t.Errorf("a page loading ah.js itself got it twice: %s", b)
	}
	if rw := get(t, e, "https://lp.example.com/img/a.png"); rw.Body.String() != "PNG" || rw.Header().Get("Content-Type") != "image/png" {
		t.Errorf("asset = %q %s", rw.Body.String(), rw.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/_notes/draft.txt", "/../site.json", "/%2e%2e/site.json", "/missing"} {
		if rw := get(t, e, "https://lp.example.com"+p); rw.Code == 200 || strings.Contains(rw.Body.String(), "clarity") {
			t.Errorf("%s = %d", p, rw.Code)
		}
	}
	if rw := get(t, e, "https://other.example.com/"); rw.Code != 404 {
		t.Errorf("unknown host = %d", rw.Code)
	}

	src2 := writeSite(t, map[string]string{"index.html": `<head></head>second`})
	v2, err := sites.Publish("lp.example.com", src2, time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC))
	if err != nil || v2 == v1 {
		t.Fatalf("second publish %q %v", v2, err)
	}
	if b := get(t, e, "https://lp.example.com/").Body.String(); !strings.HasSuffix(b, "second") {
		t.Errorf("after publish: %s", b)
	}
	if err := sites.Serve("lp.example.com", v1); err != nil {
		t.Fatal(err)
	}
	if b := get(t, e, "https://lp.example.com/").Body.String(); !strings.Contains(b, "Hi") {
		t.Errorf("after rollback: %s", b)
	}
	vs, _ := sites.Versions("lp.example.com")
	if len(vs) != 2 || !vs[0].Current || vs[1].Current {
		t.Errorf("versions = %+v", vs)
	}
	hosts, _ := sites.Hosts()
	if len(hosts) != 1 || hosts[0] != "lp.example.com" {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestPublishRefusesWhatItShouldNotCopy(t *testing.T) {
	sites := &Sites{Root: t.TempDir()}
	now := time.Now()
	if _, err := sites.Publish("LP.example.com", writeSite(t, map[string]string{"index.html": "x"}), now); err == nil {
		t.Error("upper-case host taken")
	}
	if _, err := sites.Publish("lp.example.com", writeSite(t, map[string]string{"a.html": "x"}), now); err == nil {
		t.Error("folder without index.html taken")
	}
	if _, err := sites.Publish("lp.example.com", writeSite(t, map[string]string{"index.html": "x", ".env": "SECRET"}), now); err == nil {
		t.Error("hidden file published")
	}
	src := writeSite(t, map[string]string{"index.html": "x"})
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "p")); err != nil {
		t.Fatal(err)
	}
	if _, err := sites.Publish("lp.example.com", src, now); err == nil {
		t.Error("link published")
	}
	if err := sites.SetConfig("lp.example.com", SiteConfig{Clarity: `"><script>`}); err == nil {
		t.Error("bad Clarity id taken")
	}
}
