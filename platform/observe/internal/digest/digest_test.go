package digest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/platform/observe/internal/prom"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/sentry"
)

type fakeMetrics map[string][]prom.Sample

func (f fakeMetrics) Query(_ context.Context, q string, _ time.Time) ([]prom.Sample, error) {
	s, ok := f[q]
	if !ok {
		return nil, errors.New("query failed")
	}
	return s, nil
}

type fakeErrors []sentry.Issue

func (f fakeErrors) NewSince(context.Context, time.Time) ([]sentry.Issue, error) { return f, nil }

func one(v float64) []prom.Sample { return []prom.Sample{{Value: v}} }

func quietDay() fakeMetrics {
	return fakeMetrics{
		qScrapes: one(336000), qScrapesOK: one(330000), qGaps: one(0),
		qSightings: one(7680000), qLagMax: one(95), qVisits: nil, qKept: nil,
		qAlerts: nil, qRestarts: nil, qCredits: nil,
		qDisk: {{Labels: map[string]string{"box": "worker"}, Value: 0.81}, {Labels: map[string]string{"box": "data"}, Value: 0.62}},
	}
}

var sp = time.FixedZone("BRT", -3*3600)

func TestQuietDayIsThreeLines(t *testing.T) {
	at := time.Date(2026, 9, 29, 11, 0, 0, 0, time.UTC)
	got := Write(context.Background(), quietDay(), fakeErrors(nil), at, sp)
	want := "☀️ <b>AdHunters, the last 24 hours</b> (to Tue 29 Sep 08:00)\n" +
		"All green: 336,000 scrapes, 7,680,000 sightings, no alerts, no new errors.\n" +
		"Disk free: data 62%, worker 81%"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBusyDay(t *testing.T) {
	m := quietDay()
	m[qGaps] = one(4)
	m[qVisits], m[qKept] = one(310), one(12)
	m[qAlerts] = []prom.Sample{
		{Labels: map[string]string{"alertname": "LoaderLag"}, Value: 25},
		{Labels: map[string]string{"alertname": "CaptureStopped"}, Value: 4},
	}
	m[qRestarts] = []prom.Sample{{Labels: map[string]string{"service": "tracks-loader"}, Value: 2}}
	delete(m, qLagMax) // a failed query
	issues := fakeErrors{{ShortID: "GO-7", Title: "close hour: <timeout>", Permalink: "https://s/7", Count: 3}}
	got := Write(context.Background(), m, issues, time.Date(2026, 9, 29, 11, 0, 0, 0, time.UTC), sp)
	for _, want := range []string{
		"Scrapes: 336,000, 98.2% successful",
		"Minutes without a successful scrape: 4",
		"loader at most an unknown time behind",
		"Raposa: 310 visits, 12 pages kept whole",
		"LoaderLag: firing 25m\nCaptureStopped: firing 4m",
		`<a href="https://s/7">GO-7</a> close hour: &lt;timeout&gt; (3 times)`,
		"Restarts: tracks-loader 2",
		"Some numbers could not be read",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestCreditLeft(t *testing.T) {
	m := quietDay()
	m[qCredits] = []prom.Sample{
		{Labels: map[string]string{"credit": "iproyal", "unit": "GB"}, Value: 4.237},
		{Labels: map[string]string{"credit": "openai", "unit": "USD", "how": "estimated"}, Value: 17.46},
	}
	got := Write(context.Background(), m, fakeErrors(nil), time.Date(2026, 9, 29, 11, 0, 0, 0, time.UTC), sp)
	if !strings.HasSuffix(got, "Disk free: data 62%, worker 81%\nCredit left: iproyal 4.2 GB, openai ~17.5 USD") {
		t.Fatalf("got:\n%s", got)
	}
}
