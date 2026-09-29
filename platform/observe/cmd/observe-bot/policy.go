package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/policy"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/telegram"
)

// policyPromise is how long the policy watch may go without a good crawl
// before TaskLate fires: four missed 6-hour crawls.
const policyPromise = 25 * time.Hour

// policyWatch crawls Taboola's policy pages and posts each change.
type policyWatch struct {
	crawler *policy.Crawler
	current string // the last good snapshot
	tg      *telegram.Client
}

func newPolicyWatch(state string, tg *telegram.Client) *policyWatch {
	roots := policy.DefaultRoots
	if v := os.Getenv("POLICY_URLS"); v != "" {
		roots = strings.Fields(v)
	}
	return &policyWatch{
		crawler: &policy.Crawler{
			HTTP:  &http.Client{Timeout: 30 * time.Second},
			Roots: roots,
			Max:   300,
			Pause: 2 * time.Second,
			Dir:   filepath.Join(state, "policy", "raw"),
		},
		current: filepath.Join(state, "policy", "current.json"),
		tg:      tg,
	}
}

// once crawls, compares with the last good crawl and returns the changes. The
// new snapshot becomes the last good one only after send succeeds for every
// change, so a failed send is sent again next time. The first crawl only
// sets the baseline.
func (w *policyWatch) once(ctx context.Context, send func(string) error) (int, error) {
	old, err := policy.Load(w.current)
	if err != nil {
		return 0, err
	}
	cur, _, err := w.crawler.Crawl(ctx, time.Now())
	if err != nil {
		return 0, err
	}
	if cur.Articles() == 0 {
		return 0, errors.New("the crawl found no articles: has the help center's layout changed?")
	}
	if old == nil {
		msg := fmt.Sprintf("📜 Watching %d Taboola policy articles from now on. Changes to them will be posted here.", cur.Articles())
		if err := send(msg); err != nil {
			return 0, err
		}
		return 0, policy.Save(w.current, cur)
	}
	if n, o := cur.Articles(), old.Articles(); n < o/2 {
		// Half the policy does not vanish in a day; the crawl lost its way.
		return 0, fmt.Errorf("the crawl found %d articles against %d last time: not comparing", n, o)
	}
	changes := policy.Compare(old, cur)
	for _, c := range changes {
		if err := send(policy.Message(c)); err != nil {
			return 0, err
		}
	}
	return len(changes), policy.Save(w.current, cur)
}

// run crawls now and then every interval until ctx ends.
func (w *policyWatch) run(ctx context.Context, log *slog.Logger, tasks *ops.Tasks, every time.Duration) {
	send := func(msg string) error { return w.tg.Send(ctx, msg, true) }
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		start := time.Now()
		n, err := w.once(ctx, send)
		tasks.Done("policy_watch", start, int64(n), err)
		if err != nil && ctx.Err() == nil {
			// Warn, not error: TaskLate says it once the promise is broken.
			log.Warn("policy watch", "err", err)
		} else if n > 0 {
			log.Info("policy changes posted", "changes", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// policyCmd crawls once and prints what changed against the last good crawl
// without sending or keeping anything, or compares two saved crawls again (the reading is re-runnable over any saved crawls).
func policyCmd(args []string) error {
	fs := flag.NewFlagSet("policy", flag.ExitOnError)
	list := fs.Bool("list", false, "list the saved crawls")
	from := fs.String("from", "", "compare two saved crawls: the older one (a folder name from -list)")
	to := fs.String("to", "", "with -from: the newer one (default the newest)")
	_ = fs.Parse(args)

	state := os.Getenv("STATE_DIRECTORY")
	if state == "" {
		state = "/var/lib/observe-bot"
	}
	raw := filepath.Join(state, "policy", "raw")
	if *list {
		dirs, err := policy.Crawls(raw)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			fmt.Println(filepath.Base(d))
		}
		return nil
	}
	if *from != "" {
		if *to == "" {
			dirs, err := policy.Crawls(raw)
			if err != nil || len(dirs) == 0 {
				return fmt.Errorf("no saved crawls in %s", raw)
			}
			*to = filepath.Base(dirs[len(dirs)-1])
		}
		a, err := policy.Reparse(filepath.Join(raw, *from))
		if err != nil {
			return err
		}
		b, err := policy.Reparse(filepath.Join(raw, *to))
		if err != nil {
			return err
		}
		changes := policy.Compare(a, b)
		for _, c := range changes {
			fmt.Println(policy.Message(c))
			fmt.Println()
		}
		fmt.Printf("%d changes from %s to %s\n", len(changes), *from, *to)
		return nil
	}

	// A dry run: crawl and compare, but keep the last good crawl as it is.
	// Its raw pages go to a temporary folder, so running it as root leaves
	// nothing in the service's state that the service cannot write over.
	w := newPolicyWatch(state, nil)
	tmp, err := os.MkdirTemp("", "observe-bot-policy-")
	if err != nil {
		return err
	}
	w.crawler.Dir = tmp
	ctx := context.Background()
	old, err := policy.Load(w.current)
	if err != nil {
		return err
	}
	cur, dir, err := w.crawler.Crawl(ctx, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("read %d pages, %d articles, saved in %s\n", len(cur.Pages), cur.Articles(), dir)
	if old == nil {
		fmt.Println("no last good crawl yet: observe-bot run sets it on its first crawl")
		return nil
	}
	changes := policy.Compare(old, cur)
	for _, c := range changes {
		fmt.Println(policy.Message(c))
		fmt.Println()
	}
	fmt.Printf("%d changes since %s\n", len(changes), old.Taken.Format(time.RFC3339))
	return nil
}
