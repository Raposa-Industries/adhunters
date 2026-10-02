package ops

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// httpBuckets go from 10 ms to a minute: pages, API calls and the apps'
// streams (a Create turn) all fit.
var httpBuckets = []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}

type httpMetrics struct {
	requests *prometheus.CounterVec
	seconds  *prometheus.HistogramVec
	inFlight *prometheus.GaugeVec
}

// HTTP wraps h so every request it answers is counted by route and status
// (adhunters_http_requests_total{route,code}), timed by route
// (adhunters_http_request_seconds{route}) and, while it runs, counted in
// adhunters_http_requests_in_flight{route}. route names what a request is
// for from a short fixed list (an app, a page), never from the path itself:
// every value is a series of its own. A stream counts when it ends.
func (s *Server) HTTP(h http.Handler, route func(*http.Request) string) http.Handler {
	s.httpOnce.Do(func() {
		s.http = &httpMetrics{
			requests: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "adhunters_http_requests_total",
				Help: "Requests answered, by route and status code.",
			}, []string{"route", "code"}),
			seconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "adhunters_http_request_seconds",
				Help:    "How long requests took to answer, to the last byte, by route.",
				Buckets: httpBuckets,
			}, []string{"route"}),
			inFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "adhunters_http_requests_in_flight",
				Help: "Requests being answered now, by route.",
			}, []string{"route"}),
		}
		s.Registry.MustRegister(s.http.requests, s.http.seconds, s.http.inFlight)
	})
	m := s.http
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := route(r)
		running := m.inFlight.WithLabelValues(name)
		running.Inc()
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		defer func() {
			running.Dec()
			m.seconds.WithLabelValues(name).Observe(time.Since(start).Seconds())
			m.requests.WithLabelValues(name, strconv.Itoa(sw.code())).Inc()
		}()
		h.ServeHTTP(sw, r)
	})
}

// statusWriter remembers the status an answer went out with. Unwrap lets
// http.ResponseController reach the writer beneath, so streams still flush
// and upgrades still take over the connection through it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush serves handlers that look for http.Flusher on the writer itself.
func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// code is the status sent, or 200 when the handler wrote nothing, as
// net/http then sends.
func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// outbound counts the calls our services make to outside services, for every
// Server in the process (see Register).
var outbound = struct {
	requests *prometheus.CounterVec
	seconds  *prometheus.HistogramVec
}{
	requests: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "adhunters_outbound_requests_total",
		Help: "Calls to outside services, by provider and status code (\"error\" when no answer came).",
	}, []string{"provider", "code"}),
	seconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "adhunters_outbound_request_seconds",
		Help:    "How long calls to outside services took to answer, to the headers, by provider.",
		Buckets: httpBuckets,
	}, []string{"provider"}),
}

// Transport counts and times every request sent through rt (nil means
// http.DefaultTransport) to an outside service: provider names the service
// ("taboola", "openai", "telegram"). Each try is one request, so retries
// show. Use it where the client is built, once per client:
//
//	hc := &http.Client{Timeout: time.Minute, Transport: ops.Transport("redtrack", nil)}
func Transport(provider string, rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &transport{provider: provider, next: rt}
}

type transport struct {
	provider string
	next     http.RoundTripper
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	res, err := t.next.RoundTrip(r)
	outbound.seconds.WithLabelValues(t.provider).Observe(time.Since(start).Seconds())
	code := "error"
	if err == nil {
		code = strconv.Itoa(res.StatusCode)
	}
	outbound.requests.WithLabelValues(t.provider, code).Inc()
	return res, err
}
