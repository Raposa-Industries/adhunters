package parse

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
)

const taboolaURL = "https://trc.taboola.com/foxnews-foxnews/trc/3/json?llvl=2&pubit=i&t=1&data=%7B%22ii%22%3A%22/lifestyle%22%2C%22it%22%3A%22text%22%2C%22u%22%3A%22https%3A//www.foxnews.com/lifestyle%22%2C%22uad%22%3A%7B%22mobile%22%3Afalse%2C%22platform%22%3A%22Windows%22%7D%2C%22r%22%3A%5B%5D%7D"

func taboolaRecord(t *testing.T) *spool.Record {
	t.Helper()
	body, err := os.ReadFile("testdata/taboola_feed.js")
	if err != nil {
		t.Fatal(err)
	}
	rec := &spool.Record{
		ID: "01K6BZ0000000000000000000A", At: time.Date(2026, 9, 28, 14, 3, 5, 0, time.UTC),
		Network: Taboola, Publisher: "Fox News", Device: "desktop", Line: "dc-1", Instance: "a",
		URL: taboolaURL, Status: 200, LatencyMS: 310,
	}
	rec.SetBody(body)
	return rec
}

func TestParseTaboola(t *testing.T) {
	s, err := Parse(taboolaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.Outcome != OutcomeOK || s.Domain != "foxnews.com" || s.PageURL != "https://www.foxnews.com/lifestyle" {
		t.Fatalf("outcome %q, domain %q, page %q", s.Outcome, s.Domain, s.PageURL)
	}
	if s.GeoCountry != "US" || s.TrcRoute != "US:US:V" || s.BodySHA256 == "" {
		t.Errorf("geo %q, route %q, sha %q", s.GeoCountry, s.TrcRoute, s.BodySHA256)
	}
	// The fallback card and the publisher's own story are skipped.
	if len(s.Ads) != 2 {
		t.Fatalf("got %d ads, want 2: %+v", len(s.Ads), s.Ads)
	}
	a := s.Ads[0]
	checks := map[string][2]string{
		"headline":  {a.Headline, "Doctors Stunned: This Simple Trick & More"},
		"creative":  {a.CreativeKey, "abc123.jpg"},
		"campaign":  {a.CampaignID, "50347138"},
		"site":      {a.SiteID, "1234567"},
		"account":   {a.Account, "acme-sc"},
		"placement": {a.Placement, "Below Article"},
		"click":     {a.ClickURL, "https://theconsumerguide.co/senior/tb/?camp_id=50347138&pid=1151445&site_id=1234567"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %q, want %q", name, c[0], c[1])
		}
	}
	if a.LiveURL == a.ClickURL || a.FeedPosition != 1 || a.BlockPosition != 1 {
		t.Errorf("live %q, positions %d/%d", a.LiveURL, a.FeedPosition, a.BlockPosition)
	}
	if a.Auction == nil || *a.Auction.ClearingPrice != 0.31 || *a.Auction.BidValue != 0.45 {
		t.Fatalf("auction %+v", a.Auction)
	}
	b := s.Ads[1]
	if b.FeedPosition != 4 || b.BlockPosition != 2 || b.CampaignID != "777" || b.ItemID != "888" {
		t.Errorf("second ad: %+v", b)
	}
	if b.Auction == nil || b.Auction.AuctionID != "inferred_01K6BZ0000000000000000000A_2" {
		t.Errorf("an rtb card without an auction id gets one from its scrape: %+v", b.Auction)
	}
}

func TestParseIsDeterministic(t *testing.T) {
	a, _ := Parse(taboolaRecord(t))
	b, _ := Parse(taboolaRecord(t))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("parsing the same record twice gave different scrapes")
	}
}

func TestParseOutcomes(t *testing.T) {
	base := func() *spool.Record {
		return &spool.Record{ID: "01K6BZ0000000000000000000B", At: time.Now(), Network: Taboola, URL: taboolaURL}
	}
	cases := map[string]func(r *spool.Record){
		OutcomeError:    func(r *spool.Record) { r.Error = "timeout" },
		OutcomeHTTP:     func(r *spool.Record) { r.Status = 403; r.SetBody([]byte("no")) },
		OutcomeEmpty:    func(r *spool.Record) { r.Status = 200 },
		OutcomeUnparsed: func(r *spool.Record) { r.Status = 200; r.SetBody([]byte("<html>captcha</html>")) },
		OutcomeOK:       func(r *spool.Record) { r.Status = 200; r.SetBody([]byte("TRC.callbacks.mute()")) },
	}
	for want, set := range cases {
		r := base()
		set(r)
		s, err := Parse(r)
		if err != nil {
			t.Fatal(err)
		}
		if s.Outcome != want || len(s.Ads) != 0 {
			t.Errorf("%s: got outcome %q with %d ads", want, s.Outcome, len(s.Ads))
		}
	}
	if _, err := Parse(&spool.Record{}); err == nil {
		t.Error("a record without an id parsed")
	}
}

func TestParseNewsBreakRecord(t *testing.T) {
	body, err := os.ReadFile("testdata/newsbreak_auction.json")
	if err != nil {
		t.Fatal(err)
	}
	rec := &spool.Record{
		ID: "01K6BZ0000000000000000000C", At: time.Now(), Network: NewsBreak, Publisher: "NewsBreak", Device: "phone",
		Status: 200, RequestBody: `{"site":{"page":"https://www.newsbreak.com/","domain":"www.newsbreak.com","publisher":{"domain":"newsbreak.com"}}}`,
	}
	rec.SetBody(body)
	s, err := Parse(rec)
	if err != nil {
		t.Fatal(err)
	}
	if s.Outcome != OutcomeOK || s.Domain != "newsbreak.com" || len(s.Ads) != 1 {
		t.Fatalf("outcome %q, domain %q, %d ads", s.Outcome, s.Domain, len(s.Ads))
	}
	if a := s.Ads[0]; a.ItemID != "2100412029291245569" || a.NetworkAd == nil || a.CreativeKey == "" {
		t.Errorf("ad %+v", a)
	}
}
