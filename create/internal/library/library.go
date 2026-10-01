// Package library is Create's client for the library service
// (library/README.md): it saves chosen options there as a set, fetches a
// kept creative to use as a reference, and lets Create's pages browse it
// through Create's own server, since the library refuses pages.
package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// MaxFile is the most a picture fetched from the library may weigh.
const MaxFile = 40 << 20

// Client calls the library at Base (http://127.0.0.1:8093).
type Client struct {
	Base string
	HTTP *http.Client
}

// New returns a client for base.
func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

// Error is the library's answer to a call that failed.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("library: %d %s", e.Status, e.Message) }

// ErrNotFound is a 404 from the library.
var ErrNotFound = errors.New("library: not found")

func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("library: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxFile+1))
	if err != nil {
		return fmt.Errorf("library: %w", err)
	}
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = http.StatusText(res.StatusCode)
		}
		return &Error{Status: res.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	if b, ok := out.(*[]byte); ok {
		if len(raw) > MaxFile {
			return fmt.Errorf("library: file over %d MB", MaxFile>>20)
		}
		*b = raw
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, "application/json", bytes.NewReader(b), out)
}

// NewSet is a set to add.
type NewSet struct {
	Name         string `json:"name"`
	VerticalID   string `json:"vertical_id,omitempty"`
	VerticalName string `json:"vertical_name,omitempty"`
	Origin       string `json:"origin"`
	OriginRef    string `json:"origin_ref"`
	MadeBy       string `json:"made_by"`
}

// Set is the library's set, as far as Create reads it.
type Set struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// AddSet adds a set; a name used already in the vertical gets " (2)".
func (c *Client) AddSet(ctx context.Context, s NewSet) (Set, error) {
	var out Set
	err := c.postJSON(ctx, "/api/sets", s, &out)
	return out, err
}

// CreativeMeta is what goes with a picture saved into the library.
type CreativeMeta struct {
	VerticalID   string `json:"vertical_id,omitempty"`
	VerticalName string `json:"vertical_name,omitempty"`
	Name         string `json:"name,omitempty"`
	SetID        int64  `json:"set_id,omitempty"`
	Angle        string `json:"angle"`
	Idea         string `json:"idea"`
	Origin       string `json:"origin"`
	OriginRef    string `json:"origin_ref"`
	AILabel      string `json:"ai_label"`
	MadeBy       string `json:"made_by"`
}

// Creative is the library's creative, as far as Create reads it.
type Creative struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Angle     string `json:"angle"`
	MediaType string `json:"media_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// AddCreative saves one picture; the same bytes saved before come back as
// the creative kept then (added to the set).
func (c *Client) AddCreative(ctx context.Context, meta CreativeMeta, filename string, data []byte) (Creative, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	m, err := json.Marshal(meta)
	if err != nil {
		return Creative{}, err
	}
	if err := w.WriteField("meta", string(m)); err != nil {
		return Creative{}, err
	}
	p, err := w.CreateFormFile("file", filename)
	if err != nil {
		return Creative{}, err
	}
	if _, err := p.Write(data); err != nil {
		return Creative{}, err
	}
	if err := w.Close(); err != nil {
		return Creative{}, err
	}
	var out Creative
	err = c.do(ctx, http.MethodPost, "/api/creatives", w.FormDataContentType(), &b, &out)
	return out, err
}

// NewHeadline is a headline to save.
type NewHeadline struct {
	Text       string `json:"text"`
	VerticalID string `json:"vertical_id,omitempty"`
	SetID      int64  `json:"set_id,omitempty"`
	Angle      string `json:"angle,omitempty"`
	Origin     string `json:"origin"`
	OriginRef  string `json:"origin_ref"`
	AILabel    string `json:"ai_label"`
	MadeBy     string `json:"made_by"`
}

// AddHeadlines saves headlines; a text kept already comes back as it was.
func (c *Client) AddHeadlines(ctx context.Context, hs []NewHeadline) error {
	return c.postJSON(ctx, "/api/headlines", map[string]any{"headlines": hs}, nil)
}

// RenameSet renames a set; the library renames its Drive folder on its next
// pass. A name another set of the vertical has is a 400 *Error.
func (c *Client) RenameSet(ctx context.Context, id int64, name string) error {
	b, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/sets/%d", id), "application/json", bytes.NewReader(b), nil)
}

// Creative reads one creative.
func (c *Client) Creative(ctx context.Context, id string) (Creative, error) {
	var out Creative
	err := c.do(ctx, http.MethodGet, "/api/creatives/"+url.PathEscape(id), "", nil, &out)
	return out, err
}

// File is one creative's picture.
func (c *Client) File(ctx context.Context, id string) ([]byte, error) {
	var out []byte
	err := c.do(ctx, http.MethodGet, "/files/"+url.PathEscape(id), "", nil, &out)
	return out, err
}

// Vertical is one of the library's verticals.
type Vertical struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// Verticals lists the library's verticals.
func (c *Client) Verticals(ctx context.Context) ([]Vertical, error) {
	var out struct {
		Verticals []Vertical `json:"verticals"`
	}
	err := c.do(ctx, http.MethodGet, "/api/verticals", "", nil, &out)
	return out.Verticals, err
}

// Browse serves reads of the library to Create's pages, mounted with its
// prefix stripped: GET /api/verticals, /api/sets, /api/sets/{id},
// /api/creatives, /api/creatives/{id}, /api/headlines, /files/{id} and
// /thumbs/{id}. Nothing else passes, and the page's Origin is not sent on.
func (c *Client) Browse() http.Handler {
	target, err := url.Parse(c.Base)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "the library's address is not valid", http.StatusBadGateway)
		})
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Header.Del("Origin")
			r.Out.Header.Del("Cookie")
		},
	}
	mux := http.NewServeMux()
	for _, p := range []string{"/api/verticals", "/api/sets", "/api/sets/{id}", "/api/creatives", "/api/creatives/{id}",
		"/api/headlines", "/files/{id}", "/thumbs/{id}"} {
		mux.Handle("GET "+p, proxy)
	}
	return mux
}
