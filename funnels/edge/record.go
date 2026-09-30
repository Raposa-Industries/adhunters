// Package edge is what landing page visitors reach: the hosted landing
// sites, the page script (/ah.js) and the collector (/e), which writes every
// beacon as received to the spool.
package edge

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"time"
	"unicode/utf8"
)

// Stream is the spool folder, and archive key prefix, of page events.
const Stream = "events"

// MaxBody is the most of one beacon kept. The page script sends at most 40
// events per beacon, well under it; a longer body is cut and marked.
const MaxBody = 64 << 10

// Record is one beacon as the collector received it: one line of a raw file.
// The body is kept exactly; parsing is the loader's work. The visitor's IP
// address is never stored: only a keyed hash of it (the same address gives
// the same hash while the key stays) and its network (/24 or /48), for the
// bot check.
type Record struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Instance  string    `json:"instance"`
	Version   string    `json:"version"`
	Host      string    `json:"host"`
	Origin    string    `json:"origin,omitempty"`
	Referer   string    `json:"referer,omitempty"`
	UA        string    `json:"ua,omitempty"`
	Country   string    `json:"country,omitempty"`
	IPHash    string    `json:"ip_hash,omitempty"`
	Net       string    `json:"net,omitempty"`
	Body      string    `json:"body,omitempty"`
	BodyB64   string    `json:"body_b64,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}

// SetBody keeps b as text when it is valid UTF-8, otherwise as base64, so
// every body round-trips exactly.
func (r *Record) SetBody(b []byte) {
	if utf8.Valid(b) {
		r.Body, r.BodyB64 = string(b), ""
		return
	}
	r.Body, r.BodyB64 = "", base64.StdEncoding.EncodeToString(b)
}

// RawBody is the body as received.
func (r *Record) RawBody() ([]byte, error) {
	if r.BodyB64 != "" {
		return base64.StdEncoding.DecodeString(r.BodyB64)
	}
	return []byte(r.Body), nil
}

// Marshal encodes the record as one line, HTML characters unescaped.
func (r *Record) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// HashIP returns the keyed hash of an address (16 hex characters) and its
// network: the /24 of an IPv4 address, the /48 of an IPv6 one. Both are
// empty when addr is not an address.
func HashIP(key []byte, addr string) (hash, network string) {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return "", ""
	}
	ip = ip.Unmap()
	m := hmac.New(sha256.New, key)
	m.Write([]byte(ip.String()))
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	p, _ := ip.Prefix(bits)
	return hex.EncodeToString(m.Sum(nil))[:16], p.String()
}
