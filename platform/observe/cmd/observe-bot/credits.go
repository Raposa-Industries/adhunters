package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
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

// credits exports what is left on each service and when each renewal is due.
type credits struct {
	cfg       *credit.Config
	hc        *http.Client
	tasks     *ops.Tasks
	remaining *prometheus.GaugeVec
	warn      *prometheus.GaugeVec
	due       *prometheus.GaugeVec
	remind    *prometheus.GaugeVec
}

// creditPromise is how long a balance may go unread before TaskLate fires.
const creditPromise = 2 * time.Hour

func newCredits(reg prometheus.Registerer, tasks *ops.Tasks, cfg *credit.Config) *credits {
	c := &credits{
		cfg:   cfg,
		hc:    &http.Client{Timeout: 30 * time.Second},
		tasks: tasks,
		remaining: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_credit_remaining",
			Help: "What is left on a prepaid service, in unit, as its API last answered.",
		}, []string{"credit", "unit"}),
		warn: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_credit_warn",
			Help: "CreditLow fires when adhunters_credit_remaining falls below this (credits.conf warn).",
		}, []string{"credit", "unit"}),
		due: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_renewal_due_timestamp_seconds",
			Help: "The next day a subscription renews or must be paid (credits.conf).",
		}, []string{"renewal"}),
		remind: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_renewal_remind_seconds",
			Help: "How long before a renewal RenewalDue fires.",
		}, []string{"renewal"}),
	}
	reg.MustRegister(c.remaining, c.warn, c.due, c.remind)
	for _, ch := range cfg.Checks {
		if ch.Off == "" {
			c.warn.WithLabelValues(ch.Name, ch.Unit).Set(ch.Warn)
			tasks.Promise("credit_"+ch.Name, creditPromise)
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
}

// creditsCmd prints every balance and renewal once, to try credits.conf.
func creditsCmd(args []string) error {
	fs := flag.NewFlagSet("credits", flag.ExitOnError)
	_ = fs.Parse(args)
	cfg, err := loadCredits()
	if err != nil {
		return err
	}
	if len(cfg.Checks)+len(cfg.Renewals) == 0 {
		fmt.Printf("%s lists nothing\n", creditsFile())
		return nil
	}
	ctx := context.Background()
	hc := &http.Client{Timeout: 30 * time.Second}
	failed := false
	for _, ch := range cfg.Checks {
		if ch.Off != "" {
			fmt.Printf("credit  %-20s off (%s)\n", ch.Name, ch.Off)
			continue
		}
		v, err := ch.Read(ctx, hc)
		if err != nil {
			failed = true
			fmt.Printf("credit  %-20s failed: %v\n", ch.Name, err)
			continue
		}
		low := ""
		if v < ch.Warn {
			low = "  LOW"
		}
		fmt.Printf("credit  %-20s %g %s (warn below %g)%s\n", ch.Name, v, ch.Unit, ch.Warn, low)
	}
	now := time.Now()
	for _, rn := range cfg.Renewals {
		if rn.Off != "" {
			fmt.Printf("renewal %-20s off (%s)\n", rn.Name, rn.Off)
			continue
		}
		next := rn.Next(now)
		fmt.Printf("renewal %-20s %s (every %s, reminder %s before)\n", rn.Name, next.Format("Mon 2 Jan 2006"), rn.Every, fmtDays(rn.Remind))
	}
	if failed {
		return errors.New("some checks failed")
	}
	return nil
}

func fmtDays(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return d.String()
}
