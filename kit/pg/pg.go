// Package pg opens the Postgres pool every binary uses, with the platform's
// limits applied on every connection.
//
// Each binary has its own login and passes its own name, so pg_stat_activity
// and slow-query logs say which binary ran what. Web binaries use a 5 s
// statement timeout and jobs 10 min; the total of all MaxConns stays under 60.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Timeouts the platform rules name.
const (
	WebStatementTimeout = 5 * time.Second
	JobStatementTimeout = 10 * time.Minute
)

// Config describes one binary's pool.
type Config struct {
	URL              string        // postgres://… from the binary's secrets file
	AppName          string        // e.g. "tracks-loader"; required
	StatementTimeout time.Duration // required; use WebStatementTimeout or JobStatementTimeout
	MaxConns         int32         // required; keep the platform total under 60
}

// Open connects and pings. It refuses a Config missing any limit, so no
// binary can reach the database without them.
func Open(ctx context.Context, c Config) (*pgxpool.Pool, error) {
	if c.URL == "" || c.AppName == "" {
		return nil, errors.New("pg: URL and AppName are required")
	}
	if c.StatementTimeout <= 0 || c.MaxConns <= 0 {
		return nil, errors.New("pg: StatementTimeout and MaxConns are required")
	}
	pc, err := pgxpool.ParseConfig(c.URL)
	if err != nil {
		return nil, fmt.Errorf("pg: parse url: %w", err)
	}
	pc.MaxConns = c.MaxConns
	rp := pc.ConnConfig.RuntimeParams
	rp["application_name"] = c.AppName
	rp["statement_timeout"] = fmt.Sprint(c.StatementTimeout.Milliseconds())
	rp["idle_in_transaction_session_timeout"] = "60000"

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}
