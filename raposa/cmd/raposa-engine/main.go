// Command raposa-engine runs investigations: it visits each ad one visit at
// a time, keeps the pages it reaches whole, and delivers watches.
//
//	raposa-engine migrate
//	raposa-engine run     [-workers 10] [-lines proxies.env] [-targets targets.yaml] [-files file:///var/lib/raposa/files]
//	raposa-engine request -creative 123 [-ad 456] [-mode deep|quick] [-by name]
//	raposa-engine stop    ID
//	raposa-engine status
//
// The database URL comes from DATABASE_URL; the login reads tracks_api
// (tracks_api_read). Watches need PUSHCUT_API_KEY; without it they are
// recorded as skipped. An s3:// files store takes its keys from S3_ENDPOINT,
// S3_ACCESS_KEY and S3_SECRET_KEY. run stops cleanly on SIGTERM: the visit in
// flight is dropped unwritten and runs again at the next claim.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/raposa/internal/engine"
	"github.com/Raposa-Industries/adhunters/raposa/internal/files"
	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
	"github.com/Raposa-Industries/adhunters/raposa/migrations"
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
		err = migrateCmd()
	case "request":
		err = requestCmd(os.Args[2:])
	case "stop":
		err = stopCmd(os.Args[2:])
	case "status":
		err = statusCmd()
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "raposa-engine:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: raposa-engine run|migrate|request|stop|status|version [flags]")
	os.Exit(2)
}

// open connects with the session in UTC, whatever the server's default.
func open(ctx context.Context, timeout time.Duration, conns int32) (*pgxpool.Pool, error) {
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
	return pg.Open(ctx, pg.Config{URL: u.String(), AppName: "raposa-engine", StatementTimeout: timeout, MaxConns: conns})
}

func migrateCmd() error {
	log := logx.New("raposa-engine", version)
	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 2)
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

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	workers := fs.Int("workers", 10, "visits at once (browser visits are further held to the runner's 2)")
	linesPath := fs.String("lines", "/etc/adhunters/raposa/proxies.env", "proxy lines file, key=host:port:user:pass per line")
	targetsPath := fs.String("targets", "/etc/adhunters/raposa/targets.yaml", "targets file (publishers: [...]), for referers")
	filesURI := fs.String("files", envOr("RAPOSA_FILES", "file:///var/lib/raposa/files"), "where kept files go: file:///path or s3://bucket/prefix")
	browser := fs.String("browser", envOr("RAPOSA_BROWSER", "http://127.0.0.1:8086"), "the browser runner")
	keepDir := fs.String("keep-dir", envOr("RAPOSA_KEEP_DIR", "/var/lib/raposa/keep"), "where the runner writes one keep's files; the runner must be able to write here")
	node := fs.String("node", "", "this box's name in claims (default: the host name)")
	_ = fs.Parse(args)

	log := logx.New("raposa-engine", version)
	if *node == "" {
		h, err := os.Hostname()
		if err != nil {
			return err
		}
		*node = h
	}
	ls, err := lines.Load(*linesPath)
	if err != nil {
		return err
	}
	targets, err := engine.LoadTargets(*targetsPath)
	if err != nil {
		return err
	}
	store, err := files.Open(*filesURI)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*keepDir, 0o770); err != nil {
		return err
	}
	ctx := context.Background()
	// A connection per worker, plus the keeper, the notifier, the burned
	// lines refresh and /healthz.
	db, err := open(ctx, pg.JobStatementTimeout, int32(*workers)+4)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("raposa-engine", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	e := engine.New(engine.Config{
		Node:        *node,
		Workers:     *workers,
		BrowserAddr: *browser,
		PushcutKey:  os.Getenv("PUSHCUT_API_KEY"),
		KeepDir:     *keepDir,
	}, log, engine.NewStore(db), ls, targets, store, srv.Registry)

	log.Info("engine starting", "node", *node, "workers", *workers, "lines", len(ls.All()),
		"targets", len(targets), "files", store.String(), "browser", *browser,
		"watches", os.Getenv("PUSHCUT_API_KEY") != "")
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		err := e.Run(ctx)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		log.Info("engine stopped")
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

func requestCmd(args []string) error {
	fs := flag.NewFlagSet("request", flag.ExitOnError)
	creative := fs.Int("creative", 0, "the creative to investigate (tracks creative id)")
	ad := fs.Int("ad", 0, "one of its ads, to take its publisher and campaign (optional)")
	mode := fs.String("mode", "deep", "deep or quick")
	by := fs.String("by", os.Getenv("USER"), "who asks")
	_ = fs.Parse(args)
	if *creative <= 0 {
		return errors.New("-creative is required")
	}
	ctx := context.Background()
	db, err := open(ctx, pg.WebStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	var adID *int32
	if *ad > 0 {
		a := int32(*ad)
		adID = &a
	}
	id, err := engine.NewStore(db).Request(ctx, int32(*creative), *mode, adID, *by)
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

func stopCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: raposa-engine stop ID")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("not an investigation id: %q", args[0])
	}
	ctx := context.Background()
	db, err := open(ctx, pg.WebStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	ok, err := engine.NewStore(db).Stop(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("it had already ended")
		return nil
	}
	fmt.Println("asked to stop")
	return nil
}

// statusCmd prints the investigations that are waiting or running, and the
// latest that ended.
func statusCmd() error {
	ctx := context.Background()
	db, err := open(ctx, pg.WebStatementTimeout, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(ctx, `
		(SELECT id, creative_id, mode, status, stage, visits_done, visits_target, variants_count,
		        to_char(next_visit_at, 'MM-DD HH24:MI:SS'), COALESCE(claimed_by, '')
		 FROM raposa.investigation WHERE status IN ('waiting', 'running') ORDER BY id)
		UNION ALL
		(SELECT id, creative_id, mode, status, stage, visits_done, visits_target, variants_count,
		        COALESCE(to_char(completed_at, 'MM-DD HH24:MI:SS'), ''), ''
		 FROM raposa.investigation WHERE status NOT IN ('waiting', 'running') ORDER BY completed_at DESC NULLS LAST LIMIT 10)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCREATIVE\tMODE\tSTATUS\tSTAGE\tVISITS\tVARIANTS\tNEXT/ENDED\tNODE")
	for rows.Next() {
		var id int64
		var creative int32
		var mode, status, stage, at, node string
		var done, target int32
		var variants int16
		if err := rows.Scan(&id, &creative, &mode, &status, &stage, &done, &target, &variants, &at, &node); err != nil {
			return err
		}
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%d/%d\t%d\t%s\t%s\n", id, creative, mode, status, stage, done, target, variants, at, node)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return w.Flush()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
