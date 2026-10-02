// Package taboola is the Taboola Backstage API as every service talks to it:
// the client-credentials token, sending one request, retrying what is safe to
// retry, and handing every answer to a recorder before anyone reads it
// (decision 0003). Create and Intel build their own clients on it.
//
// It holds no guard of its own. What a service may send (read-only, only its
// own campaigns, paused creates) stays in that service, checked before it
// calls Do, because each app's rules are different on purpose.
//
// What the write tests of 2026-09-29 learned about the transport is kept
// here: a 429 was not acted on, so any request may be repeated; a 5xx after a
// create may have created it, so only callers that mark a request safe
// (reads, image uploads) get it repeated; an image part must name its own
// type.
package taboola

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/ops"
)

// DefaultBase is Backstage's production host.
const DefaultBase = "https://backstage.taboola.com"

const (
	// TokenPath is where the client-credentials token is asked for.
	TokenPath = "/backstage/oauth/token"
	// APIPrefix is what every API path is relative to.
	APIPrefix = "/backstage/api/1.0/"
	// UploadPath puts an image on Taboola's CDN; it touches no campaign.
	UploadPath = "operations/upload-image"

	// maxWait caps any pause between tries.
	maxWait = 2 * time.Minute
)

// ErrNoCredentials means the client id or secret is empty.
var ErrNoCredentials = errors.New("taboola: client id or secret missing")

// Client talks to Backstage with one login's client credentials. It is safe
// for concurrent use once set up.
type Client struct {
	base, id, secret string
	http             *http.Client

	// MaxRetries is how often a 429, or a 5xx on a request marked Retry5xx,
	// is tried again.
	MaxRetries int
	// Wait pauses between tries; tests make it instant.
	Wait func(context.Context, time.Duration) error
	// Record sees every attempt (a failed one too) before its answer is read
	// or handed back, to save it raw. It never sees the token or the secret.
	// When it returns an error the answer is not handed back (*RecordError).
	Record func(Exchange) error

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New returns a client for base (DefaultBase in production). hc may carry a
// transport that refuses requests (Intel's read-only client does); nil means
// a plain client with a 2 minute timeout. Every call is counted on /metrics
// as provider "taboola" (kit/ops Transport).
func New(base, clientID, clientSecret string, hc *http.Client) *Client {
	if base == "" {
		base = DefaultBase
	}
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	counted := *hc
	counted.Transport = ops.Transport("taboola", hc.Transport)
	hc = &counted
	return &Client{
		base: strings.TrimRight(base, "/"), id: clientID, secret: clientSecret, http: hc,
		MaxRetries: 3,
		Wait:       Sleep,
		Record:     func(Exchange) error { return nil },
	}
}

// Request is one call to the API.
type Request struct {
	Method      string
	Path        string // relative to APIPrefix, for example "acme-sc/campaigns/"
	Query       url.Values
	Body        []byte
	ContentType string
	// Log is what Record sees as the request body: the JSON itself, or a
	// line in place of an image. Do never reads it.
	Log any
	// Retry5xx is true where a repeat can never make something twice:
	// reads, and image uploads.
	Retry5xx bool
}

// Exchange is one attempt and its answer, for saving raw.
type Exchange struct {
	Time    time.Time
	Method  string
	Path    string
	Attempt int // 1 for the first try
	Log     any
	Status  int // 0 when no answer arrived
	Header  http.Header
	Body    []byte
	Err     error // no answer, or an answer cut short
}

// Response is one 2xx answer (or the last answer before an error), as
// received.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Elapsed time.Duration
	Retries int // tries after the first
}

// StatusError is a non-2xx answer that was not repeated (or ran out of
// repeats).
type StatusError struct {
	Status int
	Body   []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("taboola: HTTP %d: %s", e.Status, Snippet(e.Body))
}

// SendError means no answer arrived. RecordErr is set when that failure
// could not be recorded either.
type SendError struct {
	Err       error
	RecordErr error
}

