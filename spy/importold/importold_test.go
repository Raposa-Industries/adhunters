package importold

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Raposa-Industries/adhunters/spy/internal/testdb"
)

// oldSchema is the part of the collector's spy schema the import reads.
const oldSchema = `
CREATE SCHEMA spy;
CREATE TABLE spy.network (id SMALLINT PRIMARY KEY, code TEXT NOT NULL);
INSERT INTO spy.network VALUES (7, 'taboola'), (8, 'newsbreak');
CREATE TABLE spy.seller (id INTEGER PRIMARY KEY, platform TEXT NOT NULL, account TEXT NOT NULL);
INSERT INTO spy.seller VALUES (1, 'ClickBank', 'slimpro'), (2, 'BuyGoods', '12878');
CREATE TABLE spy.operator (id INTEGER PRIMARY KEY, name TEXT NOT NULL, display_name TEXT, kind TEXT NOT NULL, vertical TEXT,
    name_is_manual BOOLEAN NOT NULL DEFAULT FALSE, seller_id INTEGER);
CREATE TABLE spy.account (id SERIAL PRIMARY KEY, network_id SMALLINT NOT NULL, external_id TEXT NOT NULL, operator_id INTEGER);
CREATE TABLE spy.creative (id INTEGER PRIMARY KEY, creative_key TEXT NOT NULL, network_id SMALLINT NOT NULL, vertical TEXT);
CREATE TABLE spy.subvertical (id SMALLINT PRIMARY KEY, vertical TEXT NOT NULL, name TEXT NOT NULL, old_vertical TEXT);
CREATE TABLE spy.creative_vertical (creative_id INTEGER PRIMARY KEY, vertical TEXT, subvertical_id SMALLINT,
    confidence NUMERIC(3, 2) NOT NULL DEFAULT 0, unsure BOOLEAN NOT NULL DEFAULT TRUE, source TEXT,
    health_from_funnel BOOLEAN NOT NULL DEFAULT FALSE);
INSERT INTO spy.operator VALUES (12, 'Acme', 'Acme Health', 'direct', 'Health', TRUE, 1), (40, 'Beta', NULL, 'arbitrage', NULL, FALSE, NULL);
INSERT INTO spy.account (network_id, external_id, operator_id) VALUES
    (7, 'tb-1', 12), (7, 'tb-2', 40), (8, 'tb-1', 40), (7, 'tb-unknown', 12), (7, 'tb-3', NULL);
INSERT INTO spy.subvertical VALUES (1, 'health', 'Weight Management & Metabolic Health', 'Weight Loss'),
    (2, 'health', 'Vision & Eye Health', NULL), (3, 'finance', 'Insurance', 'Finance');
INSERT INTO spy.creative VALUES (101, 'key-a', 7, 'Health'), (102, 'key-b', 7, 'Pets'), (103, 'key-c', 7, NULL),
    (104, 'key-d', 7, NULL), (105, 'key-unknown', 7, NULL);
INSERT INTO spy.creative_vertical VALUES
    (101, 'health', 1, 0.9, false, 'headline', false),
    (102, 'health', 2, 0.7, false, 'headline', false),
    (103, 'health', 2, 0.5, true, 'model', true),
    (104, 'finance', 3, 0.8, false, 'landing_page', false),
    (105, 'finance', 3, 0.8, false, 'landing_page', false);
CREATE TABLE spy.ad (id INTEGER PRIMARY KEY, creative_id INTEGER NOT NULL, headline TEXT NOT NULL);
INSERT INTO spy.ad VALUES (501, 101, 'Doctors stunned'), (502, 105, 'Unknown one');
CREATE TABLE spy.site (id INTEGER PRIMARY KEY, domain TEXT NOT NULL, operator_id INTEGER, group_reason TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL);
INSERT INTO spy.site VALUES (1, 'acme.com', 12, 'tracking id', '2026-08-01', '2026-09-20'),
    (2, 'beta.net', 40, NULL, '2026-08-05', '2026-09-21');
CREATE TABLE spy.clue (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, value TEXT NOT NULL, strong BOOLEAN NOT NULL);
INSERT INTO spy.clue VALUES (1, 'certificate', 'sha256:abc', TRUE), (2, 'name_servers', 'ns1.x+ns2.x', TRUE);
CREATE TABLE spy.site_clue (site_id INTEGER, clue_id INTEGER, first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL);
INSERT INTO spy.site_clue VALUES (1, 1, '2026-08-01', '2026-09-20'), (2, 1, '2026-08-05', '2026-09-21'), (2, 2, '2026-08-05', '2026-09-21');
CREATE TABLE spy.landing_page (id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL);
INSERT INTO spy.landing_page VALUES (1, 1), (2, 2);
CREATE TABLE spy.landing_page_version (id INTEGER PRIMARY KEY, landing_page_id INTEGER NOT NULL, seller_id INTEGER,
    first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL);
INSERT INTO spy.landing_page_version VALUES (1, 1, 1, '2026-08-02', '2026-08-10'), (2, 1, 1, '2026-08-11', '2026-09-20'),
    (3, 2, NULL, '2026-08-05', '2026-09-21');
CREATE TABLE spy.account_landing_page (account_id INTEGER, landing_page_id INTEGER, first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL);
INSERT INTO spy.account_landing_page VALUES (1, 1, '2026-08-01', '2026-09-20'), (4, 1, '2026-08-01', '2026-09-20');
CREATE TABLE spy.grouping_fix (id INTEGER PRIMARY KEY, action TEXT NOT NULL, site_id INTEGER, account_id INTEGER,
    operator_id INTEGER, note TEXT, created_by TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
INSERT INTO spy.grouping_fix VALUES (1, 'split', 2, NULL, NULL, 'not theirs', 'marcos', '2026-09-01'),
    (2, 'join', NULL, 2, 12, 'same owner', 'marcos', '2026-09-02'),
    (3, 'join', NULL, 4, 12, 'account Tracks lacks', 'marcos', '2026-09-03');
CREATE TABLE spy.agency (name_root TEXT PRIMARY KEY, name TEXT NOT NULL, operator_count INTEGER NOT NULL);
INSERT INTO spy.agency VALUES ('adlionmedialimited', 'Ad Lion Media', 11);
CREATE TABLE spy.direction_event (id BIGINT PRIMARY KEY, at TIMESTAMPTZ NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL,
    subject_id INTEGER GENERATED ALWAYS AS (CASE WHEN kind IN ('ad', 'creative', 'operator') THEN key::integer END) STORED,
    from_direction TEXT NOT NULL, to_direction TEXT NOT NULL, reason JSONB NOT NULL, reason_text TEXT NOT NULL);
INSERT INTO spy.direction_event (id, at, kind, key, from_direction, to_direction, reason, reason_text) VALUES
    (1, '2026-09-10', 'creative', '101', 'steady', 'rising', '{}', 'more'),
    (2, '2026-09-10', 'ad', '501', 'steady', 'rising', '{}', 'more'),
    (3, '2026-09-10', 'operator', '12', 'steady', 'fading', '{}', 'less'),
    (4, '2026-09-10', 'network', '*', 'steady', 'rising', '{}', 'more'),
    (5, '2026-09-10', 'vertical', 'Health', 'steady', 'rising', '{}', 'more'),
    (6, '2026-09-10', 'ad', '502', 'steady', 'stopped', '{}', 'gone'),
    (7, '2026-10-02', 'creative', '101', 'rising', 'steady', '{}', 'after Spy started');
CREATE TABLE spy.device (id SMALLINT PRIMARY KEY, code TEXT NOT NULL);
INSERT INTO spy.device VALUES (1, 'desktop'), (2, 'phone');
CREATE TABLE spy.publisher (id INTEGER PRIMARY KEY, name TEXT NOT NULL, domain TEXT);
INSERT INTO spy.publisher VALUES (1, 'MSN', NULL), (2, 'newsbreak', NULL);
CREATE TABLE spy.publisher_alias (alias TEXT PRIMARY KEY, publisher_id INTEGER NOT NULL);
INSERT INTO spy.publisher_alias VALUES ('msn.com', 1), ('msn', 1);
CREATE TABLE public.adhunters_rtb_auction_log (auction_id TEXT NOT NULL, publisher_domain TEXT NOT NULL,
    clearing_price NUMERIC(10, 4), bid_value NUMERIC(10, 4), cap_auction_price NUMERIC(10, 6), winning_seat TEXT,
    raw_item_id TEXT, device TEXT, intercepted_at TIMESTAMPTZ NOT NULL);
INSERT INTO public.adhunters_rtb_auction_log VALUES
    ('a1', 'msn.com', 0.10, 0.5, 1, 'taboola-native', 'it-1', 'desktop', '2026-09-20 10:00+00'),
    ('a2', 'msn.com', 0.30, 0.7, 1, 'googleadx-defaultseat', 'it-1', 'desktop', '2026-09-20 11:00+00'),
    ('a3', 'msn.com', 0.20, NULL, NULL, 'taboola-rtb', 'it-1', 'mobile', '2026-09-20 12:00+00'),
    ('a4', 'msn.com', 0.90, 1.0, 1, 'taboola-native', 'it-1', 'desktop', '2026-10-01 12:00+00'),
    ('a5', 'msn.com', 0.10, 0.5, 1, 'taboola-native', 'card-sig', 'desktop', '2026-09-20 10:00+00'),
    ('a6', 'elsewhere.com', 0.10, 0.5, 1, 'taboola-native', 'it-1', 'desktop', '2026-09-20 10:00+00');
CREATE TABLE spy.sighting (seen_at TIMESTAMPTZ NOT NULL, ad_id INTEGER NOT NULL, publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL, bid_price REAL, second_price REAL) PARTITION BY RANGE (seen_at);
CREATE TABLE spy.sighting_20260920 PARTITION OF spy.sighting FOR VALUES FROM ('2026-09-20 00:00+00') TO ('2026-09-21 00:00+00');
CREATE TABLE spy.sighting_20260921 PARTITION OF spy.sighting FOR VALUES FROM ('2026-09-21 00:00+00') TO ('2026-09-22 00:00+00');
INSERT INTO spy.sighting VALUES ('2026-09-20 10:00+00', 501, 2, 2, 1.0, 0.8), ('2026-09-20 11:00+00', 501, 2, 2, 3.0, 1.2),
    ('2026-09-20 12:00+00', 501, 1, 1, NULL, NULL), ('2026-09-21 12:00+00', 502, 2, 2, 2.0, NULL);
`

