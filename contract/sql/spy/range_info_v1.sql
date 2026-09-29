-- The range the range functions read for [p_from, p_to): precision ('hour'
-- or 'day'), its ends, its usual period ('weeks': the same hours 1-3 weeks
-- back; 'before': the whole weeks just before; 'chosen': p_usual_from to
-- p_usual_to), and hours without a single check (collection was down).
CREATE FUNCTION spy_api.range_info_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_usual_from TIMESTAMPTZ DEFAULT NULL,
                                      p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (prec TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, usual_kind TEXT, usual_periods INTEGER,
               usual_from TIMESTAMPTZ, usual_to TIMESTAMPTZ, hours INTEGER, hours_unchecked INTEGER,
               checks BIGINT, sightings BIGINT, checks_usual BIGINT, sightings_usual BIGINT)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.range_info(p_from, p_to, p_usual_from, p_usual_to, now()) $$;
