// Command intel-collect asks Taboola and RedTrack for Intel's numbers on a
// schedule and keeps every answer as received: first in its spool on local
// disk, then in intel.answer. It parses nothing (intel-numbers does), so it
// rarely changes and a deploy of the rest never pauses collection.
//
//	intel-collect run          collect until SIGTERM
//	intel-collect once JOB     run one job now (accounts, settings, reports,
//	                           month, realtime, history, rt-reports,
//	                           rt-week, rt-conversions), then drain the spool
//	intel-collect version
//
// It only reads: the Taboola client refuses anything but GETs (and its token
// request), and the RedTrack transport refuses anything but GETs. Settings
// come from the environment (see intel/deploy/intel-collect.env.example).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // account time zones, whatever the box has installed

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/intel/collect"
	"github.com/Raposa-Industries/adhunters/intel/redtrack"
	"github.com/Raposa-Industries/adhunters/intel/taboola"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
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
	case "run":
		err = runCmd()
	case "once":
		if len(os.Args) < 3 {
			usage()
		}
		err = onceCmd(os.Args[2])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "intel-collect:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: intel-collect run | once JOB | version")
	os.Exit(2)
}

type setup struct {
	spool    collect.Spool
	taboolas []*collect.Taboola
	redtrack []*collect.RedTrack
}

func build(log *slog.Logger) (*setup, error) {
	dir := env("INTEL_SPOOL", "/var/lib/intel-collect/spool")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s := &setup{spool: collect.Spool{Dir: dir}}
	perMin, _ := strconv.Atoi(env("INTEL_TABOOLA_PER_MINUTE", "40"))
	rtPerMin, _ := strconv.Atoi(env("INTEL_TABOOLA_REALTIME_PER_MINUTE", "8"))
	for _, login := range logins("TABOOLA_LOGINS") {
		id, secret := secretFor("TABOOLA", login, "CLIENT_ID"), secretFor("TABOOLA", login, "CLIENT_SECRET")
		if id == "" || secret == "" {
			return nil, fmt.Errorf("taboola login %s: client id or secret missing", login)
		}
		s.taboolas = append(s.taboolas, &collect.Taboola{
			Login: login, API: taboola.New(taboola.DefaultBase, id, secret), Spool: s.spool,
			Pace: &collect.Pacer{PerMinute: perMin, RealtimePerMinute: rtPerMin},
			Log:  log.With("taboola_login", login), Now: time.Now,
		})
	}
	for _, login := range logins("REDTRACK_LOGINS") {
		key := secretFor("REDTRACK", login, "API_KEY")
		if key == "" {
			return nil, fmt.Errorf("redtrack login %s: api key missing", login)
		}
		api := redtrack.New(key)
		api.HTTP.Transport = collect.ReadOnly{Next: http.DefaultTransport}
		s.redtrack = append(s.redtrack, &collect.RedTrack{
			Login: login, API: api, Spool: s.spool, Log: log.With("redtrack_login", login), Now: time.Now,
			Zones: s.zones,
		})
	}
	if len(s.taboolas) == 0 && len(s.redtrack) == 0 {
		return nil, errors.New("nothing to collect: set TABOOLA_LOGINS and/or REDTRACK_LOGINS")
	}
	return s, nil
}

