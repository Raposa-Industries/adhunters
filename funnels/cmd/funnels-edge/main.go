// Command funnels-edge is what landing page visitors reach: the hosted
// landing sites, the page script at /ah.js and the collector at /e, which
// writes every beacon as received to the spool.
//
//	funnels-edge run      [-addr 127.0.0.1:8095] [-sites /var/lib/funnels/sites] [-spool /var/lib/funnels/spool] [-instance a] [-trust-cloudflare]
//	funnels-edge publish  [-sites …] -site lp.example.com ./folder
//	funnels-edge versions [-sites …] -site lp.example.com
//	funnels-edge serve    [-sites …] -site lp.example.com -version 20260930T200000Z
//	funnels-edge site     [-sites …] -site lp.example.com [-clarity abcd1234]
//
// run needs FUNNELS_IP_KEY (16 characters or more), which keys the hash of
// visitors' addresses. It needs no database: when the database is down,
// beacons keep landing in the spool. /healthz and /metrics are on OPS_ADDR.
// It stops cleanly on SIGTERM: requests in flight finish and open raw files
// are sealed.
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

	"github.com/Raposa-Industries/adhunters/funnels/edge"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/shared/spool"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

const usage = `usage:
  funnels-edge run      [-addr 127.0.0.1:8095] [-sites DIR] [-spool DIR] [-instance a] [-trust-cloudflare]
  funnels-edge publish  [-sites DIR] -site HOST FOLDER
  funnels-edge versions [-sites DIR] -site HOST
  funnels-edge serve    [-sites DIR] -site HOST -version VERSION
  funnels-edge site     [-sites DIR] -site HOST [-clarity ID]
  funnels-edge version`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
		return
	case "run":
		err = runEdge(os.Args[2:])
	case "publish", "versions", "serve", "site":
		err = manage(os.Args[1], os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "funnels-edge:", err)
		os.Exit(1)
	}
}

func sitesFlag(fs *flag.FlagSet) *string {
	return fs.String("sites", envOr("FUNNELS_SITES", "/var/lib/funnels/sites"), "the hosted sites' folder")
}

func runEdge(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	addr := fs.String("addr", envOr("FUNNELS_EDGE_ADDR", "127.0.0.1:8095"), "where visitors' requests arrive (cloudflared points here)")
	sites := sitesFlag(fs)
	spoolDir := fs.String("spool", envOr("FUNNELS_SPOOL", "/var/lib/funnels/spool"), "where raw files are written")
	instance := fs.String("instance", "a", "this edge's name in raw file names")
	trustCF := fs.Bool("trust-cloudflare", os.Getenv("FUNNELS_TRUST_CLOUDFLARE") == "1", "take the visitor's address and country from Cloudflare's headers")
	_ = fs.Parse(args)

	key := os.Getenv("FUNNELS_IP_KEY")
	if len(key) < 16 {
		return edge.ErrNoKey
	}
	log := logx.New("funnels-edge", version)
	srv := ops.New("funnels-edge", version)
	metrics := edge.NewMetrics(srv.Registry)

	w, err := spool.Open(*spoolDir, "edge", *instance, log, spool.Options{})
	if err != nil {
		return err
	}
	srv.AddCheck("spool", func(context.Context) error {
		if metrics.SpoolFailing() {
			return errors.New("the last beacon could not be written to the spool")
		}
		if n := w.Pending(); n > 10 {
			return fmt.Errorf("%d raw files wait to be sealed", n)
		}
		return nil
	})

	e := edge.New(edge.Config{
		Sites: &edge.Sites{Root: *sites}, Spool: w, Instance: *instance, Version: version,
		IPKey: []byte(key), TrustCloudflare: *trustCF, Log: log, Metrics: metrics,
	})
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		w.Close()
		return err
	}
	httpSrv := &http.Server{
		Handler:           e.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Info("edge listening", "addr", ln.Addr().String(), "sites", *sites, "spool", *spoolDir, "trust_cloudflare", *trustCF)

	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		defer w.Close() // seals open files after the last request
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		go w.Run(ctx)
		go func() {
			// Lines are buffered; push them to disk every second so a crash
			// loses at most one second of beacons.
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := w.Flush(); err != nil {
						log.Error("flushing the spool", "err", err)
					}
				}
			}
		}()
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

func manage(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	root := sitesFlag(fs)
	site := fs.String("site", "", "the site's host name, like lp.example.com")
	ver := fs.String("version", "", "serve: the version to serve")
	clarity := fs.String("clarity", "", "site: the Microsoft Clarity project id (empty turns Clarity off)")
	_ = fs.Parse(args)
	if *site == "" {
		return errors.New("-site is required")
	}
	s := &edge.Sites{Root: *root}
	switch cmd {
	case "publish":
		if fs.NArg() != 1 {
			return errors.New("publish takes one folder")
		}
		v, err := s.Publish(*site, fs.Arg(0), time.Now())
		if err != nil {
			return err
		}
		fmt.Printf("%s now serves version %s\n", *site, v)
	case "versions":
		vs, err := s.Versions(*site)
		if err != nil {
			return err
		}
		for _, v := range vs {
			mark := " "
			if v.Current {
				mark = "*"
			}
			fmt.Println(mark, v.Name)
		}
	case "serve":
		if err := s.Serve(*site, *ver); err != nil {
			return err
		}
		fmt.Printf("%s now serves version %s\n", *site, *ver)
	case "site":
		c, err := s.Config(*site)
		if err != nil {
			return err
		}
		c.Clarity = *clarity
		if err := s.SetConfig(*site, c); err != nil {
			return err
		}
		fmt.Printf("%s: clarity %q\n", *site, c.Clarity)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
