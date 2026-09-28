-- Quick investigations queued without anyone asking, for the new ads Raposa
-- has no evidence for yet. The collector's spy.queue_quick_investigations
-- (its migrations 035 and 036), with one change in how an ad is picked: the
-- collector queued ads whose landing page its funnel walker never read
-- whole, and Raposa does not read the walker. It queues the ads Tracks first
-- saw lately, that run now, and whose landing page no investigation has read
-- whole, newest first.
--
-- The engine calls raposa.queue_quick() every 5 minutes. quick_queue_depth
-- set to 0 turns it off.

INSERT INTO raposa.setting (key, value, note) VALUES
    ('quick_queue_depth', '30',
     'Quick investigations Raposa keeps waiting on its own; the engine tops the queue up every 5 minutes. 0 turns automatic quick investigations off.'),
    ('quick_repeat_hours', '24',
     'No investigation of a creative is queued automatically within this many hours of its last one.'),
    ('quick_new_days', '7',
     'Only creatives Tracks first saw within this many days are queued automatically.'),
    ('quick_networks', 'taboola',
     'The ad networks (tracks_api.network_v1 codes, comma separated) whose ads are queued automatically.')
ON CONFLICT (key) DO NOTHING;

-- A landing page read whole: 40 words or more, and not a bot check or an
-- error page. Bot checks and error pages carry a title and few words. The
-- collector's spy.usable_landing_version.
CREATE FUNCTION raposa.usable_page(p_title TEXT, p_words INTEGER) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE AS $$
    SELECT COALESCE(p_words, 0) >= 40
       AND COALESCE(p_title, '') !~* '(just a moment|attention required|access denied|access to this page has been denied|checking your browser|bad gateway|gateway time-?out|service unavailable|something went wrong|^redirecting|^40[34]\M|not found|forbidden)'
$$;

-- Tops the automatic quick queue up to quick_queue_depth and returns how
-- many it queued. A creative is picked when an ad of it on one of
-- quick_networks was seen in the last hour (so a live link exists), Tracks
-- first saw the creative within quick_new_days, no visit of an
-- investigation of it landed on a usable page, and none waits or runs or was asked
-- for within quick_repeat_hours (a stopped one does not count: it did not
-- finish). Two callers at once: the second queues nothing.
CREATE FUNCTION raposa.queue_quick() RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    v_depth  INTEGER := raposa.setting_int('quick_queue_depth', 30);
    v_repeat INTEGER := raposa.setting_int('quick_repeat_hours', 24);
    v_days   INTEGER := raposa.setting_int('quick_new_days', 7);
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
           'queued automatically: a new ad Raposa has not read the landing page of'
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
    WHERE cr.first_seen_at > now() - make_interval(days => v_days)
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