func TestRun(t *testing.T) {
	ctx := context.Background()
	old := testdb.Empty(t)
	if _, err := old.Exec(ctx, oldSchema); err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	if _, err := db.Exec(ctx, `
		INSERT INTO tracks_api.account_v1 (id, network_id, external_id) VALUES (1, 1, 'tb-1'), (2, 1, 'tb-2'), (3, 2, 'tb-1');
		INSERT INTO tracks_api.creative_v1 (id, creative_key) VALUES (11, 'key-a'), (12, 'key-b'), (13, 'key-c'), (14, 'key-d');
		INSERT INTO tracks_api.ad_v1 (id, creative_id, headline) VALUES (21, 11, 'Doctors stunned'), (22, 11, 'Second headline');
		INSERT INTO tracks_api.publisher_v1 (id, network_id, name, domain) VALUES (1, 1, 'msn', 'msn.com'), (2, 2, 'newsbreak', NULL);
		INSERT INTO tracks_api.link_v1 (id, item_id) VALUES (31, 'it-1'), (32, NULL);
		INSERT INTO tracks_api.creative_link_daily_v1 VALUES ('2026-09-20', 11, 31, 5, now(), now()), ('2026-09-20', 12, 32, 50, now(), now());
		INSERT INTO tracks_api.ad_daily_v1 (day, ad_id, publisher_id, device_id, creative_id, sightings, first_seen_at, last_seen_at)
		VALUES ('2026-09-20', 22, 1, 1, 11, 9, now(), now()), ('2026-09-20', 21, 1, 1, 11, 2, now(), now()),
		       ('2026-08-01', 21, 1, 2, 11, 50, now(), now());
		INSERT INTO spy.price_day (day, ad_id, creative_id, publisher_id, device_id, auctions, rtb, clearing_n, clearing_sum,
		                           bid_n, bid_sum, second_n, second_sum)
		VALUES ('2026-10-01', 21, 11, 1, 1, 7, 0, 7, 0.7, 0, 0, 0, 0);
		INSERT INTO spy.operator (id, name) VALUES (99, 'gone');
		INSERT INTO spy.site (domain, first_seen_at, last_seen_at) VALUES ('acme.com', '2026-09-25', '2026-10-01');
		INSERT INTO spy.grouping_fix (account_id, action, note) VALUES (3, 'split', 'made in Spy');
		INSERT INTO spy.direction_event (at, kind, key, from_direction, to_direction, reason, reason_text)
		VALUES ('2026-10-01', 'creative', '11', 'steady', 'rising', '{}', 'Spy''s own');`); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for run := 0; run < 2; run++ { // the second run must give the same result
		res, err := Run(ctx, old, db, log)
		if err != nil {
			t.Fatal(err)
		}
		want := Result{Operators: Counts{2, 0}, Accounts: Counts{3, 1}, Verticals: Counts{4, 1}, Pages: Pages{
			Sites: Counts{2, 0}, Clues: Counts{2, 0}, SiteClues: Counts{3, 0}, Sellers: Counts{2, 0}, SiteSellers: Counts{1, 0},
			AccountSites: Counts{1, 1}, Fixes: Counts{2, 1}, Agencies: Counts{1, 0}, Events: Counts{4, 2}},
			Prices: Prices{Auctions: Counts{4, 2}, NewsBreak: Counts{1, 1}, Rows: 3, Kept: 1}}
		if res != want {
			t.Fatalf("run %d: got %+v, want %+v", run, res, want)
		}
	}
	var got string
	if err := db.QueryRow(ctx, `
		SELECT string_agg(format('%s=%s', account_id, operator_id), ' ' ORDER BY account_id) FROM spy.account_operator`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "1=12 2=40 3=40" {
		t.Errorf("accounts: %s", got)
	}
	if err := db.QueryRow(ctx, `
		SELECT string_agg(format('%s:%s/%s/%s', creative_id, vertical, subvertical, shown_vertical), ' | ' ORDER BY creative_id)
		FROM spy.creative_vertical_old`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := "11:health/Weight Management & Metabolic Health/Weight Loss | 12:health/Vision & Eye Health/Pets | " +
		"13:health/Vision & Eye Health/Vision & Eye Health | 14:finance/Insurance/Insurance"
	if got != want {
		t.Errorf("verticals:\n got %s\nwant %s", got, want)
	}
	if err := db.QueryRow(ctx, `SELECT string_agg(code || ' ' || name, ', ' ORDER BY id) FROM spy.operator`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "OP12 Acme, OP40 Beta" {
		t.Errorf("operators: %s", got)
	}
	if err := db.QueryRow(ctx, `SELECT string_agg(format('%s %s %s', id, name_is_manual, seller), ', ' ORDER BY id) FROM spy.operator`).Scan(&got); err != nil ||
		got != "12 t ClickBank slimpro, 40 f " {
		t.Errorf("hand names and sellers: %s (%v)", got, err)
	}
	for _, c := range []struct{ what, query, want string }{
		{"sites", `SELECT string_agg(format('%s %s %s %s', domain, operator_id, first_seen_at::date, last_seen_at::date), ', ' ORDER BY domain) FROM spy.site`,
			"acme.com 12 2026-08-01 2026-10-01, beta.net 40 2026-08-05 2026-09-21"},
		{"site clues", `SELECT string_agg(format('%s %s', s.domain, c.kind), ', ' ORDER BY s.domain, c.kind)
			FROM spy.site_clue x JOIN spy.site s ON s.id = x.site_id JOIN spy.clue c ON c.id = x.clue_id`,
			"acme.com certificate, beta.net certificate, beta.net name_servers"},
		{"site sellers", `SELECT string_agg(format('%s %s %s %s', s.domain, se.account, x.first_seen_at::date, x.last_seen_at::date), ', ')
			FROM spy.site_seller x JOIN spy.site s ON s.id = x.site_id JOIN spy.seller se ON se.id = x.seller_id`,
			"acme.com slimpro 2026-08-02 2026-09-20"},
		{"account sites", `SELECT string_agg(format('%s %s', x.account_id, s.domain), ', ') FROM spy.account_site x JOIN spy.site s ON s.id = x.site_id`,
			"1 acme.com"},
		{"fixes", `SELECT string_agg(format('%s %s %s %s %s', COALESCE(old_id::text, 'new'), action, COALESCE(f.account_id::text, s.domain), f.operator_id, f.made_by), ', ' ORDER BY old_id NULLS FIRST)
			FROM spy.grouping_fix f LEFT JOIN spy.site s ON s.id = f.site_id`,
			"new split 3  , 1 split beta.net  marcos, 2 join 2 12 marcos"},
		{"agencies", `SELECT string_agg(format('%s %s %s', name_root, name, imported), ', ') FROM spy.agency`,
			"adlionmedialimited Ad Lion Media t"},
		{"events", `SELECT string_agg(format('%s %s %s', COALESCE(old_id::text, 'new'), kind, key), ', ' ORDER BY old_id NULLS FIRST) FROM spy.direction_event`,
			"new creative 11, 1 creative 11, 2 ad 21, 3 operator 12, 4 network *"},
		// The phone auction has no ad that day: the creative's most seen ad.
		{"prices", `SELECT string_agg(format('%s ad%s p%s d%s auctions %s/%s clearing %s~%s bid %s~%s second %s~%s %s',
				day, ad_id, publisher_id, device_id, auctions, rtb, round(clearing_sum::numeric, 2), clearing_p50,
				bid_n, bid_p50, second_n, second_p50, CASE WHEN imported THEN 'copied' ELSE 'own' END), ', ' ORDER BY day, ad_id, publisher_id)
			FROM spy.price_day`,
			"2026-09-20 ad21 p1 d2 auctions 1/1 clearing 0.20~0.2 bid 0~ second 0~ copied, " +
				"2026-09-20 ad21 p2 d2 auctions 0/0 clearing 0.00~ bid 2~2 second 2~1 copied, " +
				"2026-09-20 ad22 p1 d1 auctions 2/1 clearing 0.40~0.2 bid 2~0.6 second 0~ copied, " +
				"2026-10-01 ad21 p1 d1 auctions 7/0 clearing 0.70~ bid 0~ second 0~ own"},
	} {
		if err := db.QueryRow(ctx, c.query).Scan(&got); err != nil || got != c.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", c.what, got, err, c.want)
		}
	}
	// A new operator gets an id past the copied ones.
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO spy.operator (name) VALUES ('new') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 41 {
		t.Errorf("new operator id %d, want 41", id)
	}

	// Once Spy groups operators itself, only the verticals are copied.
	if _, err := db.Exec(ctx, `UPDATE spy.setting SET text_value = 'grouping' WHERE name = 'operators_from';
		DELETE FROM spy.account_operator WHERE account_id = 3`); err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, old, db, log)
	if err != nil || res.Operators != (Counts{}) || res.Accounts != (Counts{}) || res.Verticals != (Counts{4, 1}) {
		t.Fatalf("with grouping on: %+v, %v", res, err)
	}
	if err := db.QueryRow(ctx, `SELECT string_agg(code, ' ' ORDER BY id) || ' ' || (SELECT count(*) FROM spy.account_operator)
		FROM spy.operator`).Scan(&got); err != nil || got != "OP12 OP40 OP41 2" {
		t.Errorf("with grouping on, operators and accounts changed: %s (%v)", got, err)
	}

	// An empty old grouping is refused and changes nothing.
	if _, err := old.Exec(ctx, `DELETE FROM spy.creative_vertical`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, old, db, log); err == nil {
		t.Fatal("an empty grouping was copied")
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM spy.creative_vertical_old`).Scan(&n); err != nil || n != 4 {
		t.Errorf("verticals after a refused run: %d (%v)", n, err)
	}
}
