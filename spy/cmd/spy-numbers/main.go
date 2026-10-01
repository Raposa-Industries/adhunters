// Command spy-numbers keeps Spy's derived numbers: the last 24 hours,
// landing pages and operators, the verticals, the read model, and Size and
// Direction. The numbers for any
// other range are SQL functions the apps call through spy_api; they need
// nothing running.
//
//	spy-numbers migrate
//	spy-numbers run
//	spy-numbers refresh [-rebuild] [-pages-since 2026-10-01T00:00:00Z]
//	spy-numbers import-old
//	spy-numbers status
//	spy-numbers check
//
// The database URL comes from DATABASE_URL; the login reads tracks_api
// (tracks_api_read) and owns the spy schemas. import-old copies the
// groupings (operators, accounts, verticals), pages, Direction history and
// prices from the collector's database at OLD_DATABASE_URL, which it only
// reads. run stops cleanly on SIGTERM: a
// job in flight is cancelled and its transaction rolled back.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
	"github.com/Raposa-Industries/adhunters/spy/classify"
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
	case "check":
		err = checkCmd()
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
	fmt.Fprintln(os.Stderr, "usage: spy-numbers run|migrate|refresh|import-old|status|check|version [flags]")
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
	cl, err := classifier(db, log)
	if err != nil {
		return err
	}
	r := numbers.New(db, log, numbers.Config{Tasks: srv.Tasks(), Classify: cl}, srv.Registry)
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

// refreshCmd runs every job once; -rebuild also redoes Direction's daily part,
// and -pages-since reads the landing page walks from then again (after a
// Tracks replay, or a change to spy.shared_domain).
func refreshCmd(args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	rebuild := fs.Bool("rebuild", false, "redo Direction's usual values and noise first")
	pagesSince := fs.String("pages-since", "", "read the landing page walks from this time again, RFC 3339")
	_ = fs.Parse(args)
	var since time.Time
	if *pagesSince != "" {
		var err error
		if since, err = time.Parse(time.RFC3339, *pagesSince); err != nil {
			return fmt.Errorf("-pages-since: %w", err)
		}
	}
	log := logx.New("spy-numbers", version)
	ctx := context.Background()
	db, err := open(ctx, "DATABASE_URL", pg.JobStatementTimeout, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	if !since.IsZero() {
		var n int64
		if err := db.QueryRow(ctx, `SELECT spy.refresh_pages(now(), $1)`, since).Scan(&n); err != nil {
			return err
		}
		log.Info("landing pages read again", "since", since, "pages", n)
	}
	cl, err := classifier(db, log)
	if err != nil {
		return err
	}
	return numbers.New(db, log, numbers.Config{Classify: cl}, nil).All(ctx, *rebuild)
}

// classifier is the classifier as a numbers job: it counts the creatives read
// and answered.
func classifier(db *pgxpool.Pool, log *slog.Logger) (func(context.Context) (int64, error), error) {
	c, err := classify.New(db, log, classify.Config{})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (int64, error) {
		res, err := c.Run(ctx)
		if res.Read+res.Answered > 0 || res.Trained {
			log.Info("classifier ran", "read", res.Read, "trained", res.Trained, "answered", res.Answered, "declined", res.Declined)
		}
		return int64(res.Read + res.Answered + res.Declined), err
	}, nil
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
	pg := res.Pages
	fmt.Printf("sites %d, clues %d, site clues %d, sellers %d, site sellers %d, account sites %d (%d not in Tracks yet)\n",
		pg.Sites.Copied, pg.Clues.Copied, pg.SiteClues.Copied, pg.Sellers.Copied, pg.SiteSellers.Copied,
		pg.AccountSites.Copied, pg.AccountSites.Skipped)
	fmt.Printf("hand fixes %d (%d whose site, account or operator is not here), agencies %d, direction events %d (%d skipped: unknown here, or after Spy's own began; vertical events stay in the archive)\n",
		pg.Fixes.Copied, pg.Fixes.Skipped, pg.Agencies.Copied, pg.Events.Copied, pg.Events.Skipped)
	pr := res.Prices
	fmt.Printf("prices: Taboola auctions %d (%d whose item, publisher or device is not here), NewsBreak ad days %d (%d not here), %d daily rows (%d more Spy already had from Tracks)\n",
		pr.Auctions.Copied, pr.Auctions.Skipped, pr.NewsBreak.Copied, pr.NewsBreak.Skipped, pr.Rows, pr.Kept)
	fmt.Printf("Taboola auctions by campaign item %d, by campaign %d; with no creative here %d, no publisher here %d, unknown device %d\n",
		pr.ByItem, pr.ByCampaign, pr.NoCreative, pr.NoPublisher, pr.NoDevice)
	return nil
}

// checkCmd prints what the walker, the classifier and the grouping did, to
// read before switching to Spy's own grouping. It only reads.
func checkCmd() error {
	ctx := context.Background()
	db, err := open(ctx, "DATABASE_URL", pg.WebStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	return numbers.Check(ctx, db, os.Stdout, time.Now())
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
		UNION ALL SELECT 'landing pages read to', COALESCE((SELECT to_char(read_to, 'YYYY-MM-DD HH24:MI "UTC"') FROM spy.page_mark), 'never'),
		                 (SELECT count(*) || ' sites, ' || (SELECT count(*) FROM spy.creative_page) || ' creatives' FROM spy.site)
		UNION ALL SELECT 'operators from', COALESCE((SELECT text_value FROM spy.setting WHERE name = 'operators_from'), 'import'), ''
		UNION ALL (SELECT 'last grouping', groups || ' groups; accounts: ' || accounts_same || ' same, ' || accounts_moved
		                  || ' moved, ' || accounts_new || ' new, ' || accounts_unseen || ' on no page yet' || CASE WHEN applied THEN ' (applied)' ELSE ' (proposed)' END,
		                  to_char(at, 'YYYY-MM-DD HH24:MI')
		           FROM spy.grouping_run ORDER BY id DESC LIMIT 1)
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
