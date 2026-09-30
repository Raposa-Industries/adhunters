// Command tracks-walker follows running ads' saved links to their landing
// pages and one step further, and keeps what each page says.
//
//	tracks-walker run    -lines proxies.env -spool /var/lib/tracks/walk-spool -archive s3://adhunters-raw [-workers 2]
//	tracks-walker replay -archive … -from 2026-10-01T00:00:00Z -to 2026-10-02T00:00:00Z
//	tracks-walker walk   -url https://… [-lines proxies.env]    one walk, printed, nothing saved
//
// Every walk is written to a raw file first (walk/… in the spool), and each
// sealed file goes to the archive and tracks.walk_file; replay parses them
// again. The database URL comes from DATABASE_URL, the archive's keys as for
// tracks-shipper. /healthz and /metrics are on OPS_ADDR. It stops cleanly on
// SIGTERM: a walk in flight finishes and is written.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/tracks/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/lines"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/load"
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
	case "replay":
		err = replayCmd(os.Args[2:])
	case "walk":
		err = walkCmd(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracks-walker:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tracks-walker run|replay|walk|version [flags]")
	os.Exit(2)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	linesPath := fs.String("lines", "proxies.env", "proxy lines file, key=host:port:user:pass per line")
	roles := fs.String("roles", "dc,isp", "proxy line roles to use, most preferred first")
	reserved := fs.String("reserved", "isp-1,isp-2,isp-3,isp-4,isp-5", "lines kept out")
	cooldown := fs.Duration("cooldown", 30*time.Second, "how long a line rests after an error")
	spoolDir := fs.String("spool", "/var/lib/tracks/walk-spool", "the walker's own spool folder")
	archiveURI := fs.String("archive", "", "where raw files are kept: s3://bucket/prefix or file:///path")
	instance := fs.String("instance", "a", "instance name, in every record and file name")
	workers := fs.Int("workers", 2, "parallel walks")
	pause := fs.Duration("pause", time.Second, "wait after each walk, per worker")
	timeout := fs.Duration("timeout", 15*time.Second, "per page, redirects included")
	rewalk := fs.Duration("rewalk", 6*time.Hour, "how soon an ad is walked again")
	_ = fs.Parse(args)

	log := logx.New("tracks-walker", version).With("instance", *instance)
	if *archiveURI == "" {
		return errors.New("-archive is required")
	}
	store, err := archive.Open(*archiveURI)
	if err != nil {
		return err
	}
	pool, err := lines.Load(*linesPath, split(*roles), split(*reserved), *cooldown)
	if err != nil {
		return err
	}
	if pool.Size() == 0 {
		// Never walk from the box's own address.
		return fmt.Errorf("%s has no lines of roles %s", *linesPath, *roles)
	}
	db, err := openDB("tracks-walker")
	if err != nil {
		return err
	}
	defer db.Close()
	w, err := spool.Open(*spoolDir, *instance, log, spool.Options{})
	if err != nil {
		return err
	}

	srv := ops.New("tracks-walker", version)
	metrics := walk.NewMetrics(srv.Registry)
	archived := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tracks_walker_files_archived_total", Help: "Raw walk files uploaded to the archive.",
	})
	srv.Registry.MustRegister(archived)
	var mu sync.Mutex
	var lastArchive time.Time
	var archiveErr error
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	srv.AddCheck("archive", func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if archiveErr != nil && time.Since(lastArchive) > 10*time.Minute {
			return fmt.Errorf("no raw file archived for 10 minutes: %w", archiveErr)
		}
		return nil
	})

	log.Info("walker starting", "lines", pool.Describe(), "workers", *workers, "spool", *spoolDir,
		"archive", store.String(), "rewalk", rewalk.String())
	started := time.Now()
	lastArchive = started
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		go w.Run(ctx)
		shipped := make(chan struct{})
		go func() {
			defer close(shipped)
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			for {
				n, err := walk.Archive(context.Background(), db, store, *spoolDir)
				archived.Add(float64(n))
				mu.Lock()
				if err != nil {
					log.Error("archiving raw walk files", "err", err)
					archiveErr = err
				} else {
					archiveErr, lastArchive = nil, time.Now()
				}
				mu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
		err := walk.Run(ctx, walk.Config{
			DB: db, Lines: pool, Spool: w, Instance: *instance, Version: version, Workers: *workers,
			Pause: *pause, PageTimeout: *timeout, Rewalk: *rewalk, Log: log, Metrics: metrics,
		})
		w.Close()
		<-shipped
		// The files sealed on the way out go now; any left go at the next start.
		if n, aerr := walk.Archive(context.Background(), db, store, *spoolDir); aerr != nil {
			log.Warn("archiving the last raw walk files", "err", aerr)
		} else {
			archived.Add(float64(n))
		}
		log.Info("walker stopped", "ran", time.Since(started).Round(time.Second).String())
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

func replayCmd(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	archiveURI := fs.String("archive", "", "where raw files are kept")
	from := fs.String("from", "", "first minute, RFC 3339")
	to := fs.String("to", "", "end, RFC 3339 (not included)")
	_ = fs.Parse(args)
	f, err1 := time.Parse(time.RFC3339, *from)
	t, err2 := time.Parse(time.RFC3339, *to)
	if err1 != nil || err2 != nil || !f.Before(t) {
		return errors.New("-from and -to are RFC 3339 times, from before to")
	}
	store, err := archive.Open(*archiveURI)
	if err != nil {
		return err
	}
	db, err := openDB("tracks-walker-replay")
	if err != nil {
		return err
	}
	defer db.Close()
	r, err := walk.Replay(context.Background(), db, store, f, t)
	fmt.Printf("replayed %d files, %d walks\n", r.Files, r.Walks)
	return err
}

// walkCmd walks one link and prints what the pages said, without the page
// bodies. Without -lines it goes from this box's own address: only for a
// laptop, never on a server.
func walkCmd(args []string) error {
	fs := flag.NewFlagSet("walk", flag.ExitOnError)
	link := fs.String("url", "", "the link to walk")
	referer := fs.String("referer", "", "the page the click comes from")
	linesPath := fs.String("lines", "", "proxy lines file (empty: direct)")
	timeout := fs.Duration("timeout", 15*time.Second, "per page")
	_ = fs.Parse(args)
	if *link == "" {
		return errors.New("-url is required")
	}
	var rt http.RoundTripper = http.DefaultTransport
	if *linesPath != "" {
		pool, err := lines.Load(*linesPath, []string{"dc", "isp"}, nil, time.Minute)
		if err != nil {
			return err
		}
		_, t, ok := pool.Next()
		if !ok {
			return errors.New("no proxy line")
		}
		rt = t
	}
	rec := &walk.Record{Pages: walk.Trace(context.Background(), rt, *link, *referer, *timeout)}
	parsed := walk.Parse(rec)
	for i := range rec.Pages {
		rec.Pages[i].Body, rec.Pages[i].BodyBase64 = nil, ""
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"pages": rec.Pages, "parsed": parsed})
}

func openDB(app string) (*pgxpool.Pool, error) {
	u, err := load.UTC(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	return pg.Open(context.Background(), pg.Config{URL: u, AppName: app, StatementTimeout: pg.JobStatementTimeout, MaxConns: 4})
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
