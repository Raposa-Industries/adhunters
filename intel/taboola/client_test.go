package taboola

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() { sleep = func(context.Context, time.Duration) error { return nil } }

// fake is a Backstage stand-in that counts tokens handed out.
func fake(t *testing.T, api http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var tokens atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+tokenPath, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_secret") != "s3cret" || r.Form.Get("grant_type") != "client_credentials" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		n := tokens.Add(1)
		w.Write([]byte(`{"access_token":"tok` + string(rune('0'+n)) + `","token_type":"bearer","expires_in":43200}`))
	})
	mux.HandleFunc(apiPrefix, api)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &tokens
}

func TestGetReusesToken(t *testing.T) {
	srv, tokens := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"results":[{"id":"1","status":"RUNNING"},{"id":"2","cpc":0.3}]}`))
	})
	c := New(srv.URL, "id", "s3cret")
	for range 3 {
		res, err := c.Campaigns(context.Background(), "acme-sc")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := Results(res.Body)
		if err != nil || len(rows) != 2 {
			t.Fatalf("rows %v err %v", rows, err)
		}
		if got := strings.Join(rows.Fields(), ","); got != "cpc,id,status" {
			t.Fatalf("fields %s", got)
		}
	}
	if tokens.Load() != 1 {
		t.Fatalf("fetched %d tokens, want 1", tokens.Load())
	}
}

func TestRefusesWrites(t *testing.T) {
	var hits atomic.Int32
	srv, _ := fake(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	c := New(srv.URL, "id", "s3cret")
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, _ := http.NewRequest(m, srv.URL+apiPrefix+"acme-sc/campaigns/1", strings.NewReader(`{"is_active":false}`))
		if _, err := c.http.Do(req); !errors.Is(err, ErrWriteRefused) {
			t.Fatalf("%s: got %v, want ErrWriteRefused", m, err)
		}
	}
	// A GET outside the API (say, the token path) is refused too.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+tokenPath, nil)
	if _, err := c.http.Do(req); !errors.Is(err, ErrWriteRefused) {
		t.Fatalf("GET token path: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server saw %d requests", hits.Load())
	}
}

func TestRetriesRateLimitAndNewToken(t *testing.T) {
	var calls atomic.Int32
	srv, tokens := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusUnauthorized) // token revoked
		case 2:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Write([]byte(`{"id":"acme-sc"}`))
		}
	})
	c := New(srv.URL, "id", "s3cret")
	res, err := c.CurrentAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Retries != 2 || tokens.Load() != 2 {
		t.Fatalf("retries %d tokens %d", res.Retries, tokens.Load())
	}
	rows, _ := Results(res.Body)
	if len(rows) != 1 || rows[0]["id"] != "acme-sc" {
		t.Fatalf("rows %v", rows)
	}
}

func TestErrorsKeepTheAnswer(t *testing.T) {
	srv, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"no permission"}`))
	})
	c := New(srv.URL, "id", "s3cret")
	res, err := c.Report(context.Background(), "acme-sc", "campaign-summary", "day", time.Now(), time.Now(), nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 403 || res == nil || len(res.Body) == 0 {
		t.Fatalf("res %v err %v", res, err)
	}
}

func TestBadSecret(t *testing.T) {
	srv, _ := fake(t, func(http.ResponseWriter, *http.Request) {})
	_, err := New(srv.URL, "id", "wrong").CurrentAccount(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("err %v", err)
	}
}
