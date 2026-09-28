// Command tracks-loader loads archived raw files into the tracks tables,
// closes hours and keeps the tables to their keep times.
//
//	tracks-loader migrate
//	tracks-loader run    -archive s3://adhunters-raw
//	tracks-loader replay -from 2026-09-20T00:00Z -to 2026-09-21T00:00Z [-network taboola]
//	tracks-loader status [-books]
//
// The database URL comes from DATABASE_URL and the archive's keys from
// S3_ENDPOINT, S3_ACCESS_KEY and S3_SECRET_KEY (see archive.Open). run stops
// cleanly on SIGTERM; a load or close cut off by the stop rolls back and is
// done again at the next start. replay only marks files pending, so a running
// loader does the work.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/tracks/archive"
	"github.com/Raposa-Industries/adhunters/tracks/load"
	"github.com/Raposa-Industries/adhunters/tracks/migrations"
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
		err = runCmd(os.Args[2:])
	case "migrate":
		err = migrateCmd(os.Args[2:])
	case "replay":
		err = replayCmd(os.Args[2:])
	case "status":
		err = statusCmd(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracks-loader:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tracks-loader run|migrate|replay|status|version [flags]")
	os.Exit(2)
}

func open(ctx context.Context, timeout time.Duration, conns int32) (*pgxpool.Pool, error) {
	u, err := load.UTC(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	return pg.Open(ctx, pg.Config{URL: u, AppName: "tracks-loader", StatementTimeout: timeout, MaxConns: conns})
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	archiveURI := fs.String("archive", "", "where raw files are kept: s3://bucket/prefix or file:///path")
	maxLag := fs.Duration("max-lag", 10*time.Minute, "unhealthy when the oldest pending file is older than this")
	_ = fs.Parse(args)

	log := logx.New("tracks-loader", version)
	if *archiveURI == "" {
		return errors.New("-archive is required")
	}
	store, err := archive.Open(*archiveURI)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 4)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("tracks-loader", version)
	metrics := load.NewMetrics(srv.Registry)
	l := load.New(db, store, log, load.Config{}, metrics)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	srv.AddCheck("lag", func(context.Context) error {
		if lag := time.Duration(metrics.LagSeconds()) * time.Second; lag > *maxLag {
			return fmt.Errorf("the oldest pending raw file is %s behind", lag)
		}
		return nil
	})

	log.Info("loader starting", "archive", store.String())
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		err := l.Run(ctx)
		log.Info("loader stopped")
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

func migrateCmd(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	_ = fs.Parse(args)
	log := logx.New("tracks-loader", version)
	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 1)
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
	log.Info("migrated", "applied", n)
	return nil
}

func replayCmd(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	from := fs.String("from", "", "first minute, RFC 3339 (e.g. 2026-09-20T00:00:00Z)")
	to := fs.String("to", "", "end, not included, RFC 3339")
	network := fs.String("network", "", "taboola or newsbreak (default: both)")
	_ = fs.Parse(args)
	f, err := time.Parse(time.RFC3339, *from)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	t, err := time.Parse(time.RFC3339, *to)
	if err != nil {
		return fmt.Errorf("-to: %w", err)
	}
	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	r, err := load.Replay(ctx, db, f.UTC(), t.UTC(), *network)
	if err != nil {
		return err
	}
	fmt.Printf("%d raw files marked to load again\n", r.Files)
	for _, d := range r.WholeDays {
		fmt.Printf("whole day %s loads again: its sightings had been dropped\n", d.UTC().Format("2006-01-02"))
	}
	return nil
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	books := fs.Bool("books", false, "also check that the last day's books balance (reads a day of sightings)")
	_ = fs.Parse(args)
	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	s, err := load.ReadStatus(ctx, db, *books)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return err
	}
	if len(s.UnbalancedFiles)+len(s.UnbalancedHours) > 0 {
		return errors.New("the books do not balance")
	}
	return nil
}
