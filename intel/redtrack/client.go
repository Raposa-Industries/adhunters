// Package redtrack is a thin client for the RedTrack API (api.redtrack.io).
//
// It keeps every answer as received (Response.Body) so callers can save it
// raw before reading it, and only decodes the envelope: either a bare JSON
// array or {"items": [...], "total": n}. Rows stay as raw JSON fields
// because RedTrack's row shapes are not documented for us yet.
//
// The API key travels as the api_key query parameter, as RedTrack requires.
// It never appears in a Response, an error or a log line.
package redtrack

import (
	"bytes"
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

	"github.com/Raposa-Industries/adhunters/kit/ops"
)

// DefaultBaseURL is RedTrack's API host.
const DefaultBaseURL = "https://api.redtrack.io"

// Client calls the RedTrack API. The zero value is not usable; use New.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// MinGap spaces out all requests, whatever the path.
	MinGap time.Duration
	// Limits spaces out requests to one path (matched by prefix), for the
	// endpoints RedTrack limits on their own. Measured 2026-09-29: /report
	// and /conversions each allow "20 a minute" (a calendar minute) and 2 a
	// second, but one request often uses two of the 20, so about 10 fit.
	// /campaigns, /sources and /me/settings answered 25-30 back-to-back
	// requests with no limit.
	Limits map[string]time.Duration
	// MaxRetries is how many times a 5xx or a network error is retried, with
	// doubling waits.
	MaxRetries int
	// MaxRateRetries is how many times a 429 is retried, each after the
	// server's Retry-After (up to a minute on /report).
	MaxRateRetries int

	key   string
	mu    sync.Mutex
	last  time.Time
	next  map[string]time.Time // per Limits prefix: earliest next start
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

// DefaultLimits keeps the limited endpoints at 10 requests a minute. /tracks
// sends no rate headers on errors, so it is spaced the same to be safe.
var DefaultLimits = map[string]time.Duration{
	PathReport:      6 * time.Second,
	PathConversions: 6 * time.Second,
	PathClicks:      6 * time.Second,
}

// New returns a client for the given API key.
func New(apiKey string) *Client {
	return &Client{
		BaseURL:        DefaultBaseURL,
		HTTP:           &http.Client{Timeout: 60 * time.Second, Transport: ops.Transport("redtrack", nil)},
		MinGap:         200 * time.Millisecond,
		Limits:         DefaultLimits,
		MaxRetries:     3,
		MaxRateRetries: 5,
		key:            apiKey,
		sleep:          sleepCtx,
		now:            time.Now,
	}
}

// Response is one answer, as received, except that the API key is replaced
// by REDACTED wherever the answer repeats it. Query never holds the key.
type Response struct {
	Method   string
	Path     string
	Query    url.Values
	Status   int
	Header   http.Header
	Body     []byte
	At       time.Time
	Duration time.Duration
}

// APIError is a non-2xx answer.
type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("redtrack %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, b)
}

// Get calls GET path with the query q.
func (c *Client) Get(ctx context.Context, path string, q url.Values) (*Response, error) {
	return c.Do(ctx, http.MethodGet, path, q, nil)
}

