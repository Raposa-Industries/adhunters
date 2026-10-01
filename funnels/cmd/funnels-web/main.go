// Command funnels-web serves Funnels' pages under /funnels/, in the Frame.
//
//	funnels-web            serve until SIGTERM
//	funnels-web version
//
// DATABASE_URL is a login that reads Funnels' tables and drafts
// (funnels_web_read) and writes nothing. FUNNELS_WEB_ADDR is where the pages
// listen (127.0.0.1:8099); people reach them through the Cloudflare tunnel,
// behind Cloudflare Access. FUNNELS_SITES is the edge's sites folder, read
// for the hosting page. /healthz and /metrics are on OPS_ADDR.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
	"github.com/Raposa-Industries/adhunters/funnels/pages"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, "funnels-web:", err)
		os.Exit(1)
	}
}

func serve() error {
	log := logx.New("funnels-web", version)
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
	db, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "funnels-web", StatementTimeout: pg.WebStatementTimeout, MaxConns: 4})
	if err != nil {
		return err
	}
	defer db.Close()

	var sites *edge.Sites
	if root := envOr("FUNNELS_SITES", "/var/lib/funnels/sites"); root != "-" {
		sites = &edge.Sites{Root: root}
	}
	srv := ops.New("funnels-web", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	ln, err := net.Listen("tcp", envOr("FUNNELS_WEB_ADDR", "127.0.0.1:8099"))
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           pages.Handler(pages.Config{DB: db, Sites: sites, Log: log}),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("funnels-web listening", "addr", ln.Addr().String())
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		served := make(chan error, 1)
		go func() { served <- httpSrv.Serve(ln) }()
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
