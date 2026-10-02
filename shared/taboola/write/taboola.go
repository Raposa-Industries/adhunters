// Package write is the Taboola Backstage client that changes things: the
// advertiser accounts a login may use, their groups and campaigns, a new
// campaign, a copy, image uploads and ads (Taboola's items) made in bulk.
// AdHunters Launch is the one app that writes to Taboola; create-web's
// launcher page uses it too until Launch replaces it. The walls check lets
// no other module import it (scripts/check-walls.sh).
//
// The token, sending, retries and the image form come from shared/taboola,
// which Intel's read-only client uses too (decision 0013). The guards and
// their pt-BR messages live here with the writes they guard, so both apps
// refuse the same things the same way. What the write tests
// of 2026-09-29 learned is kept, with the reason next to it.
//
// Guards, checked before a request leaves (do):
//
//   - only the advertiser accounts in Settings.Accounts, and never a network
//     account (one ending in "-network", which holds other accounts);
//   - a new campaign is bid FIXED, under the CPC and daily cap ceilings, with
//     a total budget of at most 30 daily caps;
//   - ads carry a plain link (no {macros}: Taboola escapes them) and a title
//     of 1 to 100 characters;
//   - nothing is created running unless CreateActive is on: campaigns,
//     copies and groups are made with is_active false, and every new ad is
//     sent with is_active false and paused again when Taboola's answer says
//     otherwise. With CreateActive (the owner, 2026-10-02) a new campaign,
//     its group and the ads a person makes in Launch or Create go up
//     running; copies (duplicate, move) still arrive paused, and nothing
//     else ever sets is_active true;
//   - with OnlyOwn (a lent account), only campaigns and groups this server
//     created, named with NamePrefix and recorded in StateFile, are listed
//     or touched (own.go).
//
// Every request and answer (never the token, never the secret) is kept raw in
// the keep folder before anything reads it. A reply that cannot be kept is
// not handed back (ErrKeep), as in package openai.
package write

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Raposa-Industries/adhunters/kit/keep"
	api "github.com/Raposa-Industries/adhunters/shared/taboola"
)

// DefaultBase is Backstage's production host.
const DefaultBase = api.DefaultBase

const (
	tokenPath       = api.TokenPath
	apiPrefix       = api.APIPrefix
	uploadPath      = api.UploadPath
	allowedAccounts = "users/current/allowed-accounts/"
	keepKind        = "taboola"

	// maxRetries is how often a 429 (or a 5xx where a repeat is safe) is
	// tried again.
	maxRetries = 3
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
	// MaxSpendLimit (USD), when above 0, is the most one campaign may ever
	// spend: every campaign made or copied gets a total (lifetime) spending
	// limit no higher than it, and no change raises one above it.
	MaxSpendLimit float64
	// CreateActive makes a new campaign, a new group and new ads go up
	// running (is_active true) instead of paused. Copies stay paused, and
	// no other call can turn anything on.
	CreateActive bool

	// OnlyOwn limits the client to what it created itself: each new campaign
	// and group is recorded in StateFile, and nothing else in the account is
	// listed or touched. NamePrefix, when set, is a name every new one must
	// start with. For an account
	// that is lent, where everything else is someone else's.
	OnlyOwn    bool
	NamePrefix string
	StateFile  string

	// HTTP is the client every request of this login goes through, the
	// token included; nil is a plain one. Launch gives each account with a
	// proxy one whose transport goes only through that proxy.
	HTTP *http.Client
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
	api  *api.Client
	keep *keep.Folder
	log  *slog.Logger
	// wait pauses between tries; tests make it instant.
	wait func(context.Context, time.Duration) error

	mu      sync.Mutex
	names   map[string]string // account id -> name, from allowed-accounts
	listed  []Account         // the same accounts in Taboola's order, the network left out
	network string            // the login's network account id ("" for a single account)
	namesAt time.Time

	stateMu sync.Mutex
	own     ownState // with OnlyOwn: what this client created
}

// New returns a client. Accounts are trimmed and empty ones dropped. With
// OnlyOwn it needs StateFile (NamePrefix is optional), and reads the state file when
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
	c := &Client{s: s, keep: kept, log: log, wait: api.Sleep}
	// Every call also carries the caller's deadline; the shared client's 2
	// minute timeout only stops one stuck exchange from holding a whole batch.
	c.api = api.New(s.Base, s.ClientID, s.ClientSecret, s.HTTP)
	c.api.MaxRetries = maxRetries
	c.api.Wait = func(ctx context.Context, d time.Duration) error { return c.wait(ctx, d) }
	c.api.Record = c.keepAttempt
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
// SpendLimit is the most one campaign may spend in total (0: no ceiling
// beyond 30 daily caps).
func (c *Client) SpendLimit() float64 {
	if c == nil {
		return 0
	}
	return c.s.MaxSpendLimit
}

// totalCeiling is the highest total spending limit a campaign may have.
func (c *Client) totalCeiling() float64 {
	if c.s.MaxSpendLimit > 0 {
		return c.s.MaxSpendLimit
	}
	return 30 * c.s.MaxDailyCap
}

