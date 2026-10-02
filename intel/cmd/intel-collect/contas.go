package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/intel/collect"
	"github.com/Raposa-Industries/adhunters/intel/taboola"
	"github.com/Raposa-Industries/adhunters/shared/taboola/logins"
)

// The logins people add on Launch's Contas page (decision 0028). Launch
// publishes them sealed in launch_api.taboola_login_v1; with the same key
// (LAUNCH_LOGIN_KEY_BASE64, as in launch-web.env) Intel opens each secret and
// proxy and reads the chosen accounts like its own logins, every request
// through the login's proxy and never direct. They are read at start and
// every 5 minutes; when they change, the process exits so systemd starts it
// again with the new set.

// The server's own login (TABOOLA_* in intel-collect.env) is Launch's
// "Login do servidor" too, and on Contas people may give any of its accounts
// a proxy (launch_api.taboola_account_proxy_v1). Such an account is then read
// only through that proxy, by a client of its own (as Launch does), and never
// by the env's login direct; when its proxy does not open it is not read.

// launchRows is what Launch publishes for Intel. It is kept in a file too, so
// a start while the database is away still knows which accounts never go
// direct (everything in it stays sealed).
type launchRows struct {
	Logins  []contasLogin  `json:"logins"`
	Proxies []accountProxy `json:"proxies"`
}

// accountProxy is one row of launch_api.taboola_account_proxy_v1.
type accountProxy struct {
	Account string
	Proxy   []byte
	SetAt   time.Time
}

