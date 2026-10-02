// Package logins is the work behind Launch's Contas page: the ad network
// logins people add so Launch can use more accounts than the server's own
// login (TABOOLA_* in its environment). Taboola only, for now.
//
// Adding a login never changes anything on the network: Launch asks Taboola
// for a token and the login's account list (both reads), and people choose
// which advertiser accounts to use. The secret is sealed with the server's
// key (seal.go) before it reaches the database and is never shown or logged
// again; a page sees only the start and end of the client id.
//
// Every added login goes through its proxy (proxy.go), the check before it
// is added too; an account of the server's own login goes through one when
// a person set it here, and direct otherwise.
package logins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Raposa-Industries/adhunters/kit/keep"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	tbnet "github.com/Raposa-Industries/adhunters/launch/internal/network/taboola"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
	"github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// Service keeps the added logins and the network that uses them.
type Service struct {
	st  *store.Store
	box *Box
	net *tbnet.Logins
	// server is the server's own login; template its settings, whose base,
	// ceilings and state folder every added login shares.
	server   *write.Client
	template write.Settings
	// serverSet is the server's own login's settings, for a client per
	// account of it that has a proxy.
	serverSet write.Settings
	kept      *keep.Folder
	log       *slog.Logger

	mu sync.Mutex
	// parts are the clients that serve the server's own accounts: its own
	// for those that go direct, then one per account with a proxy.
	parts []part
}

// part is one client and the accounts it serves, with its proxy's host and
// port ("" direct).
type part struct {
	c        *write.Client
	accounts []string
	proxy    string
}

// New returns the service. server is the server's own client and its
// settings; net is the network Launch's actions use.
func New(st *store.Store, box *Box, net *tbnet.Logins, server *write.Client, set write.Settings, kept *keep.Folder, log *slog.Logger) *Service {
	full := set
	set.ClientID, set.ClientSecret, set.Accounts, set.HTTP = "", "", nil, nil
	set.OnlyOwn, set.NamePrefix, set.StateFile = false, "", ""
	return &Service{st: st, box: box, net: net, server: server, template: set, serverSet: full, kept: kept, log: log}
}

// View is a login as the Contas page shows it.
type View struct {
	ID int64 `json:"id"` // 0 for the server's own
	// Server is the server's own login, set in its environment; the page
	// cannot change or remove it.
	Server   bool   `json:"server"`
	Network  string `json:"network"`
	Name     string `json:"name"`
	ClientID string `json:"client_id"` // only its start and end
	// UserID is an added login's Taboola user ID ("" for one added before
	// it, or the server's own).
	UserID   string    `json:"user_id,omitempty"`
	Accounts []Account `json:"accounts"`
	// Proxy is an added login's proxy, host and port only ("" when it has
	// none and is refused).
	Proxy   string     `json:"proxy,omitempty"`
	AddedBy string     `json:"added_by,omitempty"`
	AddedAt *time.Time `json:"added_at,omitempty"`
	// Problem is why the login is not used, in pt-BR, or "".
	Problem string `json:"problem,omitempty"`
}

// Account is one account as the Contas page shows it.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Proxy is the host and port its requests go through; "" is direct
	// (only an account of the server's own login can be).
	Proxy string `json:"proxy"`
	// Problem is why the account cannot be used now, in pt-BR, or "".
	Problem string `json:"problem,omitempty"`
	// AddedAt is when an account of the server's own login was first used
	// by Launch (an added login's accounts have their login's AddedAt).
	AddedAt *time.Time `json:"added_at,omitempty"`
}

