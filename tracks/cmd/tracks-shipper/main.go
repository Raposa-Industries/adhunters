// Command tracks-shipper copies sealed raw files from the spool to the archive
// and records each one in tracks.raw_file, where the loader finds it.
//
//	tracks-shipper run -spool /var/lib/tracks/spool -archive s3://adhunters-raw [-box worker]
//
// The database URL comes from DATABASE_URL and the archive's keys from
// S3_ENDPOINT, S3_ACCESS_KEY and S3_SECRET_KEY (see archive.Open). A shipped
// file stays in the spool for -keep, then goes. It stops cleanly on SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/tracks/archive"
	"github.com/Raposa-Industries/adhunters/tracks/load"
	"github.com/Raposa-Industries/adhunters/tracks/ship"
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
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracks-shipper:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tracks-shipper run|version [flags]")
	os.Exit(2)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	spoolDir := fs.String("spool", "/var/lib/tracks/spool", "spool folder")
	archiveURI := fs.String("archive", "", "where raw files are kept: s3://bucket/prefix or file:///path")
	box := fs.String("box", hostname(), "this box's name, recorded with each file")
	every := fs.Duration("every", 10*time.Second, "between passes")
	keep := fs.Duration("keep", 48*time.Hour, "how long a shipped file stays in the spool")
	_ = fs.Parse(args)

	log := logx.New("tracks-shipper", version).With("box", *box)
	if *archiveURI == "" {
		return errors.New("-archive is required")
	}
	store, err := archive.Open(*archiveURI)
	if err != nil {
		return err
	}
	dbURL, err := load.UTC(os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := pg.Open(ctx, pg.Config{URL: dbURL, AppName: "tracks-shipper", StatementTimeout: pg.WebStatementTimeout, MaxConns: 2})
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("tracks-shipper", version)
	metrics := ship.NewMetrics(srv.Registry)
	started := time.Now()
	srv.AddCheck("shipping", func(context.Context) error { return metrics.Healthy(started, 5*time.Minute) })
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })

	log.Info("shipper starting", "spool", *spoolDir, "archive", store.String(), "every", every.String())
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		err := ship.Run(ctx, ship.Config{
			Spool: *spoolDir, Store: store, DB: db, Box: *box, Every: *every, KeepFor: *keep,
			Log: log, Metrics: metrics,
		})
		log.Info("shipper stopped")
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
