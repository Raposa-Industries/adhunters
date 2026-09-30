package edge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/funnels/web"
)

// Spool is where the collector writes beacons (shared/spool's Writer).
type Spool interface {
	WriteLine(stream string, line []byte) error
}

// Config is one edge.
type Config struct {
	Sites    *Sites
	Spool    Spool
	Instance string
	Version  string
	// IPKey keys the hash of visitors' addresses. Keep it: a new key makes
	// every address look new.
	IPKey []byte
	// TrustCloudflare takes the visitor's address and country from
	// Cloudflare's headers. Set it only when every request comes through
	// Cloudflare (a tunnel), or anyone could write those headers.
	TrustCloudflare bool
	Log             *slog.Logger
	Metrics         *Metrics
	Now             func() time.Time
}

// Metrics are the edge's live signals.
type Metrics struct {
	Beacons    *prometheus.CounterVec // by outcome
	BeaconByte prometheus.Counter
	Pages      *prometheus.CounterVec // by status class
	Failing    prometheus.Gauge       // 1 while the spool refuses writes
	failing    atomic.Bool
}

// NewMetrics registers the edge's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Beacons: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "funnels_edge_beacons_total", Help: "Beacons received, by outcome (saved, cut, failed, refused).",
		}, []string{"outcome"}),
		BeaconByte: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "funnels_edge_beacon_bytes_total", Help: "Beacon bytes written to the spool.",
		}),
		Pages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "funnels_edge_page_requests_total", Help: "Requests for hosted landing sites, by status class (2xx, 3xx, 4xx, 5xx).",
		}, []string{"status"}),
		Failing: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "funnels_edge_spool_failing", Help: "1 while the last beacon could not be written to the spool.",
		}),
	}
	reg.MustRegister(m.Beacons, m.BeaconByte, m.Pages, m.Failing)
	return m
}

// SpoolFailing says whether the last spool write failed and none worked
// since.
func (m *Metrics) SpoolFailing() bool { return m.failing.Load() }

// Edge answers every request visitors send.
type Edge struct {
	c        Config
	scriptET string
	entropy  io.Reader
}

// New makes an edge.
func New(c Config) *Edge {
	if c.Now == nil {
		c.Now = time.Now
	}
	sum := sha256.Sum256(web.Script)
	return &Edge{c: c, scriptET: `"` + hex.EncodeToString(sum[:8]) + `"`, entropy: ulid.DefaultEntropy()}
}

// Handler routes /e to the collector, /ah.js to the page script and
// everything else to the hosted sites.
func (e *Edge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/e", e.collect)
	mux.HandleFunc("/ah.js", e.script)
	mux.Handle("/", e.countPages(e.c.Sites))
	return mux
}

func (e *Edge) script(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", e.scriptET)
	if r.Header.Get("If-None-Match") == e.scriptET {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(w, r, "ah.js", time.Time{}, bytes.NewReader(web.Script))
}

// collect writes one beacon to the spool as received and answers 204. Pages
// on other domains (the team's current ones) may send here too, so any
// origin is allowed; the beacon carries no cookie of ours.
func (e *Edge) collect(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodOptions:
		h.Set("Access-Control-Allow-Methods", "POST")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
	default:
		e.c.Metrics.Beacons.WithLabelValues("refused").Inc()
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || len(body) == 0 {
		e.c.Metrics.Beacons.WithLabelValues("refused").Inc()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	now := e.c.Now().UTC()
	rec := &Record{
		ID:       ulid.MustNew(ulid.Timestamp(now), e.entropy).String(),
		At:       now,
		Instance: e.c.Instance,
		Version:  e.c.Version,
		Host:     requestHost(r),
		Origin:   clip(r.Header.Get("Origin"), 300),
		Referer:  clip(r.Header.Get("Referer"), 1000),
		UA:       clip(r.Header.Get("User-Agent"), 500),
	}
	addr := remoteIP(r)
	if e.c.TrustCloudflare {
		if v := r.Header.Get("CF-Connecting-IP"); v != "" {
			addr = v
		}
		rec.Country = clip(r.Header.Get("CF-IPCountry"), 2)
	}
	rec.IPHash, rec.Net = HashIP(e.c.IPKey, addr)
	outcome := "saved"
	if len(body) > MaxBody {
		body, rec.Truncated, outcome = body[:MaxBody], true, "cut"
	}
	rec.SetBody(body)
	line, err := rec.Marshal()
	if err == nil {
		err = e.c.Spool.WriteLine(Stream, line)
	}
	if err != nil {
		e.c.Metrics.failing.Store(true)
		e.c.Metrics.Failing.Set(1)
		e.c.Metrics.Beacons.WithLabelValues("failed").Inc()
		e.c.Log.Error("writing a beacon to the spool", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if e.c.Metrics.failing.Swap(false) {
		e.c.Metrics.Failing.Set(0)
	}
	e.c.Metrics.Beacons.WithLabelValues(outcome).Inc()
	e.c.Metrics.BeaconByte.Add(float64(len(line)))
	w.WriteHeader(http.StatusNoContent)
}

func (e *Edge) countPages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		e.c.Metrics.Pages.WithLabelValues(string("12345"[sw.status/100-1]) + "xx").Inc()
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !validTail(s) {
		s = s[:len(s)-1]
	}
	return s
}

// validTail reports whether s does not end inside a UTF-8 sequence.
func validTail(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

// ErrNoKey is returned when the edge has no key for visitors' addresses.
var ErrNoKey = errors.New("FUNNELS_IP_KEY is not set (16 characters or more)")
