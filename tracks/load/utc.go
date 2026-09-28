package load

import (
	"context"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UTC returns the database URL with the session time zone pinned to UTC.
// Tracks' days and hours are UTC days and hours, and Postgres turns a
// timestamptz into a date in the session's time zone, so every tracks
// connection must use UTC whatever the server's default is.
func UTC(dbURL string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", fmt.Errorf("database url: %w", err)
	}
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// CheckUTC refuses a pool whose sessions are not in UTC.
func CheckUTC(ctx context.Context, db *pgxpool.Pool) error {
	var tz string
	if err := db.QueryRow(ctx, `SHOW timezone`).Scan(&tz); err != nil {
		return err
	}
	if tz != "UTC" && tz != "Etc/UTC" {
		return fmt.Errorf("database session time zone is %q, want UTC (open it with load.UTC)", tz)
	}
	return nil
}
