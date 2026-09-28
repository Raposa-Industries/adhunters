// Package spool writes capture's raw files and reads them back.
//
// A raw file is one minute of one capture instance's scrapes of one network,
// one JSON line per scrape, as received. While the minute is open it is
// <dir>/<network>/<yyyy>/<mm>/<dd>/<hh>/capture-<instance>-<hhmm>.ndjson; when
// the minute ends it is compressed with zstd (level 9) to the same name plus
// .zst and the plain file is removed. The layout under <dir> is the archive
// key under raw/, so the shipper uploads a file to the same path.
package spool

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"
)

// Record is one scrape: the request capture sent and what came back. A scrape
// that failed is recorded too, with Error set, because errors count as scrapes.
type Record struct {
	ID          string            `json:"id"` // ULID
	At          time.Time         `json:"at"` // when the request was sent, UTC
	Network     string            `json:"network"`
	Publisher   string            `json:"publisher"`
	Device      string            `json:"device"`
	Line        string            `json:"line"` // proxy line key, never its credentials
	Instance    string            `json:"instance"`
	Version     string            `json:"version"`
	URL         string            `json:"url"`
	RequestBody string            `json:"request_body,omitempty"` // NewsBreak's auction request
	Status      int               `json:"status"`                 // 0 when no answer came
	LatencyMS   int64             `json:"latency_ms"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        *string           `json:"body,omitempty"`     // the answer, when it is valid UTF-8
	BodyBase64  string            `json:"body_b64,omitempty"` // the answer, when it is not
	Truncated   bool              `json:"truncated,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// KeptHeaders are the response headers a record keeps.
var KeptHeaders = []string{"Content-Type", "Content-Encoding", "Date", "Server", "Cache-Control", "Age", "X-Cache", "Cf-Ray", "Via"}

// SetBody stores body so that it round-trips exactly: as a string when it is
// valid UTF-8 (every feed today), else as base64.
func (r *Record) SetBody(body []byte) {
	if utf8.Valid(body) {
		s := string(body)
		r.Body = &s
		r.BodyBase64 = ""
		return
	}
	r.Body = nil
	r.BodyBase64 = base64.StdEncoding.EncodeToString(body)
}

// BodyBytes returns the answer exactly as it was received.
func (r *Record) BodyBytes() ([]byte, error) {
	if r.Body != nil {
		return []byte(*r.Body), nil
	}
	if r.BodyBase64 != "" {
		return base64.StdEncoding.DecodeString(r.BodyBase64)
	}
	return nil, nil
}

// SetHeaders keeps the headers in KeptHeaders that the answer carried.
func (r *Record) SetHeaders(h http.Header) {
	for _, k := range KeptHeaders {
		if v := h.Get(k); v != "" {
			if r.Headers == nil {
				r.Headers = map[string]string{}
			}
			r.Headers[k] = v
		}
	}
}

// Marshal encodes the record as one line, without escaping HTML characters,
// so bodies stay close to their received size.
func (r *Record) Marshal() ([]byte, error) {
	return marshalLine(r)
}

func marshalLine(v any) ([]byte, error) {
	var buf lineBuffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.b, nil
}

type lineBuffer struct{ b []byte }

func (l *lineBuffer) Write(p []byte) (int, error) { l.b = append(l.b, p...); return len(p), nil }
