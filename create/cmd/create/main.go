// Command create is AdHunters Create: briefs, the images and headlines
// OpenAI makes from them, and saves of the chosen ones into the library
// (create/README.md). Its pages are under /create/ on the shared shell.
//
//	create           serve the pages and run the worker
//	create version
//
// Settings come from the environment (create/deploy/create.env.example).
// It runs its migrations on start, listens on localhost behind Cloudflare
// Access, serves /healthz and /metrics on OPS_ADDR, and stops cleanly on
// SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/create/internal/briefs"
	"github.com/Raposa-Industries/adhunters/create/internal/keep"
	"github.com/Raposa-Industries/adhunters/create/internal/library"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/create/internal/site"
	"github.com/Raposa-Industries/adhunters/create/migrations"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openDB(ctx context.Context, log *slog.Logger) (*pgxpool.Pool, error) {
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
	db, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "create", StatementTimeout: pg.WebStatementTimeout, MaxConns: 10})
	if err != nil {
		return nil, err
	}
	migs, err := migrations.Load()
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := migrate.Up(ctx, db, log, migrations.Schema, migs); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// openAIStatus tells the pages why making is off.
type openAIStatus struct{ c *openai.Client }

func (s openAIStatus) OpenAIWhy() string { return s.c.Why() }

func serve(args []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	addr := fs.String("addr", envOr("CREATE_ADDR", "127.0.0.1:8094"), "where the pages listen")
	filesURI := fs.String("files", envOr("CREATE_FILES", "file:///var/lib/create/files"), "where references and made pictures are kept: file:///path or s3://bucket/prefix")
	keepDir := fs.String("keep", envOr("CREATE_KEEP_DIR", "/var/lib/create/kept"), "where every OpenAI reply is kept as it came")
	libraryURL := fs.String("library", envOr("LIBRARY_URL", "http://127.0.0.1:8093"), "the library's API")
	workers := fs.Int("workers", 3, "jobs run at once (pictures are made this many at a time)")
	_ = fs.Parse(args)
	if v := os.Getenv("CREATE_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10 {
			return fmt.Errorf("CREATE_WORKERS must be 1 to 10, not %q", v)
		}
		*workers = n
	}

	log := logx.New("create", version)
	settings, err := openai.SettingsFromEnv()
	if err != nil {
		return err
	}
	store0, err := files.Open(*filesURI)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := openDB(ctx, log)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("create", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	ai := openai.New(settings, srv, keep.New(*keepDir), log)
	lib := library.New(*libraryURL)

	var worker *briefs.Worker
	st := briefs.New(db, store0, func() { worker.Kick() })
	worker = briefs.NewWorker(st, ai, lib, log)
	worker.Workers = *workers
	tasks := srv.Tasks()
	worker.Done = func(kind string, start time.Time, err error) { tasks.Done("job-"+kind, start, 1, err) }

	web := site.New(st, lib.Browse(), openAIStatus{ai}, log, version)
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: web.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("create listening", "addr", ln.Addr().String(), "files", store0.String(), "library", *libraryURL,
		"openai", ai.Available(), "image_model", settings.ImageModel, "text_model", settings.TextModel)

	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		workDone := make(chan error, 1)
		go func() { workDone <- worker.Run(ctx) }()
		served := make(chan error, 1)
		go func() { served <- httpSrv.Serve(ln) }()
		var err error
		select {
		case err = <-served:
		case <-ctx.Done():
			shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = httpSrv.Shutdown(shut)
			cancel()
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if werr := <-workDone; err == nil {
			err = werr
		}
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}
