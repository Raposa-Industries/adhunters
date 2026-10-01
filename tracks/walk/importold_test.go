package walk

import (
	"context"
	"testing"

	"github.com/Raposa-Industries/adhunters/tracks/internal/collectortest"
	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
)

// The collector's landing pages, as its funnel walker stored them (013, 023).
const oldLanders = `
CREATE TABLE spy.landing_page (id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL, host TEXT NOT NULL, path TEXT NOT NULL,
    visits INTEGER NOT NULL, first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL);
CREATE TABLE spy.landing_page_version (id INTEGER PRIMARY KEY, landing_page_id INTEGER NOT NULL, content_hash UUID NOT NULL,
    version_no INTEGER NOT NULL, page_type TEXT, title TEXT, word_count INTEGER, body_excerpt TEXT,
    headings JSONB NOT NULL DEFAULT '{}', meta_tags JSONB NOT NULL DEFAULT '{}', tracking_pixels JSONB NOT NULL DEFAULT '{}',
    server JSONB NOT NULL DEFAULT '{}', legal JSONB NOT NULL DEFAULT '{}', disclaimers JSONB NOT NULL DEFAULT '[]',
    pricing JSONB NOT NULL DEFAULT '[]', funnel_steps JSONB NOT NULL DEFAULT '[]', vsl JSONB NOT NULL DEFAULT '{}',
    favicon_url TEXT, checkout_platform TEXT, seller_id INTEGER, affiliate_id TEXT, sample_final_url TEXT NOT NULL,
    sample_redirect_chain JSONB NOT NULL DEFAULT '[]', visits INTEGER NOT NULL, first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL, body_text TEXT);
CREATE TABLE spy.landing_page_use (id INTEGER PRIMARY KEY, creative_id INTEGER NOT NULL, ad_id INTEGER,
    landing_page_id INTEGER NOT NULL, visits INTEGER NOT NULL, first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL);

INSERT INTO spy.creative (id, creative_key, image_url, first_seen_at, last_seen_at) VALUES
    (1, 'ck', '', '2026-09-01', '2026-09-30'), (2, 'ck-unknown', '', '2026-09-01', '2026-09-30');
INSERT INTO spy.ad (id, creative_id, headline, first_seen_at, last_seen_at) VALUES (11, 1, 'h2', '2026-09-01', '2026-09-30');
INSERT INTO spy.seller (id, platform, account) VALUES (5, 'ClickBank', 'slimpro');
INSERT INTO spy.landing_page VALUES (1, 1, 'www.Acme.com', '/belly', 3, '2026-09-01', '2026-09-30');
INSERT INTO spy.landing_page_version (id, landing_page_id, content_hash, version_no, page_type, title, word_count, body_excerpt,
    headings, tracking_pixels, legal, disclaimers, funnel_steps, checkout_platform, seller_id, sample_final_url,
    sample_redirect_chain, visits, first_seen_at, last_seen_at, body_text) VALUES
    (1, 1, gen_random_uuid(), 1, 'ADVERTORIAL', 'Doctors Stunned', 900, 'excerpt', '{"h1": ["Belly fat"]}',
     '{"facebook": {"detected": true, "ids": ["123", "123"]}, "redtrack": {"detected": true}}',
     '{"emails": ["help@acme.com"], "entities": ["Acme Health LLC"]}', '["Not medical advice"]',
     '[{"cta_url": "https://acme.com/order", "final_url": "https://pay.clickbank.net/?vendor=slimpro", "domain": "pay.clickbank.net",
        "page_type": "CHECKOUT", "checkout_platform": "ClickBank", "merchant_id": "slimpro", "status": 200, "redirect_chain": [{}, {}]}]',
     NULL, 5, 'https://www.acme.com/belly?x=1', '[{}, {}, {}]', 2, '2026-09-01', '2026-09-10', 'Full text of the page'),
    (2, 1, gen_random_uuid(), 2, 'VSL', 'Watch this', 300, 'short', '{}', '{}', '{}', '[]', '[]',
     NULL, NULL, 'https://acme.com/belly', '[]', 1, '2026-09-20', '2026-09-30', NULL);
INSERT INTO spy.landing_page_use VALUES
    (1, 1, NULL, 1, 2, '2026-09-01', '2026-09-05'),   -- only version 1 was seen then; no ad: the creative's lead ad
    (2, 1, 11, 1, 1, '2026-09-08', '2026-09-25'),     -- both versions
    (3, 2, NULL, 1, 1, '2026-09-01', '2026-09-30');   -- a creative Tracks lacks
`

