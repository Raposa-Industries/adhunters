// Command tracks-loader loads archived raw files into the tracks tables,
// closes hours and keeps the tables to their keep times.
//
//	tracks-loader migrate
//	tracks-loader run    -archive s3://adhunters-raw
//	tracks-loader replay -from 2026-09-20T00:00Z -to 2026-09-21T00:00Z [-network taboola]
//	tracks-loader status [-books]
//	tracks-loader import-old -before 2026-10-01T00:00:00Z [-from 2026-06-01T00:00:00Z]
//	tracks-loader hourly status|check|drop [-month 2026-09] [-keep 35]
//	tracks-loader walks check|drop
//
// The database URL comes from DATABASE_URL and the archive's keys from
// S3_ENDPOINT, S3_ACCESS_KEY and S3_SECRET_KEY (see archive.Open). run stops
// cleanly on SIGTERM; a load or close cut off by the stop rolls back and is
// done again at the next start. replay only marks files pending, so a running
// loader does the work. import-old copies the collector's counts from before
// the switch-over, reading OLD_DATABASE_URL and never writing it. hourly
// shows where each month's hourly counts are, checks a month against its
// hour files in the archive, and drops its partitions when every hour
// matches (decision 0023); the archive comes from -archive or ARCHIVE.
// walks compares the old walk_page table with its copy, where each URL is
// kept once, and drops it when every row matches (decision 0025).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/load"
	"github.com/Raposa-Industries/adhunters/tracks/migrations"
	"github.com/Raposa-Industries/adhunters/tracks/walk"
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
	case "import-old":
		err = importOldCmd(os.Args[2:])
	case "hourly":
		err = hourlyCmd(os.Args[2:])
	case "walks":
		err = walksCmd(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "usage: tracks-loader run|migrate|replay|status|import-old|hourly|walks|version [flags]")
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

func importOldCmd(args []string) error {
	fs := flag.NewFlagSet("import-old", flag.ExitOnError)
	before := fs.String("before", "", "the switch-over, 00:00 UTC of Tracks' first day alone, RFC 3339")
	from := fs.String("from", "", "first day to copy, RFC 3339 (default: the collector's first)")
	_ = fs.Parse(args)
	b, err := time.Parse(time.RFC3339, *before)
	if err != nil {
		return fmt.Errorf("-before: %w", err)
	}
	var f time.Time
	if *from != "" {
		if f, err = time.Parse(time.RFC3339, *from); err != nil {
			return fmt.Errorf("-from: %w", err)
		}
	}
	oldURL := os.Getenv("OLD_DATABASE_URL")
	if oldURL == "" {
		return errors.New("OLD_DATABASE_URL is not set (the collector's database on prodbox)")
	}
	log := logx.New("tracks-loader", version)
	ctx := context.Background()
	db, err := open(ctx, time.Hour, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	old, err := pg.Open(ctx, pg.Config{URL: oldURL, AppName: "tracks-loader import-old", StatementTimeout: time.Hour, MaxConns: 1})
	if err != nil {
		return err
	}
	defer old.Close()
	res, err := load.ImportOld(ctx, db, old, load.ImportConfig{Before: b, From: f, Log: log})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	return err
}

func hourlyCmd(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: tracks-loader hourly status|check|drop [-month 2026-09] [-keep 35] [-archive s3://…]")
	}
	what := args[0]
	fs := flag.NewFlagSet("hourly "+what, flag.ExitOnError)
	monthFlag := fs.String("month", "", "the month, e.g. 2026-09 (check and drop)")
	keep := fs.Int("keep", 35, "days hourly counts stay in the database after their month ends")
	archiveURI := fs.String("archive", os.Getenv("ARCHIVE"), "where hour files are kept: s3://bucket/prefix or file:///path")
	_ = fs.Parse(args[1:])

	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	if what == "status" {
		months, err := load.HourlyStatus(ctx, db, *keep)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(w, "month\tdays\twritten to hour files\tin the database\tsize\tbrought back\tcan go from\t")
		for _, m := range months {
			where := "yes"
			switch {
			case m.Archived && m.InDatabase:
				where = "archived, some days back"
			case m.Archived:
				where = "archived"
			}
			fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%d\t%s\t\n", m.Month.Format("2006-01"), m.Days, m.Written, where,
				gb(m.Bytes), m.BroughtBack, m.DropFrom.Format("2006-01-02"))
		}
		return w.Flush()
	}
	if what != "check" && what != "drop" {
		return fmt.Errorf("hourly %s: want status, check or drop", what)
	}
	month, err := time.Parse("2006-01", *monthFlag)
	if err != nil {
		return fmt.Errorf("-month: %w", err)
	}
	if *archiveURI == "" {
		return errors.New("-archive is required (or ARCHIVE, as in tracks-loader.env)")
	}
	store, err := archive.Open(*archiveURI)
	if err != nil {
		return err
	}
	l := load.New(db, store, logx.New("tracks-loader", version), load.Config{}, nil)
	var c load.MonthCheck
	var dropErr error
	if what == "check" {
		c, err = l.CheckMonth(ctx, month, *keep)
		if err != nil {
			return err
		}
	} else {
		var daysBefore int64
		if err := db.QueryRow(ctx, `SELECT COALESCE(sum(sightings), 0) FROM tracks.ad_daily WHERE day >= $1 AND day < $2`,
			month, month.AddDate(0, 1, 0)).Scan(&daysBefore); err != nil {
			return err
		}
		c, dropErr = l.DropMonth(ctx, month, *keep)
		defer func() {
			if dropErr != nil {
				return
			}
			var daysAfter int64
			_ = db.QueryRow(ctx, `SELECT COALESCE(sum(sightings), 0) FROM tracks.ad_daily WHERE day >= $1 AND day < $2`,
				month, month.AddDate(0, 1, 0)).Scan(&daysAfter)
			fmt.Printf("\nAfter: %s's hourly partitions are gone (%s freed). Its daily counts: %s sightings before, %s after.\n",
				month.Format("2006-01"), gb(c.Bytes), num(daysBefore), num(daysAfter))
			fmt.Println("A page brings an archived day back with tracks_api.hourly_days_v1; tracks-loader hourly status shows each month.")
		}()
	}
	printCheck(c)
	if what == "drop" {
		return dropErr
	}
	if !c.OK() {
		return errors.New("the month does not match its hour files yet")
	}
	return nil
}

