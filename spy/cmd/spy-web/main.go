// Command spy-web serves AdHunters Spy's pages and their JSON under /spy/,
// in the shared Frame.
//
//	spy-web [-addr 127.0.0.1:8097]
//
// The database URL comes from DATABASE_URL; the login needs spy_api_read,
// tracks_api_read and (for the Raposa parts of an ad's page) raposa_api_read
// with EXECUTE on raposa_api.request_investigation_v1. With ACCESS_TEAM and
// ACCESS_AUD set, every request must carry a valid Cloudflare Access token;
// without them it serves anyone who can reach it, so it listens on
// localhost only. /healthz and /metrics are on OPS_ADDR, as for every
// binary. It stops cleanly on SIGTERM.
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
	"github.com/Raposa-Industries/adhunters/spy/web"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "spy-web:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("spy-web", flag.ExitOnError)
	addr := fs.String("addr", envOr("SPY_WEB_ADDR", "127.0.0.1:8097"), "where the pages listen")
	_ = fs.Parse(args)

	log := logx.New("spy-web", version)
	var access *web.Access
	team, aud := os.Getenv("ACCESS_TEAM"), os.Getenv("ACCESS_AUD")
	switch {
	case team != "" && aud != "":
		access = &web.Access{Team: team, Audience: aud}
	case team != "" || aud != "":
		return errors.New("set both ACCESS_TEAM and ACCESS_AUD, or neither")
	default:
		host, _, err := net.SplitHostPort(*addr)
		if err != nil {
			return fmt.Errorf("-addr: %w", err)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("without Cloudflare Access (ACCESS_TEAM, ACCESS_AUD) spy-web listens only on localhost")
		}
		log.Warn("no Cloudflare Access check: anyone who reaches the address can read Spy")
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
	// Ranges other than the last 24 hours are counted when asked and run
	// under their own longer timeout (web.Config.RangeLimit).
	db, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "spy-web", StatementTimeout: pg.WebStatementTimeout, MaxConns: 8})
	if err != nil {
		return err
	}
	defer db.Close()

	pages, err := web.New(db, log, web.Config{Access: access})
	if err != nil {
		return err
	}
	srv := ops.New("spy-web", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: pages.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("pages listening", "addr", ln.Addr().String(), "access", access != nil)

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
