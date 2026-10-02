package logins

import (
	"net/http"
	"net/url"

	shared "github.com/Raposa-Industries/adhunters/shared/taboola/logins"
)

// The sealing and the proxy transport are shared with Intel, which opens the
// same logins to read their accounts (shared/taboola/logins, decision 0028).

// Box seals and opens secrets with AES-256-GCM.
type Box = shared.Box

// ErrSealed is a secret that cannot be opened: the key changed or the row
// was altered. The login has to be added again.
var ErrSealed = shared.ErrSealed

// KeyFrom, OpenKey and NewBox are shared.KeyFrom, OpenKey and NewBox.
func KeyFrom(setting, path string) (*Box, bool, error) { return shared.KeyFrom(setting, path) }
func OpenKey(path string) (*Box, error)                { return shared.OpenKey(path) }
func NewBox(key []byte) (*Box, error)                  { return shared.NewBox(key) }

// ProxyError is a request a proxy did not carry, with one pt-BR line.
type ProxyError = shared.ProxyError

// ParseProxy reads a proxy address as people type it; a bad one is refused
// with a pt-BR line (shared.ParseProxy).
func ParseProxy(raw string) (*url.URL, error) {
	u, err := shared.ParseProxy(raw)
	if err != nil {
		return nil, refuse("%s", err.Error())
	}
	return u, nil
}

// HostPort is what pages show of a proxy: its host and port.
func HostPort(u *url.URL) string { return shared.HostPort(u) }

func proxyClient(u *url.URL) *http.Client        { return shared.ProxyClient(u) }
func blockedClient(line string) *http.Client     { return shared.BlockedClient(line) }
func bound(network, clientID string) []byte      { return shared.LoginBound(network, clientID) }
func proxyBound(network, clientID string) []byte { return shared.ProxyBound(network, clientID) }
func accountBound(network, account string) []byte {
	return shared.AccountBound(network, account)
}
