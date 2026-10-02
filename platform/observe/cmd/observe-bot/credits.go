package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/credit"
)

// creditsFile is where setup.sh puts credits.conf.
func creditsFile() string {
	if p := os.Getenv("CREDITS_FILE"); p != "" {
		return p
	}
	return "/etc/adhunters/credits.conf"
}

// loadCredits reads the credits file; a missing file is no checks, not an
// error.
func loadCredits() (*credit.Config, error) {
	cfg, err := credit.Load(creditsFile(), os.Getenv)
	if errors.Is(err, fs.ErrNotExist) {
		return &credit.Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", creditsFile(), err)
	}
	return cfg, nil
}

// spending answers PromQL at a moment (prom.Client): what our services
// report spending, for the estimates.
type spending interface {
	One(ctx context.Context, q string, t time.Time) (float64, bool, error)
}

// credits exports what is left on each service and when each renewal is due.
type credits struct {
	cfg       *credit.Config
	hc        *http.Client
	tasks     *ops.Tasks
	spend     spending
	state     string // where the estimates' ledgers live
	remaining *prometheus.GaugeVec
	warn      *prometheus.GaugeVec
	estimated *prometheus.GaugeVec
	due       *prometheus.GaugeVec
	remind    *prometheus.GaugeVec
}

// creditPromise is how long a balance may go unread before TaskLate fires.
const creditPromise = 2 * time.Hour

// spendLag keeps an estimate from counting up to the last minute or two,
// which Grafana Cloud may not have received yet.
const spendLag = 2 * time.Minute

func newCredits(reg prometheus.Registerer, tasks *ops.Tasks, cfg *credit.Config, spend spending, state string) *credits {
	c := &credits{
		cfg:   cfg,
		hc:    &http.Client{Timeout: 30 * time.Second, Transport: ops.Transport("credits", nil)},
		tasks: tasks,
		spend: spend,
		state: state,
		remaining: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_credit_remaining",
			Help: "What is left on a prepaid service, in unit, as its API last answered.",
		}, []string{"credit", "unit"}),
		warn: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_credit_warn",
			Help: "CreditLow fires when adhunters_credit_remaining falls below this (credits.conf warn).",
		}, []string{"credit", "unit"}),
		estimated: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_credit_estimated",
			Help: "1 when adhunters_credit_remaining is worked out from our own spending (credits.conf [estimate]) rather than read from an API.",
		}, []string{"credit"}),
		due: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_renewal_due_timestamp_seconds",
			Help: "The next day a subscription renews or must be paid (credits.conf).",
		}, []string{"renewal"}),
		remind: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_renewal_remind_seconds",
			Help: "How long before a renewal RenewalDue fires.",
		}, []string{"renewal"}),
	}
	reg.MustRegister(c.remaining, c.warn, c.estimated, c.due, c.remind)
	for _, ch := range cfg.Checks {
		if ch.Off == "" {
			c.warn.WithLabelValues(ch.Name, ch.Unit).Set(ch.Warn)
			tasks.Promise("credit_"+ch.Name, creditPromise)
		}
	}
	for _, es := range cfg.Estimates {
		if es.Off == "" {
			c.warn.WithLabelValues(es.Name, "USD").Set(es.Warn)
			c.estimated.WithLabelValues(es.Name).Set(1)
			tasks.Promise("credit_"+es.Name, creditPromise)
		}
	}
	return c
}

