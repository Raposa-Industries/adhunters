-- Builds the lookups from the archived ids, then loads the day's sightings
-- into the new layout. The archive has ids only, so values the archive lacks
-- are made up, deterministically from the ids, at realistic counts:
--   account: one of 5,000 per creative; operator: one of 1,500 per account;
--   brand: one of 8,000 per ad; vertical: one of 25 per creative (15% none);
--   2% of creatives junk; first sighting 0 to 39 days before the archived day.
-- They only decide how rows group, which is what the timing depends on.

CREATE FUNCTION spy.bench_hash(p INTEGER, p_mod INTEGER) RETURNS INTEGER
LANGUAGE sql IMMUTABLE AS $$ SELECT ((p::bigint * 2654435761) % 4294967296 % p_mod)::integer $$;

INSERT INTO spy.ad (id, creative_id, account_id, brand_id, first_seen_at, last_seen_at)
SELECT ad_id, min(creative_id),
       1 + spy.bench_hash(min(creative_id), 5000),
       1 + spy.bench_hash(ad_id, 8000),
       min(seen_at) - make_interval(days => spy.bench_hash(ad_id, 40)),
       max(seen_at)
FROM spy.sighting_archive
GROUP BY ad_id;

INSERT INTO spy.account (id, operator_id)
SELECT g, 1 + spy.bench_hash(g, 1500) FROM generate_series(1, 5000) g;

INSERT INTO spy.creative_stats (creative_id, is_junk)
SELECT DISTINCT creative_id, spy.bench_hash(creative_id, 50) = 0 FROM spy.ad;

INSERT INTO spy.creative_vertical (creative_id, vertical)
SELECT creative_id, CASE WHEN spy.bench_hash(creative_id + 7, 100) < 15 THEN NULL
                         ELSE 'vertical ' || (1 + spy.bench_hash(creative_id + 3, 25)) END
FROM spy.creative_stats;

INSERT INTO spy.creative_campaign (creative_id, campaign_id, sightings, first_seen_at, last_seen_at)
SELECT creative_id, campaign_id, count(*),
       min(seen_at) - make_interval(days => spy.bench_hash(campaign_id, 30)), max(seen_at)
FROM spy.sighting_archive
WHERE campaign_id IS NOT NULL
GROUP BY 1, 2;

ANALYZE spy.ad;
ANALYZE spy.account;
ANALYZE spy.creative_stats;
ANALYZE spy.creative_vertical;
ANALYZE spy.creative_campaign;
