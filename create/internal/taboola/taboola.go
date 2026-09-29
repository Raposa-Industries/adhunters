// Package taboola is Create's Taboola Backstage client: the advertiser
// accounts the page may use, their campaigns, a new campaign, image uploads
// and ads (Taboola's items) made in bulk.
//
// Ported from intel/taboola/act (the write client proven against the real
// API on 2026-09-29) and intel/taboola/client.go (token flow, retries), when
// the repo still forbade sharing code between services. Decision 0013 now
// allows it: this and Intel's client are to become one package in shared/.
// What those tests learned is kept, with the reason next to it.
//
// Guards, checked before a request leaves (do):
//
//   - only the advertiser accounts in Settings.Accounts, and never a network
//     account (one ending in "-network", which holds other accounts);
//   - a new campaign is bid FIXED, under the CPC and daily cap ceilings, with
//     a total budget of at most 30 daily caps;
//   - ads carry a plain link (no {macros}: Taboola escapes them) and a title
//     of 1 to 100 characters;
//   - nothing is ever created running: campaigns, copies and groups are made
//     with is_active false, and every new ad is sent with is_active false
//     and paused again when Taboola's answer says otherwise. Only a person
//     turns anything on, in Taboola's own dashboard;
//   - with OnlyOwn (a lent account), only campaigns and groups this server
//     created, named with NamePrefix and recorded in StateFile, are listed
//     or touched (own.go).
//
// Every request and answer (never the token, never the secret) is kept raw in
// the keep folder before anything reads it. A reply that cannot be kept is
// not handed back (ErrKeep), as in package openai.
package taboola

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Raposa-Industries/adhunters/create/internal/keep"
)

// DefaultBase is Backstage's production host.
const DefaultBase = "https://backstage.taboola.com"

const (
	tokenPath       = "/backstage/oauth/token"
	apiPrefix       = "/backstage/api/1.0/"
	uploadPath      = "operations/upload-image"
	allowedAccounts = "users/current/allowed-accounts/"
	keepKind        = "taboola"

	// maxRetries is how often a 429 (or a 5xx where a repeat is safe) is
	// tried again.
	maxRetries = 3
	// maxWait caps any pause between tries.
	maxWait = 2 * time.Minute
)

// Settings are the knobs create-web reads from its environment.
type Settings struct {
	// Base is Backstage's host; empty means DefaultBase.
	Base         string
	ClientID     string
	ClientSecret string
	// Accounts are the advertiser account ids ("acme-sc") the page may use.
	Accounts []string
	// MaxCPC and MaxDailyCap (USD) bound a new campaign; its total budget is
	// at most 30 daily caps.
	MaxCPC, MaxDailyCap float64

	// OnlyOwn limits the client to what it created itself: new campaigns and
	// groups must be named with NamePrefix, each is recorded in StateFile,
	// and nothing else in the account is listed or touched. For an account
	// that is lent, where everything else is someone else's.
	OnlyOwn    bool
	NamePrefix string
	StateFile  string
}

// ErrKeep means Taboola answered but the answer could not be kept, so it is
// not handed back. What was asked may have happened.
var ErrKeep = errors.New("a Taboola respondeu, mas não consegui guardar a resposta no disco; confira no Taboola antes de repetir")

// ErrNotConfigured is returned by every call when the client is off.
var ErrNotConfigured = errors.New("taboola: not configured")

// Refused is a request the guard stopped before it was sent. Message is one
// pt-BR line for the person.
type Refused struct{ Message string }

func (e *Refused) Error() string { return e.Message }

func refuse(f string, a ...any) error { return &Refused{Message: fmt.Sprintf(f, a...)} }

// Error is a failed answer from Taboola (or none at all), with one short
// pt-BR line built from Taboola's own message, never a whole payload.
type Error struct {
	Status  int // 0 when no answer arrived
	Message string
}

func (e *Error) Error() string { return "taboola: " + e.Message }

