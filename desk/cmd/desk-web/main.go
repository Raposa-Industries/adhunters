// Command desk-web serves Desk's pages under /desk/: the conversations, the
// plans people OK, the choices, the to-do list and the settings with the
// stop switch.
//
//	desk-web [-addr 127.0.0.1:8092]
//
// The database URL comes from DATABASE_URL. It listens on localhost, behind
// the Cloudflare Tunnel with Access in front: who is asking is Access's
// email header. DESK_DEV_PERSON stands in for it on a laptop, never on a
// server. /healthz and /metrics are on OPS_ADDR. It stops cleanly on SIGTERM.
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

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
	"github.com/Raposa-Industries/adhunters/desk/internal/web"
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
	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "desk-web:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("desk-web", flag.ExitOnError)
	addr := fs.String("addr", envOr("DESK_WEB_ADDR", "127.0.0.1:8092"), "where the pages listen")
	_ = fs.Parse(args)

	log := logx.New("desk-web", version)
	catalog, err := actions.Load()
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
	db, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "desk-web", StatementTimeout: pg.WebStatementTimeout, MaxConns: 6})
	if err != nil {
		return err
	}
	defer db.Close()

	dev := os.Getenv("DESK_DEV_PERSON")
	if dev != "" {
		log.Warn("DESK_DEV_PERSON is set: anyone reaching the pages is this person", "person", dev)
	}
	pages, err := web.New(store.New(db), catalog, log, dev)
	if err != nil {
		return err
	}
	srv := ops.New("desk-web", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: pages.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("pages listening", "addr", ln.Addr().String(), "actions", len(catalog.Latest()))

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
