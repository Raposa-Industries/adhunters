// Command tracks-capture scrapes the ad networks' feeds and writes every
// answer, as received, to the spool. It has no database.
//
//	tracks-capture run   -targets targets.yaml -lines proxies.env -spool /var/lib/tracks/spool [-instance a]
//	tracks-capture stats [-rate 14000] /var/lib/tracks/spool
//
// run stops cleanly on SIGTERM, or by itself after -for. stats reads the
// sealed raw files and prints bytes per scrape.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/tracks/capture/feed"
	"github.com/Raposa-Industries/adhunters/tracks/capture/lines"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/capture/sweep"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "stats":
		err = statsCmd(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracks-capture:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tracks-capture run|stats|version [flags]")
	os.Exit(2)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	targetsPath := fs.String("targets", "targets.yaml", "targets file (the collector's publishers.yaml format)")
	linesPath := fs.String("lines", "proxies.env", "proxy lines file, key=host:port:user:pass per line")
	spoolDir := fs.String("spool", "spool", "spool folder")
	instance := fs.String("instance", "a", "instance name, in every record and file name")
	workers := fs.Int("workers", 4, "parallel workers")
	throttle := fs.Duration("throttle", 150*time.Millisecond, "wait after each scrape, per worker")
	backoff := fs.Duration("backoff", 500*time.Millisecond, "wait after a failed scrape, per worker")
	timeout := fs.Duration("timeout", 8*time.Second, "per request")
	roles := fs.String("roles", "dc,isp", "proxy line roles to use, most preferred first")
	reserved := fs.String("reserved", "isp-1,isp-2,isp-3,isp-4,isp-5", "lines kept out (the collector's backup lines)")
	cooldown := fs.Duration("cooldown", 30*time.Second, "how long a line rests after an error")
	stopAfter := fs.Duration("for", 0, "stop by itself after this long (0: run until stopped)")
	_ = fs.Parse(args)

	log := logx.New("tracks-capture", version).With("instance", *instance)

	targets, err := feed.LoadTargets(*targetsPath)
	if err != nil {
		return err
	}
	pool, err := lines.Load(*linesPath, split(*roles), split(*reserved), *cooldown)
	if err != nil {
		return err
	}
	if pool.Size() == 0 {
		// Never scrape from the box's own address.
		return fmt.Errorf("%s has no lines of roles %s", *linesPath, *roles)
	}

	srv := ops.New("tracks-capture", version)
	metrics := sweep.NewMetrics(srv.Registry)
	sealedBytes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tracks_capture_spool_sealed_bytes_total", Help: "Bytes of sealed raw files by network and stage (raw before zstd, zst after).",
	}, []string{"network", "stage"})
	sealedRows := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tracks_capture_spool_sealed_rows_total", Help: "Scrapes in sealed raw files, by network.",
	}, []string{"network"})
	srv.Registry.MustRegister(sealedBytes, sealedRows)

	w, err := spool.Open(*spoolDir, *instance, log, spool.Options{OnSealed: func(s spool.Sealed) {
		sealedBytes.WithLabelValues(s.Network, "raw").Add(float64(s.RawBytes))
		sealedBytes.WithLabelValues(s.Network, "zst").Add(float64(s.ZstBytes))
		sealedRows.WithLabelValues(s.Network).Add(float64(s.Rows))
	}})
	if err != nil {
		return err
	}
	srv.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "tracks_capture_spool_unsealed_files", Help: "Closed raw files waiting to be compressed.",
	}, func() float64 { return float64(w.Pending()) }))
	srv.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "tracks_capture_lines_cooling_down", Help: "Proxy lines resting after an error.",
	}, func() float64 { return float64(pool.CoolingDown()) }))

	started := time.Now()
	srv.AddCheck("spool", func(context.Context) error {
		if n := w.Pending(); n > 10 {
			return fmt.Errorf("%d raw files waiting to be sealed", n)
		}
		return nil
	})
	srv.AddCheck("scraping", func(context.Context) error {
		last := time.Unix(metrics.LastWriteUnix.Load(), 0)
		if time.Since(started) > 2*time.Minute && time.Since(last) > 2*time.Minute {
			return errors.New("nothing written to the spool for 2 minutes")
		}
		return nil
	})

	log.Info("capture starting", "targets", len(targets), "lines", pool.Describe(),
		"workers", *workers, "throttle", throttle.String(), "spool", *spoolDir, "for", stopAfter.String())

	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		if *stopAfter > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *stopAfter)
			defer cancel()
		}
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		go w.Run(ctx)

		err := sweep.Run(ctx, sweep.Config{
			Targets: targets, Lines: pool, Spool: w, Instance: *instance, Version: version,
			Workers: *workers, Throttle: *throttle, Backoff: *backoff, Timeout: *timeout,
			Log: log, Metrics: metrics,
		})
		w.Close()
		log.Info("capture stopped", "ran", time.Since(started).Round(time.Second).String())
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

func statsCmd(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	rate := fs.Int("rate", 14000, "scrapes an hour to project the archive's growth at")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: tracks-capture stats [-rate N] <spool folder>")
	}
	totals, err := spool.Summarize(fs.Arg(0))
	if err != nil {
		return err
	}
	spool.Report(os.Stdout, totals, *rate)
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