func (e *SendError) Error() string { return "taboola: no answer: " + e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// RecordError means an answer arrived but Record refused it, so it is not
// handed back. What was asked may have happened.
type RecordError struct {
	Status int
	Err    error
}

func (e *RecordError) Error() string {
	return fmt.Sprintf("taboola: answer (HTTP %d) not recorded: %v", e.Status, e.Err)
}
func (e *RecordError) Unwrap() error { return e.Err }

// CutError means the answer's body stopped before its end.
type CutError struct {
	Status int
	Err    error
}

func (e *CutError) Error() string {
	return fmt.Sprintf("taboola: answer (HTTP %d) cut short: %v", e.Status, e.Err)
}
func (e *CutError) Unwrap() error { return e.Err }

// TokenError is a token request that failed. Status is 0 when no answer
// arrived, 200 when the answer held no token.
type TokenError struct {
	Status int
	Body   string // Taboola's words (bad secret, no API access); never a token
	Err    error
}

func (e *TokenError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("taboola: token request: no answer: %v", e.Err)
	}
	if e.Status == http.StatusOK {
		return "taboola: token request: no access_token in the answer"
	}
	return fmt.Sprintf("taboola: token request: HTTP %d: %s", e.Status, e.Body)
}
func (e *TokenError) Unwrap() error { return e.Err }

// Do sends one request. A 401 fetches a new token and repeats once; a 429,
// and a 5xx when r.Retry5xx, is repeated up to MaxRetries times after the
// pause Retry-After asks for (else 2, 4, 8 s). A non-2xx answer comes back
// with a *StatusError and the Response, so the caller can still read it.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	u := c.base + APIPrefix + strings.TrimLeft(r.Path, "/")
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	start := time.Now()
	refreshed := false
	for attempt, retries := 1, 0; ; attempt++ {
		tok, err := c.Token(ctx)
		if err != nil {
			return nil, err
		}
		var rd io.Reader
		if r.Body != nil {
			rd = bytes.NewReader(r.Body)
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, u, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if r.ContentType != "" {
			req.Header.Set("Content-Type", r.ContentType)
		}
		ex := Exchange{Time: time.Now().UTC(), Method: r.Method, Path: r.Path, Attempt: attempt, Log: r.Log}
		res, err := c.http.Do(req)
		if err != nil {
			ex.Err = err
			return nil, &SendError{Err: err, RecordErr: c.Record(ex)}
		}
		body, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		ex.Status, ex.Header, ex.Body, ex.Err = res.StatusCode, res.Header, body, readErr
		if err := c.Record(ex); err != nil {
			return nil, &RecordError{Status: res.StatusCode, Err: err}
		}
		if readErr != nil {
			return nil, &CutError{Status: res.StatusCode, Err: readErr}
		}
		out := &Response{Status: res.StatusCode, Header: res.Header, Body: body, Elapsed: time.Since(start), Retries: attempt - 1}

		switch {
		case res.StatusCode == http.StatusUnauthorized && !refreshed:
			// Expired early or revoked: fetch a new token, once. A 401 was
			// not acted on, so repeating it is safe for any method.
			refreshed = true
			c.forgetToken()
			continue
		case (res.StatusCode == http.StatusTooManyRequests || (res.StatusCode >= 500 && r.Retry5xx)) && retries < c.MaxRetries:
			if err := c.Wait(ctx, Backoff(res.Header, retries)); err != nil {
				return out, err
			}
			retries++
			continue
		case res.StatusCode/100 == 2:
			return out, nil
		}
		return out, &StatusError{Status: res.StatusCode, Body: body}
	}
}

// Token returns a cached token, fetching one when none is left or the
// current one ends within a minute. The token exchange is never recorded:
// its request holds the secret and its answer the token.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > time.Minute {
		return c.token, nil
	}
	if c.id == "" || c.secret == "" {
		return "", ErrNoCredentials
	}
	form := url.Values{
		"client_id":     {c.id},
		"client_secret": {c.secret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+TokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return "", &TokenError{Err: err}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		// The body names the problem and holds no token, so it is safe to show.
		return "", &TokenError{Status: res.StatusCode, Body: Snippet(body)}
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if json.Unmarshal(body, &t) != nil || t.AccessToken == "" {
		return "", &TokenError{Status: res.StatusCode}
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

// ImageForm is the multipart body UploadPath takes for one image, and its
// content type. Taboola refuses a part sent as application/octet-stream
// ("unsupported type of image"), so the part names the image's own type.
func ImageForm(name string, data []byte) (body []byte, contentType string, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	h.Set("Content-Type", http.DetectContentType(data))
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// Backoff honours Retry-After (seconds, at most 2 minutes) and otherwise
// waits 2, 4, 8 s.
func Backoff(h http.Header, retry int) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxWait)
	}
	return time.Duration(2<<retry) * time.Second
}

// Sleep waits d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Snippet is b trimmed and cut to 300 bytes, for an error line.
func Snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
