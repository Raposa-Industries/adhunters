// Command create-web answers the team's address (hunt-teste.fyi): every
// request comes through it. It asks for the team's sign-in (SIGNIN_USER and
// SIGNIN_PASSWORD_HASH, see create/signin), then sends each request to the
// app whose path it starts with (web.Apps) and every other path to Launch.
// The sign-in replaced Cloudflare Access on 2026-10-01 (owner's word). It
// used to serve the old launcher page and its API; the owner retired both
// the same day, so nothing here reaches OpenAI or Taboola any more.
//
//	create-web [-addr 127.0.0.1:8091]
//	create-web hash-password < password   prints the SIGNIN_PASSWORD_HASH= line
//	create-web version
//
// It refuses to start without the sign-in settings. It listens on localhost
// unless told otherwise (CREATE_WEB_ADDR). /healthz and /metrics are on
// OPS_ADDR, 127.0.0.1:9109 by default. It stops cleanly on SIGTERM. The rest
// of /etc/adhunters/create-web.env is no longer read.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/create/signin"
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
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := hashPassword(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "create-web:", err)
			os.Exit(1)
		}
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
	gate, err := signin.New(os.Getenv("SIGNIN_USER"), os.Getenv("SIGNIN_PASSWORD_HASH"))
	if err != nil {
		return fmt.Errorf("sign-in (SIGNIN_USER, SIGNIN_PASSWORD_HASH from create-web hash-password): %w", err)
	}
	srv := ops.New("create-web", version)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		// Every request is counted and timed by the app it goes to, on
		// /metrics (adhunters_http_*), the sign-in's own pages as "signin".
		Handler: srv.HTTP(gate.Wrap(web.Site(web.Apps, log)), func(r *http.Request) string {
			if strings.HasPrefix(r.URL.Path, signin.Path) {
				return "signin"
			}
			return web.Route(web.Apps, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: the apps' own answers can stream for minutes
		// (a Create turn); each app sets its own limits.
		IdleTimeout: 2 * time.Minute,
	}
	log.Info("create-web listening", "addr", ln.Addr().String(), "apps", len(web.Apps), "else", web.Home)

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

// hashPassword reads a password (one line) and prints the env line holding
// its hash. Nothing else is printed, so the output can go straight into the
// env file.
func hashPassword(in io.Reader, out io.Writer) error {
	b, err := io.ReadAll(io.LimitReader(in, 1024))
	if err != nil {
		return err
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if len(pw) < 8 {
		return errors.New("the password is shorter than 8 characters")
	}
	h, err := signin.Hash(pw)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "SIGNIN_PASSWORD_HASH="+h)
	return err
}
