package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/credit"
)

// TestCreditsExport reads one balance that answers, one that fails and one
// that is off, plus a monthly renewal (next: 25 Oct), and checks what
// /metrics then shows.
func TestCreditsExport(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"available_traffic": 1.5}`))
	}))
	defer api.Close()

	conf := `
[credit iproyal]
url = ` + api.URL + `/me
field = available_traffic
unit = GB
warn = 2

[credit fal]
url = ` + api.URL + `/broken
field = credits.current_balance
unit = USD
warn = 20

[credit together]
url = ` + api.URL + `/x
header = Authorization: Bearer ${TOGETHER_KEY}
field = balance
unit = USD
warn = 10

[renewal proxies]
due = 2026-01-25
every = month
remind = 5d
`
	cfg, err := credit.Parse(strings.NewReader(conf), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	srv := ops.New("observe-bot", "test")
	c := newCredits(srv.Registry, srv.Tasks(), cfg, &fakeSpend{}, t.TempDir())
	c.once(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), time.Date(2026, 9, 29, 13, 0, 0, 0, time.UTC))

	want := `
# HELP adhunters_credit_remaining What is left on a prepaid service, in unit, as its API last answered.
# TYPE adhunters_credit_remaining gauge
adhunters_credit_remaining{credit="iproyal",unit="GB"} 1.5
# HELP adhunters_credit_warn CreditLow fires when adhunters_credit_remaining falls below this (credits.conf warn).
# TYPE adhunters_credit_warn gauge
adhunters_credit_warn{credit="fal",unit="USD"} 20
adhunters_credit_warn{credit="iproyal",unit="GB"} 2
# HELP adhunters_renewal_due_timestamp_seconds The next day a subscription renews or must be paid (credits.conf).
# TYPE adhunters_renewal_due_timestamp_seconds gauge
adhunters_renewal_due_timestamp_seconds{renewal="proxies"} 1.7928864e+09
# HELP adhunters_renewal_remind_seconds How long before a renewal RenewalDue fires.
# TYPE adhunters_renewal_remind_seconds gauge
adhunters_renewal_remind_seconds{renewal="proxies"} 432000
# HELP adhunters_task_runs_total Task runs by result (ok, error).
# TYPE adhunters_task_runs_total counter
adhunters_task_runs_total{result="error",task="credit_fal"} 1
adhunters_task_runs_total{result="error",task="credit_iproyal"} 0
adhunters_task_runs_total{result="ok",task="credit_fal"} 0
adhunters_task_runs_total{result="ok",task="credit_iproyal"} 1
`
	if err := testutil.GatherAndCompare(srv.Registry, strings.NewReader(want),
		"adhunters_credit_remaining", "adhunters_credit_warn", "adhunters_renewal_due_timestamp_seconds",
		"adhunters_renewal_remind_seconds", "adhunters_task_runs_total"); err != nil {
		t.Fatal(err)
	}
}

// fakeSpend answers every query with the next value, and remembers the
// queries.
type fakeSpend struct {
	values []float64
	fail   bool
	asked  []string
}

func (f *fakeSpend) One(_ context.Context, q string, _ time.Time) (float64, bool, error) {
	f.asked = append(f.asked, q)
	if f.fail {
		return 0, false, errors.New("grafana is down")
	}
	if len(f.values) == 0 {
		return 0, false, nil
	}
	v := f.values[0]
	f.values = f.values[1:]
	return v, true, nil
}

// TestEstimate counts spending round by round from the balance entered,
// survives a restart, holds its place through a failed query, and starts
// over when the owner puts in a new balance.
func TestEstimate(t *testing.T) {
	state := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	conf := func(balance, asOf string) *credit.Config {
		cfg, err := credit.Parse(strings.NewReader("[estimate openai]\nbalance = "+balance+"\nas_of = "+asOf+"\nwarn = 5\n"), os.Getenv)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	remaining := func(srv *ops.Server) string {
		t.Helper()
		mfs, err := srv.Registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() == "adhunters_credit_remaining" {
				return fmt.Sprintf("%.2f", mf.GetMetric()[0].GetGauge().GetValue())
			}
		}
		return "none"
	}
	start := time.Date(2026, 9, 29, 14, 0, 0, 0, time.UTC)

	spend := &fakeSpend{values: []float64{1.5, 0.25}}
	srv := ops.New("observe-bot", "test")
	c := newCredits(srv.Registry, srv.Tasks(), conf("19", "2026-09-29 13:35"), spend, state)
	c.once(context.Background(), log, start)
	if got := remaining(srv); got != "17.50" {
		t.Fatalf("after the first round: %s, want 17.50", got)
	}
	want := `sum((adhunters_spend_usd_total{provider="openai"} unless adhunters_spend_usd_total{provider="openai"} offset 1380s) or increase(adhunters_spend_usd_total{provider="openai"}[1380s]))`
	if spend.asked[0] != want {
		t.Fatalf("query\n%s\nwant\n%s", spend.asked[0], want)
	}

	// Restarted: the ledger carries on from where it was counted.
	srv = ops.New("observe-bot", "test")
	c = newCredits(srv.Registry, srv.Tasks(), conf("19", "2026-09-29 13:35"), spend, state)
	c.once(context.Background(), log, start.Add(15*time.Minute))
	if got := remaining(srv); got != "17.25" {
		t.Fatalf("after a restart: %s, want 17.25", got)
	}
	if !strings.Contains(spend.asked[1], "offset 900s") {
		t.Fatalf("the second round should count only its 15 minutes: %s", spend.asked[1])
	}

	// Grafana fails: nothing is counted, and the next round covers both.
	spend.fail = true
	c.once(context.Background(), log, start.Add(30*time.Minute))
	if got := remaining(srv); got != "17.25" {
		t.Fatalf("after a failed query: %s, want 17.25", got)
	}
	spend.fail = false
	c.once(context.Background(), log, start.Add(45*time.Minute))
	if !strings.Contains(spend.asked[3], "offset 1800s") {
		t.Fatalf("after a failure the next round should cover 30 minutes: %s", spend.asked[3])
	}

	// The owner reads 12 USD off the billing page: the count starts again.
	srv = ops.New("observe-bot", "test")
	c = newCredits(srv.Registry, srv.Tasks(), conf("12", "2026-09-29 15:00"), &fakeSpend{}, state)
	c.once(context.Background(), log, start.Add(time.Hour))
	if got := remaining(srv); got != "12.00" {
		t.Fatalf("after a new balance: %s, want 12.00", got)
	}
}
