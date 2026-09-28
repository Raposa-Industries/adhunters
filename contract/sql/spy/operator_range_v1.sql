-- Operators over any range, one row per operator and network, with their
-- launches in the range and their hit rate. See spy/METRICS.md.
CREATE FUNCTION spy_api.operator_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ,
                                          p_usual_from TIMESTAMPTZ DEFAULT NULL, p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (operator_id INTEGER, network_id INTEGER, sightings BIGINT, sightings_usual BIGINT, checks BIGINT,
               checks_usual BIGINT, presence NUMERIC, presence_usual NUMERIC, phone_presence NUMERIC,
               desktop_presence NUMERIC, share_pct NUMERIC, share_usual_pct NUMERIC, share_gain_pts NUMERIC,
               rank INTEGER, momentum NUMERIC, momentum_low NUMERIC, momentum_high NUMERIC, momentum_word TEXT,
               momentum_sure TEXT, momentum_rank INTEGER,
               fall_on_one_publisher BOOLEAN, noise NUMERIC, usual_periods INTEGER, publishers INTEGER,
               first_seen_at TIMESTAMPTZ, launches INTEGER, hits INTEGER, misses INTEGER,
               testing INTEGER, hit_rate_pct NUMERIC, hit_rate_low_pct NUMERIC, hit_rate_high_pct NUMERIC)
LANGUAGE sql
AS $$ SELECT * FROM spy.operator_range(p_from, p_to, p_usual_from, p_usual_to, now()) $$;
