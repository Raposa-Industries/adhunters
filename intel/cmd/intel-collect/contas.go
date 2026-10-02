package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
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
// accounts, proxy or name move changed_at).
func fingerprint(ls []contasLogin) string {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%d/%s/%d", l.ID, l.ClientID, l.ChangedAt.UnixNano()))
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

// readByOwn tells whether one of the logins in intel-collect.env already
// reads account; an account two logins share is read once, by the env's.
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
