// Command intel-taboola reads our own Taboola accounts through the Backstage
// API. It only reads: the client refuses anything but GETs.
//
//	intel-taboola probe [-account acme-sc] [-days 30] [-items 5] -out DIR
//
// probe calls every read once (account, campaigns, items, reports by day,
// campaign, site, country, platform, hour and item), saves each answer raw
// under DIR/raw before reading it, and writes DIR/summary.md: status, rows,
// fields, size, time and any rate limit headers per read. A read that fails
// is recorded and the probe goes on, since what is not available is a
// finding too.
//
// Credentials come from TABOOLA_CLIENT_ID and TABOOLA_CLIENT_SECRET; the
// host from TABOOLA_BASE (default https://backstage.taboola.com). The token
// is never written anywhere. It is a one-shot command, not a service, so it
// serves no /healthz; SIGTERM stops it between reads.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/taboola"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/run"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "probe":
		err = probeCmd(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "intel-taboola:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: intel-taboola probe|version [flags]")
	os.Exit(2)
}

func probeCmd(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	account := fs.String("account", "", "account id (default: the credentials' own account)")
	days := fs.Int("days", 30, "report range: the last N days, today included")
	items := fs.Int("items", 5, "read items of at most this many campaigns")
	out := fs.String("out", "", "directory for raw answers and summary.md (required)")
	fs.Parse(args)
	if *out == "" {
		return errors.New("probe: -out is required")
	}
	base := os.Getenv("TABOOLA_BASE")
	if base == "" {
		base = taboola.DefaultBase
	}
	c := taboola.New(base, os.Getenv("TABOOLA_CLIENT_ID"), os.Getenv("TABOOLA_CLIENT_SECRET"))
	log := logx.New("intel-taboola", version)
	p := &probe{c: c, dir: *out, account: *account, days: *days, items: *items, now: time.Now()}
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		if err := p.run(ctx); err != nil {
			return err
		}
		fmt.Printf("wrote %s/summary.md\n", *out)
		return nil
	})
}