// run reads every balance now and then every interval until ctx ends.
func (c *credits) run(ctx context.Context, log *slog.Logger, every time.Duration) {
	for _, ch := range c.cfg.Checks {
		if ch.Off != "" {
			log.Info("credit check off", "credit", ch.Name, "why", ch.Off)
		}
	}
	for _, es := range c.cfg.Estimates {
		if es.Off != "" {
			log.Info("credit estimate off", "credit", es.Name, "why", es.Off)
		}
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		c.once(ctx, log, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (c *credits) once(ctx context.Context, log *slog.Logger, now time.Time) {
	for _, rn := range c.cfg.Renewals {
		if rn.Off != "" {
			continue
		}
		c.due.WithLabelValues(rn.Name).Set(float64(rn.Next(now).Unix()))
		c.remind.WithLabelValues(rn.Name).Set(rn.Remind.Seconds())
	}
	for _, ch := range c.cfg.Checks {
		if ch.Off != "" {
			continue
		}
		start := time.Now()
		v, err := ch.Read(ctx, c.hc)
		if err == nil {
			c.remaining.WithLabelValues(ch.Name, ch.Unit).Set(v)
		} else if ctx.Err() == nil {
			// Warn, not error: TaskLate says it once the promise is broken,
			// rather than a Sentry event every 15 minutes.
			log.Warn("read credit", "credit", ch.Name, "err", err)
		}
		c.tasks.Done("credit_"+ch.Name, start, 1, err)
	}
	for _, es := range c.cfg.Estimates {
		if es.Off != "" {
			continue
		}
		start := time.Now()
		l, err := c.estimate(ctx, es, now)
		c.remaining.WithLabelValues(es.Name, "USD").Set(l.Remaining())
		if err != nil && ctx.Err() == nil {
			log.Warn("estimate credit", "credit", es.Name, "err", err)
		}
		c.tasks.Done("credit_"+es.Name, start, 1, err)
	}
}

// estimate adds what was spent since the ledger was last counted and saves
// it. On an error the ledger stays where it was, so the next round counts
// the same stretch rather than skipping it.
func (c *credits) estimate(ctx context.Context, es credit.Estimate, now time.Time) (credit.Ledger, error) {
	path := credit.LedgerPath(c.state, es.Name)
	old, err := credit.ReadLedger(path)
	if err != nil {
		return credit.Ledger{Balance: es.Balance}, fmt.Errorf("read %s: %w", path, err)
	}
	l := old.Start(es)
	at := now.Add(-spendLag).Truncate(time.Second)
	if at.After(l.Through) {
		spent, _, err := c.spend.One(ctx, spentQuery(es.Provider, at.Sub(l.Through)), at)
		if err != nil {
			return l, err
		}
		l.Spent += spent
		l.Through = at
	}
	if l != old {
		if err := credit.WriteLedger(path, l); err != nil {
			return l, err
		}
	}
	return l, nil
}

// spentQuery is what provider's spending counters grew by in the window w
// before the query's moment. A counter that first appeared inside the window
// counts whole, since it started from nothing, while increase() alone would
// miss everything up to its first sample.
func spentQuery(provider string, w time.Duration) string {
	sel := fmt.Sprintf(`adhunters_spend_usd_total{provider=%q}`, provider)
	win := fmt.Sprintf("%ds", int64(math.Ceil(w.Seconds())))
	return fmt.Sprintf(`sum((%s unless %s offset %s) or increase(%s[%s]))`, sel, sel, win, sel, win)
}

// creditsCmd prints every balance, estimate and renewal once, to try
// credits.conf. It reads the estimates' ledgers but never writes them: the
// running observe-bot owns them.
func creditsCmd(args []string) error {
	fs := flag.NewFlagSet("credits", flag.ExitOnError)
	_ = fs.Parse(args)
	cfg, err := loadCredits()
	if err != nil {
		return err
	}
	if len(cfg.Checks)+len(cfg.Estimates)+len(cfg.Renewals) == 0 {
		fmt.Printf("%s lists nothing\n", creditsFile())
		return nil
	}
	ctx := context.Background()
	hc := &http.Client{Timeout: 30 * time.Second}
	failed := false
	for _, ch := range cfg.Checks {
		if ch.Off != "" {
			fmt.Printf("credit   %-20s off (%s)\n", ch.Name, ch.Off)
			continue
		}
		v, err := ch.Read(ctx, hc)
		if err != nil {
			failed = true
			fmt.Printf("credit   %-20s failed: %v\n", ch.Name, err)
			continue
		}
		fmt.Printf("credit   %-20s %g %s (warn below %g)%s\n", ch.Name, v, ch.Unit, ch.Warn, low(v, ch.Warn))
	}
	state := os.Getenv("STATE_DIRECTORY")
	if state == "" {
		state = "/var/lib/observe-bot"
	}
	for _, es := range cfg.Estimates {
		if es.Off != "" {
			fmt.Printf("estimate %-20s off (%s)\n", es.Name, es.Off)
			continue
		}
		old, err := credit.ReadLedger(credit.LedgerPath(state, es.Name))
		if err != nil {
			failed = true
			fmt.Printf("estimate %-20s failed: %v\n", es.Name, err)
			continue
		}
		l := old.Start(es)
		v := l.Remaining()
		fmt.Printf("estimate %-20s ~%.2f USD (%g USD at %s, less %.2f spent up to %s; warn below %g)%s\n",
			es.Name, v, es.Balance, es.AsOf.Format("2006-01-02 15:04 UTC"), l.Spent, l.Through.Format("2006-01-02 15:04 UTC"), es.Warn, low(v, es.Warn))
	}
	now := time.Now()
	for _, rn := range cfg.Renewals {
		if rn.Off != "" {
			fmt.Printf("renewal  %-20s off (%s)\n", rn.Name, rn.Off)
			continue
		}
		next := rn.Next(now)
		fmt.Printf("renewal  %-20s %s (every %s, reminder %s before)\n", rn.Name, next.Format("Mon 2 Jan 2006"), rn.Every, fmtDays(rn.Remind))
	}
	if failed {
		return errors.New("some checks failed")
	}
	return nil
}

func low(v, warn float64) string {
	if v < warn {
		return "  LOW"
	}
	return ""
}

func fmtDays(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return d.String()
}
