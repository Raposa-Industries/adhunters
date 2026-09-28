-- The published face of Raposa, version 1. Each statement is the matching
-- file in contract/sql/raposa/, word for word (a test checks). Other
-- services read these and call these, and nothing else of Raposa.

CREATE SCHEMA raposa_api;

-- One investigation and where it stands.
CREATE VIEW raposa_api.investigation_v1 AS
SELECT id, creative_id, ad_id, mode, origin, requested_by, requested_at, status, stop_requested,
       stage, stage_note, target_device, burn_scope, rung_reached, breach_rung, white_page_id,
       visits_target, visits_done, variants_count, bytes_used, is_cloaked, cloaked_confidence,
       retry_of, attempt, next_visit_at, started_at, completed_at
FROM raposa.investigation;

-- One version of one page an investigation reached. The HTML is not here:
-- raposa-web shows it.
CREATE VIEW raposa_api.page_v1 AS
SELECT id, page_key, url, host, path, title, page_kind, word_count, is_dark, checkout_platform,
       checkout_merchant_id, capture_state, first_seen_at, last_seen_at, times_seen
FROM raposa.page;

-- One distinct dark funnel of one investigation and its share of the sample.
CREATE VIEW raposa_api.variant_v1 AS
SELECT id, investigation_id, label, page_ids, first_page_id, visits, share_pct, first_seen_at, last_seen_at
FROM raposa.variant;

-- A line one site shows the white page to. ended_at is NULL while it lasts.
CREATE VIEW raposa_api.line_burn_v1 AS
SELECT scope, line_key, rung, visits, dark, other_visits, other_dark, detected_at, checked_at, ended_at
FROM raposa.line_burn;

-- The landing pages investigations reached, as proof of who runs an ad.
CREATE VIEW raposa_api.evidence_v1 AS
SELECT id, investigation_id, creative_id, ad_id, account_id, campaign_external_id, device, outcome,
       landing_page_id, final_url, domain, redirect_hops, page_kind, title, pixels, checkout_platform,
       checkout_merchant_id, seller_platform, seller_account, funnel_steps, recorded_at
FROM raposa.evidence;

-- Asks for an investigation of a creative (and of one of its ads, when
-- given): 'deep' or 'quick'. When the creative already has one waiting or
-- running, that one's id comes back and nothing new is queued.
CREATE FUNCTION raposa_api.request_investigation_v1(p_creative_id INTEGER, p_mode TEXT, p_ad_id INTEGER DEFAULT NULL, p_requested_by TEXT DEFAULT '')
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF p_mode NOT IN ('deep', 'quick') THEN
        RAISE EXCEPTION 'mode must be deep or quick, not %', p_mode;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('raposa request ' || p_creative_id));
    SELECT id INTO v_id FROM raposa.investigation
    WHERE creative_id = p_creative_id AND status IN ('waiting', 'running')
    ORDER BY id LIMIT 1;
    IF FOUND THEN
        RETURN v_id;
    END IF;
    INSERT INTO raposa.investigation (creative_id, ad_id, mode, origin, requested_by, visits_target)
    VALUES (p_creative_id, p_ad_id, p_mode, 'user', COALESCE(p_requested_by, ''),
            CASE WHEN p_mode = 'deep' THEN raposa.setting_int('visits_target', 100) ELSE 0 END)
    RETURNING id INTO v_id;
    RETURN v_id;
END;
$$;

-- Asks an investigation to stop. One that has not started stops at once; a
-- running one stops at its next visit and keeps what it found. Returns false
-- when it had already ended.
CREATE FUNCTION raposa_api.stop_investigation_v1(p_id BIGINT)
RETURNS BOOLEAN
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    UPDATE raposa.investigation
    SET status = 'stopped', stop_requested = TRUE, stage = 'done',
        stage_note = 'stopped before it started', completed_at = now()
    WHERE id = p_id AND status = 'waiting' AND claim_token IS NULL;
    IF FOUND THEN
        PERFORM raposa.emit(p_id, 'finished', 'Investigation stopped', 'stopped before it started');
        RETURN TRUE;
    END IF;
    UPDATE raposa.investigation
    SET stop_requested = TRUE, next_visit_at = LEAST(next_visit_at, now())
    WHERE id = p_id AND status IN ('waiting', 'running');
    RETURN FOUND;
END;
$$;

-- Readers get the published views through the raposa_api_read role, which
-- the box setup creates and grants to each reading service's login.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'raposa_api_read') THEN
        GRANT USAGE ON SCHEMA raposa_api TO raposa_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA raposa_api TO raposa_api_read;
        GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA raposa_api TO raposa_api_read;
    END IF;
END;
$$;
REVOKE EXECUTE ON FUNCTION raposa_api.request_investigation_v1(INTEGER, TEXT, INTEGER, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION raposa_api.stop_investigation_v1(BIGINT) FROM PUBLIC;
