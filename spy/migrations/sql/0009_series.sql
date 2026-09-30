-- Sparklines for the lists: presence per day for a page of creatives,
-- computed when asked from the read model's creative_day.

-- Published: each creative's presence per UTC day, for the lists'
-- sparklines: sightings per 100 checks of its network that day, closed hours
-- only (today's so far included). A day with no check of the network has no
-- presence; a day checked without a sighting is a real 0. The creative's
-- network is the one of its publishers.
CREATE FUNCTION spy_api.creative_series_v1(p_creative_ids INTEGER[], p_days INTEGER DEFAULT 14,
                                           p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (creative_id INTEGER, day DATE, sightings BIGINT, checks BIGINT, presence NUMERIC)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
    WITH days AS (
        SELECT ((p_now AT TIME ZONE 'UTC')::date - g) AS day FROM generate_series(0, GREATEST(p_days, 1) - 1) g
    ),
    checks AS (
        SELECT (c.hour AT TIME ZONE 'UTC')::date AS day, p.network_id, sum(c.scrapes) AS checks
        FROM tracks_api.scrape_coverage_v2 c
        JOIN tracks_api.publisher_v1 p ON p.id = c.publisher_id
        WHERE c.hour >= (SELECT min(day) FROM days)::timestamp AT TIME ZONE 'UTC'
          AND c.hour < p_now AND c.closed
        GROUP BY 1, 2
    ),
    net AS (
        SELECT cs.creative_id, p.network_id
        FROM spy.creative_stats cs
        JOIN tracks_api.publisher_v1 p ON p.id = cs.publisher_ids[1]
        WHERE cs.creative_id = ANY (p_creative_ids)
    )
    SELECT n.creative_id, d.day, COALESCE(cd.sightings, 0), ch.checks,
           CASE WHEN ch.checks > 0 THEN round(100.0 * COALESCE(cd.sightings, 0) / ch.checks, 4) END
    FROM net n
    CROSS JOIN days d
    LEFT JOIN checks ch ON ch.day = d.day AND ch.network_id = n.network_id
    LEFT JOIN spy.creative_day cd ON cd.creative_id = n.creative_id AND cd.day = d.day
    ORDER BY 1, 2
$$;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'spy_api_read') THEN
        GRANT EXECUTE ON FUNCTION spy_api.creative_series_v1(INTEGER[], INTEGER, TIMESTAMPTZ) TO spy_api_read;
    END IF;
END;
$$;
