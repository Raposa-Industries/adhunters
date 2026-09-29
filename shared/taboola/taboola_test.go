package taboola

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var ctx = context.Background()

// fake answers like Backstage: tokens tok-1, tok-2…; answer decides the rest.
func fake(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, n int)) (*Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var tokens, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == TokenPath {
			r.ParseForm()
			if r.Form.Get("client_secret") != "s3cret" {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"invalid_client"}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":43200}`, tokens.Add(1))
			return
		}
		answer(w, r, int(calls.Add(1)))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "id", "s3cret", nil)
	c.Wait = func(context.Context, time.Duration) error { return nil }
	return c, &tokens, &calls
}

func TestTokenCachedAndRefreshedOnceOn401(t *testing.T) {
	c, tokens, calls := fake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 3 || n == 4 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{}`)
	})
	for range 2 {
		if _, err := c.Do(ctx, Request{Method: "GET", Path: "a/campaigns/"}); err != nil {
			t.Fatal(err)
		}
	}
	// Two 401s in a row: one new token, then the second 401 is the answer.
	_, err := c.Do(ctx, Request{Method: "POST", Path: "a/campaigns/"})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 401 || tokens.Load() != 2 || calls.Load() != 4 {
		t.Fatalf("err %v tokens %d calls %d", err, tokens.Load(), calls.Load())
	}
}

func TestRetries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   []int
		retry5xx bool
		calls    int
		ok       bool
	}{
		{"429 always repeated", []int{429}, false, 2, true},
		{"5xx repeated when safe", []int{503, 502}, true, 3, true},
		{"5xx gives up after MaxRetries", []int{500, 500, 500, 500}, true, 4, false},
		{"5xx never repeated on a create", []int{500}, false, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, calls := fake(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if n <= len(tc.status) {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(tc.status[n-1])
					return
				}
				io.WriteString(w, `{"id":"9"}`)
			})
			var waits []time.Duration
			c.Wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
			res, err := c.Do(ctx, Request{Method: "POST", Path: "a/campaigns/", Retry5xx: tc.retry5xx})
			if (err == nil) != tc.ok || int(calls.Load()) != tc.calls {
				t.Fatalf("err %v after %d calls", err, calls.Load())
			}
			if tc.ok && res.Retries != tc.calls-1 {
				t.Errorf("retries %d", res.Retries)
			}
			for _, d := range waits {
				if d != 7*time.Second {
					t.Errorf("waited %v, not Retry-After", d)
				}
			}
		})
	}
}

func TestRecordSeesEveryAttemptAndCanWithhold(t *testing.T) {
	c, _, _ := fake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	})
	var seen []Exchange
	c.Record = func(ex Exchange) error { seen = append(seen, ex); return nil }
	if _, err := c.Do(ctx, Request{Method: "POST", Path: "a/x", Body: []byte(`{}`), Log: "logged"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Status != 429 || seen[1].Attempt != 2 || seen[1].Log != "logged" || string(seen[1].Body) != `{"ok":true}` {
		t.Fatalf("%+v", seen)
	}
	for _, ex := range seen {
		if strings.Contains(fmt.Sprint(ex), "tok-") || strings.Contains(fmt.Sprint(ex), "s3cret") {
			t.Errorf("record saw a secret: %+v", ex)
		}
	}
	c.Record = func(Exchange) error { return errors.New("disk full") }
	res, err := c.Do(ctx, Request{Method: "GET", Path: "a/x"})
	var re *RecordError
	if res != nil || !errors.As(err, &re) || re.Status != 200 {
		t.Fatalf("res %v err %v", res, err)
	}
}

func TestNoAnswerIsRecorded(t *testing.T) {
	c := New("http://127.0.0.1:1", "id", "s3cret", nil)
	c.token, c.expires = "tok", time.Now().Add(time.Hour)
	var seen []Exchange
	c.Record = func(ex Exchange) error { seen = append(seen, ex); return nil }
	_, err := c.Do(ctx, Request{Method: "GET", Path: "a/x"})
	var se *SendError
	if !errors.As(err, &se) || len(seen) != 1 || seen[0].Err == nil || seen[0].Status != 0 {
		t.Fatalf("err %v seen %+v", err, seen)
	}
}

func TestTokenErrors(t *testing.T) {
	c, _, _ := fake(t, func(http.ResponseWriter, *http.Request, int) {})
	bad := New(c.base, "id", "wrong", nil)
	_, err := bad.Do(ctx, Request{Method: "GET", Path: "a/x"})
	var te *TokenError
	if !errors.As(err, &te) || te.Status != 401 || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("%v", err)
	}
	if _, err := New(c.base, "", "", nil).Token(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestImageFormNamesTheImagesType(t *testing.T) {
	body, ctype, err := ImageForm("x.png", []byte("\x89PNG\r\n\x1a\n...."))
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := strings.Cut(ctype, "boundary=")
	r := multipart.NewReader(strings.NewReader(string(body)), params)
	p, err := r.NextPart()
	if err != nil || p.FormName() != "file" || p.FileName() != "x.png" || p.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("%v %v", p, err)
	}
}
