// Package taboola is a read-only client for Taboola's Backstage API, the
// API behind the Taboola Ads UI for our own accounts.
//
// It only reads. The one request that is not a GET is the token request;
// anything else is refused before it leaves the process (see readOnly), so a
// bug here cannot create, change, pause or delete a campaign. Writing has its
// own client (act).
//
// The token, retries and sending come from shared/taboola, which Create's
// client uses too (decision 0013). Every answer is handed back raw, with its
// headers, so callers save it before reading it (decision 0003).
package taboola

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	api "github.com/Raposa-Industries/adhunters/shared/taboola"
)

// DefaultBase is Backstage's production host.
const DefaultBase = api.DefaultBase

const (
	tokenPath = api.TokenPath
	apiPrefix = api.APIPrefix
)

// ErrWriteRefused means a request other than a GET (or the token request)
// was about to be sent.
var ErrWriteRefused = errors.New("taboola: client is read-only")

// Client talks to Backstage with client credentials. It is safe for
// concurrent use.
type Client struct {
	api  *api.Client
	http *http.Client // the read-only one the shared client sends through
}

// New returns a client for the given host (DefaultBase in production).
func New(base, clientID, clientSecret string) *Client {
	hc := &http.Client{Timeout: 2 * time.Minute, Transport: readOnly{http.DefaultTransport}}
	c := &Client{api: api.New(base, clientID, clientSecret, hc), http: hc}
	c.api.Wait = func(ctx context.Context, d time.Duration) error { return sleep(ctx, d) }
	return c
}

// Response is one answer, as received.
type Response = api.Response

// StatusError is a non-2xx answer.
type StatusError = api.StatusError

// Get reads path (relative to /backstage/api/1.0/, for example
// "acme-sc/campaigns") with the given query. A 429 or 5xx is retried up to
// 3 times. A non-2xx answer is returned with an error of type *StatusError,
// so the caller can still save it.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.api.Do(ctx, api.Request{Method: http.MethodGet, Path: path, Query: query, Retry5xx: true})
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

var sleep = api.Sleep