// Load reads the added logins and the server's accounts' proxies, and hands
// them to the network: the server's own client for its accounts without a
// proxy, one client per account with one, then each added login through
// its proxy. A login whose secret does not open is left out and logged; a
// proxy that does not open refuses every request of its accounts.
func (s *Service) Load(ctx context.Context) error {
	rows, err := s.st.Logins(ctx)
	if err != nil {
		return err
	}
	proxies, err := s.st.AccountProxies(ctx, "taboola")
	if err != nil {
		return err
	}
	var added []tbnet.Login
	var parts []part
	direct := []string{}
	for _, a := range s.serverSet.Accounts {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		sealed, ok := proxies[a]
		if !ok || !s.server.Available() {
			direct = append(direct, a)
			continue
		}
		c, at, err := s.accountClient(a, sealed)
		if err != nil {
			return err
		}
		added = append(added, tbnet.Login{T: tbnet.New(c), Accounts: []string{a}})
		parts = append(parts, part{c: c, accounts: []string{a}, proxy: at})
	}
	if len(direct) > 0 {
		parts = append([]part{{c: s.server, accounts: direct}}, parts...)
	}
	for _, r := range rows {
		c, _, err := s.client(r)
		if err != nil {
			s.log.Warn("login not used", "login", r.ID, "err", err)
			continue
		}
		added = append(added, tbnet.Login{T: tbnet.New(c), Accounts: r.Accounts})
	}
	s.net.Use(direct, added)
	s.mu.Lock()
	s.parts = parts
	s.mu.Unlock()
	return nil
}

// client opens a row's secret and proxy and builds its client, with the
// proxy's host and port. Without a proxy that opens, every request of the
// login is refused: an added login never goes direct.
func (s *Service) client(r store.Login) (*write.Client, string, error) {
	sec, err := s.box.Open(r.Secret, bound(r.Network, r.ClientID))
	if err != nil {
		return nil, "", err
	}
	hc, at, _ := s.loginProxy(r)
	set := s.template
	set.ClientID, set.ClientSecret, set.Accounts, set.HTTP = r.ClientID, string(sec), r.Accounts, hc
	c, err := write.New(set, s.kept, s.log)
	return c, at, err
}

// loginProxy is the HTTP client an added login's requests go through and
// its proxy's host and port; without a usable proxy, a client that refuses
// every request, and why.
func (s *Service) loginProxy(r store.Login) (hc *http.Client, at, why string) {
	switch raw, err := s.box.Open(r.Proxy, proxyBound(r.Network, r.ClientID)); {
	case len(r.Proxy) == 0:
		why = noProxy
	case err != nil:
		why = "não consegui abrir o proxy deste acesso: a chave do servidor mudou; defina o proxy de novo em Contas"
	default:
		u, err := ParseProxy(string(raw))
		if err != nil {
			why = "o proxy guardado deste acesso não vale; defina o proxy de novo em Contas"
			break
		}
		return proxyClient(u), HostPort(u), ""
	}
	return blockedClient(why), "", why
}

// noProxy is why an added login without a proxy is refused.
const noProxy = "este acesso não tem proxy, e as contas dele nunca vão direto à Taboola; defina o proxy em Contas, no menu ··· da conta"

// accountClient is the client of one account of the server's own login that
// has a proxy: the server's settings, that account only, through it.
func (s *Service) accountClient(account string, sealed []byte) (*write.Client, string, error) {
	set := s.serverSet
	set.Accounts = []string{account}
	at := ""
	raw, err := s.box.Open(sealed, accountBound("taboola", account))
	switch {
	case s.serverSet.OnlyOwn:
		// Two clients would share one state file.
		set.HTTP = blockedClient("o proxy por conta não funciona com TABOOLA_ONLY_OWN ligado; tire o proxy desta conta")
	case err != nil:
		set.HTTP = blockedClient("não consegui abrir o proxy desta conta: a chave do servidor mudou; defina o proxy de novo em Contas")
	default:
		u, perr := ParseProxy(string(raw))
		if perr != nil {
			set.HTTP = blockedClient("o proxy guardado desta conta não vale; defina o proxy de novo em Contas")
			break
		}
		set.HTTP, at = proxyClient(u), HostPort(u)
	}
	if s.serverSet.OnlyOwn {
		set.OnlyOwn, set.StateFile = false, ""
	}
	c, err := write.New(set, s.kept, s.log)
	return c, at, err
}

