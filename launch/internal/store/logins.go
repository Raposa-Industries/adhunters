package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Login is one ad network login people added on the Contas page. Secret is
// sealed (launch/internal/logins); this package never opens it.
type Login struct {
	ID       int64  `json:"id"`
	Network  string `json:"network"`
	Name     string `json:"name"`
	ClientID string `json:"-"`
	Secret   []byte `json:"-"`
	// Proxy is the sealed proxy every request of the login goes through;
	// empty for a login added before proxies, whose requests are refused.
	Proxy []byte `json:"-"`
	// UserID is the login's Taboola user ID; "" for one added before it.
	UserID    string    `json:"user_id"`
	Accounts  []string  `json:"accounts"`
	AddedBy   string    `json:"added_by"`
	AddedAt   time.Time `json:"added_at"`
	ChangedAt time.Time `json:"changed_at"`
}

// ErrLoginTaken is a login whose client id is already added.
var ErrLoginTaken = errors.New("store: login already added")

// Logins lists every added login, oldest first.
func (s *Store) Logins(ctx context.Context) ([]Login, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, network, name, client_id, secret, coalesce(proxy, ''::bytea), user_id, accounts, added_by, added_at, changed_at
		FROM launch.login ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Login, error) {
		var l Login
		err := r.Scan(&l.ID, &l.Network, &l.Name, &l.ClientID, &l.Secret, &l.Proxy, &l.UserID, &l.Accounts, &l.AddedBy, &l.AddedAt, &l.ChangedAt)
		return l, err
	})
}

// Login reads one login.
func (s *Store) Login(ctx context.Context, id int64) (Login, error) {
	var l Login
	err := s.db.QueryRow(ctx, `
		SELECT id, network, name, client_id, secret, coalesce(proxy, ''::bytea), user_id, accounts, added_by, added_at, changed_at
		FROM launch.login WHERE id = $1`, id).
		Scan(&l.ID, &l.Network, &l.Name, &l.ClientID, &l.Secret, &l.Proxy, &l.UserID, &l.Accounts, &l.AddedBy, &l.AddedAt, &l.ChangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// AddLogin saves a login and returns its id.
func (s *Store) AddLogin(ctx context.Context, l Login) (int64, error) {
	if l.Accounts == nil {
		l.Accounts = []string{}
	}
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO launch.login (network, name, client_id, secret, proxy, user_id, accounts, added_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		l.Network, l.Name, l.ClientID, l.Secret, l.Proxy, l.UserID, l.Accounts, l.AddedBy).Scan(&id)
	if isUnique(err) {
		return 0, ErrLoginTaken
	}
	return id, err
}

// SetLoginAccounts changes which of a login's accounts are used, and its name.
func (s *Store) SetLoginAccounts(ctx context.Context, id int64, name string, accounts []string) error {
	if accounts == nil {
		accounts = []string{}
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE launch.login SET name = $2, accounts = $3, changed_at = now() WHERE id = $1`, id, name, accounts)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// RemoveLogin forgets a login. Nothing on the network changes.
func (s *Store) RemoveLogin(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM launch.login WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetLoginProxy changes the sealed proxy a login's requests go through.
func (s *Store) SetLoginProxy(ctx context.Context, id int64, proxy []byte) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE launch.login SET proxy = $2, changed_at = now() WHERE id = $1`, id, proxy)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetLoginUserID changes a login's Taboola user ID.
func (s *Store) SetLoginUserID(ctx context.Context, id int64, userID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE launch.login SET user_id = $2, changed_at = now() WHERE id = $1`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AccountProxies are the sealed proxies set on accounts of the server's own
// login, by account id.
func (s *Store) AccountProxies(ctx context.Context, network string) (map[string][]byte, error) {
	rows, err := s.db.Query(ctx, `SELECT account, proxy FROM launch.account_proxy WHERE network = $1`, network)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var a string
		var p []byte
		if err := rows.Scan(&a, &p); err != nil {
			return nil, err
		}
		out[a] = p
	}
	return out, rows.Err()
}

// SetAccountProxy sets an account's sealed proxy; nil takes it away, and
// the account goes direct again.
func (s *Store) SetAccountProxy(ctx context.Context, network, account string, proxy []byte, who string) error {
	if proxy == nil {
		_, err := s.db.Exec(ctx, `DELETE FROM launch.account_proxy WHERE network = $1 AND account = $2`, network, account)
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO launch.account_proxy (network, account, proxy, set_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (network, account) DO UPDATE SET proxy = EXCLUDED.proxy, set_by = EXCLUDED.set_by, set_at = now()`,
		network, account, proxy, who)
	return err
}

// AccountsSeen records the server's own accounts Launch uses (once each) and
// returns when each was first seen, by account id.
func (s *Store) AccountsSeen(ctx context.Context, network string, accounts []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(accounts) == 0 {
		return out, nil
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO launch.account_seen (network, account)
		SELECT $1, a FROM unnest($2::text[]) AS a WHERE a <> ''
		ON CONFLICT (network, account) DO NOTHING`, network, accounts); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT account, seen_at FROM launch.account_seen WHERE network = $1 AND account = ANY($2)`, network, accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var at time.Time
		if err := rows.Scan(&a, &at); err != nil {
			return nil, err
		}
		out[a] = at
	}
	return out, rows.Err()
}
