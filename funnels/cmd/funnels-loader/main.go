// Command funnels-loader turns the edge's raw files into journeys and
// counts:
//
//	funnels-loader migrate
//	funnels-loader run    -archive s3://adhunters-raw/funnels [-spool /var/lib/funnels/spool] [-close-after 1h]
//	funnels-loader replay -archive … -from 2026-09-30T00:00:00Z -to 2026-10-01T00:00:00Z
//	funnels-loader status
//
// run archives sealed files from the spool (the edge runs on the same box),
// loads each archived file into events, and closes each dirty hour an hour
// after it ends: its journeys are rebuilt and its counts computed. replay
// marks a range's files pending again; the running loader does the work.
//
// The database URL comes from DATABASE_URL; the archive's keys from
// S3_ENDPOINT, S3_REGION, S3_ACCESS_KEY and S3_SECRET_KEY. /healthz and
// /metrics are on OPS_ADDR. It stops cleanly on SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/funnels/load"
	"github.com/Raposa-Industries/adhunters/funnels/migrations"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/shared/archive"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
		return
	case "migrate":
		err = migrateCmd()
	case "run":
		err = runCmd(os.Args[2:])
	case "replay":
		err = replayCmd(os.Args[2:])
	case "status":
		err = statusCmd()
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "funnels-loader:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: funnels-loader run|migrate|replay|status|version [flags]")
	os.Exit(2)
}

func openDB(ctx context.Context, conns int32) (*pgxpool.Pool, error) {
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
	return pg.Open(ctx, pg.Config{URL: u.String(), AppName: "funnels-loader", StatementTimeout: pg.JobStatementTimeout, MaxConns: conns})
}

func migrateCmd() error {
	ctx := context.Background()
	db, err := openDB(ctx, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	migs, err := migrations.Load()
	if err != nil {
		return err
	}
	n, err := migrate.Up(ctx, db, logx.New("funnels-loader", version), migrations.Schema, migs)
	if err == nil {
		fmt.Printf("applied %d migrations\n", n)
	}
	return err
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	archiveURI := fs.String("archive", os.Getenv("FUNNELS_ARCHIVE"), "the archive: s3://bucket/prefix or file:///path")
	spoolDir := fs.String("spool", envOr("FUNNELS_SPOOL", "/var/lib/funnels/spool"), "the edge's spool on this box; empty when none")
	closeAfter := fs.Duration("close-after", time.Hour, "how long after an hour ends its journeys are counted")
	every := fs.Duration("every", 10*time.Second, "between passes")
	_ = fs.Parse(args)
	if *archiveURI == "" {
		return errors.New("-archive is required")
	}
	store, err := archive.Open(*archiveURI)
	if err != nil {
		return err
	}
	log := logx.New("funnels-loader", version)
	ctx := context.Background()
	db, err := openDB(ctx, 3)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("funnels-loader", version)
	metrics := load.NewMetrics(srv.Registry)
	l := load.New(load.Config{DB: db, Store: store, Spool: *spoolDir, CloseAfter: *closeAfter, Log: log, Metrics: metrics})
	tasks := srv.Tasks()
	tasks.Promise("funnels_archive", 5*time.Minute)
	tasks.Promise("funnels_load", 15*time.Minute)
	tasks.Promise("funnels_hour_close", 2*time.Hour+*closeAfter)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	log.Info("loader running", "archive", store.String(), "spool", *spoolDir, "close_after", closeAfter.String())

	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		t := time.NewTicker(*every)
		defer t.Stop()
		for {
			pass(ctx, l, tasks, log)
			select {
			case <-ctx.Done():
				return <-opsDone
			case <-t.C:
			}
		}
	})
}

// pass does one round: archive, load everything pending, close due hours.
// A step that fails is logged and tried again next round.
func pass(ctx context.Context, l *load.Loader, tasks *ops.Tasks, log *slog.Logger) {
	start := time.Now()
	n, err := l.Archive(ctx)
	tasks.Done("funnels_archive", start, int64(n), err)
	if err != nil {
		log.Error("archiving raw files", "err", err)
	}

	start = time.Now()
	loaded := 0
	for ctx.Err() == nil {
		more, err := l.LoadOne(ctx)
		if err != nil {
			log.Error("loading a raw file", "err", err)
			break
		}
		if !more {
			tasks.Done("funnels_load", start, int64(loaded), nil)
			break
		}
		loaded++
	}

	start = time.Now()
	closed, err := l.CloseDue(ctx)
	if err != nil {
		log.Error("closing hours", "err", err)
	}
	st, serr := l.Status(ctx)
	if serr != nil {
		log.Error("reading the loader's status", "err", serr)
	}
	// Nothing left to close is a success too, or a quiet night looks late.
	if err != nil || closed > 0 || (serr == nil && st.DirtyHours == 0) {
		tasks.Done("funnels_hour_close", start, int64(closed), err)
	}
}

func replayCmd(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	from := fs.String("from", "", "start, RFC 3339 (included)")
	to := fs.String("to", "", "end, RFC 3339 (left out)")
	_ = fs.Parse(args)
	f, err := time.Parse(time.RFC3339, *from)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	t, err := time.Parse(time.RFC3339, *to)
	if err != nil || !t.After(f) {
		return fmt.Errorf("-to must be a time after -from")
	}
	ctx := context.Background()
	db, err := openDB(ctx, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	n, err := load.New(load.Config{DB: db, Log: logx.New("funnels-loader", version)}).Replay(ctx, f, t)
	if err == nil {
		fmt.Printf("%d raw files will load again; the running loader does it\n", n)
	}
	return err
}

func statusCmd() error {
	ctx := context.Background()
	db, err := openDB(ctx, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	st, err := load.New(load.Config{DB: db, Log: logx.New("funnels-loader", version)}).Status(ctx)
	if err != nil {
		return err
	}
	last := "none"
	if st.LastClosedHour != nil {
		last = st.LastClosedHour.UTC().Format(time.RFC3339)
	}
	fmt.Printf("pending files %d, quarantined %d, dirty hours %d, last closed hour %s\n", st.Pending, st.Quarantined, st.DirtyHours, last)
	if st.Quarantined > 0 {
		os.Exit(1)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
