// Command intel-collect asks Taboola and RedTrack for Intel's numbers on a
// schedule and keeps every answer as received: first in its spool on local
// disk, then in intel.answer. It parses nothing (intel-numbers does), so it
// rarely changes and a deploy of the rest never pauses collection.
//
//	intel-collect run          collect until SIGTERM
//	intel-collect once JOB     run one job now (accounts, settings, status, reports,
//	                           month, realtime, history, rt-reports,
//	                           rt-week, rt-conversions), then drain the spool
//	intel-collect version
//
// It only reads: the Taboola client refuses anything but GETs (and its token
// request), and the RedTrack transport refuses anything but GETs. Settings
// come from the environment (see intel/deploy/intel-collect.env.example).
// With LAUNCH_LOGIN_KEY_BASE64 it also reads the Taboola logins added on
// Launch's Contas page, each through its own proxy (contas.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	"github.com/Raposa-Industries/adhunters/shared/taboola/logins"
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
	taboolas []*collect.Taboola // the env's logins, then the Contas ones
	own      []*collect.Taboola // the env's logins (TABOOLA_LOGINS)
	redtrack []*collect.RedTrack

	envLogins        []envLogin
	proxied          map[string]bool // accounts with a proxy on Contas: never read by the env's logins
	kept             string          // where what Launch published is kept between starts
	box              *logins.Box     // nil: LAUNCH_LOGIN_KEY_BASE64 unset, Contas logins not read
	perMin, rtPerMin int
}

// envLogin is one Taboola login of intel-collect.env.
type envLogin struct{ name, id, secret string }

