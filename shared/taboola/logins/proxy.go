package logins

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A proxy is the address every request to Taboola for an account goes
// through, so Taboola sees each account come from its own address. A login
// added on Contas must have one; an account of the server's own login may.
// Once an account has one, its requests (the token, reads, writes, image
// uploads) go only through it: when the proxy fails the request fails, and
// nothing is ever sent direct instead.
//
// A proxy address may hold a user and password, so it is sealed like a
// secret, never logged, and pages see only its host and port. Errors here
// never repeat the address.

// ParseProxy reads a proxy address as people type it:
// http://user:password@host:port, https://… or socks5://…, the user and
// password optional. Its errors are one pt-BR line, never the address.
func ParseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("falta o proxy: http://usuario:senha@host:porta")
	}
	if len(raw) > 500 {
		return nil, errors.New("proxy longo demais")
	}
	bad := errors.New("proxy inválido: use http://usuario:senha@host:porta (http, https ou socks5)")
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, bad
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, bad
	}
	if u.Hostname() == "" {
		return nil, bad
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("proxy sem porta: http://usuario:senha@host:porta")
	}
	u.Path = ""
	return u, nil
}

// HostPort is what pages show of a proxy: its host and port, never the user
// or password.
func HostPort(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Host
}

// ProxyClient is an HTTP client that sends every request through u and only
// through u: the transport's proxy is fixed (the environment's proxy
// settings and NO_PROXY do not apply), and a failure is a ProxyError, never
// a direct retry.
func ProxyClient(u *url.URL) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	return &http.Client{Timeout: 2 * time.Minute, Transport: &viaProxy{rt: tr, at: HostPort(u)}}
}

// BlockedClient refuses every request with line: an account that must go
// through a proxy it does not have (or that cannot be opened).
func BlockedClient(line string) *http.Client {
	return &http.Client{Transport: blocked(line)}
}

type blocked string

func (b blocked) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, &ProxyError{line: string(b)}
}

// ProxyError is a request a proxy did not carry (or that had no usable
// proxy), with one pt-BR line for the person. Say lets the Taboola client
// show it as it is (shared/taboola/write).
type ProxyError struct{ line string }

func (e *ProxyError) Error() string { return e.line }
func (e *ProxyError) Say() string   { return e.line }

type viaProxy struct {
	rt *http.Transport
	at string // host:port
}

func (v *viaProxy) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := v.rt.RoundTrip(r)
	after := ""
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		after = "; confira no Taboola antes de repetir"
	}
	if err != nil {
		if r.Context().Err() != nil {
			return nil, err
		}
		return nil, &ProxyError{line: fmt.Sprintf("o proxy %s %s; nada foi enviado direto à Taboola%s", v.at, why(err), after)}
	}
	// A plain HTTP proxy answers 407 itself when it refuses the user and
	// password; that is the proxy, not Taboola.
	if res.StatusCode == http.StatusProxyAuthRequired {
		res.Body.Close()
		return nil, &ProxyError{line: fmt.Sprintf("o proxy %s recusou o usuário e a senha; nada foi enviado direto à Taboola", v.at)}
	}
	return res, nil
}

// why names a proxy failure in pt-BR, without the error's own text (which
// could hold the address).
func why(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	s := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return "não respondeu a tempo"
	case errors.As(err, &dns):
		return "não foi encontrado (o host não existe)"
	case strings.Contains(s, "Proxy Authentication Required") || strings.Contains(s, "username/password authentication failed"):
		return "recusou o usuário e a senha"
	case errors.As(err, &op) && op.Op == "dial", strings.Contains(s, "connection refused"):
		return "recusou a conexão ou está fora do ar"
	}
	return "falhou ao levar o pedido à Taboola"
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
