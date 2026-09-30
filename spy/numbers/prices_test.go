package numbers

import (
	"fmt"
	"testing"
	"time"
)

func TestPrices(t *testing.T) {
	b := newBench(t)
	b.exec(`INSERT INTO tracks_api.publisher_v1 (id, network_id, name) VALUES (3, 2, 'NewsBreak app')`)
	b.ad(10, 100, 0, "ck-10", "Headline")
	today := now.Truncate(24 * time.Hour)
	// Taboola: three priced auctions today, one RTB; one ten days ago.
	for i, p := range []float64{0.2, 0.3, 0.7} {
		b.exec(`INSERT INTO tracks_api.auction_v1 (seen_at, ad_id, publisher_id, device_id, auction_id, clearing_price, bid_value, is_rtb)
			VALUES ($1, 100, 1, 2, $2, $3::real, $3::real + 0.1, $4)`, today.Add(time.Duration(i)*time.Hour), fmt.Sprint(i), p, i == 0)
	}
	b.exec(`INSERT INTO tracks_api.auction_v1 (seen_at, ad_id, publisher_id, device_id, auction_id, clearing_price, is_rtb)
		VALUES ($1, 100, 1, 2, 'old', 1.0, false)`, today.AddDate(0, 0, -10))
	// NewsBreak: two sightings with bids today.
	b.exec(`INSERT INTO tracks_api.sighting_v1 (seen_at, ad_id, creative_id, publisher_id, device_id, bid_price, second_price)
		VALUES ($1, 100, 10, 3, 2, 12, 8), ($1, 100, 10, 3, 2, 14, 10)`, today.Add(time.Hour))

	if n, err := b.r.Prices(b.ctx); err != nil || n != 3 {
		t.Fatalf("prices: %d rows, %v", n, err)
	}
	if got := b.text(`SELECT format('%s %s %s %s', auctions, rtb, clearing_p50, round(clearing_sum::numeric, 2))
		FROM spy.price_day WHERE day = $1 AND publisher_id = 1`, today); got != "3 1 0.3 1.20" {
		t.Errorf("Taboola today: %s", got)
	}
	if got := b.text(`SELECT format('%s %s %s', network_id, round(bid_avg::numeric, 1), round(second_avg::numeric, 1))
		FROM spy_api.creative_prices_v1($1::date, $1::date) WHERE network_id = 2`, today); got != "2 13.0 9.0" {
		t.Errorf("NewsBreak: %s", got)
	}
	if got := b.float(`SELECT clearing_avg FROM spy_api.creative_prices_v1($1::date - 14, $1::date) WHERE network_id = 1`, today); !close3(got, 0.55) {
		t.Errorf("Taboola over 14 days: %v, want 0.55", got)
	}
	// Again: the same rows, nothing counted twice. Only yesterday and today are redone.
	if _, err := b.r.Prices(b.ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.float(`SELECT sum(auctions)::float8 FROM spy.price_day`); got != 4 {
		t.Errorf("after a second run: %v auctions, want 4", got)
	}
}