// List is the server's own login, then every added one, with their accounts
// named as Taboola names them and the proxy each goes through. Each list is
// asked of the client that serves those accounts, so through their proxy.
func (s *Service) List(ctx context.Context) ([]View, error) {
	var out []View
	if s.server.Available() {
		v := View{Server: true, Network: "taboola", Name: "Login do servidor", ClientID: hint(s.serverID())}
		s.mu.Lock()
		parts := s.parts
		s.mu.Unlock()
		for _, p := range parts {
			v.Accounts = append(v.Accounts, accountsOf(ctx, p.c, p.accounts, p.proxy)...)
		}
		ids := make([]string, len(v.Accounts))
		for i, a := range v.Accounts {
			ids[i] = a.ID
		}
		// "Adicionada" on Contas; without it the column says "—".
		if seen, err := s.st.AccountsSeen(ctx, "taboola", ids); err != nil {
			s.log.Warn("accounts seen", "err", err)
		} else {
			for i, a := range v.Accounts {
				if at, ok := seen[a.ID]; ok {
					v.Accounts[i].AddedAt = &at
				}
			}
		}
		out = append(out, v)
	}
	rows, err := s.st.Logins(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		at := r.AddedAt
		v := View{ID: r.ID, Network: r.Network, Name: r.Name, ClientID: hint(r.ClientID), UserID: r.UserID, AddedBy: r.AddedBy, AddedAt: &at}
		c, proxy, err := s.client(r)
		v.Proxy = proxy
		if err != nil {
			v.Problem = "Não consegui abrir o secret deste login: a chave do servidor mudou. Remova e adicione de novo."
			for _, a := range r.Accounts {
				v.Accounts = append(v.Accounts, Account{ID: a, Name: a, Proxy: proxy})
			}
		} else {
			v.Accounts = accountsOf(ctx, c, r.Accounts, proxy)
		}
		out = append(out, v)
	}
	return out, nil
}

// accountsOf names a client's accounts, the ones in want only, asking
// Taboola through the client (so through its proxy) which accounts it sees.
// When that fails, they are listed by id with why.
func accountsOf(ctx context.Context, c *write.Client, want []string, proxy string) []Account {
	var out []Account
	list, err := c.Allowed(ctx)
	names := map[string]string{}
	network := map[string]bool{}
	for _, a := range list {
		names[a.ID], network[a.ID] = a.Name, a.Network
	}
	for _, id := range want {
		// A network account is never used, so never listed.
		if network[id] || strings.HasSuffix(id, "-network") {
			continue
		}
		a := Account{ID: id, Name: names[id], Proxy: proxy}
		if a.Name == "" {
			a.Name = id
		}
		if err != nil {
			a.Problem = tbnet.Message(err)
		}
		out = append(out, a)
	}
	return out
}

func (s *Service) serverID() string { return s.server.ClientID() }

// hint shows a client id's first and last four characters.
func hint(id string) string {
	r := []rune(id)
	if len(r) <= 8 {
		if len(r) <= 2 {
			return "…"
		}
		return string(r[:2]) + "…"
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

func refuse(f string, a ...any) error { return &network.Refused{Message: fmt.Sprintf(f, a...)} }

// Check asks Taboola, through the proxy, which accounts a login can see. It
// only reads: a token and the account list.
func (s *Service) Check(ctx context.Context, clientID, secret, proxy string) ([]write.Allowed, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" {
		return nil, refuse("preencha o client ID e o client secret")
	}
	u, err := ParseProxy(proxy)
	if err != nil {
		return nil, err
	}
	return s.check(ctx, clientID, secret, proxyClient(u))
}

// check asks Taboola which accounts a login sees, through hc.
func (s *Service) check(ctx context.Context, clientID, secret string, hc *http.Client) ([]write.Allowed, error) {
	clientID, secret = strings.TrimSpace(clientID), strings.TrimSpace(secret)
	if clientID == "" || secret == "" {
		return nil, refuse("preencha o client ID e o client secret")
	}
	if len(clientID) > 200 || len(secret) > 500 {
		return nil, refuse("client ID ou client secret longo demais")
	}
	set := s.template
	set.ClientID, set.ClientSecret, set.HTTP = clientID, secret, hc
	c, err := write.New(set, s.kept, s.log)
	if err != nil {
		return nil, err
	}
	list, err := c.Allowed(ctx)
	var we *write.Error
	if errors.As(err, &we) && (we.Status == http.StatusUnauthorized || we.Status == http.StatusBadRequest || we.Status == http.StatusForbidden) {
		if !looksLikeKey(clientID) {
			return nil, refuse("a Taboola recusou esse client ID e client secret (HTTP %d): o client ID é a chave de 32 letras e números da página de API do Backstage, e esse parece o nome de uma conta", we.Status)
		}
		return nil, refuse("a Taboola recusou esse client ID e client secret (HTTP %d); confira e tente de novo", we.Status)
	}
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(list, func(a write.Allowed) bool { return !a.Network }) {
		return nil, refuse("a Taboola aceitou o login, mas ele não tem nenhuma conta de anunciante")
	}
	return list, nil
}

// chosen checks people's choice against what the login can see.
func chosen(allowed []write.Allowed, accounts []string) ([]string, error) {
	var out []string
	for _, a := range accounts {
		a = strings.TrimSpace(a)
		if a == "" || slices.Contains(out, a) {
			continue
		}
		i := slices.IndexFunc(allowed, func(x write.Allowed) bool { return x.ID == a })
		if i < 0 {
			return nil, refuse("a conta %s não está neste login", a)
		}
		if allowed[i].Network {
			return nil, refuse("a conta %s é uma conta de rede e nunca é usada aqui", a)
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, refuse("escolha pelo menos uma conta")
	}
	return out, nil
}

func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", refuse("dê um nome ao login")
	}
	if utf8.RuneCountInString(name) > 60 {
		return "", refuse("nome longo demais (até 60 letras)")
	}
	return name, nil
}

