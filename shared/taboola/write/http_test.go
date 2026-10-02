package write

import (
	"net/http"
	"strings"
	"testing"
)

// sayer is a transport's error with its own line for the person.
type sayer struct{}

func (sayer) Error() string { return "proxy down" }
func (sayer) Say() string   { return "o proxy x:1 não respondeu; nada foi enviado direto à Taboola" }

type refuseAll struct{ n int }

func (r *refuseAll) RoundTrip(*http.Request) (*http.Response, error) {
	r.n++
	return nil, sayer{}
}

// Settings.HTTP carries every request, the token included, and its
// transport's own line reaches the person.
func TestSettingsHTTPCarriesEveryRequest(t *testing.T) {
	f := newFake(t, nil)
	rt := &refuseAll{}
	c, _ := clientWith(t, Settings{Base: f.srv.URL, HTTP: &http.Client{Transport: rt}})
	_, err := c.Allowed(ctx)
	if err == nil || !strings.Contains(err.Error(), "nada foi enviado direto") {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Groups(ctx, "acme-sc"); err == nil || !strings.Contains(err.Error(), "o proxy x:1") {
		t.Fatalf("got %v", err)
	}
	if rt.n == 0 || f.tokens.Load() != 0 || len(f.seen()) != 0 {
		t.Fatalf("went around the client: %d through it, %d tokens, %d requests direct", rt.n, f.tokens.Load(), len(f.seen()))
	}
}
