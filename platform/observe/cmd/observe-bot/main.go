// Command observe-bot posts the platform's own messages to the Telegram group
// "AdHunters alerts" that Alertmanager cannot: the 08:00 digest, and each new
// Sentry issue as it first appears. It also exports what is left on each
// prepaid service and when subscriptions renew, for the credit alerts, and
// posts each change to Taboola's advertiser policy pages.
//
//	observe-bot run                 the digest at DIGEST_AT (default 08:00 São Paulo), the Sentry relay
//	                                the credit checks and the policy watch
//	observe-bot digest [-send]      write the digest for the last 24 hours now; print it, or send it
//	observe-bot credits             read every balance and renewal in credits.conf once and print them
//	observe-bot policy              crawl the policy pages now and print the changes, sending nothing
//	observe-bot policy -list | -from CRAWL [-to CRAWL]
//	                                list the saved crawls, or compare two of them again
//	observe-bot sentry-test         send one test error to Sentry, to try SENTRY_DSN
//	observe-bot version
//
// Settings come from the environment (/etc/adhunters/observe-bot.env):
// TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID; GRAFANA_QUERY_URL (the stack's
// Prometheus URL followed by /api/prom), GRAFANA_QUERY_USER and
// GRAFANA_QUERY_TOKEN (metrics:read); SENTRY_URL (default https://sentry.io),
// SENTRY_ORG, SENTRY_PROJECT and SENTRY_API_TOKEN (read-only; empty leaves
// the relay off). It keeps the relay's place in STATE_DIRECTORY, so a restart
// neither repeats nor skips an issue. The credit checks, estimates and
// renewals come from CREDITS_FILE (default /etc/adhunters/credits.conf), with
// their keys in the same environment; each estimate's ledger is kept in
// STATE_DIRECTORY too. The policy watch reads POLICY_URLS (help center
// collections or articles, space separated; default the Policy & Content
// Review collection and the AI-content and branding text articles) and keeps every crawl raw in STATE_DIRECTORY/policy. It stops
// cleanly on SIGTERM.
package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // São Paulo's zone without relying on the box's tzdata

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/digest"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/prom"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/sentry"
	"github.com/Raposa-Industries/adhunters/shared/telegram"
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
	case "digest":
		err = digestCmd(os.Args[2:])
	case "credits":
		err = creditsCmd(os.Args[2:])
	case "policy":
		err = policyCmd(os.Args[2:])
	case "sentry-test":
		err = sentryTestCmd()
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "observe-bot:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: observe-bot run|digest|credits|policy|sentry-test|version [flags]")
	os.Exit(2)
}

type config struct {
	tg     *telegram.Client
	metric *prom.Client
	errs   *sentry.Client // nil: relay off
	loc    *time.Location
	at     time.Duration // time of day of the digest
	state  string
}

func load() (*config, error) {
	var missing []string
	get := func(k string) string {
		v := os.Getenv(k)
		if v == "" || v == "FILL_ME" {
			missing = append(missing, k)
		}
		return v
	}
	c := &config{
		tg: telegram.New(get("TELEGRAM_BOT_TOKEN"), get("TELEGRAM_CHAT_ID")),
		metric: &prom.Client{
			URL:   strings.TrimRight(get("GRAFANA_QUERY_URL"), "/"),
			User:  get("GRAFANA_QUERY_USER"),
			Token: get("GRAFANA_QUERY_TOKEN"),
		},
		state: os.Getenv("STATE_DIRECTORY"),
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	if tok := os.Getenv("SENTRY_API_TOKEN"); tok != "" && tok != "FILL_ME" {
		u := os.Getenv("SENTRY_URL")
		if u == "" {
			u = "https://sentry.io"
		}
		c.errs = &sentry.Client{URL: strings.TrimRight(u, "/"), Org: os.Getenv("SENTRY_ORG"), Project: os.Getenv("SENTRY_PROJECT"), Token: tok}
	}
	zone := os.Getenv("DIGEST_ZONE")
	if zone == "" {
		zone = "America/Sao_Paulo"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("DIGEST_ZONE: %w", err)
	}
	c.loc = loc
	at := os.Getenv("DIGEST_AT")
	if at == "" {
		at = "08:00"
	}
	hm, err := time.Parse("15:04", at)
	if err != nil {
		return nil, fmt.Errorf("DIGEST_AT: %w", err)
	}
	c.at = time.Duration(hm.Hour())*time.Hour + time.Duration(hm.Minute())*time.Minute
	if c.state == "" {
		c.state = "/var/lib/observe-bot"
	}
	return c, nil
}

// errsOrNil keeps a nil *sentry.Client from becoming a non-nil interface.
func (c *config) errsOrNil() digest.Errors {
	if c.errs == nil {
		return nil
	}
	return c.errs
}

func digestCmd(args []string) error {
	fs := flag.NewFlagSet("digest", flag.ExitOnError)
	send := fs.Bool("send", false, "send it to Telegram instead of printing it")
	_ = fs.Parse(args)
	c, err := load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	msg := digest.Write(ctx, c.metric, c.errsOrNil(), time.Now(), c.loc)
	if !*send {
		fmt.Println(msg)
		return nil
	}
	return c.tg.Send(ctx, msg, false)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	every := fs.Duration("poll", time.Minute, "how often to ask Sentry for new issues")
	creditEvery := fs.Duration("credit-poll", 15*time.Minute, "how often to read the balances")
	policyEvery := fs.Duration("policy-poll", 6*time.Hour, "how often to crawl Taboola's policy pages; 0 turns the watch off")
	_ = fs.Parse(args)

	log := logx.New("observe-bot", version)
	c, err := load()
	if err != nil {
		return err
	}
	cfg, err := loadCredits()
	if err != nil {
		return err
	}
	srv := ops.New("observe-bot", version)
	tasks := srv.Tasks()
	tasks.Promise("digest", 25*time.Hour)
	if c.errs != nil {
		tasks.Promise("sentry_relay", 10*time.Minute)
	} else {
		log.Warn("SENTRY_API_TOKEN is not set: new Sentry issues are not relayed")
	}

	cr := newCredits(srv.Registry, tasks, cfg, c.metric, c.state)
	pw := newPolicyWatch(c.state, c.tg)
	if *policyEvery > 0 {
		tasks.Promise("policy_watch", policyPromise)
	}

	log.Info("observe-bot starting", "digest_at", fmtClock(c.at), "zone", c.loc.String(),
		"credit_checks", len(cfg.Checks), "estimates", len(cfg.Estimates), "renewals", len(cfg.Renewals))
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		relayDone := make(chan struct{})
		go func() {
			defer close(relayDone)
			if c.errs != nil {
				relay(ctx, log, c, tasks, *every)
			}
		}()
		creditsDone := make(chan struct{})
		go func() {
			defer close(creditsDone)
			cr.run(ctx, log, *creditEvery)
		}()
		policyDone := make(chan struct{})
		go func() {
			defer close(policyDone)
			if *policyEvery > 0 {
				pw.run(ctx, log, tasks, *policyEvery)
			}
		}()
		digests(ctx, log, c, tasks)
		<-relayDone
		<-creditsDone
		<-policyDone
		return <-opsDone
	})
}