// cleanUserID checks a Taboola user ID as typed. Taboola's API never asks
// for it (the token takes only the client ID and secret); Launch keeps it to
// tell logins apart.
func cleanUserID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", refuse("preencha o user ID")
	}
	if utf8.RuneCountInString(id) > 100 || strings.ContainsFunc(id, unicode.IsSpace) {
		return "", refuse("user ID inválido: uma palavra de até 100 letras")
	}
	return id, nil
}

// SetUserID changes an added login's Taboola user ID. Nothing is asked of
// Taboola.
func (s *Service) SetUserID(ctx context.Context, id int64, userID string) error {
	userID, err := cleanUserID(userID)
	if err != nil {
		return err
	}
	if err := s.st.SetLoginUserID(ctx, id, userID); err != nil {
		return err
	}
	s.log.Info("login user id changed", "login", id)
	return nil
}

// Add checks a login with Taboola again through its proxy, seals its secret
// and proxy and starts using the chosen accounts.
func (s *Service) Add(ctx context.Context, who, name, clientID, userID, secret, proxy string, accounts []string) (int64, error) {
	name, err := cleanName(name)
	if err != nil {
		return 0, err
	}
	if userID, err = cleanUserID(userID); err != nil {
		return 0, err
	}
	clientID, secret = strings.TrimSpace(clientID), strings.TrimSpace(secret)
	if clientID == s.serverID() {
		return 0, refuse("esse é o login do servidor, que o Launch já usa")
	}
	u, err := ParseProxy(proxy)
	if err != nil {
		return 0, err
	}
	allowed, err := s.check(ctx, clientID, secret, proxyClient(u))
	if err != nil {
		return 0, err
	}
	use, err := chosen(allowed, accounts)
	if err != nil {
		return 0, err
	}
	sealed, err := s.box.Seal([]byte(secret), bound("taboola", clientID))
	if err != nil {
		return 0, err
	}
	sealedProxy, err := s.box.Seal([]byte(u.String()), proxyBound("taboola", clientID))
	if err != nil {
		return 0, err
	}
	id, err := s.st.AddLogin(ctx, store.Login{Network: "taboola", Name: name, ClientID: clientID, Secret: sealed, Proxy: sealedProxy, UserID: userID, Accounts: use, AddedBy: who})
	if errors.Is(err, store.ErrLoginTaken) {
		return 0, refuse("esse login já foi adicionado; mude as contas dele na lista")
	}
	if err != nil {
		return 0, err
	}
	s.log.Info("login added", "login", id, "network", "taboola", "accounts", len(use), "by", who)
	return id, s.Load(ctx)
}

