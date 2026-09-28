// Package migrate applies one service's schema migrations at its deploy.
//
// Each service owns one schema and keeps its migrations as numbered files,
// 0001_create_scrape.sql and so on, embedded in its binary. Up applies the
// pending ones in order under an advisory lock, each in its own transaction
// with lock_timeout = 3s, and records them in <schema>.schema_migration with
// a checksum. An applied file that has since changed is an error: write a new
// migration instead.
//
// A file whose first line is "-- migrate: no-transaction" runs outside a
// transaction, which CREATE INDEX CONCURRENTLY needs. Keep such a file to
// that one statement.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LockTimeout is applied to every migration, so a migration waiting on a busy
// table fails fast instead of queueing every query behind it.
const LockTimeout = "3s"

const noTxMarker = "-- migrate: no-transaction"

var fileRe = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)
var schemaRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Migration is one parsed file.
type Migration struct {
	Version int
	Name    string
	SQL     string
	Sum     string
	NoTx    bool
}

// Load reads and orders the migrations in dir of fsys.
func Load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrate: %s: name must look like 0001_what_it_does.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrate: %s and %s share version %d", prev, e.Name(), v)
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		sql := string(b)
		out = append(out, Migration{
			Version: v,
			Name:    m[2],
			SQL:     sql,
			Sum:     hex.EncodeToString(sum[:]),
			NoTx:    strings.HasPrefix(strings.TrimSpace(sql), noTxMarker),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Up applies every pending migration for schema and returns how many ran.
func Up(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, schema string, migs []Migration) (int, error) {
	if !schemaRe.MatchString(schema) {
		return 0, fmt.Errorf("migrate: bad schema name %q", schema)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	// Session-level lock: two deploys of the same service never interleave.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('migrate:' || $1))`, schema); err != nil {
		return 0, fmt.Errorf("migrate: lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('migrate:' || $1))`, schema)

	ident := pgx.Identifier{schema}.Sanitize()
	table := pgx.Identifier{schema, "schema_migration"}.Sanitize()
	if _, err := conn.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+ident); err != nil {
		return 0, fmt.Errorf("migrate: create schema: %w", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		version    int PRIMARY KEY,
		name       text NOT NULL,
		sha256     text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return 0, fmt.Errorf("migrate: create table: %w", err)
	}

	applied := map[int]string{}
	rows, err := conn.Query(ctx, `SELECT version, sha256 FROM `+table)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var v int
		var s string
		if err := rows.Scan(&v, &s); err != nil {
			rows.Close()
			return 0, err
		}
		applied[v] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	ran := 0
	for _, m := range migs {
		if sum, ok := applied[m.Version]; ok {
			if sum != m.Sum {
				return ran, fmt.Errorf("migrate: %s.%04d_%s changed after it was applied; add a new migration instead", schema, m.Version, m.Name)
			}
			continue
		}
		log.Info("applying migration", "schema", schema, "version", m.Version, "name", m.Name)
		if err := apply(ctx, conn.Conn(), table, m); err != nil {
			return ran, fmt.Errorf("migrate: %s.%04d_%s: %w", schema, m.Version, m.Name, err)
		}
		ran++
	}
	return ran, nil
}

func apply(ctx context.Context, conn *pgx.Conn, table string, m Migration) error {
	record := `INSERT INTO ` + table + ` (version, name, sha256) VALUES ($1, $2, $3)`
	if m.NoTx {
		if _, err := conn.Exec(ctx, `SET lock_timeout = '`+LockTimeout+`'`); err != nil {
			return err
		}
		defer conn.Exec(context.WithoutCancel(ctx), `RESET lock_timeout`)
		if _, err := conn.Exec(ctx, m.SQL); err != nil {
			return err
		}
		_, err := conn.Exec(ctx, record, m.Version, m.Name, m.Sum)
		return err
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '`+LockTimeout+`'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, record, m.Version, m.Name, m.Sum)
		return err
	})
}
