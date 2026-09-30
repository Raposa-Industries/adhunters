// Command intel-web serves Intel's pages under /intel/, in the Frame.
//
//	intel-web            serve until SIGTERM
//	intel-web version
//
// DATABASE_URL reads the intel schema and writes only a suggestion's "not
// now". INTEL_WEB_ADDR is where the pages listen (127.0.0.1:8096); people
// reach them through the Cloudflare tunnel, behind Cloudflare Access, which
// also names who said "not now". /healthz and /metrics are on OPS_ADDR
// (127.0.0.1:9100 unless set). Nothing here talks to Taboola or RedTrack.
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

	"github.com/Raposa-Industries/adhunters/intel/web"
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
		fmt.Fprintln(os.Stderr, "intel-web:", err)
		os.Exit(1)
	}
}

func serve() error {
	log := logx.New("intel-web", version)
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
	db, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "intel-web", StatementTimeout: pg.WebStatementTimeout, MaxConns: 4})
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("intel-web", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	addr := os.Getenv("INTEL_WEB_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8096"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           web.Handler(db, log),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("intel-web listening", "addr", ln.Addr().String())
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
