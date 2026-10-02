package write

import (
	"context"
	"net/http"
)

// Allowed is one account a login can see, as Taboola lists it.
type Allowed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Network is true for the login's network account, which holds the
	// others and is never used here.
	Network bool `json:"network"`
}

// Allowed lists every account the login can see, network account included,
// asking Taboola. It reads only (a token, then allowed-accounts) and needs
// only the login's client id and secret, so a login can be checked before
// any of its accounts is chosen.
func (c *Client) Allowed(ctx context.Context) ([]Allowed, error) {
	if c == nil || c.s.ClientID == "" || c.s.ClientSecret == "" {
		return nil, ErrNotConfigured
	}
	out, err := c.sendJSON(ctx, http.MethodGet, allowedAccounts, nil, true)
	if err != nil {
		return nil, err
	}
	var list []Allowed
	for _, r := range results(out) {
		id := str(r["account_id"])
		if id == "" {
			continue
		}
		list = append(list, Allowed{ID: id, Name: oneLine(str(r["name"]), 100), Network: str(r["type"]) == "NETWORK" || isNetwork(id)})
	}
	return list, nil
}

// ClientID is the login's client id ("" when off). The secret has no getter.
func (c *Client) ClientID() string {
	if c == nil {
		return ""
	}
	return c.s.ClientID
}