// digests sends one digest a day at c.at in c.loc until ctx ends.
func digests(ctx context.Context, log *slog.Logger, c *config, tasks *ops.Tasks) {
	for {
		next := nextAt(time.Now(), c.at, c.loc)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		start := time.Now()
		msg := digest.Write(ctx, c.metric, c.errsOrNil(), start, c.loc)
		err := c.tg.Send(ctx, msg, false)
		tasks.Done("digest", start, 1, err)
		if err != nil {
			log.Error("send digest", "err", err)
			continue
		}
		log.Info("digest sent")
	}
}

// nextAt is the next moment after now that is the time of day at in loc.
func nextAt(now time.Time, at time.Duration, loc *time.Location) time.Time {
	l := now.In(loc)
	day := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	t := day.Add(at)
	if !t.After(now) {
		t = time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc).Add(at)
	}
	return t
}

// relay posts each new Sentry issue once, as a silent chat message.
func relay(ctx context.Context, log *slog.Logger, c *config, tasks *ops.Tasks, every time.Duration) {
	cursorFile := filepath.Join(c.state, "sentry-cursor")
	cursor := readCursor(cursorFile)
	if cursor.IsZero() {
		// First start: announce only what is new from now on.
		cursor = time.Now().UTC()
		if err := writeCursor(cursorFile, cursor); err != nil {
			log.Error("save the Sentry cursor", "err", err)
		}
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		start := time.Now()
		n, next, err := relayOnce(ctx, c, cursor)
		if next.After(cursor) {
			cursor = next
			if werr := writeCursor(cursorFile, cursor); werr != nil && err == nil {
				err = fmt.Errorf("save the Sentry cursor: %w", werr)
			}
		}
		tasks.Done("sentry_relay", start, int64(n), err)
		if err != nil && ctx.Err() == nil {
			log.Error("relay Sentry issues", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// relayOnce sends the issues first seen after cursor, oldest first, and
// returns how many went out and the new cursor. It stops at the first failed
// send, so that issue is tried again next time.
func relayOnce(ctx context.Context, c *config, cursor time.Time) (int, time.Time, error) {
	issues, err := c.errs.NewSince(ctx, cursor)
	if err != nil {
		return 0, cursor, err
	}
	sent := 0
	for _, is := range issues {
		msg := fmt.Sprintf("🟠 <b>New error</b> <a href=\"%s\">%s</a>\n%s",
			html.EscapeString(is.Permalink), html.EscapeString(is.ShortID), html.EscapeString(is.Title))
		if is.Culprit != "" {
			msg += "\n<i>" + html.EscapeString(is.Culprit) + "</i>"
		}
		if err := c.tg.Send(ctx, msg, true); err != nil {
			return sent, cursor, err
		}
		sent++
		cursor = is.FirstSeen
	}
	return sent, cursor, nil
}

func readCursor(path string) time.Time {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}
	}
	return t
}

func writeCursor(path string, t time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(t.UTC().Format(time.RFC3339Nano)+"\n"), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fmtClock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}
