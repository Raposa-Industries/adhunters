-- Verticals over any range, one row per vertical and network. See
-- spy/METRICS.md.
CREATE FUNCTION spy_api.vertical_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ,
                                          p_usual_from TIMESTAMPTZ DEFAULT NULL, p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (vertical TEXT, network_id INTEGER, sightings BIGINT, sightings_usual BIGINT, checks BIGINT,
               checks_usual BIGINT, presence NUMERIC, presence_usual NUMERIC, phone_presence NUMERIC,
               desktop_presence NUMERIC, share_pct NUMERIC, share_usual_pct NUMERIC, share_gain_pts NUMERIC,
               rank INTEGER, momentum NUMERIC, momentum_low NUMERIC, momentum_high NUMERIC, momentum_word TEXT,
               momentum_sure TEXT, momentum_rank INTEGER, noise NUMERIC, usual_periods INTEGER, publishers INTEGER,
               first_seen_at TIMESTAMPTZ)
LANGUAGE sql
AS $$ SELECT * FROM spy.subject_range('vertical', p_from, p_to, p_usual_from, p_usual_to, now()) $$;
