// Package logins is the work behind Launch's Contas page: the ad network
// logins people add so Launch can use more accounts than the server's own
// login (TABOOLA_* in its environment). Taboola only, for now.
//
// Adding a login never changes anything on the network: Launch asks Taboola
// for a token and the login's account list (both reads), and people choose
// which advertiser accounts to use. The secret is sealed with the server's
// key (seal.go) before it reaches the database and is never shown or logged
// again; a page sees only the start and end of the client id.
package logins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
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
	kept     *keep.Folder
	log      *slog.Logger
}

// New returns the service. server is the server's own client and its
// settings; net is the network Launch's actions use.
func New(st *store.Store, box *Box, net *tbnet.Logins, server *write.Client, set write.Settings, kept *keep.Folder, log *slog.Logger) *Service {
	set.ClientID, set.ClientSecret, set.Accounts = "", "", nil
	set.OnlyOwn, set.NamePrefix, set.StateFile = false, "", ""
	return &Service{st: st, box: box, net: net, server: server, template: set, kept: kept, log: log}
}

// View is a login as the Contas page shows it.
type View struct {
	ID int64 `json:"id"` // 0 for the server's own
	// Server is the server's own login, set in its environment; the page
	// cannot change or remove it.
	Server   bool            `json:"server"`
	Network  string          `json:"network"`
	Name     string          `json:"name"`
	ClientID string          `json:"client_id"` // only its start and end
	Accounts []write.Account `json:"accounts"`
	AddedBy  string          `json:"added_by,omitempty"`
	AddedAt  *time.Time      `json:"added_at,omitempty"`
	// Problem is why the login is not used, in pt-BR, or "".
	Problem string `json:"problem,omitempty"`
}

// Load reads the added logins and hands them to the network. A login whose
// secret does not open is left out and logged.
func (s *Service) Load(ctx context.Context) error {
	rows, err := s.st.Logins(ctx)
	if err != nil {
		return err
	}
	var added []tbnet.Login
	for _, r := range rows {
		c, err := s.client(r)
		if err != nil {
			s.log.Warn("login not used", "login", r.ID, "err", err)
			continue
		}
		added = append(added, tbnet.Login{T: tbnet.New(c), Accounts: r.Accounts})
	}
	s.net.Set(added)
	return nil
}

// client opens a row's secret and builds its client.
func (s *Service) client(r store.Login) (*write.Client, error) {
	sec, err := s.box.Open(r.Secret, bound(r.Network, r.ClientID))
	if err != nil {
		return nil, err
	}
	set := s.template
	set.ClientID, set.ClientSecret, set.Accounts = r.ClientID, string(sec), r.Accounts
	return write.New(set, s.kept, s.log)
}

// bound ties a sealed secret to its login.
func bound(network, clientID string) []byte { return []byte(network + "\x00" + clientID) }

// List is the server's own login, then every added one, with their accounts
// named as Taboola names them.
func (s *Service) List(ctx context.Context) ([]View, error) {
	var out []View
	if s.server.Available() {
		v := View{Server: true, Network: "taboola", Name: "Login do servidor", ClientID: hint(s.serverID())}
		v.Accounts, _ = s.server.Accounts(ctx)
		out = append(out, v)
	}
	rows, err := s.st.Logins(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		at := r.AddedAt
		v := View{ID: r.ID, Network: r.Network, Name: r.Name, ClientID: hint(r.ClientID), AddedBy: r.AddedBy, AddedAt: &at}
		c, err := s.client(r)
		if err != nil {
			v.Problem = "Não consegui abrir o secret deste login: a chave do servidor mudou. Remova e adicione de novo."
			for _, a := range r.Accounts {
				v.Accounts = append(v.Accounts, write.Account{ID: a, Name: a})
			}
		} else if v.Accounts, err = c.Accounts(ctx); err != nil {
			v.Problem = tbnet.Message(err)
		}
		out = append(out, v)
	}
	return out, nil
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

// Check asks Taboola which accounts a login can see. It only reads.
func (s *Service) Check(ctx context.Context, clientID, secret string) ([]write.Allowed, error) {
	clientID, secret = strings.TrimSpace(clientID), strings.TrimSpace(secret)
	if clientID == "" || secret == "" {
		return nil, refuse("preencha o client ID e o client secret")
	}
	if len(clientID) > 200 || len(secret) > 500 {
		return nil, refuse("client ID ou client secret longo demais")
	}
	set := s.template
	set.ClientID, set.ClientSecret = clientID, secret
	c, err := write.New(set, s.kept, s.log)
	if err != nil {
		return nil, err
	}
	list, err := c.Allowed(ctx)
	var we *write.Error
	if errors.As(err, &we) && (we.Status == http.StatusUnauthorized || we.Status == http.StatusBadRequest || we.Status == http.StatusForbidden) {
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

// Add checks a login with Taboola again, seals its secret and starts using
// the chosen accounts.
func (s *Service) Add(ctx context.Context, who, name, clientID, secret string, accounts []string) (int64, error) {
	name, err := cleanName(name)
	if err != nil {
		return 0, err
	}
	clientID, secret = strings.TrimSpace(clientID), strings.TrimSpace(secret)
	if clientID == s.serverID() {
		return 0, refuse("esse é o login do servidor, que o Launch já usa")
	}
	allowed, err := s.Check(ctx, clientID, secret)
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
	id, err := s.st.AddLogin(ctx, store.Login{Network: "taboola", Name: name, ClientID: clientID, Secret: sealed, Accounts: use, AddedBy: who})
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
	return s.Check(ctx, r.ClientID, string(sec))
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
