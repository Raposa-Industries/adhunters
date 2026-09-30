// Package walk follows running ads' saved links to their landing pages, one
// step further to the page the main button leads to, and reads what each
// page says (tracks-walker).
//
// Ported from adhunters-collector e20148c, internal/funnel: the same
// requests (a desktop browser's headers, redirects followed by hand up to 10
// hops, 500 KB of each page, one step through the button most links point
// to, the same cookies for both), through capture's datacenter lines first.
// Two things differ, as in capture: every walk is written whole to a raw file
// before it is read, and reading a raw file again (Replay) rebuilds the
// tables for any range.
package walk

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"
)

// Network is the folder and key prefix of the walker's raw files.
const Network = "walk"

// Record is one walk, as the raw file keeps it.
type Record struct {
	ID          string    `json:"id"` // ULID
	At          time.Time `json:"at"`
	Instance    string    `json:"instance"`
	Version     string    `json:"version"`
	Line        string    `json:"line"` // proxy line key, never its credentials
	AdID        int       `json:"ad_id"`
	CreativeID  int       `json:"creative_id"`
	AccountID   *int      `json:"account_id,omitempty"`
	LinkID      int       `json:"link_id"`
	PublisherID *int      `json:"publisher_id,omitempty"`
	Pages       []Page    `json:"pages"`
	LatencyMS   int64     `json:"latency_ms"`
}

// Page is one page of a walk: step 0 is the landing page, step 1 the page
// its main button led to.
type Page struct {
	Step       int               `json:"step"`
	URL        string            `json:"url"`
	Referer    string            `json:"referer,omitempty"`
	Hops       []Hop             `json:"hops"`
	FinalURL   string            `json:"final_url,omitempty"`
	Status     int               `json:"status"` // 0 when no answer came
	Headers    map[string]string `json:"headers,omitempty"`
	Body       *string           `json:"body,omitempty"`     // when it is valid UTF-8
	BodyBase64 string            `json:"body_b64,omitempty"` // when it is not
	Truncated  bool              `json:"truncated,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// Hop is one request of a page: a redirect, or the answer at the end.
type Hop struct {
	URL       string `json:"url"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Location  string `json:"location,omitempty"`
	Server    string `json:"server,omitempty"`
}

// keptHeaders are the final answer's headers a page keeps.
var keptHeaders = []string{"Content-Type", "Server", "X-Powered-By", "Cf-Ray", "Date"}

func (p *Page) setHeaders(h http.Header) {
	for _, k := range keptHeaders {
		if v := h.Get(k); v != "" {
			if p.Headers == nil {
				p.Headers = map[string]string{}
			}
			p.Headers[k] = v
		}
	}
}

func (p *Page) setBody(b []byte) {
	if utf8.Valid(b) {
		s := string(b)
		p.Body = &s
		return
	}
	p.BodyBase64 = base64.StdEncoding.EncodeToString(b)
}

// BodyBytes is the page as received.
func (p *Page) BodyBytes() []byte {
	if p.Body != nil {
		return []byte(*p.Body)
	}
	b, _ := base64.StdEncoding.DecodeString(p.BodyBase64)
	return b
}

// Outcome is ok when the landing page answered 2xx, http_error when it
// answered otherwise, error when it did not answer.
func (r *Record) Outcome() string {
	if len(r.Pages) == 0 || r.Pages[0].Status == 0 {
		return "error"
	}
	if s := r.Pages[0].Status; s >= 200 && s < 300 {
		return "ok"
	}
	return "http_error"
}

// Error is the landing page's error, if any.
func (r *Record) Error() string {
	if len(r.Pages) == 0 {
		return "no page"
	}
	return r.Pages[0].Error
}

// Marshal is the record as one raw file line, HTML left unescaped.
func (r *Record) Marshal() ([]byte, error) {
	var buf lineBuffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.b, nil
}

type lineBuffer struct{ b []byte }

func (l *lineBuffer) Write(p []byte) (int, error) { l.b = append(l.b, p...); return len(p), nil }
