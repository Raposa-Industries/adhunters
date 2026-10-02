// Package testdb gives a test its own throwaway database with a stand-in for
// the Tracks views Spy reads and the spy migrations applied. It needs
// PG_TEST_URL (any database on a server the test may create databases on);
// without it the test is skipped.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/spy/migrations"
)

// New creates a database with the Tracks stand-in, migrates it, and drops it
// when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := Empty(t)
	if _, err := pool.Exec(context.Background(), TracksFixture); err != nil {
		t.Fatal(err)
	}
	Migrate(t, pool)
	return pool
}

// Empty creates a database with nothing in it, dropped when the test ends
// (kept, to look into, when SPY_KEEP_DB is set).
func Empty(t testing.TB) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("PG_TEST_URL")
	if base == "" {
		t.Skip("PG_TEST_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "spy_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	pool, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "spy-test", StatementTimeout: 10 * time.Minute, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if os.Getenv("SPY_KEEP_DB") != "" {
			t.Logf("kept database %s", name)
		} else {
			_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		}
		_ = admin.Close(ctx)
	})
	return pool
}

// Migrate applies the spy migrations.
func Migrate(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(context.Background(), pool, slog.New(slog.NewTextHandler(io.Discard, nil)), migrations.Schema, migs); err != nil {
		t.Fatal(err)
	}
}

