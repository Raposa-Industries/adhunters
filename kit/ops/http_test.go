package ops

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPCountsByRouteAndCode(t *testing.T) {
	s := New("create-web", "v1")
	h := s.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/spy/missing":
			http.NotFound(w, r)
		case "/spy/stream":
			w.Write([]byte("a"))
			http.NewResponseController(w).Flush()
		case "/raposa/down":
			w.WriteHeader(http.StatusBadGateway)
		}
	}), func(r *http.Request) string { return strings.Split(r.URL.Path, "/")[1] })
	for _, p := range []string{"/spy/missing", "/spy/stream", "/spy/stream", "/raposa/down", "/raposa/empty"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if p == "/spy/stream" && !rec.Flushed {
			t.Fatal("a stream no longer flushes through the wrapper")
		}
	}
	_, body := get(t, s.Handler(), "/metrics")
	for _, want := range []string{
		`adhunters_http_requests_total{code="404",route="spy"} 1`,
		`adhunters_http_requests_total{code="200",route="spy"} 2`,
		`adhunters_http_requests_total{code="502",route="raposa"} 1`,
		`adhunters_http_requests_total{code="200",route="raposa"} 1`,
		`adhunters_http_request_seconds_count{route="spy"} 3`,
		`adhunters_http_requests_in_flight{route="spy"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportCountsCalls(t *testing.T) {
	s := New("launch-web", "v1")
	ok := Transport("test-ok", roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: http.NoBody}, nil
	}))
	down := Transport("test-down", roundTrip(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	req := httptest.NewRequest("GET", "https://example.com/", nil)
	if _, err := ok.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if _, err := down.RoundTrip(req); err == nil {
		t.Fatal("the error was swallowed")
	}
	_, body := get(t, s.Handler(), "/metrics")
	for _, want := range []string{
		`adhunters_outbound_requests_total{code="429",provider="test-ok"} 1`,
		`adhunters_outbound_requests_total{code="error",provider="test-down"} 1`,
		`adhunters_outbound_request_seconds_count{provider="test-down"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
}
