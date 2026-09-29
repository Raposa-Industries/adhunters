package ops

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestOutOfCredit(t *testing.T) {
	s := New("forge-api", "test")
	s.OutOfCredit("openai")
	s.OutOfCredit("openai")
	s.OutOfCredit("fal")
	want := `
# HELP adhunters_out_of_credit_total Calls a paid service refused because the account ran out of credit or quota.
# TYPE adhunters_out_of_credit_total counter
adhunters_out_of_credit_total{provider="fal"} 1
adhunters_out_of_credit_total{provider="openai"} 2
`
	if err := testutil.GatherAndCompare(s.Registry, strings.NewReader(want), "adhunters_out_of_credit_total"); err != nil {
		t.Fatal(err)
	}
}

func TestSpent(t *testing.T) {
	s := New("forge-api", "test")
	s.Spent("openai", 0.04)
	s.Spent("openai", 0.0101)
	s.Spent("openai", -1) // ignored: a counter only goes up
	want := `
# HELP adhunters_spend_usd_total Money spent on a prepaid service, in USD, as our services work it out per call.
# TYPE adhunters_spend_usd_total counter
adhunters_spend_usd_total{provider="openai"} 0.0501
`
	if err := testutil.GatherAndCompare(s.Registry, strings.NewReader(want), "adhunters_spend_usd_total"); err != nil {
		t.Fatal(err)
	}
}
