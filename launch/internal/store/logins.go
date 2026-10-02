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
	ID        int64     `json:"id"`
	Network   string    `json:"network"`
	Name      string    `json:"name"`
	ClientID  string    `json:"-"`
	Secret    []byte    `json:"-"`
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
		SELECT id, network, name, client_id, secret, accounts, added_by, added_at, changed_at
		FROM launch.login ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Login, error) {
		var l Login
		err := r.Scan(&l.ID, &l.Network, &l.Name, &l.ClientID, &l.Secret, &l.Accounts, &l.AddedBy, &l.AddedAt, &l.ChangedAt)
		return l, err
	})
}

// Login reads one login.
func (s *Store) Login(ctx context.Context, id int64) (Login, error) {
	var l Login
	err := s.db.QueryRow(ctx, `
		SELECT id, network, name, client_id, secret, accounts, added_by, added_at, changed_at
		FROM launch.login WHERE id = $1`, id).
		Scan(&l.ID, &l.Network, &l.Name, &l.ClientID, &l.Secret, &l.Accounts, &l.AddedBy, &l.AddedAt, &l.ChangedAt)
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
		INSERT INTO launch.login (network, name, client_id, secret, accounts, added_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		l.Network, l.Name, l.ClientID, l.Secret, l.Accounts, l.AddedBy).Scan(&id)
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
