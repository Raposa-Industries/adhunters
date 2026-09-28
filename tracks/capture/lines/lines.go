// Package lines hands out proxy lines to capture workers.
//
// Ported from adhunters-collector e20148c, internal/proxy/pool.go: round robin
// over ready lines of the first role that has one (datacenter first, ISP only
// while every datacenter line cools down), a cooldown after any error, and the
// least recently used line when every line is cooling down.
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

// Line is one proxy line. Key is safe to log and store; the rest is secret.
type Line struct {
	Key      string
	Role     string // dc, isp or residential, from the key's prefix
	Host     string
	Port     int
	Username string
	Password string
}

// URL is the proxy address with its credentials.
func (l Line) URL() *url.URL {
	u := &url.URL{Scheme: "http", Host: net.JoinHostPort(l.Host, strconv.Itoa(l.Port))}
	if l.Username != "" {
		u.User = url.UserPassword(l.Username, l.Password)
	}
	return u
}

type state struct {
	line          Line
	cooldownUntil time.Time
	lastUsed      time.Time
	transport     *http.Transport
}

// Pool is the set of lines one capture instance uses.
type Pool struct {
	mu       sync.Mutex
	lines    []*state
	next     int
	roles    []string
	cooldown time.Duration
	now      func() time.Time
}

// Load reads a lines file: one "key=host:port:user:pass" per line, "#" for
// comments (the collector's secrets/proxies.env). Lines named in reserved are
// kept out, as the collector keeps its backup lines on a 24 h cooldown.
func Load(path string, roles []string, reserved []string, cooldown time.Duration) (*Pool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	skip := map[string]bool{}
	for _, k := range reserved {
		skip[strings.TrimSpace(k)] = true
	}
	var out []Line
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l, ok := parse(sc.Text())
		if ok && !skip[l.Key] {
			out = append(out, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return New(out, roles, cooldown), nil
}

func parse(text string) (Line, bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "#") {
		return Line{}, false
	}
	key, val, ok := strings.Cut(text, "=")
	if !ok {
		return Line{}, false
	}
	key, val = strings.TrimSpace(key), strings.TrimSpace(val)
	parts := strings.Split(val, ":")
	if len(parts) < 4 {
		return Line{}, false
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return Line{}, false
	}
	role := "isp"
	switch {
	case strings.HasPrefix(key, "res-"):
		role = "residential"
	case strings.HasPrefix(key, "dc-"):
		role = "dc"
	}
	return Line{Key: key, Role: role, Host: parts[0], Port: port, Username: parts[2], Password: parts[3]}, true
}

// New returns a pool over lines, preferring roles in the order given. Lines of
// other roles are never used.
func New(lines []Line, roles []string, cooldown time.Duration) *Pool {
	p := &Pool{roles: roles, cooldown: cooldown, now: time.Now}
	for _, l := range lines {
		p.lines = append(p.lines, &state{line: l, transport: transport(l)})
	}
	return p
}

// Size is how many lines of the preferred roles the pool has.
func (p *Pool) Size() int {
	n := 0
	for _, s := range p.lines {
		for _, r := range p.roles {
			if s.line.Role == r {
				n++
			}
		}
	}
	return n
}

// Next returns a line and the transport that goes through it, or ok=false
// when the pool has no line of a preferred role.
func (p *Pool) Next() (key string, rt http.RoundTripper, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.lines)
	now := p.now()
	for _, role := range p.roles {
		for i := 0; i < n; i++ {
			idx := (p.next + i) % n
			s := p.lines[idx]
			if s.line.Role != role || now.Before(s.cooldownUntil) {
				continue
			}
			p.next = (idx + 1) % n
			s.lastUsed = now
			return s.line.Key, s.transport, true
		}
	}
	var lru *state
	for _, s := range p.lines {
		if !p.preferred(s.line.Role) {
			continue
		}
		if lru == nil || s.lastUsed.Before(lru.lastUsed) {
			lru = s
		}
	}
	if lru == nil {
		return "", nil, false
	}
	lru.lastUsed = now
	return lru.line.Key, lru.transport, true
}

func (p *Pool) preferred(role string) bool {
	for _, r := range p.roles {
		if r == role {
			return true
		}
	}
	return false
}

// Failed puts a line on cooldown. The collector cools a line after its first
// error, so this does too.
func (p *Pool) Failed(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.lines {
		if s.line.Key == key {
			s.cooldownUntil = p.now().Add(p.cooldown)
			return
		}
	}
}

// CoolingDown counts lines of the preferred roles that are cooling down.
func (p *Pool) CoolingDown() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, now := 0, p.now()
	for _, s := range p.lines {
		if p.preferred(s.line.Role) && now.Before(s.cooldownUntil) {
			n++
		}
	}
	return n
}

// One transport per line keeps its connections warm between scrapes, where
// the collector dialled a new one every time.
func transport(l Line) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyURL(l.URL()),
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// Describe summarises the pool for the start-up log line, without secrets.
func (p *Pool) Describe() string {
	count := map[string]int{}
	for _, s := range p.lines {
		count[s.line.Role]++
	}
	return fmt.Sprintf("dc=%d isp=%d residential=%d, using %v", count["dc"], count["isp"], count["residential"], p.roles)
}
