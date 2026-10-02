// Command raposa-engine runs investigations: it visits each ad one visit at
// a time, keeps the pages it reaches whole, and delivers watches.
//
//	raposa-engine migrate
//	raposa-engine run     [-workers 10] [-lines proxies.env] [-targets targets.yaml] [-files file:///var/lib/raposa/files]
//	raposa-engine request -creative 123 [-ad 456] [-mode deep|quick] [-by name]
//	raposa-engine stop    ID
//	raposa-engine status
//	raposa-engine import-old [-from URL] [-old-files s3://bucket/raposa] [-dry-run]
//
// The database URL comes from DATABASE_URL; the login reads tracks_api
// (tracks_api_read). Watched events go to the ops group "AdHunters operation"
// on Telegram with TELEGRAM_BOT_TOKEN and OPS_TELEGRAM_CHAT_ID, linking the
// investigation under RAPOSA_BASE_URL; without them they are recorded as
// skipped. An s3:// files store takes its keys from S3_ENDPOINT,
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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/raposa/internal/engine"
	"github.com/Raposa-Industries/adhunters/raposa/internal/importold"
	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
	"github.com/Raposa-Industries/adhunters/raposa/migrations"
	"github.com/Raposa-Industries/adhunters/shared/files"
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
	case "import-old":
		err = importOldCmd(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "usage: raposa-engine run|migrate|request|stop|status|import-old|version [flags]")
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
	cfg := engine.Config{
		Node:        *node,
		Workers:     *workers,
		BrowserAddr: *browser,
		BaseURL:     os.Getenv("RAPOSA_BASE_URL"),
		KeepDir:     *keepDir,
	}
	if tok, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("OPS_TELEGRAM_CHAT_ID"); tok != "" && chat != "" {
		cfg.Telegram = telegram.New(tok, chat)
	}
	e := engine.New(cfg, log, engine.NewStore(db), ls, targets, store, srv.Registry)

	log.Info("engine starting", "node", *node, "workers", *workers, "lines", len(ls.All()),
		"targets", len(targets), "files", store.String(), "browser", *browser,
		"watches", cfg.Telegram != nil)
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

// importOldCmd copies the collector's investigations (spy.raposa_*) into
// raposa. It only reads the collector's database. Run it again at any time:
// it copies only the jobs not copied yet.
func importOldCmd(args []string) error {
	fs := flag.NewFlagSet("import-old", flag.ExitOnError)
	from := fs.String("from", os.Getenv("OLD_DATABASE_URL"), "the collector's database (default: OLD_DATABASE_URL)")
	oldFiles := fs.String("old-files", "", "the collector's object storage, s3://BUCKET/raposa (default: from BLOB_BUCKET); its keys come from the collector's BLOB_ENDPOINT, BLOB_REGION, BLOB_KEY_ID and BLOB_KEY_SECRET")
	filesURI := fs.String("files", envOr("RAPOSA_FILES", "file:///var/lib/raposa/files"), "where kept files go now")
	tmp := fs.String("tmp", os.TempDir(), "where one file waits between the two stores")
	dryRun := fs.Bool("dry-run", false, "read and check everything, write nothing")
	_ = fs.Parse(args)
	if *from == "" {
		return errors.New("-from (or OLD_DATABASE_URL) is required")
	}
	if *oldFiles == "" && os.Getenv("BLOB_BUCKET") != "" {
		*oldFiles = "s3://" + os.Getenv("BLOB_BUCKET") + "/raposa"
	}
	log := logx.New("raposa-engine", version)
	ctx := context.Background()

	u, err := url.Parse(*from)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	q := u.Query()
	q.Set("timezone", "UTC")
	q.Set("default_transaction_read_only", "on") // the collector's database is only read
	u.RawQuery = q.Encode()
	old, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "raposa-import", StatementTimeout: pg.JobStatementTimeout, MaxConns: 2})
	if err != nil {
		return fmt.Errorf("the collector's database: %w", err)
	}
	defer old.Close()
	db, err := open(ctx, pg.JobStatementTimeout, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	store, err := files.Open(*filesURI)
	if err != nil {
		return err
	}
	var oldStore files.Store
	if *oldFiles != "" {
		ou, err := url.Parse(*oldFiles)
		if err != nil || ou.Scheme != "s3" {
			return fmt.Errorf("-old-files must be s3://BUCKET/PREFIX, not %q", *oldFiles)
		}
		s3, err := files.OpenS3(files.S3Config{
			Endpoint:  os.Getenv("BLOB_ENDPOINT"),
			Region:    os.Getenv("BLOB_REGION"),
			AccessKey: os.Getenv("BLOB_KEY_ID"),
			SecretKey: os.Getenv("BLOB_KEY_SECRET"),
		}, ou.Host, ou.Path)
		if err != nil {
			return fmt.Errorf("-old-files: %w", err)
		}
		oldStore = s3
	}

	r, err := importold.Run(ctx, importold.Config{
		Old: old, New: db, OldFiles: oldStore, Files: store, TmpDir: *tmp, DryRun: *dryRun, Log: log,
	})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if *dryRun {
		fmt.Fprintln(w, "DRY RUN: nothing was written.")
	}
	fmt.Fprintf(w, "jobs in the collector\t%d\n", r.Jobs)
	fmt.Fprintf(w, "copied now\t%d\n", r.Copied)
	fmt.Fprintf(w, "copied before\t%d\n", r.AlreadyCopied)
	fmt.Fprintf(w, "waiting for Tracks to know the creative\t%d\n", r.UnknownCreative)
	fmt.Fprintf(w, "failed\t%d\n", r.Failed)
	fmt.Fprintf(w, "visits\t%d\n", r.Visits)
	fmt.Fprintf(w, "pages copied\t%d\n", r.PagesNew)
	fmt.Fprintf(w, "files copied\t%d (%d MB)\n", r.Files, r.FileBytes>>20)
	_ = w.Flush()
	if len(r.UnknownKeys) > 0 {
		fmt.Printf("\nCreatives Tracks does not know yet (first %d): %s\n", len(r.UnknownKeys), strings.Join(r.UnknownKeys, ", "))
	}
	if len(r.SettingsDiffer) > 0 {
		fmt.Println("\nSettings the collector had at another value (not changed; UPDATE raposa.setting to carry one over):")
		for _, l := range r.SettingsDiffer {
			fmt.Println("  " + l)
		}
	}
	if r.Failed > 0 {
		fmt.Println("\nNot copied:")
		for _, l := range r.Errors {
			fmt.Println("  " + l)
		}
		return fmt.Errorf("%d jobs were not copied", r.Failed)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
