// Package lines reads the proxy lines Raposa visits through, from the same
// proxies.env the capture boxes use (key=host:port:user:pass, one per line),
// and remembers which of them failed lately.
//
// The role comes from the key: res- is the metered residential line, dc- a
// datacenter line, anything else an ISP line. isp-1 to isp-5 are the capture
// boxes' backups and are never used here.
package lines

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Line is one proxy line.
type Line struct {
	Key      string
	Role     string // residential, dc, isp
	Host     string
	Port     int
	Username string
	Password string
}

// URL is the line as a proxy address, with its credentials.
func (l Line) URL() *url.URL {
	return &url.URL{
		Scheme: "http",
		User:   url.UserPassword(l.Username, l.Password),
		Host:   net.JoinHostPort(l.Host, strconv.Itoa(l.Port)),
	}
}

// backups are the capture boxes' spare lines.
var backups = map[string]bool{"isp-1": true, "isp-2": true, "isp-3": true, "isp-4": true, "isp-5": true}

// Parse reads the lines out of a proxies.env. Blank lines, comments and
// lines it cannot read are skipped.
func Parse(text string) []Line {
	var out []Line
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		row := strings.TrimSpace(sc.Text())
		if row == "" || strings.HasPrefix(row, "#") {
			continue
		}
		key, val, ok := strings.Cut(row, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(val), `"'`)
		parts := strings.SplitN(val, ":", 4)
		if len(parts) < 4 || backups[key] {
			continue
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		role := "isp"
		switch {
		case strings.HasPrefix(key, "res-"):
			role = "residential"
		case strings.HasPrefix(key, "dc-"):
			role = "dc"
		}
		out = append(out, Line{Key: key, Role: role, Host: parts[0], Port: port, Username: parts[2], Password: parts[3]})
	}
	return out
}

// Set is the lines of one box, with what failed lately. A line that failed
// three visits in a row rests for a minute.
type Set struct {
	mu     sync.Mutex
	lines  []Line
	errors map[string]int
	rest   map[string]time.Time
}

// Load reads a proxies.env. A missing file is an error: without lines every
// rung past direct fails.
func Load(path string) (*Set, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read proxy lines: %w", err)
	}
	return New(Parse(string(b))), nil
}

// New holds the given lines.
func New(ls []Line) *Set {
	return &Set{lines: ls, errors: map[string]int{}, rest: map[string]time.Time{}}
}

// All is every line not resting.
func (s *Set) All() []Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := make([]Line, 0, len(s.lines))
	for _, l := range s.lines {
		if now.Before(s.rest[l.Key]) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Role is the role of the line with this key, or "".
func (s *Set) Role(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if l.Key == key {
			return l.Role
		}
	}
	return ""
}

// Failed counts a visit the line could not carry.
func (s *Set) Failed(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors[key]++
	if s.errors[key] >= 3 {
		s.rest[key] = time.Now().Add(time.Minute)
		s.errors[key] = 0
	}
}

// Worked clears the line's failures.
func (s *Set) Worked(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.errors, key)
}

// Transport sends requests through the line, or direct when line is nil.
func Transport(line *Line) *http.Transport {
	t := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if line != nil && line.Host != "" {
		t.Proxy = http.ProxyURL(line.URL())
	}
	return t
}