// Do calls the API. body, when not nil, is sent as JSON. A non-2xx answer
// returns both the Response and an *APIError.
func (c *Client) Do(ctx context.Context, method, path string, q url.Values, body any) (*Response, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("redtrack %s %s: encode body: %w", method, path, err)
		}
	}
	clean := url.Values{}
	for k, v := range q {
		if k != "api_key" {
			clean[k] = v
		}
	}
	u, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/"))
	if err != nil {
		return nil, fmt.Errorf("redtrack %s %s: %w", method, path, err)
	}
	full := url.Values{}
	for k, v := range clean {
		full[k] = v
	}
	full.Set("api_key", c.key)
	u.RawQuery = full.Encode()

	rateTries := 0
	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx, path); err != nil {
			return nil, err
		}
		start := time.Now()
		resp, err := c.once(ctx, method, u.String(), payload)
		if err != nil {
			if ctx.Err() != nil || attempt >= c.MaxRetries {
				return nil, fmt.Errorf("redtrack %s %s: %s", method, path, c.redact(err.Error()))
			}
			if err := c.sleep(ctx, backoff(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}
		// An answer that echoes the key (an error quoting the URL, a redirect)
		// must not carry it on: the key is the one thing changed.
		for k, vs := range resp.header {
			for i := range vs {
				resp.header[k][i] = c.redact(vs[i])
			}
		}
		r := &Response{
			Method: method, Path: path, Query: clean,
			Status: resp.status, Header: resp.header, Body: []byte(c.redact(string(resp.body))),
			At: start.UTC(), Duration: time.Since(start),
		}
		c.hold(path, r.Header)
		if r.Status == http.StatusTooManyRequests && rateTries < c.MaxRateRetries {
			rateTries++
			attempt--
			if err := c.sleep(ctx, backoff(rateTries-1, r.Header.Get("Retry-After"))); err != nil {
				return nil, err
			}
			continue
		}
		if r.Status >= 500 && attempt < c.MaxRetries {
			if err := c.sleep(ctx, backoff(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}
		if r.Status < 200 || r.Status > 299 {
			return r, &APIError{Method: method, Path: path, Status: r.Status, Body: string(r.Body)}
		}
		return r, nil
	}
}

type rawResp struct {
	status int
	header http.Header
	body   []byte
}

func (c *Client) once(ctx context.Context, method, u string, payload []byte) (*rawResp, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &rawResp{status: resp.StatusCode, header: resp.Header, body: b}, nil
}

// limitOf returns the Limits prefix path falls under, or "".
func (c *Client) limitOf(path string) string {
	path = "/" + strings.TrimLeft(path, "/")
	best := ""
	for p := range c.Limits {
		if (path == p || strings.HasPrefix(path, p+"/")) && len(p) > len(best) {
			best = p
		}
	}
	return best
}

// wait keeps MinGap between the starts of any two requests, and the path's
// Limits gap between two requests to that path.
func (c *Client) wait(ctx context.Context, path string) error {
	c.mu.Lock()
	now := c.now()
	next := c.last.Add(c.MinGap)
	lim := c.limitOf(path)
	if lim != "" && c.next[lim].After(next) {
		next = c.next[lim]
	}
	if next.Before(now) {
		next = now
	}
	c.last = next
	if lim != "" {
		if c.next == nil {
			c.next = map[string]time.Time{}
		}
		c.next[lim] = next.Add(c.Limits[lim])
	}
	c.mu.Unlock()
	return c.sleep(ctx, next.Sub(now))
}

// hold reads RedTrack's rate headers: when the minute's allowance for a
// limited path is used up, no request to it starts before the reset.
func (c *Client) hold(path string, h http.Header) {
	lim := c.limitOf(path)
	if lim == "" || strings.TrimSpace(h.Get("X-Ratelimit-Remaining-Minute")) != "0" {
		return
	}
	reset, err := strconv.Atoi(strings.TrimSpace(h.Get("Ratelimit-Reset")))
	if err != nil || reset < 0 || reset > 300 {
		reset = 60
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until := c.now().Add(time.Duration(reset) * time.Second)
	if c.next == nil {
		c.next = map[string]time.Time{}
	}
	if until.After(c.next[lim]) {
		c.next[lim] = until
	}
}

func (c *Client) redact(s string) string {
	if c.key == "" {
		return s
	}
	s = strings.ReplaceAll(s, c.key, "REDACTED")
	return strings.ReplaceAll(s, url.QueryEscape(c.key), "REDACTED")
}

func backoff(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s >= 0 && s <= 300 {
		return time.Duration(s) * time.Second
	}
	return time.Duration(1<<attempt) * 2 * time.Second
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Items decodes a list answer: a bare array, or {"items": [...], "total": n}.
// An empty list may come as null, or as {"items": null}. total is -1 when
// the answer does not give it as a number (/campaigns/v2 sends an object).
func Items(body []byte) (items []json.RawMessage, total int, err error) {
	body = bytes.TrimSpace(body)
	if string(body) == "null" {
		return nil, -1, nil
	}
	if len(body) > 0 && body[0] == '[' {
		err = json.Unmarshal(body, &items)
		return items, -1, err
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, -1, err
	}
	raw, ok := env["items"]
	if !ok {
		return nil, -1, errors.New("redtrack: answer is neither a list nor {items}")
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, -1, fmt.Errorf("redtrack: items: %w", err)
	}
	total = -1
	var n int
	if json.Unmarshal(env["total"], &n) == nil && env["total"] != nil {
		total = n
	}
	return items, total, nil
}

// Pages walks a paged list with page=1,2,… and per=per, calling fn with each
// answer and its items. It stops at a short page, at total, or at maxPages
// (0 means no cap).
func (c *Client) Pages(ctx context.Context, path string, q url.Values, per, maxPages int, fn func(*Response, []json.RawMessage) error) error {
	seen := 0
	for page := 1; maxPages == 0 || page <= maxPages; page++ {
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		pq.Set("page", strconv.Itoa(page))
		pq.Set("per", strconv.Itoa(per))
		r, err := c.Get(ctx, path, pq)
		if err != nil {
			return err
		}
		items, total, err := Items(r.Body)
		if err != nil {
			return fmt.Errorf("redtrack GET %s page %d: %w", path, page, err)
		}
		if err := fn(r, items); err != nil {
			return err
		}
		seen += len(items)
		if len(items) < per || (total >= 0 && seen >= total) {
			return nil
		}
	}
	return nil
}
