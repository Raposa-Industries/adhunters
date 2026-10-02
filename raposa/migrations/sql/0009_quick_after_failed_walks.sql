-- Automatic quick investigations only for ads whose landing page tracks-walker
-- could not get, as the collector did (its funnel walker): a creative is
-- queued when the walker tried the saved link of its ads at least
-- quick_walk_failures times and no walk read the landing page whole. A
-- creative the walker has not tried yet waits for it; one it read is never
-- queued on its own.

INSERT INTO raposa.setting (key, value, note) VALUES
    ('quick_walk_failures', '2',
     'Walks of a creative''s ads by tracks-walker that must fail to read the landing page, with none reading it, before Raposa queues a quick investigation of it on its own.')
ON CONFLICT (key) DO NOTHING;

UPDATE raposa.setting SET note = 'Quick investigations Raposa keeps waiting on its own, for new ads whose landing page tracks-walker could not get; the engine tops the queue up every 5 minutes. 0 turns automatic quick investigations off.'
WHERE key = 'quick_queue_depth';

-- Tops the automatic quick queue up to quick_queue_depth and returns how
-- many it queued. A creative is picked when an ad of it on one of
-- quick_networks was seen in the last hour (so a live link exists), Tracks
-- first saw the creative within quick_new_days, tracks-walker's landing page
-- (step 0) of its ads failed at least quick_walk_failures times and was never
-- read whole, no visit of an investigation of it landed on a usable page, and
-- none waits or runs or was asked for within quick_repeat_hours (a stopped
-- one does not count: it did not finish). Two callers at once: the second
-- queues nothing.
CREATE OR REPLACE FUNCTION raposa.queue_quick() RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    v_depth  INTEGER := raposa.setting_int('quick_queue_depth', 30);
    v_repeat INTEGER := raposa.setting_int('quick_repeat_hours', 24);
    v_days   INTEGER := raposa.setting_int('quick_new_days', 7);
    v_fails  INTEGER := GREATEST(raposa.setting_int('quick_walk_failures', 2), 1);
    v_nets   TEXT[];
    v_room   INTEGER;
    v_rows   INTEGER;
BEGIN
    IF NOT pg_try_advisory_xact_lock(hashtext('raposa.queue_quick')) THEN
        RETURN 0;
    END IF;
    SELECT array_agg(btrim(n)) INTO v_nets
    FROM unnest(string_to_array(COALESCE((SELECT value FROM raposa.setting WHERE key = 'quick_networks'), 'taboola'), ',')) n
    WHERE btrim(n) <> '';
    SELECT v_depth - count(*) INTO v_room FROM raposa.investigation WHERE mode = 'quick' AND status = 'waiting';
    IF v_room <= 0 OR v_nets IS NULL THEN
        RETURN 0;
    END IF;

    INSERT INTO raposa.investigation (creative_id, mode, origin, requested_by, stage_note)
    SELECT c.creative_id, 'quick', 'auto', 'raposa',
           'queued automatically: tracks-walker could not get the landing page of this new ad'
    FROM (
        SELECT a.creative_id
        FROM tracks_api.ad_v1 a
        JOIN tracks_api.network_ad_v1 na ON na.ad_id = a.id
        JOIN tracks_api.network_v1 n ON n.id = na.network_id
        WHERE a.last_seen_at > now() - interval '1 hour'
          AND n.code = ANY (v_nets)
        GROUP BY a.creative_id
    ) c
    JOIN tracks_api.creative_v1 cr ON cr.id = c.creative_id
    -- What the walker got of the landing page of the creative's ads.
    CROSS JOIN LATERAL (
        SELECT count(*) FILTER (WHERE NOT raposa.usable_page(pv.title, pv.word_count)) AS failed,
               count(*) FILTER (WHERE raposa.usable_page(pv.title, pv.word_count)) AS read
        FROM tracks_api.ad_v1 ca
        JOIN tracks_api.walk_page_v1 w ON w.ad_id = ca.id AND w.step = 0
        LEFT JOIN tracks_api.page_version_v1 pv ON pv.hash = w.version_hash
        WHERE ca.creative_id = c.creative_id
    ) wk
    WHERE cr.first_seen_at > now() - make_interval(days => v_days)
      AND wk.read = 0 AND wk.failed >= v_fails
      AND NOT EXISTS (
            SELECT 1 FROM raposa.investigation i
            JOIN raposa.visit v ON v.investigation_id = i.id
            JOIN raposa.page p ON p.id = v.landed_page_id
            WHERE i.creative_id = c.creative_id AND raposa.usable_page(p.title, p.word_count))
      AND NOT EXISTS (
            SELECT 1 FROM raposa.investigation i
            WHERE i.creative_id = c.creative_id
              AND (i.status IN ('waiting', 'running')
                   OR (i.requested_at > now() - make_interval(hours => v_repeat) AND i.status <> 'stopped')))
    -- The newest ads first.
    ORDER BY cr.first_seen_at DESC, c.creative_id
    LIMIT v_room;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    RETURN v_rows;
END;
$$;
