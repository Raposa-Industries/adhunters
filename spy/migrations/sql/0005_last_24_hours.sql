-- lint: new-table
-- The last 24 hours, kept ready for the lists: the creative and operator
-- range numbers (0003) for the 24 closed hours up to where Tracks' closed
-- hours stop, against the same hours of the 3 weeks before. They are the
-- same rows spy.creative_range() and spy.operator_range() return for that
-- range, so a list and a range never disagree.
--
-- They are rebuilt when closed hours move on, or when an hour of the last
-- 4 weeks closes again (a late raw file or a replay).

CREATE TABLE spy.recent_window (
    one BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (one),
    window_end TIMESTAMPTZ NOT NULL,             -- the 24 hours end here (the first hour not closed)
    network_sightings_24h BIGINT NOT NULL,       -- all sightings in those 24 hours
    refreshed_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE spy.creative_recent OF spy.creative_range_row (PRIMARY KEY (creative_id, network_id));
CREATE INDEX creative_recent_rank_idx ON spy.creative_recent (network_id, momentum_rank);

CREATE TABLE spy.operator_recent OF spy.operator_range_row (PRIMARY KEY (operator_id, network_id));

-- Returns the creatives written, or 0 when the 24 hours are already current
-- or another refresh is running.
CREATE FUNCTION spy.refresh_recent(p_now TIMESTAMPTZ DEFAULT now()) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    v_end TIMESTAMPTZ := spy.recent_end(p_now);
    v_weeks INTEGER := COALESCE((spy.cfg()->>'usual_weeks')::int, 3);
    v_rows BIGINT;
BEGIN
    IF v_end IS NULL THEN
        RETURN 0;
    END IF;
    -- Current: same end, and no hour they read closed again since.
    IF EXISTS (SELECT 1 FROM spy.recent_window w
               WHERE w.window_end = v_end
                 AND NOT EXISTS (SELECT 1 FROM tracks_api.closed_hour_v1 c
                                 WHERE c.hour >= v_end - make_interval(days => 7 * v_weeks + 1) AND c.hour < v_end
                                   AND c.closed_at > w.refreshed_at)) THEN
        RETURN 0;
    END IF;
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.refresh_recent')) THEN
        RETURN 0;
    END IF;

    DELETE FROM spy.creative_recent;
    INSERT INTO spy.creative_recent
    SELECT * FROM spy.creative_range(v_end - interval '24 hours', v_end, NULL, NULL, p_now);
    GET DIAGNOSTICS v_rows = ROW_COUNT;

    DELETE FROM spy.operator_recent;
    INSERT INTO spy.operator_recent
    SELECT * FROM spy.operator_range(v_end - interval '24 hours', v_end, NULL, NULL, p_now);

    INSERT INTO spy.recent_window (one, window_end, network_sightings_24h, refreshed_at)
    SELECT TRUE, v_end, COALESCE(sum(sightings), 0), clock_timestamp()
    FROM tracks_api.scrape_coverage_v2
    WHERE hour >= v_end - interval '24 hours' AND hour < v_end AND closed
    ON CONFLICT (one) DO UPDATE
        SET window_end = EXCLUDED.window_end,
            network_sightings_24h = EXCLUDED.network_sightings_24h,
            refreshed_at = EXCLUDED.refreshed_at;

    RETURN v_rows;
END;
$$;
