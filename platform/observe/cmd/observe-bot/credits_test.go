package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	c := newCredits(srv.Registry, srv.Tasks(), cfg)
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
