// Command spy-numbers keeps Spy's derived numbers: the last 24 hours, the
// read model, and Size and Direction. The numbers for any other range are
// SQL functions the apps call through spy_api; they need nothing running.
//
//	spy-numbers migrate
//	spy-numbers run
//	spy-numbers refresh [-rebuild]
//	spy-numbers import-old
//	spy-numbers status
//
// The database URL comes from DATABASE_URL; the login reads tracks_api
// (tracks_api_read) and owns the spy schemas. import-old copies the
// groupings (operators, accounts, verticals) from the collector's database
// at OLD_DATABASE_URL, which it only reads. run stops cleanly on SIGTERM: a
// job in flight is cancelled and its transaction rolled back.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/spy/importold"
	"github.com/Raposa-Industries/adhunters/spy/migrations"
	"github.com/Raposa-Industries/adhunters/spy/numbers"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd()
	case "migrate":
		err = migrateCmd()
	case "refresh":
		err = refreshCmd(os.Args[2:])
	case "import-old":
		err = importCmd()
	case "status":
		err = statusCmd()
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spy-numbers:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: spy-numbers run|migrate|refresh|import-old|status|version [flags]")
	os.Exit(2)
}

// open connects with the session in UTC, whatever the server's default.
func open(ctx context.Context, env string, timeout time.Duration, conns int32) (*pgxpool.Pool, error) {
	raw := os.Getenv(env)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set", env)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", env, err)
	}
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	return pg.Open(ctx, pg.Config{URL: u.String(), AppName: "spy-numbers", StatementTimeout: timeout, MaxConns: conns})
}

func migrateCmd() error {
	log := logx.New("spy-numbers", version)
	ctx := context.Background()
	db, err := open(ctx, "DATABASE_URL", pg.JobStatementTimeout, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	migs, err := migrations.Load()
	if err != nil {
		return err
	}
	n, err := migrate.Up(ctx, db, log, migrations.Schema, migs)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "count", n)
	return nil
}

func runCmd() error {
	log := logx.New("spy-numbers", version)
	ctx := context.Background()
	// The jobs run one at a time; one more for /healthz.
	db, err := open(ctx, "DATABASE_URL", pg.JobStatementTimeout, 3)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("spy-numbers", version)
	r := numbers.New(db, log, numbers.Config{}, srv.Registry)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	srv.AddCheck("numbers", r.Healthy)

	log.Info("numbers starting")
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		err := r.Run(ctx)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		log.Info("numbers stopped")
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

// refreshCmd runs every job once; -rebuild also redoes Direction's daily part.
func refreshCmd(args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	rebuild := fs.Bool("rebuild", false, "redo Direction's usual values and noise first")
	_ = fs.Parse(args)
	log := logx.New("spy-numbers", version)
	ctx := context.Background()
	db, err := open(ctx, "DATABASE_URL", pg.JobStatementTimeout, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	return numbers.New(db, log, numbers.Config{}, nil).All(ctx, *rebuild)
}

func importCmd() error {
	log := logx.New("spy-numbers", version)
	ctx := context.Background()
	old, err := open(ctx, "OLD_DATABASE_URL", pg.JobStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer old.Close()
	db, err := open(ctx, "DATABASE_URL", pg.JobStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := importold.Run(ctx, old, db, log)
	if err != nil {
		return err
	}
	fmt.Printf("operators %d, accounts %d (%d not in Tracks yet), verticals %d (%d not in Tracks yet)\n",
		res.Operators.Copied, res.Accounts.Copied, res.Accounts.Skipped, res.Verticals.Copied, res.Verticals.Skipped)
	return nil
}

// statusCmd prints where the last 24 hours end, how big each table is, and
// when each grouping was last copied.
func statusCmd() error {
	ctx := context.Background()
	db, err := open(ctx, "DATABASE_URL", pg.WebStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(ctx, `
		SELECT 'last 24 hours end', COALESCE((SELECT to_char(window_end, 'YYYY-MM-DD HH24:MI "UTC"') FROM spy.recent_window), 'not built'), ''
		UNION ALL SELECT 'creatives, last 24 hours', (SELECT count(*) FROM spy.creative_recent)::text, ''
		UNION ALL SELECT 'creatives in the read model', (SELECT count(*) FROM spy.creative_stats)::text, ''
		UNION ALL SELECT 'Direction judged', (SELECT count(*) FROM spy.direction_stats)::text, ''
		UNION ALL (SELECT 'copied ' || what, copied::text || ' (' || skipped || ' skipped)', to_char(done_at, 'YYYY-MM-DD HH24:MI')
		           FROM spy.import_mark ORDER BY what)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for rows.Next() {
		var what, value, at string
		if err := rows.Scan(&what, &value, &at); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", what, value, at)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return w.Flush()
}
