// Command raposa-web serves Raposa's plain pages: ask for an investigation,
// follow it, read its visits, variants and evidence, open the pages it
// stored, set watches, and see the burned lines.
//
//	raposa-web [-addr 127.0.0.1:8090] [-files file:///var/lib/raposa/files]
//
// The database URL comes from DATABASE_URL. It listens on localhost unless
// told otherwise: reach it over Tailscale until Cloudflare Access is in
// front. /healthz and /metrics are on OPS_ADDR, as for every binary. It stops
// cleanly on SIGTERM.
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
	"time"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/raposa/internal/web"
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
		fmt.Fprintln(os.Stderr, "raposa-web:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("raposa-web", flag.ExitOnError)
	addr := fs.String("addr", envOr("RAPOSA_WEB_ADDR", "127.0.0.1:8090"), "where the pages listen")
	filesURI := fs.String("files", envOr("RAPOSA_FILES", "file:///var/lib/raposa/files"), "where kept files are: file:///path or s3://bucket/prefix")
	_ = fs.Parse(args)

	log := logx.New("raposa-web", version)
	store, err := files.Open(*filesURI)
	if err != nil {
		return err
	}
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		return errors.New("DATABASE_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("DATABASE_URL: %w", err)
	}
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	ctx := context.Background()
	db, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "raposa-web", StatementTimeout: pg.WebStatementTimeout, MaxConns: 4})
	if err != nil {
		return err
	}
	defer db.Close()

	pages, err := web.New(db, store, log)
	if err != nil {
		return err
	}
	srv := ops.New("raposa-web", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: pages.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("pages listening", "addr", ln.Addr().String(), "files", store.String())

	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
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
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
