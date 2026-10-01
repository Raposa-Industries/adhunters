// Command intel-numbers turns the answers intel-collect kept into Intel's
// numbers: it loads them into tables, joins Taboola with RedTrack, works out
// results with likely ranges, and keeps the alerts and suggestions.
//
//	intel-numbers migrate
//	intel-numbers run                   every INTEL_EVERY (2 min) until SIGTERM
//	intel-numbers once                  one round, then exit
//	intel-numbers reload -from D -to D  load the answers fetched in [from, to) again (UTC days)
//	intel-numbers status
//
// DATABASE_URL owns the intel schemas. TELEGRAM_BOT_TOKEN and
// TELEGRAM_CHAT_ID, when set, send each new alert, delivery status change and
// suggestion to "AdHunters alerts"; INTEL_BASE_URL (https://hunt-teste.fyi for
// now) makes the links absolute, so a suggestion opens Launch in one tap.
// It never talks to Taboola or RedTrack, and never writes anywhere but the
// intel schemas.
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
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/intel/judge"
	"github.com/Raposa-Industries/adhunters/intel/load"
	"github.com/Raposa-Industries/adhunters/intel/migrations"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/shared/telegram"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "migrate":
		err = migrateCmd()
	case "run":
		err = runCmd()
	case "once":
		err = onceCmd()
	case "reload":
		err = reloadCmd(os.Args[2:])
	case "status":
		err = statusCmd()
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "intel-numbers:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: intel-numbers migrate|run|once|reload|status|version")
	os.Exit(2)
}

func open(ctx context.Context, conns int32) (*pgxpool.Pool, error) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	return pg.Open(ctx, pg.Config{URL: u.String(), AppName: "intel-numbers", StatementTimeout: pg.JobStatementTimeout, MaxConns: conns})
}

func migrateCmd() error {
	log := logx.New("intel-numbers", version)
	ctx := context.Background()
	db, err := open(ctx, 2)
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

type rounds struct {
	db    *pgxpool.Pool
	log   *slog.Logger
	send  judge.Sender
	base  string
	tasks *ops.Tasks
}

// round loads new answers, then judges everything again.
func (r *rounds) round(ctx context.Context) error {
	start := time.Now()
	loaded, err := (&load.Loader{DB: r.db, Log: r.log}).Pending(ctx)
	r.done("load", start, int64(loaded), err)
	if err != nil {
		return err
	}
	if _, err := judge.LinkMoves(ctx, r.db); err != nil {
		r.log.Error("moves not linked", "err", err)
	}
	start = time.Now()
	s, err := judge.LoadSettings(ctx, r.db)
	if err != nil {
		return err
	}
	d, err := judge.Gather(ctx, r.db, time.Now())
	if err != nil {
		return err
	}
	n, err := judge.Results(ctx, r.db, d, s)
	r.done("results", start, int64(n), err)
	if err != nil {
		return err
	}
	start = time.Now()
	found, err := judge.Detect(ctx, r.db, d, s)
	if err == nil {
		n, err = judge.Record(ctx, r.db, r.log, found, d.Now, r.send, r.base)
	}
	r.done("alerts", start, int64(n), err)
	if err != nil {
		return err
	}
	start = time.Now()
	if _, err = judge.FindStatusChanges(ctx, r.db, d.Now); err == nil {
		n, err = judge.SendStatusChanges(ctx, r.db, r.log, s, d.Now, r.send, r.base)
	}
	r.done("status", start, int64(n), err)
	if err != nil {
		return err
	}
	start = time.Now()
	list, err := judge.Suggest(ctx, r.db, d, s, found)
	if err == nil {
		n, err = judge.Keep(ctx, r.db, list, d.Now)
	}
	if err == nil {
		_, err = judge.SendSuggestions(ctx, r.db, r.log, s, d.Now, r.send, r.base)
	}
	r.done("suggestions", start, int64(n), err)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `INSERT INTO intel.job_mark (job, done_at) VALUES ('round', now())
		ON CONFLICT (job) DO UPDATE SET done_at = EXCLUDED.done_at`)
	return err
}

func (r *rounds) done(task string, start time.Time, rows int64, err error) {
	if r.tasks != nil {
		r.tasks.Done(task, start, rows, err)
	}
}

func newRounds(db *pgxpool.Pool, log *slog.Logger) *rounds {
	r := &rounds{db: db, log: log, base: os.Getenv("INTEL_BASE_URL")}
	if tok, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"); tok != "" && chat != "" {
		r.send = telegram.New(tok, chat)
	}
	return r
}

func runCmd() error {
	log := logx.New("intel-numbers", version)
	ctx := context.Background()
	db, err := open(ctx, 3)
	if err != nil {
		return err
	}
	defer db.Close()
	every := 2 * time.Minute
	if v, err := time.ParseDuration(os.Getenv("INTEL_EVERY")); err == nil && v > 0 {
		every = v
	}
	srv := ops.New("intel-numbers", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	r := newRounds(db, log)
	r.tasks = srv.Tasks()
	for _, t := range []string{"load", "results", "alerts", "status", "suggestions"} {
		r.tasks.Promise(t, 3*every+time.Minute)
	}
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		log.Info("numbers starting", "every", every.String(), "telegram", r.send != nil)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			if err := r.round(ctx); err != nil && ctx.Err() == nil {
				log.Error("round failed", "err", err)
			}
			select {
			case <-ctx.Done():
				log.Info("numbers stopped")
				return <-opsDone
			case <-t.C:
			}
		}
	})
}

func onceCmd() error {
	log := logx.New("intel-numbers", version)
	ctx := context.Background()
	db, err := open(ctx, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	return newRounds(db, log).round(ctx)
}

func reloadCmd(args []string) error {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	from := fs.String("from", "", "first UTC day of the answers' fetch time (2006-01-02)")
	to := fs.String("to", "", "day after the last (2006-01-02)")
	_ = fs.Parse(args)
	f, err1 := time.Parse(time.DateOnly, *from)
	t, err2 := time.Parse(time.DateOnly, *to)
	if err1 != nil || err2 != nil || !t.After(f) {
		return errors.New("reload needs -from and -to days, from before to")
	}
	log := logx.New("intel-numbers", version)
	ctx := context.Background()
	db, err := open(ctx, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	n, err := (&load.Loader{DB: db, Log: log}).Reload(ctx, f, t)
	fmt.Printf("%d answers loaded again\n", n)
	return err
}

func statusCmd() error {
	ctx := context.Background()
	db, err := open(ctx, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(ctx, `
		SELECT 'answers kept', count(*)::text, COALESCE(to_char(max(fetched_at), 'YYYY-MM-DD HH24:MI "UTC"'), '') FROM intel.answer
		UNION ALL SELECT 'answers not loaded', count(*)::text, '' FROM intel.answer WHERE loaded_at IS NULL
		UNION ALL SELECT 'answers that failed to load', count(*)::text, '' FROM intel.answer WHERE load_error IS NOT NULL
		UNION ALL SELECT 'campaigns known', count(*)::text, '' FROM intel.tb_campaign
		UNION ALL SELECT 'open alerts', count(*)::text, '' FROM intel.alert WHERE closed_at IS NULL
		UNION ALL SELECT 'open suggestions', count(*)::text, '' FROM intel.suggestion WHERE state = 'open'
		UNION ALL SELECT 'last round', '', COALESCE((SELECT to_char(done_at, 'YYYY-MM-DD HH24:MI "UTC"') FROM intel.job_mark WHERE job = 'round'), 'never')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", a, b, c)
	}
	w.Flush()
	return rows.Err()
}