// zones is every Taboola account's time zone, so RedTrack is asked in each.
func (s *setup) zones() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range s.taboolas {
		accs, _ := t.Known(context.Background())
		for _, a := range accs {
			if !seen[a.TimeZone] {
				seen[a.TimeZone] = true
				out = append(out, a.TimeZone)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *setup) jobs() []collect.Job {
	var jobs []collect.Job
	for i, t := range s.taboolas {
		p := "taboola_" + t.Login + "_"
		stagger := time.Duration(i) * 7 * time.Second
		jobs = append(jobs,
			collect.Job{Name: p + "accounts", Every: time.Hour, Delay: stagger, Run: t.Accounts},
			collect.Job{Name: p + "realtime", Every: 5 * time.Minute, Delay: stagger + 10*time.Second, Run: t.Realtime},
			collect.Job{Name: p + "reports", Every: time.Hour, Delay: stagger + 30*time.Second, Run: func(ctx context.Context) error { return t.Reports(ctx, 2) }},
			collect.Job{Name: p + "settings", Every: time.Hour, Delay: stagger + 2*time.Minute, Run: t.Settings},
			collect.Job{Name: p + "history", Every: time.Hour, Delay: stagger + 4*time.Minute, Run: t.History},
			collect.Job{Name: p + "month", Every: 24 * time.Hour, Delay: stagger + 20*time.Minute, Run: t.Month},
		)
	}
	for i, r := range s.redtrack {
		p := "redtrack_" + r.Login + "_"
		stagger := time.Duration(i)*11*time.Second + time.Minute
		jobs = append(jobs,
			collect.Job{Name: p + "conversions", Every: 15 * time.Minute, Delay: stagger, Run: r.Conversions},
			collect.Job{Name: p + "reports", Every: time.Hour, Delay: stagger + time.Minute, Run: func(ctx context.Context) error { return r.Reports(ctx, 2) }},
			collect.Job{Name: p + "week", Every: 24 * time.Hour, Delay: stagger + 30*time.Minute, Run: func(ctx context.Context) error { return r.Reports(ctx, 7) }},
		)
	}
	return jobs
}

func runCmd() error {
	log := logx.New("intel-collect", version)
	s, err := build(log)
	if err != nil {
		return err
	}
	srv := ops.New("intel-collect", version)
	// The database may be away; collection is not. It connects lazily and
	// the drain retries.
	var db *pgxpool.Pool
	srv.AddCheck("spool", func(ctx context.Context) error {
		files, err := s.spool.Pending()
		if err != nil {
			return err
		}
		if len(files) > 5000 {
			return fmt.Errorf("%d answers waiting in the spool", len(files))
		}
		return nil
	})
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		jobs := s.jobs()
		jobs = append(jobs, collect.Job{Name: "drain", Every: 15 * time.Second, Run: func(ctx context.Context) error {
			if db == nil {
				p, err := open(ctx)
				if err != nil {
					return err
				}
				db = p
			}
			n, err := s.spool.Drain(ctx, db)
			if n > 0 {
				log.Debug("answers stored", "count", n)
			}
			return err
		}})
		log.Info("collect starting", "taboola_logins", len(s.taboolas), "redtrack_logins", len(s.redtrack))
		collect.Schedule(ctx, log, srv.Tasks(), jobs)
		if db != nil {
			db.Close()
		}
		log.Info("collect stopped")
		return <-opsDone
	})
}

func onceCmd(job string) error {
	log := logx.New("intel-collect", version)
	s, err := build(log)
	if err != nil {
		return err
	}
	ctx := context.Background()
	var errs []error
	for _, t := range s.taboolas {
		switch job {
		case "accounts":
			errs = append(errs, t.Accounts(ctx))
		case "settings":
			errs = append(errs, t.Settings(ctx))
		case "reports":
			errs = append(errs, t.Reports(ctx, 2))
		case "month":
			errs = append(errs, t.Month(ctx))
		case "realtime":
			errs = append(errs, t.Realtime(ctx))
		case "history":
			errs = append(errs, t.History(ctx))
		}
	}
	for _, r := range s.redtrack {
		switch job {
		case "rt-reports":
			errs = append(errs, r.Reports(ctx, 2))
		case "rt-week":
			errs = append(errs, r.Reports(ctx, 7))
		case "rt-conversions":
			errs = append(errs, r.Conversions(ctx))
		}
	}
	if err := errors.Join(errs...); err != nil {
		log.Error("job failed", "job", job, "err", err)
	}
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	n, err := s.spool.Drain(ctx, db)
	fmt.Printf("%s: %d answers stored\n", job, n)
	return err
}

func open(ctx context.Context) (*pgxpool.Pool, error) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	return pg.Open(ctx, pg.Config{URL: u.String(), AppName: "intel-collect", StatementTimeout: pg.WebStatementTimeout * 6, MaxConns: 2})
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// logins reads a comma-separated list of login names ("zoltagroup,team").
func logins(k string) []string {
	var out []string
	for _, l := range strings.Split(os.Getenv(k), ",") {
		if l = strings.TrimSpace(strings.ToLower(l)); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// secretFor reads PREFIX_<LOGIN>_NAME, falling back to PREFIX_NAME for a
// single login (TABOOLA_CLIENT_ID, REDTRACK_API_KEY).
func secretFor(prefix, login, name string) string {
	if v := os.Getenv(prefix + "_" + strings.ToUpper(strings.ReplaceAll(login, "-", "_")) + "_" + name); v != "" {
		return v
	}
	if len(logins(prefix+"_LOGINS")) == 1 {
		return os.Getenv(prefix + "_" + name)
	}
	return ""
}
