// Package taboola is a read-only client for Taboola's Backstage API, the
// API behind the Taboola Ads UI for our own accounts.
//
// It only reads. The one request that is not a GET is the token request;
// anything else is refused before it leaves the process (see readOnly), so a
// bug here cannot create, change, pause or delete a campaign. Writing, when
// it comes, gets its own client and its own decision.
//
// Every answer is handed back raw, with its headers, so callers save it
// before reading it (decision 0003).
package taboola

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBase is Backstage's production host.
const DefaultBase = "https://backstage.taboola.com"

const (
	tokenPath = "/backstage/oauth/token"
	apiPrefix = "/backstage/api/1.0/"
)

// ErrWriteRefused means a request other than a GET (or the token request)
// was about to be sent.
var ErrWriteRefused = errors.New("taboola: client is read-only")

// Client talks to Backstage with client credentials. It is safe for
// concurrent use.
type Client struct {
	base   string
	id     string
	secret string
	http   *http.Client

	// MaxRetries is how often a 429 or 5xx answer is retried (default 3).
	MaxRetries int

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New returns a client for the given host (DefaultBase in production).
func New(base, clientID, clientSecret string) *Client {
	return &Client{
		base:       strings.TrimRight(base, "/"),
		id:         clientID,
		secret:     clientSecret,
		http:       &http.Client{Timeout: 2 * time.Minute, Transport: readOnly{http.DefaultTransport}},
		MaxRetries: 3,
	}
}

// Response is one answer, as received.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Elapsed time.Duration
	Retries int
}

// Get reads path (relative to /backstage/api/1.0/, for example
// "acme-sc/campaigns") with the given query. A non-2xx answer is returned
// with an error of type *StatusError, so the caller can still save it.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	u := c.base + apiPrefix + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	start := time.Now()
	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		res, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		out := &Response{Status: res.StatusCode, Header: res.Header, Body: body, Elapsed: time.Since(start), Retries: attempt}

		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			c.forgetToken() // expired early or revoked: fetch a new one once
			continue
		}
		if (res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500) && attempt < c.MaxRetries {
			if err := sleep(ctx, backoff(res.Header, attempt)); err != nil {
				return out, err
			}
			continue
		}
		if res.StatusCode/100 != 2 {
			return out, &StatusError{Status: res.StatusCode, Body: snippet(body)}
		}
		return out, nil
	}
}

// StatusError is a non-2xx answer.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("taboola: HTTP %d: %s", e.Status, e.Body)
}

// accessToken returns a cached token, fetching one when none is left or the
// current one ends within a minute.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > time.Minute {
		return c.token, nil
	}
	if c.id == "" || c.secret == "" {
		return "", errors.New("taboola: client id or secret missing")
	}
	form := url.Values{
		"client_id":     {c.id},
		"client_secret": {c.secret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		// The body names the problem (bad secret, no API access) and holds
		// no token, so it is safe to show.
		return "", fmt.Errorf("taboola: token request: HTTP %d: %s", res.StatusCode, snippet(body))
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return "", errors.New("taboola: token request: no access_token in the answer")
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 3600
	}
	c.token = t.AccessToken
	c.expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *Client) forgetToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// readOnly refuses every request except GETs under the API and the token
// POST. It sits in the transport so no caller can get around it.
type readOnly struct{ next http.RoundTripper }

func (r readOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, apiPrefix):
	case req.Method == http.MethodPost && req.URL.Path == tokenPath:
	default:
		return nil, fmt.Errorf("%w: %s %s", ErrWriteRefused, req.Method, req.URL.Path)
	}
	return r.next.RoundTrip(req)
}

// backoff honours Retry-After (seconds) and otherwise waits 2, 4, 8 s.
func backoff(h http.Header, attempt int) time.Duration {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, 2*time.Minute)
	}
	return time.Duration(2<<attempt) * time.Second
}

var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
