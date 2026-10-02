package write

import (
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Raposa-Industries/adhunters/kit/keep"
)

// loginOnly is a client with a login's id and secret and nothing chosen.
func loginOnly(t *testing.T, base, sec string) *Client {
	t.Helper()
	c, err := New(Settings{Base: base, ClientID: "id", ClientSecret: sec}, keep.New(t.TempDir()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAllowedListsEveryAccountWithoutChosenOnes(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		io.WriteString(w, `{"results":[
			{"account_id":"big-network","name":"Big","type":"NETWORK"},
			{"account_id":"acme-sc","name":"Acme","type":"PARTNER"},
			{"account_id":"","name":"no id"}]}`)
	})
	// No accounts chosen yet: only the login's id and secret.
	c := loginOnly(t, f.srv.URL, secret)
	list, err := c.Allowed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0] != (Allowed{ID: "big-network", Name: "Big", Network: true}) || list[1] != (Allowed{ID: "acme-sc", Name: "Acme"}) {
		t.Fatalf("got %+v", list)
	}
	seen := f.seen()
	if len(seen) != 1 || seen[0].Method != "GET" || seen[0].Path != allowedAccounts {
		t.Fatalf("asked %+v; want one GET of allowed-accounts", seen)
	}
}

func TestAllowedNeedsCredentialsAndSaysWhenRefused(t *testing.T) {
	if _, err := mustNew(t, Settings{ClientID: "id"}).Allowed(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no secret: %v", err)
	}
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) { t.Error("asked without a token") })
	_, err := loginOnly(t, f.srv.URL, "wrong").Allowed(ctx)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %v", err)
	}
}