func TestImportOld(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	old := collectortest.New(t)
	if _, err := old.Exec(ctx, oldLanders); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO tracks.creative (id, creative_key, image_url, first_seen_at, last_seen_at) VALUES (10, 'ck', '', now(), now());
		INSERT INTO tracks.account (id, network_id, external_id, first_seen_at, last_seen_at) VALUES (7, 1, 'acc', now(), now());
		INSERT INTO tracks.ad (id, creative_id, headline, account_id, first_seen_at, last_seen_at)
		VALUES (100, 10, 'h', 7, now(), now()), (101, 10, 'h2', NULL, now(), now());
		INSERT INTO tracks.link (id, link_key, host, path, sample_url, first_seen_at, last_seen_at)
		VALUES (1, gen_random_uuid(), 'a', '/', 'https://a/', now(), now()), (2, gen_random_uuid(), 'b', '/', 'https://b/', now(), now());
		INSERT INTO tracks.publisher (id, network_id, name, first_seen_at, last_seen_at) VALUES (1, 1, 'msn', now(), now());
		INSERT INTO tracks.ad_daily (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes, feed_position_sum, first_seen_at, last_seen_at)
		VALUES ('2026-09-02', 100, 1, 1, 10, 30, 30, 0, now(), now()), ('2026-09-02', 101, 1, 1, 10, 3, 3, 0, now(), now());
		INSERT INTO tracks.creative_link_daily (day, creative_id, link_id, sightings, first_seen_at, last_seen_at)
		VALUES ('2026-09-02', 10, 1, 2, now(), now()), ('2026-09-02', 10, 2, 9, now(), now())`); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ { // running it again changes nothing
		res, err := ImportOld(ctx, db, old)
		if err != nil {
			t.Fatal(err)
		}
		if want := (OldWalks{Versions: 2, Pages: 2, Uses: 3, Walks: 3, Skipped: 1}); res != want {
			t.Fatalf("run %d: %+v, want %+v", run, res, want)
		}
	}
	var got string
	if err := db.QueryRow(ctx, `
		SELECT string_agg(format('%s %s ad%s acc%s link%s step%s %s %s %s %s %s %s', w.record_id, w.at::date, w.ad_id,
		           COALESCE(w.account_id::text, '-'), w.link_id, p.step, p.host, p.status, p.hops, p.page_type,
		           COALESCE(p.checkout_platform, '-'), COALESCE(p.seller_account, '-')), ' | ' ORDER BY w.record_id, p.step)
		FROM tracks_api.walk_page_v1 p JOIN tracks.walk w ON w.id = p.walk_id`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := "old-1-1 2026-09-05 ad100 acc7 link2 step0 acme.com 200 2 ADVERTORIAL ClickBank slimpro | " +
		"old-1-1 2026-09-05 ad100 acc7 link2 step1 pay.clickbank.net 200 1 CHECKOUT ClickBank slimpro | " +
		"old-2-1 2026-09-10 ad101 acc- link2 step0 acme.com 200 2 ADVERTORIAL ClickBank slimpro | " +
		"old-2-1 2026-09-10 ad101 acc- link2 step1 pay.clickbank.net 200 1 CHECKOUT ClickBank slimpro | " +
		"old-2-2 2026-09-25 ad101 acc- link2 step0 acme.com 200 0 VSL - -"
	if got != want {
		t.Errorf("walks:\n got %s\nwant %s", got, want)
	}
	if err := db.QueryRow(ctx, `
		SELECT string_agg(format('%s %s %s %s %s %s %s', title, word_count, pixels, emails, companies, disclaimers, text), ' | ' ORDER BY title)
		FROM tracks_api.page_version_v1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want = `Doctors Stunned 900 {"facebook": ["123"], "redtrack": []} {help@acme.com} {"Acme Health LLC"} {"Not medical advice"} Full text of the page | ` +
		`Watch this 300 {} {} {} {} short`
	if got != want {
		t.Errorf("page versions:\n got %s\nwant %s", got, want)
	}
}