// Client talks to Backstage with one login's client credentials. It is safe
// for concurrent use. A nil *Client is a client that is off.
type Client struct {
	s    Settings
	http *http.Client
	keep *keep.Folder
	log  *slog.Logger
	// wait pauses between tries; tests make it instant.
	wait func(context.Context, time.Duration) error

	mu      sync.Mutex
	token   string
	expires time.Time
	names   map[string]string // account id -> name, from allowed-accounts
	namesAt time.Time

	stateMu sync.Mutex
	own     ownState // with OnlyOwn: what this client created
}

// New returns a client. Accounts are trimmed and empty ones dropped. With
// OnlyOwn it needs NamePrefix and StateFile, and reads the state file when
// there is one.
func New(s Settings, kept *keep.Folder, log *slog.Logger) (*Client, error) {
	if s.Base == "" {
		s.Base = DefaultBase
	}
	s.Base = strings.TrimRight(s.Base, "/")
	var accts []string
	for _, a := range s.Accounts {
		if a = strings.TrimSpace(a); a != "" {
			accts = append(accts, a)
		}
	}
	s.Accounts = accts
	c := &Client{
		s: s,
		// Every call also carries the caller's deadline; this only stops one
		// stuck exchange from holding a whole batch.
		http: &http.Client{Timeout: 2 * time.Minute},
		keep: kept,
		log:  log,
		wait: sleep,
	}
	if s.OnlyOwn {
		if err := c.loadOwn(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Available reports whether calls can be made at all.
func (c *Client) Available() bool {
	return c != nil && c.s.ClientID != "" && c.s.ClientSecret != "" && len(c.usable()) > 0
}

// Why is the reason the client is off, in pt-BR, or "" when it is on.
func (c *Client) Why() string {
	if c.Available() {
		return ""
	}
	var missing []string
	if c == nil || c.s.ClientID == "" {
		missing = append(missing, "TABOOLA_CLIENT_ID")
	}
	if c == nil || c.s.ClientSecret == "" {
		missing = append(missing, "TABOOLA_CLIENT_SECRET")
	}
	if c == nil || len(c.usable()) == 0 {
		missing = append(missing, "TABOOLA_ACCOUNTS")
	}
	if len(missing) == 3 {
		return "Taboola não conectado: falta TABOOLA_CLIENT_ID, TABOOLA_CLIENT_SECRET ou TABOOLA_ACCOUNTS no servidor"
	}
	return "Taboola não conectado: falta " + strings.Join(missing, " e ") + " no servidor"
}

// OnlyOwn reports whether the client only sees what it created, and the
// prefix those names start with.
func (c *Client) OnlyOwn() (bool, string) {
	if c == nil || !c.s.OnlyOwn {
		return false, ""
	}
	return true, c.s.NamePrefix
}

// Limits are the ceilings a new campaign is checked against (0, 0 when off).
func (c *Client) Limits() (maxCPC, maxDailyCap float64) {
	if c == nil {
		return 0, 0
	}
	return c.s.MaxCPC, c.s.MaxDailyCap
}

// usable is Settings.Accounts without network accounts.
func (c *Client) usable() []string {
	var out []string
	for _, a := range c.s.Accounts {
		if !isNetwork(a) {
			out = append(out, a)
		}
	}
	return out
}

func isNetwork(account string) bool { return strings.HasSuffix(account, "-network") }

// CheckAccount refuses a network account and any account not in
// Settings.Accounts.
func (c *Client) CheckAccount(account string) error {
	if !c.Available() {
		return ErrNotConfigured
	}
	if account == "" {
		return refuse("escolha a conta da Taboola")
	}
	if isNetwork(account) {
		return refuse("a conta %s é uma conta de rede e nunca é usada aqui", account)
	}
	for _, a := range c.s.Accounts {
		if a == account {
			return nil
		}
	}
	return refuse("a conta %s não está liberada em TABOOLA_ACCOUNTS", account)
}

// call is one request to the API.
type call struct {
	method string
	path   string // relative to apiPrefix
	body   []byte
	ctype  string
	// kept is what is saved as the request body (the body itself for JSON, a
	// line for an image).
	kept any
	// retry5xx is true where a repeat can never make something twice: reads,
	// and image uploads (a second copy on the CDN touches no campaign).
	retry5xx bool
}

// exchange is one request and its answer, as kept. The token and the secret
// are never in it.
type exchange struct {
	Time    time.Time `json:"time"`
	Method  string    `json:"method"`
	Path    string    `json:"path"`
	Attempt int       `json:"attempt"`
	Request any       `json:"request,omitempty"`
	Status  int       `json:"status"`
	Body    any       `json:"body,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// do sends one request and returns the body of a 2xx answer. It is the only
// place requests leave, and it re-checks the path against the guard.
func (c *Client) do(ctx context.Context, k call) ([]byte, error) {
	if err := c.checkPath(k.method, k.path); err != nil {
		return nil, err
	}
	refreshed := false
	for attempt, retries := 1, 0; ; attempt++ {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		var rd io.Reader
		if k.body != nil {
			rd = bytes.NewReader(k.body)
		}
		req, err := http.NewRequestWithContext(ctx, k.method, c.s.Base+apiPrefix+k.path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if k.ctype != "" {
			req.Header.Set("Content-Type", k.ctype)
		}
		ex := exchange{Time: time.Now().UTC(), Method: k.method, Path: k.path, Attempt: attempt, Request: k.kept}
		res, err := c.http.Do(req)
		if err != nil {
			ex.Error = err.Error()
			if kerr := c.keepExchange(ex); kerr != nil {
				c.log.Error("taboola exchange not kept", "err", kerr)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			msg := "sem resposta da Taboola"
			if k.method != http.MethodGet {
				msg += "; confira no Taboola antes de repetir"
			}
			return nil, &Error{Message: msg}
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		ex.Status = res.StatusCode
		ex.Body = rawBody(body)
		if err != nil {
			ex.Error = err.Error()
		}
		if kerr := c.keepExchange(ex); kerr != nil {
			c.log.Error("taboola answer not kept, not handed back", "method", k.method, "path", k.path, "status", res.StatusCode, "err", kerr)
			return nil, fmt.Errorf("%w: %v", ErrKeep, kerr)
		}
		if err != nil {
			return nil, &Error{Status: res.StatusCode, Message: "resposta da Taboola cortada no meio"}
		}

		switch {
		case res.StatusCode == http.StatusUnauthorized && !refreshed:
			// Expired early or revoked: fetch a new token, once. A 401 was
			// not acted on, so repeating it is safe for any method.
			refreshed = true
			c.forgetToken()
			continue
		case (res.StatusCode == http.StatusTooManyRequests || (res.StatusCode >= 500 && k.retry5xx)) && retries < maxRetries:
			// A 429 was not acted on. A 5xx after a create may have created
			// it, so creates are never repeated on one.
			if err := c.wait(ctx, backoff(res.Header, retries)); err != nil {
				return nil, err
			}
			retries++
			continue
		case res.StatusCode/100 == 2:
			return body, nil
		}
		return nil, answerError(k.method, res.StatusCode, body)
	}
}

// checkPath lets through the image upload, the list of accounts the login
// may read, and paths under an allowed account.
func (c *Client) checkPath(method, path string) error {
	switch {
	case method == http.MethodPost && path == uploadPath:
		return nil
	case method == http.MethodGet && path == allowedAccounts:
		return nil
	}
	account, rest, ok := strings.Cut(path, "/")
	if !ok {
		return refuse("caminho da Taboola fora das contas liberadas: %s", path)
	}
	if err := c.CheckAccount(account); err != nil {
		return err
	}
	if c.s.OnlyOwn {
		return c.checkOwnPath(account, rest)
	}
	return nil
}

func (c *Client) keepExchange(ex exchange) error {
	_, err := c.keep.JSON(keepKind, ex)
	return err
}

// rawBody keeps a JSON answer as JSON and anything else as text.
func rawBody(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}

// answerError turns a non-2xx answer into one line: Taboola's own message,
// and the field it objected to when it names one.
func answerError(method string, status int, body []byte) error {
	var a struct {
		Message        string `json:"message"`
		OffendingField string `json:"offending_field"`
	}
	_ = json.Unmarshal(body, &a)
	var msg string
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		msg = fmt.Sprintf("a Taboola recusou o acesso (HTTP %d)", status)
	case status >= 500:
		msg = fmt.Sprintf("a Taboola falhou (HTTP %d)", status)
	default:
		msg = fmt.Sprintf("a Taboola recusou (HTTP %d)", status)
	}
	if m := oneLine(a.Message, 200); m != "" {
		msg += ": " + m
	}
	if f := oneLine(a.OffendingField, 60); f != "" {
		msg += " (campo " + f + ")"
	}
	if status >= 500 && method != http.MethodGet {
		msg += "; confira no Taboola antes de repetir"
	}
	return &Error{Status: status, Message: msg}
}

// Message is err as one pt-BR line for the person.
func Message(err error) string {
	var r *Refused
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &r):
		return r.Message
	case errors.As(err, &e):
		return e.Message
	case errors.Is(err, ErrKeep):
		return ErrKeep.Error()
	case errors.Is(err, ErrNotConfigured):
		return (*Client)(nil).Why()
	case errors.Is(err, context.DeadlineExceeded):
		return "a Taboola demorou demais; confira no Taboola o que foi criado antes de repetir"
	}
	return "falha ao falar com a Taboola"
}

// oneLine collapses whitespace and cuts s to max characters.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// accessToken returns a cached token, fetching one when none is left or the
// current one ends within a minute. The token exchange is never kept: its
// request holds the secret and its answer the token.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > time.Minute {
		return c.token, nil
	}
	form := url.Values{
		"client_id":     {c.s.ClientID},
		"client_secret": {c.s.ClientSecret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.s.Base+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &Error{Message: "sem resposta da Taboola ao pedir acesso"}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK {
		c.log.Warn("taboola token refused", "status", res.StatusCode)
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusForbidden {
			return "", &Error{Status: res.StatusCode, Message: fmt.Sprintf("a Taboola recusou TABOOLA_CLIENT_ID/TABOOLA_CLIENT_SECRET (HTTP %d)", res.StatusCode)}
		}
		return "", &Error{Status: res.StatusCode, Message: fmt.Sprintf("a Taboola falhou ao dar acesso (HTTP %d)", res.StatusCode)}
	}
	if json.Unmarshal(body, &t) != nil || t.AccessToken == "" {
		return "", &Error{Status: res.StatusCode, Message: "a Taboola deu acesso sem token"}
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 3600
	}
	c.token = t.AccessToken
	c.expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *Client) forgetToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// backoff honours Retry-After (seconds) and otherwise waits 2, 4, 8 s.
func backoff(h http.Header, retry int) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxWait)
	}
	return time.Duration(2<<retry) * time.Second
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// obj is a JSON object as Taboola sends or takes it.
type obj = map[string]any

// sendJSON sends body as JSON and decodes the answer into an object.
func (c *Client) sendJSON(ctx context.Context, method, path string, body any, retry5xx bool) (obj, error) {
	k := call{method: method, path: path, retry5xx: retry5xx}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		k.body, k.ctype, k.kept = raw, "application/json", json.RawMessage(raw)
	}
	b, err := c.do(ctx, k)
	if err != nil {
		return nil, err
	}
	var out obj
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, &Error{Status: http.StatusOK, Message: "a Taboola respondeu algo que não é JSON"}
	}
	return out, nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func results(o obj) []obj {
	rows, _ := o["results"].([]any)
	out := make([]obj, 0, len(rows))
	for _, r := range rows {
		if m, ok := r.(obj); ok {
			out = append(out, m)
		}
	}
	return out
}
