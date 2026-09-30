package drive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// LoginRedirect is where Google sends the browser after consent. Nothing
// listens there: the page fails to load, and the person copies its address
// back into the terminal. That works from any computer, whatever box the
// command runs on.
const LoginRedirect = "http://127.0.0.1:8765/"

// Login is one sign-in in progress.
type Login struct {
	app      App
	state    string
	verifier string
}

// StartLogin prepares a sign-in.
func StartLogin(app App) (*Login, error) {
	if app.ClientID == "" || app.ClientSecret == "" {
		return nil, errors.New("LIBRARY_GOOGLE_CLIENT_ID and LIBRARY_GOOGLE_CLIENT_SECRET must be set")
	}
	return &Login{app: app, state: random(16), verifier: random(48)}, nil
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// URL is the consent page to open. It asks for a refresh token (offline,
// with consent shown again so Google always sends one) and uses PKCE.
func (l *Login) URL() string {
	sum := sha256.Sum256([]byte(l.verifier))
	q := url.Values{
		"client_id":             {l.app.ClientID},
		"redirect_uri":          {LoginRedirect},
		"response_type":         {"code"},
		"scope":                 {Scope},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {l.state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	return l.app.ends().Auth + "?" + q.Encode()
}

// Finish reads the address the browser ended on and trades its code for a
// refresh token.
func (l *Login) Finish(ctx context.Context, pasted string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil || u.RawQuery == "" {
		return "", errors.New("that is not the address from the browser's address bar (it starts with http://127.0.0.1:8765/?)")
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("Google said %q: access was not given", e)
	}
	if q.Get("state") != l.state {
		return "", errors.New("that address is from another sign-in; open the link printed above and try again")
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("the address has no code in it")
	}
	if !strings.Contains(" "+q.Get("scope")+" ", " "+Scope+" ") && q.Get("scope") != "" {
		return "", errors.New("Drive access was not ticked on Google's page; sign in again and allow it")
	}
	tok, err := postToken(ctx, http.DefaultClient, l.app.ends().Token, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {LoginRedirect},
		"client_id":     {l.app.ClientID},
		"client_secret": {l.app.ClientSecret},
		"code_verifier": {l.verifier},
	})
	if err != nil {
		return "", err
	}
	if tok.RefreshToken == "" {
		return "", errors.New("Google sent no refresh token; remove the app's access at myaccount.google.com/permissions and sign in again")
	}
	return tok.RefreshToken, nil
}