// TracksFixture stands in for the Tracks views Spy reads
// (contract/sql/tracks), as plain tables a test fills, with the columns of
// the published views in the same order (a test checks) and the indexes the
// real tables have. Spy's code may only read what is here.
const TracksFixture = `
CREATE SCHEMA tracks_api;
CREATE TABLE tracks_api.network_v1 (id SMALLINT PRIMARY KEY, code TEXT NOT NULL, name TEXT NOT NULL);
INSERT INTO tracks_api.network_v1 VALUES (1, 'taboola', 'Taboola'), (2, 'newsbreak', 'NewsBreak Ads');
CREATE TABLE tracks_api.device_v1 (id SMALLINT PRIMARY KEY, code TEXT NOT NULL);
INSERT INTO tracks_api.device_v1 VALUES (1, 'desktop'), (2, 'phone');
CREATE TABLE tracks_api.publisher_v1 (id INTEGER PRIMARY KEY, network_id SMALLINT NOT NULL DEFAULT 1, name TEXT NOT NULL,
    domain TEXT, first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE tracks_api.brand_v1 (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE tracks_api.account_v1 (id INTEGER PRIMARY KEY, network_id SMALLINT NOT NULL DEFAULT 1, external_id TEXT NOT NULL,
    org_external_id TEXT, first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE tracks_api.campaign_v1 (id INTEGER PRIMARY KEY, network_id SMALLINT NOT NULL DEFAULT 1, external_id TEXT NOT NULL,
    name TEXT, account_id INTEGER, parent_external_id TEXT, parent_name TEXT, objective TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE tracks_api.creative_v1 (id INTEGER PRIMARY KEY, creative_key TEXT NOT NULL, image_url TEXT NOT NULL DEFAULT '',
    format_type TEXT, video_duration INTEGER, thumb_dimensions TEXT, language TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE tracks_api.ad_v1 (id INTEGER PRIMARY KEY, creative_id INTEGER NOT NULL, headline TEXT NOT NULL DEFAULT '',
    description TEXT, cta TEXT, brand_id INTEGER, account_id INTEGER,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX ON tracks_api.ad_v1 (creative_id);
CREATE TABLE tracks_api.link_v1 (id INTEGER PRIMARY KEY, link_key UUID, host TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '/',
    item_id TEXT, tracker TEXT, affiliate_network TEXT, params JSONB NOT NULL DEFAULT '{}', sample_url TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE tracks_api.sighting_v1 (seen_at TIMESTAMPTZ NOT NULL, scrape_id BIGINT NOT NULL DEFAULT 0, ad_id INTEGER NOT NULL,
    creative_id INTEGER NOT NULL, publisher_id INTEGER NOT NULL, device_id SMALLINT NOT NULL, placement_id INTEGER,
    campaign_id INTEGER, link_id INTEGER, account_id INTEGER, brand_id INTEGER, site_id INTEGER, ecpa_percentile REAL,
    feed_position SMALLINT, block_position SMALLINT, bid_price REAL, second_price REAL);
CREATE INDEX ON tracks_api.sighting_v1 (ad_id, seen_at);
CREATE TABLE tracks_api.auction_v1 (seen_at TIMESTAMPTZ NOT NULL, scrape_id BIGINT NOT NULL DEFAULT 0, ad_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL, device_id SMALLINT NOT NULL, auction_id TEXT NOT NULL, placement TEXT,
    clearing_price REAL, bid_value REAL, cap_auction_price REAL, currency TEXT, winning_seat TEXT, is_rtb BOOLEAN NOT NULL);
CREATE TABLE tracks_api.ad_hourly_v1 (hour TIMESTAMPTZ NOT NULL, ad_id INTEGER NOT NULL, publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL, sightings INTEGER NOT NULL, scrapes INTEGER NOT NULL DEFAULT 0,
    feed_position_sum BIGINT NOT NULL DEFAULT 0, feed_position_min SMALLINT, feed_position_max SMALLINT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (hour, ad_id, publisher_id, device_id));
CREATE INDEX ON tracks_api.ad_hourly_v1 (ad_id, hour);
CREATE TABLE tracks_api.ad_account_brand_hourly_v1 (hour TIMESTAMPTZ NOT NULL, ad_id INTEGER NOT NULL, account_id INTEGER,
    brand_id INTEGER, publisher_id INTEGER NOT NULL, device_id SMALLINT NOT NULL, sightings INTEGER NOT NULL);
CREATE INDEX ON tracks_api.ad_account_brand_hourly_v1 (hour);
-- Stands in for Tracks' hour files: a day listed here has its hourly counts
-- in the archive ('archive'), and reads 'coming' once asked for.
CREATE TABLE tracks_api.hourly_days_fake (day DATE PRIMARY KEY, state TEXT NOT NULL);
CREATE FUNCTION tracks_api.hourly_days_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_bring_back BOOLEAN)
RETURNS TABLE (day DATE, state TEXT, kept_until TIMESTAMPTZ) LANGUAGE plpgsql AS $$
DECLARE
    d0 DATE := (p_from AT TIME ZONE 'UTC')::date;
    d1 DATE := ((p_to - interval '1 microsecond') AT TIME ZONE 'UTC')::date;
BEGIN
    IF p_bring_back THEN
        UPDATE tracks_api.hourly_days_fake f SET state = 'coming' WHERE f.state = 'archive' AND f.day BETWEEN d0 AND d1;
    END IF;
    RETURN QUERY SELECT g::date, COALESCE(f.state, 'database'), NULL::timestamptz
    FROM generate_series(d0::timestamp, d1::timestamp, interval '1 day') g
    LEFT JOIN tracks_api.hourly_days_fake f ON f.day = g::date ORDER BY 1;
END;
$$;
CREATE TABLE tracks_api.scrape_coverage_v2 (hour TIMESTAMPTZ NOT NULL, publisher_id INTEGER NOT NULL, device_id SMALLINT NOT NULL,
    scrapes INTEGER NOT NULL, answered INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0,
    sightings INTEGER NOT NULL DEFAULT 0, closed BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (hour, publisher_id, device_id));
CREATE TABLE tracks_api.closed_hour_v1 (hour TIMESTAMPTZ PRIMARY KEY, closed_at TIMESTAMPTZ NOT NULL, dirty BOOLEAN NOT NULL DEFAULT FALSE);
CREATE TABLE tracks_api.ad_daily_v1 (day DATE NOT NULL, ad_id INTEGER NOT NULL, publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL, creative_id INTEGER NOT NULL, sightings INTEGER NOT NULL, scrapes INTEGER NOT NULL DEFAULT 0,
    feed_position_sum BIGINT NOT NULL DEFAULT 0, first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (day, ad_id, publisher_id, device_id));
CREATE INDEX ON tracks_api.ad_daily_v1 (ad_id, day);
CREATE INDEX ON tracks_api.ad_daily_v1 (creative_id, day);
CREATE TABLE tracks_api.ad_account_daily_v1 (day DATE NOT NULL, ad_id INTEGER NOT NULL, account_id INTEGER, brand_id INTEGER,
    publisher_id INTEGER NOT NULL, device_id SMALLINT NOT NULL, creative_id INTEGER NOT NULL, sightings INTEGER NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL);
CREATE INDEX ON tracks_api.ad_account_daily_v1 (account_id, day);
CREATE TABLE tracks_api.creative_link_daily_v1 (day DATE NOT NULL, creative_id INTEGER NOT NULL, link_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL, first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (day, creative_id, link_id));
CREATE TABLE tracks_api.creative_campaign_daily_v1 (day DATE NOT NULL, creative_id INTEGER NOT NULL, campaign_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL, first_seen_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (day, creative_id, campaign_id));
CREATE INDEX ON tracks_api.creative_campaign_daily_v1 (campaign_id, day);
CREATE TABLE tracks_api.walk_page_v1 (walk_id BIGINT NOT NULL, at TIMESTAMPTZ NOT NULL, ad_id INTEGER NOT NULL,
    creative_id INTEGER NOT NULL, account_id INTEGER, link_id INTEGER NOT NULL DEFAULT 0, publisher_id INTEGER,
    outcome TEXT NOT NULL DEFAULT 'ok', step SMALLINT NOT NULL DEFAULT 0, url TEXT NOT NULL DEFAULT '', final_url TEXT, host TEXT,
    status INTEGER, hops SMALLINT NOT NULL DEFAULT 0, page_type TEXT, checkout_platform TEXT, seller_account TEXT,
    version_hash UUID, PRIMARY KEY (walk_id, step));
CREATE INDEX ON tracks_api.walk_page_v1 (at);
CREATE TABLE tracks_api.page_version_v1 (hash UUID PRIMARY KEY, title TEXT, word_count INTEGER NOT NULL DEFAULT 0,
    headings JSONB NOT NULL DEFAULT '{}', meta JSONB NOT NULL DEFAULT '{}', favicon_url TEXT, pixels JSONB NOT NULL DEFAULT '{}',
    emails TEXT[] NOT NULL DEFAULT '{}', phones TEXT[] NOT NULL DEFAULT '{}', companies TEXT[] NOT NULL DEFAULT '{}',
    disclaimers TEXT[] NOT NULL DEFAULT '{}', vsl JSONB NOT NULL DEFAULT '{}', text TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
`