// walksCmd checks the old walk_page table against walk_step and page_url,
// and with drop removes it when every row matches (decision 0025, step 3).
func walksCmd(args []string) error {
	if len(args) != 1 || (args[0] != "check" && args[0] != "drop") {
		return errors.New("usage: tracks-loader walks check|drop")
	}
	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	var o walk.OldPages
	var dropErr error
	if args[0] == "check" {
		if o, err = walk.CheckOldPages(ctx, db); err != nil {
			return err
		}
	} else {
		o, dropErr = walk.DropOldPages(ctx, db)
	}
	fmt.Printf("Walk pages: the old walk_page table against walk_step, where each URL is kept once, every value compared\n\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "\trows\tsize\t")
	if o.Gone {
		fmt.Fprintln(w, "walk_page\tdropped\t\t")
	} else {
		fmt.Fprintf(w, "walk_page\t%s\t%s\t\n", num(o.Rows), gb(o.OldBytes))
		fmt.Fprintf(w, "  with a copy\t%s\t\t\n", num(o.Copied))
		fmt.Fprintf(w, "  different from it\t%s\t\t\n", num(o.Differ))
	}
	fmt.Fprintf(w, "walk_step (new walks too)\t%s\t%s\t\n", num(o.Steps), gb(o.StepBytes))
	fmt.Fprintf(w, "page_url\t%s\t%s\t\n", num(o.URLs), gb(o.URLBytes))
	_ = w.Flush()
	switch {
	case args[0] == "drop" && dropErr == nil:
		fmt.Printf("\nDROPPED: walk_page is gone (%s freed). tracks_api.walk_page_v1 reads walk_step and page_url.\n", gb(o.OldBytes))
		return nil
	case args[0] == "drop":
		fmt.Println()
		for _, p := range o.Problems() {
			fmt.Println("  - " + p)
		}
		return dropErr
	case o.OK():
		fmt.Printf("\nMATCH: every walk_page row has an equal copy. tracks-loader walks drop frees %s.\n", gb(o.OldBytes))
		return nil
	case o.Gone:
		fmt.Println("\nwalk_page was dropped already.")
		return nil
	}
	fmt.Println("\nDIFFERENT, nothing may go yet:")
	for _, p := range o.Problems() {
		fmt.Println("  - " + p)
	}
	return errors.New("walk_page does not match its copy yet")
}

// printCheck prints a month's check the way platform/retire/reconcile.sh
// prints its comparisons: one line per day and table, then the verdict.
func printCheck(c load.MonthCheck) {
	fmt.Printf("Hourly counts of %s: the database against its hour files, every hour and every value\n\n", c.Month.Format("2006-01"))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "day\ttable\tdb rows\tfile rows\tdb sightings\tfile sightings\thours\thours differ\t")
	var dbRows, fileRows, dbS, fileS int64
	var differ int
	for _, d := range c.Days {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t\n", d.Day.Format("2006-01-02"), d.Table, num(d.DBRows), num(d.FileRows),
			num(d.DBSightings), num(d.FileSightings), d.Hours, d.HoursDiffer)
		dbRows += d.DBRows
		fileRows += d.FileRows
		dbS += d.DBSightings
		fileS += d.FileSightings
		differ += d.HoursDiffer
	}
	fmt.Fprintf(w, "total\t\t%s\t%s\t%s\t%s\t\t%d\t\n", num(dbRows), num(fileRows), num(dbS), num(fileS), differ)
	_ = w.Flush()

	fmt.Printf("\nDaily counts (they stay in the database) against the hourly sightings\n\n")
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "day\thourly sightings\tdaily sightings\tdifference\t")
	for _, d := range c.Daily {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", d.Day.Format("2006-01-02"), num(d.HourlySightings), num(d.DailySightings),
			num(d.DailySightings-d.HourlySightings))
	}
	_ = w.Flush()

	fmt.Printf("\nIn the database: %s in the month's hourly partitions.\n", gb(c.Bytes))
	if c.OK() {
		fmt.Printf("MATCH: every hour of every day matches its hour file. %s can go from %s.\n",
			c.Month.Format("2006-01"), c.DropFrom.Format("2006-01-02"))
		return
	}
	fmt.Println("DIFFERENT, nothing may go yet:")
	for _, p := range c.Problems {
		fmt.Println("  - " + p)
	}
}

func gb(b int64) string { return fmt.Sprintf("%.2f GB", float64(b)/(1<<30)) }

// num writes n with thousands separators.
func num(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}