// readLaunch reads the account proxies, and the Contas logins when the key
// is there to open them.
func readLaunch(ctx context.Context, db *pgxpool.Pool, withLogins bool) (launchRows, error) {
	var r launchRows
	rows, err := db.Query(ctx, `SELECT account, proxy, set_at FROM launch_api.taboola_account_proxy_v1 ORDER BY account`)
	if err != nil {
		return r, fmt.Errorf("read launch_api.taboola_account_proxy_v1: %w", err)
	}
	for rows.Next() {
		var p accountProxy
		if err := rows.Scan(&p.Account, &p.Proxy, &p.SetAt); err != nil {
			rows.Close()
			return r, err
		}
		r.Proxies = append(r.Proxies, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return r, err
	}
	if withLogins {
		if r.Logins, err = loadContas(ctx, db); err != nil {
			return r, fmt.Errorf("read launch_api.taboola_login_v1: %w", err)
		}
	}
	return r, nil
}

// keep writes the rows for the next start; readKept reads them back.
func keep(path string, r launchRows) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readKept(path string) (launchRows, error) {
	var r launchRows
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

// contasLogin is one row of launch_api.taboola_login_v1.
type contasLogin struct {
	ID        int64
	Name      string
	ClientID  string
	Secret    []byte
	Proxy     []byte
	Accounts  []string
	ChangedAt time.Time
}

func loadContas(ctx context.Context, db *pgxpool.Pool) ([]contasLogin, error) {
	rows, err := db.Query(ctx, `
		SELECT id, name, client_id, secret, coalesce(proxy, ''::bytea), accounts, changed_at
		FROM launch_api.taboola_login_v1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contasLogin
	for rows.Next() {
		var l contasLogin
		if err := rows.Scan(&l.ID, &l.Name, &l.ClientID, &l.Secret, &l.Proxy, &l.Accounts, &l.ChangedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// fingerprint changes whenever a login is added, removed or changed (its
// accounts, proxy or name move changed_at), or an account's proxy is set,
// changed or taken away.
func fingerprint(r launchRows) string {
	parts := make([]string, 0, len(r.Logins)+len(r.Proxies))
	for _, l := range r.Logins {
		parts = append(parts, fmt.Sprintf("%d/%s/%d", l.ID, l.ClientID, l.ChangedAt.UnixNano()))
	}
	for _, p := range r.Proxies {
		parts = append(parts, fmt.Sprintf("p/%s/%d", p.Account, p.SetAt.UnixNano()))
	}
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(h[:8])
}

// contasName is the login's name in intel.answer and in the job names.
func contasName(id int64) string { return "contas-" + strconv.FormatInt(id, 10) }

// contasCollectors opens each login and makes its collector. A login whose
// secret or proxy does not open (another key, or no proxy yet) is left out
// and said in the log: its accounts are never read direct.
func contasCollectors(box *logins.Box, ls []contasLogin, base string, s *setup, perMin, rtPerMin int, log *slog.Logger) []*collect.Taboola {
	var out []*collect.Taboola
	for _, l := range ls {
		name := contasName(l.ID)
		lg := log.With("taboola_login", name, "contas_name", l.Name)
		secret, err := box.Open(l.Secret, logins.LoginBound("taboola", l.ClientID))
		if err != nil {
			lg.Warn("Contas login not read: its secret does not open with LAUNCH_LOGIN_KEY_BASE64")
			continue
		}
		if len(l.Proxy) == 0 {
			lg.Warn("Contas login not read: it has no proxy, and its accounts never go direct")
			continue
		}
		raw, err := box.Open(l.Proxy, logins.ProxyBound("taboola", l.ClientID))
		if err != nil {
			lg.Warn("Contas login not read: its proxy does not open with LAUNCH_LOGIN_KEY_BASE64")
			continue
		}
		u, err := logins.ParseProxy(string(raw))
		if err != nil {
			lg.Warn("Contas login not read: its saved proxy is not valid")
			continue
		}
		only := map[string]bool{}
		for _, a := range l.Accounts {
			only[a] = true
		}
		out = append(out, &collect.Taboola{
			Login: name, API: taboola.NewVia(base, l.ClientID, string(secret), logins.ProxyClient(u)), Spool: s.spool,
			Pace: &collect.Pacer{PerMinute: perMin, RealtimePerMinute: rtPerMin},
			Log:  lg, Now: time.Now, Only: only, Skip: s.readByOwn,
		})
		lg.Info("Contas login read through its proxy", "proxy", logins.HostPort(u), "accounts", len(l.Accounts))
	}
	return out
}

// accountCollectors makes, for each account of the env's logins that has a
// proxy, a client of its own through it: the env login's id and secret, that
// account only. One whose proxy does not open is said in the log and not
// read; the env's logins skip them all (proxied).
func accountCollectors(box *logins.Box, ps []accountProxy, s *setup, base string, log *slog.Logger) []*collect.Taboola {
	var out []*collect.Taboola
	for _, p := range ps {
		for _, e := range s.envLogins {
			name := e.name + "-" + p.Account
			lg := log.With("taboola_login", name, "account", p.Account)
			if box == nil {
				lg.Warn("account not read: it has a proxy on Contas, and LAUNCH_LOGIN_KEY_BASE64 is not set to open it")
				continue
			}
			raw, err := box.Open(p.Proxy, logins.AccountBound("taboola", p.Account))
			if err != nil {
				lg.Warn("account not read: its proxy does not open with LAUNCH_LOGIN_KEY_BASE64")
				continue
			}
			u, err := logins.ParseProxy(string(raw))
			if err != nil {
				lg.Warn("account not read: its saved proxy is not valid")
				continue
			}
			out = append(out, &collect.Taboola{
				Login: name, API: taboola.NewVia(base, e.id, e.secret, logins.ProxyClient(u)), Spool: s.spool,
				Pace: &collect.Pacer{PerMinute: s.perMin, RealtimePerMinute: s.rtPerMin},
				Log:  lg, Now: time.Now, Only: map[string]bool{p.Account: true},
			})
			lg.Info("account read through its own proxy", "proxy", logins.HostPort(u))
		}
	}
	return out
}

// useLaunch puts what Launch published into the schedule: the accounts with
// a proxy leave the env's logins for clients of their own, and the Contas
// logins are added. It runs once, before the jobs start.
func (s *setup) useLaunch(r launchRows, base string, log *slog.Logger) {
	s.proxied = map[string]bool{}
	for _, p := range r.Proxies {
		s.proxied[p.Account] = true
	}
	acc := accountCollectors(s.box, r.Proxies, s, base, log)
	s.taboolas = append(s.taboolas, acc...)
	s.own = append(s.own, acc...)
	if s.box != nil {
		s.taboolas = append(s.taboolas, contasCollectors(s.box, r.Logins, base, s, s.perMin, s.rtPerMin, log)...)
	}
}

// hasProxy tells the env's logins to leave out an account that has a proxy.
func (s *setup) hasProxy(account string) bool { return s.proxied[account] }

// readByOwn tells whether one of the logins in intel-collect.env (or a
// proxied account's own client) already reads account; an account two
// logins share is read once, by the env's.
func (s *setup) readByOwn(account string) bool {
	for _, t := range s.own {
		accs, _ := t.Known(context.Background())
		for _, a := range accs {
			if a.ID == account {
				return true
			}
		}
	}
	return false
}