// CreateActive reports whether new campaigns, groups and ads go up running.
func (c *Client) CreateActive() bool {
	return c != nil && c.s.CreateActive
}

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
	res, err := c.api.Do(ctx, api.Request{
		Method: k.method, Path: k.path, Body: k.body, ContentType: k.ctype,
		Log: k.kept, Retry5xx: k.retry5xx,
	})
	if err == nil {
		return res.Body, nil
	}
	var (
		send *api.SendError
		rec  *api.RecordError
		cut  *api.CutError
		st   *api.StatusError
		tok  *api.TokenError
	)
	switch {
	case errors.As(err, &send):
		if send.RecordErr != nil {
			c.log.Error("taboola exchange not kept", "err", send.RecordErr)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if line, ok := saysItself(send.Err); ok {
			return nil, &Error{Message: line}
		}
		msg := "sem resposta da Taboola"
		if k.method != http.MethodGet {
			msg += "; confira no Taboola antes de repetir"
		}
		return nil, &Error{Message: msg}
	case errors.As(err, &rec):
		c.log.Error("taboola answer not kept, not handed back", "method", k.method, "path", k.path, "status", rec.Status, "err", rec.Err)
		return nil, fmt.Errorf("%w: %v", ErrKeep, rec.Err)
	case errors.As(err, &cut):
		return nil, &Error{Status: cut.Status, Message: "resposta da Taboola cortada no meio"}
	case errors.As(err, &st):
		return nil, answerError(k.method, st.Status, st.Body)
	case errors.As(err, &tok):
		return nil, c.tokenError(ctx, tok)
	case errors.Is(err, api.ErrNoCredentials):
		return nil, ErrNotConfigured
	}
	return nil, err
}

// keepAttempt keeps one attempt raw before anything reads it.
func (c *Client) keepAttempt(a api.Exchange) error {
	ex := exchange{Time: a.Time, Method: a.Method, Path: a.Path, Attempt: a.Attempt, Request: a.Log, Status: a.Status, Body: rawBody(a.Body)}
	if a.Err != nil {
		ex.Error = a.Err.Error()
	}
	return c.keepExchange(ex)
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

// tokenError is a failed token request as one pt-BR line. The token
// exchange itself is never kept: its request holds the secret and its answer
// the token.
func (c *Client) tokenError(ctx context.Context, e *api.TokenError) error {
	switch e.Status {
	case 0:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if line, ok := saysItself(e.Err); ok {
			return &Error{Message: line}
		}
		return &Error{Message: "sem resposta da Taboola ao pedir acesso"}
	case http.StatusOK:
		return &Error{Status: e.Status, Message: "a Taboola deu acesso sem token"}
	}
	c.log.Warn("taboola token refused", "status", e.Status)
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusBadRequest || e.Status == http.StatusForbidden {
		return &Error{Status: e.Status, Message: fmt.Sprintf("a Taboola recusou TABOOLA_CLIENT_ID/TABOOLA_CLIENT_SECRET (HTTP %d)", e.Status)}
	}
	return &Error{Status: e.Status, Message: fmt.Sprintf("a Taboola falhou ao dar acesso (HTTP %d)", e.Status)}
}

// saysItself finds, in a failed exchange's error, one from Settings.HTTP's
// transport that carries its own pt-BR line for the person (Launch's proxy:
// which proxy failed, and that nothing went direct).
func saysItself(err error) (string, bool) {
	var s interface{ Say() string }
	if errors.As(err, &s) {
		return s.Say(), true
	}
	return "", false
}

// obj is a JSON object as Taboola sends or takes it.
type obj = map[string]any

// sendJSON sends body as JSON and decodes the answer into an object.
// turnsOn reports whether a JSON body sets is_active true anywhere in it.
func turnsOn(raw []byte) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return true // not ours to send
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if k == "is_active" && e != false {
					return true
				}
				if walk(e) {
					return true
				}
			}
		case []any:
			for _, e := range x {
				if walk(e) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

func (c *Client) sendJSON(ctx context.Context, method, path string, body any, retry5xx bool) (obj, error) {
	return c.send(ctx, method, path, body, retry5xx, false)
}

// sendNew is sendJSON for a create that may go up running: is_active true
// passes the guard only with CreateActive.
func (c *Client) sendNew(ctx context.Context, method, path string, body any) (obj, error) {
	return c.send(ctx, method, path, body, false, c.s.CreateActive)
}

func (c *Client) send(ctx context.Context, method, path string, body any, retry5xx, on bool) (obj, error) {
	k := call{method: method, path: path, retry5xx: retry5xx}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		// Nothing here turns a campaign or an ad on, except a new one when
		// CreateActive is on. Checked on the bytes sent, whatever built them.
		if !on && turnsOn(raw) {
			return nil, refuse("o Launch nunca liga campanha nem anúncio: só uma pessoa liga, no Taboola")
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
