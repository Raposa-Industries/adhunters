package credit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sample = `
# comment
[credit iproyal]
url    = https://resi-api.iproyal.com/v1/me
header = Authorization: Bearer ${IPROYAL_API_TOKEN}
field  = available_traffic
unit   = GB
warn   = 2

[credit fal]
url    = https://api.fal.ai/v1/account/billing?expand=credits
header = Authorization: Key ${FAL_ADMIN_KEY}
field  = credits.current_balance
unit   = USD
warn   = 20

[renewal proxies-datacenter]
due    = 2026-01-31
every  = month
remind = 5d

[renewal domain]
due    = FILL_ME
every  = year
`

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParse(t *testing.T) {
	c, err := Parse(strings.NewReader(sample), env(map[string]string{"IPROYAL_API_TOKEN": "tok", "FAL_ADMIN_KEY": "FILL_ME"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 2 || len(c.Renewals) != 2 {
		t.Fatalf("got %d checks, %d renewals", len(c.Checks), len(c.Renewals))
	}
	ip := c.Checks[0]
	if ip.Off != "" || ip.Headers[0] != [2]string{"Authorization", "Bearer tok"} || ip.Warn != 2 || ip.Scale != 1 {
		t.Errorf("iproyal: %+v", ip)
	}
	if c.Checks[1].Off != "FAL_ADMIN_KEY not set" {
		t.Errorf("fal should be off: %+v", c.Checks[1])
	}
	dc := c.Renewals[0]
	if dc.Off != "" || dc.Every != "month" || dc.Remind != 5*24*time.Hour {
		t.Errorf("renewal: %+v", dc)
	}
	if c.Renewals[1].Off != "due not set" {
		t.Errorf("domain should be off: %+v", c.Renewals[1])
	}
}

func TestParseRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"no section":    "url = x",
		"bad section":   "[cheque x]",
		"missing warn":  "[credit a]\nurl = x\nfield = f\nunit = GB",
		"bad every":     "[renewal a]\ndue = 2026-01-01\nevery = fortnight",
		"bad due":       "[renewal a]\ndue = 1/1/2026",
		"twice":         "[renewal a]\ndue = 2026-01-01\n[renewal a]\ndue = 2026-01-01",
		"header no ':'": "[credit a]\nurl = x\nfield = f\nunit = GB\nwarn = 1\nheader = nope",
	} {
		if _, err := Parse(strings.NewReader(text), env(nil)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestNext(t *testing.T) {
	jan31 := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		every string
		now   string
		want  string
	}{
		{"month", "2026-01-15T10:00:00Z", "2026-01-31"},
		{"month", "2026-01-31T23:00:00Z", "2026-01-31"}, // due today
		{"month", "2026-02-01T00:00:00Z", "2026-02-28"}, // the month has no 31st
		{"month", "2026-04-02T00:00:00Z", "2026-04-30"},
		{"month", "2026-05-20T00:00:00Z", "2026-05-31"},
		{"year", "2026-09-29T00:00:00Z", "2027-01-31"},
		{"week", "2026-02-02T00:00:00Z", "2026-02-07"},
		{"none", "2026-09-29T00:00:00Z", "2026-01-31"}, // stays overdue
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		got := Renewal{Due: jan31, Every: tc.every}.Next(now).Format("2006-01-02")
		if got != tc.want {
			t.Errorf("%s at %s: got %s, want %s", tc.every, tc.now, got, tc.want)
		}
	}
}

func TestRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") != "Key secret":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"bad key Key secret"}`))
		case r.URL.Path == "/fal":
			_, _ = w.Write([]byte(`{"username":"x","credits":{"current_balance":31.5,"currency":"USD"}}`))
		case r.URL.Path == "/string":
			_, _ = w.Write([]byte(`{"data":[{"left":"12.25"}]}`))
		default:
			_, _ = w.Write([]byte(`{"a":1,"b":2}`))
		}
	}))
	defer srv.Close()
	auth := [][2]string{{"Authorization", "Key secret"}}
	ctx := context.Background()

	v, err := Check{URL: srv.URL + "/fal", Headers: auth, Field: "credits.current_balance", Scale: 1}.Read(ctx, srv.Client())
	if err != nil || v != 31.5 {
		t.Fatalf("fal: %v %v", v, err)
	}
	v, err = Check{URL: srv.URL + "/string", Headers: auth, Field: "data.0.left", Scale: 1000}.Read(ctx, srv.Client())
	if err != nil || v != 12250 {
		t.Fatalf("string and scale: %v %v", v, err)
	}
	_, err = Check{URL: srv.URL + "/other", Headers: auth, Field: "credits.current_balance", Scale: 1}.Read(ctx, srv.Client())
	if err == nil || err.Error() != `no "credits" in the answer; it has: a, b` {
		t.Fatalf("missing field: %v", err)
	}
	_, err = Check{URL: srv.URL + "/fal", Field: "credits.current_balance", Scale: 1}.Read(ctx, srv.Client())
	if err == nil || err.Error() != "HTTP 401" {
		t.Fatalf("refused: %v", err)
	}
	_, err = Check{URL: "http://127.0.0.1:1/x?key=secret", Field: "a", Scale: 1}.Read(ctx, srv.Client())
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unreachable: %v", err)
	}
}
