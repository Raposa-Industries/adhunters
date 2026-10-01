// Command launch-web serves AdHunters Launch: its pages and API under
// /launch/, the Frame under /launch/_frame/, the shared ad code under
// /launch/_ads/, and every write Launch makes on an ad network (Taboola
// today).
//
//	launch-web [-addr 127.0.0.1:8094] [-data /var/lib/launch-web]
//	launch-web version
//
// It applies the launch schema's migrations at start. Every Taboola request
// and answer is kept raw under <data>/kept/<UTC date>/ before it is read,
// and the pictures people bring are kept by their SHA-256 under
// <data>/images/. /healthz and /metrics are on OPS_ADDR (127.0.0.1:9111 by
// default). It stops cleanly on SIGTERM.
//
// Settings come from the environment:
//
//	DATABASE_URL            the launch-web login on the data box's Postgres
//	LAUNCH_WEB_ADDR         127.0.0.1:8094
//	LAUNCH_DATA_DIR         launch-data (kept exchanges, pictures, only-own state)
//	LAUNCH_WATCH_EVERY      5m: how often moves are checked for started copies
//	TABOOLA_*               one Taboola login (shared/taboola/write, SettingsFromEnv)
//
// Nothing is created running: groups, campaigns and ads are made paused,
// and a person turns them on in Taboola's own dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/keep"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/launch/internal/actions"
	"github.com/Raposa-Industries/adhunters/launch/internal/api"
	"github.com/Raposa-Industries/adhunters/launch/internal/images"
	"github.com/Raposa-Industries/adhunters/launch/internal/library"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	tbnet "github.com/Raposa-Industries/adhunters/launch/internal/network/taboola"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
	"github.com/Raposa-Industries/adhunters/launch/migrations"
	"github.com/Raposa-Industries/adhunters/launch/web"
	"github.com/Raposa-Industries/adhunters/shared/adsweb"
	"github.com/Raposa-Industries/adhunters/shared/frame"
	"github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "launch-web:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("launch-web", flag.ExitOnError)
	addr := fs.String("addr", envOr("LAUNCH_WEB_ADDR", "127.0.0.1:8094"), "where the pages and API listen")
	dataDir := fs.String("data", envOr("LAUNCH_DATA_DIR", "launch-data"), "folder for kept exchanges, pictures and only-own state")
	_ = fs.Parse(args)
	every, err := time.ParseDuration(envOr("LAUNCH_WATCH_EVERY", "5m"))
	if err != nil || every < time.Minute {
		return fmt.Errorf("LAUNCH_WATCH_EVERY must be a duration of a minute or more, not %q", os.Getenv("LAUNCH_WATCH_EVERY"))
	}

	log := logx.New("launch-web", version)
	ctx := context.Background()
	db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	migs, err := migrations.Load()
	if err != nil {
		return err
	}
	if n, err := migrate.Up(ctx, db, log, migrations.Schema, migs); err != nil {
		return err
	} else if n > 0 {
		log.Info("migrations applied", "count", n)
	}

	tbSet, err := write.SettingsFromEnv(os.Getenv, filepath.Join(*dataDir, "taboola-state.json"))
	if err != nil {
		return err
	}
	tb, err := write.New(tbSet, keep.New(filepath.Join(*dataDir, "kept")), log)
	if err != nil {
		return err
	}
	if tb.Available() {
		log.Info("taboola connected", "accounts", len(tbSet.Accounts), "max_cpc", tbSet.MaxCPC, "max_daily_cap", tbSet.MaxDailyCap, "max_spend_limit", tbSet.MaxSpendLimit, "only_own", tbSet.OnlyOwn)
	} else {
		log.Warn("taboola off", "reason", tb.Why())
	}

	img := &images.Store{Dir: filepath.Join(*dataDir, "images")}
	st := store.New(db)
	l := actions.New(st, img, log, tbnet.Message, tbnet.New(tb))
	a := api.New(ctx, l, img, log, classify)
	a.Limits = map[string]any{"max_cpc": tbSet.MaxCPC, "max_daily_cap": tbSet.MaxDailyCap, "max_spend_limit": tbSet.MaxSpendLimit, "only_own": tbSet.OnlyOwn}
	if lib := envOr("LAUNCH_LIBRARY_URL", "http://127.0.0.1:8093"); lib != "off" {
		a.Library = library.New(lib)
	}

	srv := ops.New("launch-web", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	tasks := srv.Tasks()
	tasks.Promise("launch_watch_moves", 3*every)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           handler(a),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("launch-web listening", "addr", ln.Addr().String(), "data", *dataDir)

	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, envOr("OPS_ADDR", "127.0.0.1:9111")) }()
		go watch(ctx, l, tasks, log, every)
		served := make(chan error, 1)
		go func() { served <- httpSrv.Serve(ln) }()
		var err error
		select {
		case err = <-served:
		case <-ctx.Done():
			shut, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err = httpSrv.Shutdown(shut)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				log.Warn("requests still running at stop were cut")
				_ = httpSrv.Close()
				err = nil
			}
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

// handler is everything launch-web serves on its address.
func handler(a *api.API) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/launch/_frame/", http.StripPrefix("/launch/_frame", frame.Handler()))
	mux.Handle("/launch/_ads/", http.StripPrefix("/launch/_ads", adsweb.Handler()))
	mux.Handle("/launch/api/", a.Handler())
	mux.Handle("/launch/", web.Handler())
	mux.Handle("/{$}", http.RedirectHandler("/launch/", http.StatusFound))
	// The API changes campaigns, so a request another site makes the
	// browser send is refused; the pages' own fetches are same-origin.
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"pedido vindo de outro site recusado"}` + "\n"))
	}))
	return web.Secure(cop.Handler(mux))
}

// watch pauses a move's originals once a person starts the copies.
func watch(ctx context.Context, l *actions.Launch, tasks *ops.Tasks, log interface{ Warn(string, ...any) }, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		start := time.Now()
		n, err := l.Watch(ctx)
		if err != nil {
			log.Warn("watching moves", "err", err)
		}
		tasks.Done("launch_watch_moves", start, int64(n), err)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func classify(err error) (int, string) {
	var r *network.Refused
	var wr *write.Refused
	switch {
	case errors.As(err, &r), errors.As(err, &wr):
		return http.StatusBadRequest, tbnet.Message(err)
	case errors.Is(err, write.ErrNotConfigured):
		return http.StatusServiceUnavailable, tbnet.Message(err)
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, tbnet.Message(err)
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "não encontrado"
	}
	var we *write.Error
	if errors.As(err, &we) {
		return http.StatusBadGateway, we.Message
	}
	return http.StatusInternalServerError, "falha no Launch; tente de novo"
}

func openDB(ctx context.Context) (*pgxpool.Pool, error) {
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
	return pg.Open(ctx, pg.Config{URL: u.String(), AppName: "launch-web", StatementTimeout: pg.WebStatementTimeout, MaxConns: 4})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