func build(log *slog.Logger) (*setup, error) {
	dir := env("INTEL_SPOOL", "/var/lib/intel-collect/spool")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s := &setup{spool: collect.Spool{Dir: dir}, kept: env("INTEL_LAUNCH_KEPT", filepath.Join(filepath.Dir(dir), "launch.json"))}
	perMin, _ := strconv.Atoi(env("INTEL_TABOOLA_PER_MINUTE", "40"))
	rtPerMin, _ := strconv.Atoi(env("INTEL_TABOOLA_REALTIME_PER_MINUTE", "8"))
	s.perMin, s.rtPerMin = perMin, rtPerMin
	if key := strings.TrimSpace(os.Getenv("LAUNCH_LOGIN_KEY_BASE64")); key != "" {
		box, _, err := logins.KeyFrom(key, "")
		if err != nil {
			return nil, fmt.Errorf("LAUNCH_LOGIN_KEY_BASE64: %w", err)
		}
		s.box = box
	}
	for _, login := range loginNames("TABOOLA_LOGINS") {
		id, secret := secretFor("TABOOLA", login, "CLIENT_ID"), secretFor("TABOOLA", login, "CLIENT_SECRET")
		if id == "" || secret == "" {
			return nil, fmt.Errorf("taboola login %s: client id or secret missing", login)
		}
		s.envLogins = append(s.envLogins, envLogin{login, id, secret})
		s.taboolas = append(s.taboolas, &collect.Taboola{
			Login: login, API: taboola.New(taboola.DefaultBase, id, secret), Spool: s.spool,
			Pace: &collect.Pacer{PerMinute: perMin, RealtimePerMinute: rtPerMin},
			Log:  log.With("taboola_login", login), Now: time.Now, Skip: s.hasProxy,
		})
	}
	s.own = append([]*collect.Taboola(nil), s.taboolas...)
	for _, login := range loginNames("REDTRACK_LOGINS") {
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
			collect.Job{Name: p + "status", Every: 5 * time.Minute, Delay: stagger + 20*time.Second, Run: t.Statuses},
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
	var db lazyDB
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
		jobCtx, stop := context.WithCancel(ctx)
		defer stop()
		changed := false
		// What Launch published is read at start, so the proxied accounts'
		// and the Contas logins' jobs are in the schedule. A deploy starts
		// intel-collect before launch-web has migrated a view it reads, so it
		// tries for up to launchWait before going on; only then the copy kept
		// from the last read stands in, and the launch job reads it again.
		// Without the wait, the view appearing a minute later would restart
		// intel-collect while the deploy checks it, which rolls the box back.
		fp := "unread"
		r, err := s.readLaunchWaiting(ctx, &db, log)
		if err != nil {
			log.Warn("what Launch publishes not read yet; using the copy kept from the last read", "err", err)
			if k, kerr := readKept(s.kept); kerr == nil {
				r, fp = k, fingerprint(k)
			}
		} else {
			fp = fingerprint(r)
		}
		s.useLaunch(r, taboola.DefaultBase, log)
		if s.box == nil {
			log.Info("LAUNCH_LOGIN_KEY_BASE64 is not set: the logins added on Launch's Contas page are not read")
		}
		extra := []collect.Job{{Name: "launch", Every: 5 * time.Minute, Delay: time.Minute, Run: func(ctx context.Context) error {
			r, err := s.readLaunchAt(ctx, &db)
			if err != nil {
				return err
			}
			if fingerprint(r) != fp {
				log.Info("the Contas logins or account proxies changed: starting again", "logins", len(r.Logins), "proxies", len(r.Proxies))
				changed = true
				stop()
			}
			return nil
		}}}
		jobs := append(s.jobs(), extra...)
		jobs = append(jobs, collect.Job{Name: "drain", Every: 15 * time.Second, Run: func(ctx context.Context) error {
			pool, err := db.get(ctx)
			if err != nil {
				return err
			}
			n, err := s.spool.Drain(ctx, pool)
			if n > 0 {
				log.Debug("answers stored", "count", n)
			}
			return err
		}})
		log.Info("collect starting", "taboola_logins", len(s.taboolas), "redtrack_logins", len(s.redtrack))
		collect.Schedule(jobCtx, log, srv.Tasks(), jobs)
		db.close()
		log.Info("collect stopped")
		if changed && ctx.Err() == nil {
			// Ending cleanly lets systemd (Restart=always) start collection
			// again at once with the new logins; the ops server goes with
			// the process.
			return nil
		}
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
	var db lazyDB
	defer db.close()
	r, err := s.readLaunchAt(ctx, &db)
	if err != nil {
		if r, err = readKept(s.kept); err != nil {
			return fmt.Errorf("what Launch publishes is not readable, so the accounts that only go through a proxy are unknown: %w", err)
		}
	}
	s.useLaunch(r, taboola.DefaultBase, log)
	var errs []error
	for _, t := range s.taboolas {
		switch job {
		case "accounts":
			errs = append(errs, t.Accounts(ctx))
		case "settings":
			errs = append(errs, t.Settings(ctx))
		case "status":
			errs = append(errs, t.Statuses(ctx))
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
	pool, err := db.get(ctx)
	if err != nil {
		return err
	}
	n, err := s.spool.Drain(ctx, pool)
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

// loginNames reads a comma-separated list of login names ("zoltagroup,team").
func loginNames(k string) []string {
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
	if len(loginNames(prefix+"_LOGINS")) == 1 {
		return os.Getenv(prefix + "_" + name)
	}
	return ""
}

// readLaunchAt reads what Launch publishes and keeps a copy for the next
// start.
func (s *setup) readLaunchAt(ctx context.Context, db *lazyDB) (launchRows, error) {
	pool, err := db.get(ctx)
	if err != nil {
		return launchRows{}, err
	}
	r, err := readLaunch(ctx, pool, s.box != nil)
	if err != nil {
		return r, err
	}
	if err := keep(s.kept, r); err != nil {
		return r, fmt.Errorf("keep %s: %w", s.kept, err)
	}
	return r, nil
}

// launchWait is how long a start waits for Launch's views (see runCmd).
var launchWait = 2 * time.Minute

// readLaunchWaiting reads what Launch publishes, trying again every 5
// seconds for up to launchWait.
func (s *setup) readLaunchWaiting(ctx context.Context, db *lazyDB, log *slog.Logger) (launchRows, error) {
	until := time.Now().Add(launchWait)
	for {
		try, cancel := context.WithTimeout(ctx, 15*time.Second)
		r, err := s.readLaunchAt(try, db)
		cancel()
		if err == nil || time.Now().After(until) || ctx.Err() != nil {
			return r, err
		}
		log.Info("waiting for what Launch publishes", "err", err)
		select {
		case <-ctx.Done():
			return r, err
		case <-time.After(5 * time.Second):
		}
	}
}

// lazyDB connects on first use and keeps the pool; the drain and the launch
// job share it.
type lazyDB struct {
	mu   sync.Mutex
	pool *pgxpool.Pool
}

func (d *lazyDB) get(ctx context.Context) (*pgxpool.Pool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pool == nil {
		p, err := open(ctx)
		if err != nil {
			return nil, err
		}
		d.pool = p
	}
	return d.pool, nil
}

func (d *lazyDB) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pool != nil {
		d.pool.Close()
	}
}
