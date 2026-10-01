// Command create-web answers the team's address (hunt-teste.fyi) on every
// path no other app claims, and sends each request on to Launch. It used to
// serve the old launcher page and its API; the owner retired both on
// 2026-10-01 (Launch replaced them), so nothing here reaches OpenAI or
// Taboola any more.
//
//	create-web [-addr 127.0.0.1:8091]
//	create-web version
//
// It listens on localhost unless told otherwise (CREATE_WEB_ADDR). /healthz
// and /metrics are on OPS_ADDR, 127.0.0.1:9109 by default. It stops cleanly
// on SIGTERM. The rest of /etc/adhunters/create-web.env is no longer read.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Raposa-Industries/adhunters/create/web"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
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
		fmt.Fprintln(os.Stderr, "create-web:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("create-web", flag.ExitOnError)
	addr := fs.String("addr", envOr("CREATE_WEB_ADDR", "127.0.0.1:8091"), "where it listens")
	_ = fs.Parse(args)

	log := logx.New("create-web", version)
	srv := ops.New("create-web", version)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           web.Secure(web.Root()),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("create-web listening", "addr", ln.Addr().String(), "to", web.Home)

	opsAddr := envOr("OPS_ADDR", "127.0.0.1:9109")
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, opsAddr) }()
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
