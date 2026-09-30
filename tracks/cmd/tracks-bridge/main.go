// Command tracks-bridge writes Tracks' scrapes into the collector's database
// on prodbox, so today's Spy and the collector's other jobs keep working after
// the collector stops scraping (platform/SWITCH-OVER.md).
//
//	tracks-bridge run    [-from 2026-10-01T00:00:00Z] -archive s3://adhunters-raw
//	tracks-bridge check  [-from …]
//	tracks-bridge status [-from …]
//	tracks-bridge redo   -from 2026-10-01T10:00:00Z -to 2026-10-01T12:00:00Z
//
// DATABASE_URL is Tracks' database (the loader's login), OLD_DATABASE_URL the
// collector's (its own login: the bridge does the collector's writing and makes
// its sighting partitions). -from defaults to BRIDGE_FROM: the minute Tracks
// took over scraping. The archive's keys come from S3_ENDPOINT, S3_ACCESS_KEY
// and S3_SECRET_KEY (see archive.Open). run stops cleanly on SIGTERM.
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
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/bridge"
	"github.com/Raposa-Industries/adhunters/tracks/load"
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
	case "check":
		err = checkCmd(os.Args[2:])
	case "status":
		err = statusCmd(os.Args[2:])
	case "redo":
		err = redoCmd(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracks-bridge:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tracks-bridge run|check|status|redo|version [flags]")
	os.Exit(2)
}

// fromFlag adds -from, defaulting to BRIDGE_FROM.
func fromFlag(fs *flag.FlagSet) *string {
	return fs.String("from", os.Getenv("BRIDGE_FROM"), "the first minute to write, RFC 3339 (default BRIDGE_FROM)")
}

func parseFrom(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("-from (or BRIDGE_FROM) is required: the minute Tracks took over scraping")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("-from: %w", err)
	}
	return t.UTC(), nil
}

func openTracks(ctx context.Context, conns int32) (*pgxpool.Pool, error) {
	u, err := load.UTC(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	return pg.Open(ctx, pg.Config{URL: u, AppName: "tracks-bridge", StatementTimeout: pg.JobStatementTimeout, MaxConns: conns})
}

func openOld(ctx context.Context) (*pgxpool.Pool, error) {
	u := os.Getenv("OLD_DATABASE_URL")
	if u == "" {
		return nil, errors.New("OLD_DATABASE_URL is not set (the collector's database on prodbox)")
	}
	return pg.Open(ctx, pg.Config{URL: u, AppName: "tracks-bridge", StatementTimeout: 2 * time.Minute, MaxConns: 3})
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	archiveURI := fs.String("archive", "", "where raw files are kept: s3://bucket/prefix or file:///path")
	fromS := fromFlag(fs)
	maxLag := fs.Duration("max-lag", 10*time.Minute, "unhealthy when the oldest pending file is older than this")
	_ = fs.Parse(args)

	log := logx.New("tracks-bridge", version)
	from, err := parseFrom(*fromS)
	if err != nil {
		return err
	}
	if *archiveURI == "" {
		return errors.New("-archive is required")
	}
	store, err := archive.Open(*archiveURI)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := openTracks(ctx, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	old, err := openOld(ctx)
	if err != nil {
		return err
	}
	defer old.Close()

	srv := ops.New("tracks-bridge", version)
	metrics := bridge.NewMetrics(srv.Registry)
	b := bridge.New(db, old, store, log, bridge.Config{From: from, Version: version}, metrics)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	srv.AddCheck("collector_database", func(ctx context.Context) error { return old.Ping(ctx) })
	srv.AddCheck("lag", func(context.Context) error {
		if lag := time.Duration(metrics.LagSeconds()) * time.Second; lag > *maxLag {
			return fmt.Errorf("the oldest raw file not yet in the collector's database is %s behind", lag)
		}
		return nil
	})

	log.Info("bridge starting", "archive", store.String(), "from", from)
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		err := b.Run(ctx)
		log.Info("bridge stopped")
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

// written are the collector's tables the bridge writes.
var written = []string{"network", "device", "publisher", "publisher_alias", "placement", "proxy_line", "brand",
	"account", "campaign", "creative", "ad", "link", "network_ad", "scrape", "sighting", "ad_hourly", "ad_daily",
	"ad_account_brand_hourly", "ad_account_daily", "placement_daily", "campaign_daily", "publisher_hourly",
	"creative_link", "creative_campaign", "walk_queue"}

// checkCmd checks both databases before the switch-over and, during it,
// shows who wrote the collector's newest scrapes. It changes nothing.
func checkCmd(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	fromS := fromFlag(fs)
	_ = fs.Parse(args)
	from, err := parseFrom(*fromS)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := openTracks(ctx, 1)
	if err != nil {
		return fmt.Errorf("Tracks' database: %w", err)
	}
	defer db.Close()
	old, err := openOld(ctx)
	if err != nil {
		return fmt.Errorf("the collector's database: %w", err)
	}
	defer old.Close()

	var problems []string
	var queue bool
	if err := old.QueryRow(ctx, `SELECT to_regclass('spy.walk_queue') IS NOT NULL`).Scan(&queue); err != nil {
		return err
	}
	if !queue {
		problems = append(problems, "spy.walk_queue is missing: apply the collector's migration 042 (collector-cli db migrate)")
	}
	for _, t := range written {
		if t == "walk_queue" && !queue {
			continue
		}
		var ok bool
		if err := old.QueryRow(ctx, `SELECT has_table_privilege('spy.'||$1, 'INSERT') AND has_table_privilege('spy.'||$1, 'UPDATE')`, t).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			problems = append(problems, "this login cannot write spy."+t)
		}
	}
	var owner bool
	if err := old.QueryRow(ctx, `SELECT pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = 'spy.sighting'::regclass), 'USAGE')`).Scan(&owner); err != nil {
		return err
	}
	if !owner {
		problems = append(problems, "this login does not own spy.sighting, so it cannot make its daily partitions: use the collector's own login")
	}

	type who struct {
		Collector, Bridge *time.Time
	}
	var w who
	if err := old.QueryRow(ctx, `
		SELECT max(scraped_at) FILTER (WHERE worker_node IS NULL OR worker_node NOT LIKE 'tracks:%'),
		       max(scraped_at) FILTER (WHERE worker_node LIKE 'tracks:%')
		FROM spy.scrape WHERE scraped_at > now() - interval '1 hour'`).Scan(&w.Collector, &w.Bridge); err != nil {
		return err
	}
	var files int64
	if err := db.QueryRow(ctx, `SELECT count(*) FROM tracks.raw_file WHERE minute >= $1`, from).Scan(&files); err != nil {
		return err
	}
	out := map[string]any{
		"from":                           from,
		"raw_files_from_then":            files,
		"newest_scrape_by_the_collector": w.Collector,
		"newest_scrape_by_the_bridge":    w.Bridge,
		"problems":                       problems,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New("not ready")
	}
	return nil
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fromS := fromFlag(fs)
	_ = fs.Parse(args)
	from, err := parseFrom(*fromS)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := openTracks(ctx, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	s, err := bridge.ReadStatus(ctx, db, from, 0)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

func redoCmd(args []string) error {
	fs := flag.NewFlagSet("redo", flag.ExitOnError)
	from := fs.String("from", "", "first minute, RFC 3339")
	to := fs.String("to", "", "end, not included, RFC 3339")
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
	db, err := openTracks(ctx, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	n, err := bridge.Redo(ctx, db, f.UTC(), t.UTC())
	if err != nil {
		return err
	}
	fmt.Printf("%d raw files will be written again (scrapes already there are skipped)\n", n)
	return nil
}
