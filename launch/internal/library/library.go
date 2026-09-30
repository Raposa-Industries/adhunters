// Package library reads the team's library (library/): the creatives and
// headlines Create saved, for a new pair. Launch only reads it; the library
// runs on the same box and is reached on localhost.
package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls the library's API at Base (http://127.0.0.1:8093).
type Client struct {
	Base string
	HTTP *http.Client
}

// New returns a client for base; an empty base means no library.
func New(base string) *Client {
	if base == "" {
		return nil
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Error is the library's answer when it is not 2xx; Message is its one line.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("library: %d %s", e.Status, e.Message) }

// ErrDown is the library not answering.
var ErrDown = errors.New("library: not answering")

// Get calls GET path (with query q) and returns the response for the
// caller to pass on or read; a non-2xx answer is an *Error. The caller
// closes the body.
func (c *Client) Get(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDown, err)
	}
	if res.StatusCode/100 != 2 {
		defer res.Body.Close()
		var e struct{ Error string }
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&e)
		return nil, &Error{Status: res.StatusCode, Message: e.Error}
	}
	return res, nil
}

// Creative is what Launch needs of one of the library's creatives.
type Creative struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
	AILabel   string `json:"ai_label"` // unset, ai, not_ai
}

// File reads a creative and its picture, up to max bytes.
func (c *Client) File(ctx context.Context, id int64, max int64) (Creative, []byte, error) {
	var cr Creative
	res, err := c.Get(ctx, fmt.Sprintf("/api/creatives/%d", id), nil)
	if err != nil {
		return cr, nil, err
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&cr)
	res.Body.Close()
	if err != nil {
		return cr, nil, fmt.Errorf("library: creative %d: %w", id, err)
	}
	res, err = c.Get(ctx, fmt.Sprintf("/files/%d", id), nil)
	if err != nil {
		return cr, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return cr, nil, fmt.Errorf("library: file %d: %w", id, err)
	}
	if int64(len(data)) > max {
		return cr, nil, &Error{Status: http.StatusRequestEntityTooLarge, Message: "picture too big"}
	}
	return cr, data, nil
}
