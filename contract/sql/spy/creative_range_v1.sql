-- Creatives over any range, one row per creative and network. See
-- spy/METRICS.md for every column.
CREATE FUNCTION spy_api.creative_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ,
                                          p_usual_from TIMESTAMPTZ DEFAULT NULL, p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (creative_id INTEGER, network_id INTEGER, sightings BIGINT, sightings_usual BIGINT, checks BIGINT,
               checks_usual BIGINT, presence NUMERIC, presence_usual NUMERIC, phone_presence NUMERIC,
               desktop_presence NUMERIC, share_pct NUMERIC, share_usual_pct NUMERIC, share_gain_pts NUMERIC,
               rank INTEGER, momentum NUMERIC, momentum_low NUMERIC, momentum_high NUMERIC, momentum_word TEXT,
               momentum_sure TEXT, momentum_rank INTEGER, noise NUMERIC, usual_periods INTEGER, publishers INTEGER,
               first_seen_at TIMESTAMPTZ, vertical TEXT, vertical_share_pct NUMERIC,
               vertical_rank INTEGER, scaled BOOLEAN, last_seen_at TIMESTAMPTZ, running BOOLEAN,
               lifespan_days INTEGER, lifespan_pct NUMERIC, lifespan_curve TEXT, operator_id INTEGER,
               is_junk BOOLEAN)
LANGUAGE sql
AS $$ SELECT * FROM spy.creative_range(p_from, p_to, p_usual_from, p_usual_to, now()) $$;