// Allowed is what an added login can see now, for choosing its accounts
// again.
func (s *Service) Allowed(ctx context.Context, id int64) ([]write.Allowed, error) {
	r, err := s.st.Login(ctx, id)
	if err != nil {
		return nil, err
	}
	sec, err := s.box.Open(r.Secret, bound(r.Network, r.ClientID))
	if err != nil {
		return nil, refuse("não consegui abrir o secret deste login; remova e adicione de novo")
	}
	hc, _, why := s.loginProxy(r)
	if why != "" {
		return nil, refuse("%s", why)
	}
	return s.check(ctx, r.ClientID, string(sec), hc)
}

// Change renames an added login and chooses its accounts again.
func (s *Service) Change(ctx context.Context, id int64, name string, accounts []string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	allowed, err := s.Allowed(ctx, id)
	if err != nil {
		return err
	}
	use, err := chosen(allowed, accounts)
	if err != nil {
		return err
	}
	if err := s.st.SetLoginAccounts(ctx, id, name, use); err != nil {
		return err
	}
	s.log.Info("login changed", "login", id, "accounts", len(use))
	return s.Load(ctx)
}

// Remove stops using an added login and forgets its secret. Nothing on the
// network changes.
func (s *Service) Remove(ctx context.Context, id int64) error {
	if err := s.st.RemoveLogin(ctx, id); err != nil {
		return err
	}
	s.log.Info("login removed", "login", id)
	return s.Load(ctx)
}

// SetLoginProxy changes the proxy an added login's accounts go through,
// after asking Taboola through the new one which accounts the login sees
// (a read). It cannot be emptied: an added login never goes direct.
func (s *Service) SetLoginProxy(ctx context.Context, id int64, proxy string) error {
	u, err := ParseProxy(proxy)
	if err != nil {
		return err
	}
	r, err := s.st.Login(ctx, id)
	if err != nil {
		return err
	}
	sec, err := s.box.Open(r.Secret, bound(r.Network, r.ClientID))
	if err != nil {
		return refuse("não consegui abrir o secret deste login; remova e adicione de novo")
	}
	if _, err := s.check(ctx, r.ClientID, string(sec), proxyClient(u)); err != nil {
		return err
	}
	sealed, err := s.box.Seal([]byte(u.String()), proxyBound(r.Network, r.ClientID))
	if err != nil {
		return err
	}
	if err := s.st.SetLoginProxy(ctx, id, sealed); err != nil {
		return err
	}
	s.log.Info("login proxy changed", "login", id)
	return s.Load(ctx)
}

// SetAccountProxy sets the proxy of one account of the server's own login,
// after asking Taboola through it for the login's accounts (a read); an
// empty proxy takes it away, and the account goes direct again.
func (s *Service) SetAccountProxy(ctx context.Context, who, account, proxy string) error {
	account = strings.TrimSpace(account)
	if !s.server.Available() || !slices.Contains(s.serverAccounts(), account) {
		return refuse("o proxy da conta %s muda pelo acesso dela", account)
	}
	if strings.TrimSpace(proxy) == "" {
		if err := s.st.SetAccountProxy(ctx, "taboola", account, nil, who); err != nil {
			return err
		}
		s.log.Info("account proxy removed", "account", account, "by", who)
		return s.Load(ctx)
	}
	if s.serverSet.OnlyOwn {
		return refuse("o proxy por conta não funciona com TABOOLA_ONLY_OWN ligado")
	}
	u, err := ParseProxy(proxy)
	if err != nil {
		return err
	}
	allowed, err := s.check(ctx, s.serverSet.ClientID, s.serverSet.ClientSecret, proxyClient(u))
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(allowed, func(a write.Allowed) bool { return a.ID == account }) {
		return refuse("pelo proxy, a Taboola não mostrou a conta %s", account)
	}
	sealed, err := s.box.Seal([]byte(u.String()), accountBound("taboola", account))
	if err != nil {
		return err
	}
	if err := s.st.SetAccountProxy(ctx, "taboola", account, sealed, who); err != nil {
		return err
	}
	s.log.Info("account proxy set", "account", account, "by", who)
	return s.Load(ctx)
}

// serverAccounts are the server's own login's accounts.
func (s *Service) serverAccounts() []string {
	var out []string
	for _, a := range s.serverSet.Accounts {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// looksLikeKey is a client ID shaped like Taboola's: 32 hex characters.
func looksLikeKey(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
