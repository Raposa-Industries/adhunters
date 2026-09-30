// Command desk-agent is Desk at work: it answers the conversations that
// wait for it (on Claude) and carries out the plans people OK'd, step by
// step.
//
//	desk-agent migrate
//	desk-agent run [-turns 2]
//	desk-agent version
//
// The database URL comes from DATABASE_URL; the login owns the desk
// schemas and holds each app's <app>_api role, so Desk reads and calls what
// a teammate's page does and nothing more. ANTHROPIC_API_KEY is the Claude
// key. run takes no work while the stop switch is on, and no turn once the
// day's Claude budget is spent. It stops cleanly on SIGTERM: a turn or step
// in flight is let go and taken again later.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/internal/agent"
	"github.com/Raposa-Industries/adhunters/desk/internal/claude"
	deskrun "github.com/Raposa-Industries/adhunters/desk/internal/run"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
	"github.com/Raposa-Industries/adhunters/desk/migrations"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
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
		err = runCmd(os.Args[2:])
	case "migrate":
		err = migrateCmd()
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "desk-agent:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: desk-agent run|migrate|version [flags]")
	os.Exit(2)
}

// open connects with the session in UTC, whatever the server's default.
func open(ctx context.Context, timeout time.Duration, conns int32) (*pgxpool.Pool, error) {
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
	return pg.Open(ctx, pg.Config{URL: u.String(), AppName: "desk-agent", StatementTimeout: timeout, MaxConns: conns})
}

func migrateCmd() error {
	log := logx.New("desk-agent", version)
	ctx := context.Background()
	db, err := open(ctx, pg.JobStatementTimeout, 2)
	if err != nil {
		return err
	}
	defer db.Close()
	migs, err := migrations.Load()
	if err != nil {
		return err
	}
	n, err := migrate.Up(ctx, db, log, migrations.Schema, migs)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "count", n)
	return nil
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	turns := fs.Int("turns", 2, "conversations answered at once")
	_ = fs.Parse(args)

	log := logx.New("desk-agent", version)
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" || key == "FILL_ME" {
		return errors.New("ANTHROPIC_API_KEY is not set")
	}
	catalog, err := actions.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	// A step's call waits on the app's function; a minute is ample for any
	// of them, and a stuck one gives the connection back.
	db, err := open(ctx, time.Minute, int32(*turns)+4)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := ops.New("desk-agent", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	m := newMetrics(srv.Registry)
	s := store.New(db)
	model := metered{next: claude.New(key), srv: srv, m: m}
	a := agent.New(s, catalog, model, log)
	r := &deskrun.Runner{Store: s, Catalog: catalog, Suggest: a.Suggest, Log: log}

	log.Info("desk starting", "turns", *turns, "actions", len(catalog.Latest()), "tools", len(a.Tools()))
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		var wg sync.WaitGroup
		for i := 0; i < *turns; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				turnLoop(ctx, log, s, a, m)
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			planLoop(ctx, log, s, r, m)
		}()
		wg.Wait()
		log.Info("desk stopped")
		return <-opsDone
	})
}

// idle is how long a loop with nothing to do waits before looking again.
const idle = time.Second

func turnLoop(ctx context.Context, log *slog.Logger, s *store.Store, a *agent.Agent, m *metrics) {
	for ctx.Err() == nil {
		if s.Stopped(ctx) || overBudget(ctx, s) {
			sleep(ctx, 10*time.Second)
			continue
		}
		token := newToken()
		id, err := s.ClaimTurn(ctx, token, 5*time.Minute)
		if err != nil {
			log.Error("claim turn", "err", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		if id == 0 {
			sleep(ctx, idle)
			continue
		}
		start := time.Now()
		err = a.Turn(ctx, id, token)
		m.turns.WithLabelValues(outcome(err)).Inc()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("turn failed", "conversation", id, "took", time.Since(start).String(), "err", err)
		}
	}
}

func planLoop(ctx context.Context, log *slog.Logger, s *store.Store, r *deskrun.Runner, m *metrics) {
	for ctx.Err() == nil {
		if s.Stopped(ctx) {
			sleep(ctx, 10*time.Second)
			continue
		}
		token := newToken()
		id, err := s.ClaimPlan(ctx, token, 2*time.Minute)
		if err != nil {
			log.Error("claim plan", "err", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		if id == 0 {
			sleep(ctx, idle)
			continue
		}
		err = r.Plan(ctx, id, token)
		m.passes.WithLabelValues(outcome(err)).Inc()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("plan pass failed", "plan", id, "err", err)
		}
	}
}

func overBudget(ctx context.Context, s *store.Store) bool {
	spent, err := s.SpentToday(ctx)
	return err != nil || spent >= s.SettingFloat(ctx, "daily_usd", 20)
}

func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	return "error"
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// newToken is a claim's token: a random UUID.
func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type metrics struct {
	turns, passes, calls *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		turns:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "desk_turns_total", Help: "Conversation turns Desk took, by outcome."}, []string{"outcome"}),
		passes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "desk_plan_passes_total", Help: "Passes over OK'd plans, by outcome."}, []string{"outcome"}),
		calls:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "desk_model_calls_total", Help: "Calls to Claude, by stop reason."}, []string{"stop"}),
	}
	reg.MustRegister(m.turns, m.passes, m.calls)
	return m
}

// metered counts each call to Claude: its cost for the credit estimate, and
// a refusal for lack of credit for the OutOfCredit alert.
type metered struct {
	next claude.Caller
	srv  *ops.Server
	m    *metrics
}

func (c metered) Call(ctx context.Context, r claude.Request) (claude.Response, error) {
	resp, err := c.next.Call(ctx, r)
	if err != nil {
		if strings.Contains(err.Error(), "credit balance is too low") {
			c.srv.OutOfCredit("anthropic")
		}
		c.m.calls.WithLabelValues("error").Inc()
		return resp, err
	}
	c.srv.Spent("anthropic", claude.Cost(r.Model, resp.Usage))
	c.m.calls.WithLabelValues(resp.StopReason).Inc()
	return resp, nil
}
