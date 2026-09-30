-- lint: new-table
-- What other services may read of Launch, and how Desk asks it for a
-- change. Each launch_api statement is the matching file in
-- contract/sql/launch/, word for word (a test checks). Other services read
-- these and call this, and nothing else of Launch.

-- A request is a change another service asks for (Desk, for the person it
-- works for). Nothing is sent until a person confirms it on its page in
-- Launch: waiting, then confirmed and sent (or failed), or refused.
CREATE TABLE launch.request (
    id bigserial PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('pause', 'pause_ads', 'change', 'duplicate', 'move')),
    input jsonb NOT NULL,
    requested_by text NOT NULL DEFAULT '',
    origin text NOT NULL UNIQUE CHECK (origin <> ''),
    state text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'confirmed', 'sent', 'refused', 'failed')),
    confirmed_by text NOT NULL DEFAULT '',
    result jsonb,
    made_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz
);
CREATE INDEX request_waiting ON launch.request (made_at) WHERE state = 'waiting';

-- An item is one ad Launch made on a network (in a new pair, or a copy),
-- with our ad id, so readers can join the network's numbers to our ads.
CREATE TABLE launch.item (
    network text NOT NULL,
    account text NOT NULL,
    campaign_id text NOT NULL,
    item_id text NOT NULL,
    ad_id text NOT NULL DEFAULT '',
    made_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (network, account, campaign_id, item_id)
);

CREATE SCHEMA launch_api;

-- One request and where it stands. input is what was asked, as sent.
CREATE VIEW launch_api.request_v1 AS
SELECT id, kind, input, requested_by, origin, state, confirmed_by, result, made_at, decided_at
FROM launch.request;

-- A campaign moved to another group: Taboola cannot change a campaign's
-- group, so a move is a paused copy there with a new id. The ids are
-- numbers (NULL for a network whose ids are not).
CREATE VIEW launch_api.campaign_move_v1 AS
SELECT m.id, m.network, m.account,
       CASE WHEN m.from_campaign ~ '^[0-9]{1,18}$' THEN m.from_campaign::bigint END AS old_campaign_id,
       CASE WHEN m.to_campaign ~ '^[0-9]{1,18}$' THEN m.to_campaign::bigint END AS new_campaign_id,
       m.from_campaign, m.to_campaign, m.to_group, m.originals, m.state, m.made_at AS moved_at, m.done_at
FROM launch.move m;

-- The ads Launch made, with our ad id (ah-…). The network's ids are also
-- given as numbers where they are numbers.
CREATE VIEW launch_api.item_v1 AS
SELECT network, account, campaign_id, item_id,
       CASE WHEN campaign_id ~ '^[0-9]{1,18}$' THEN campaign_id::bigint END AS campaign_number,
       CASE WHEN item_id ~ '^[0-9]{1,18}$' THEN item_id::bigint END AS item_number,
       ad_id, made_at
FROM launch.item;

-- The desktop and mobile campaigns Launch made together.
CREATE VIEW launch_api.pair_v1 AS
SELECT id, network, account, group_id, name, desktop_id, mobile_id, preset_id, made_by, made_at
FROM launch.pair;

-- The presets people saved, for a new group or a new pair's campaigns.
-- account '' is for every account of the network.
CREATE VIEW launch_api.preset_v1 AS
SELECT id, level, network, account, name, fields, made_by, made_at, changed_by, changed_at
FROM launch.preset;

-- History: each thing Launch did on a network, who did it and who asked.
CREATE VIEW launch_api.change_v1 AS
SELECT id, at, who, asked_by, network, account, group_id, campaign_id, kind, summary, before, after, result, problems
FROM launch.change;

-- Asks Launch for a change; a person confirms it in Launch before anything
-- is sent. p_kind is pause, pause_ads, change, duplicate or move; p_input
-- holds network, account, campaigns (ids) and, by kind, ads (ids), change
-- ({cpc, daily_cap, spending_limit, name}), to_group and originals
-- (when_started, now, leave). p_origin names the ask ("desk:42"): asking
-- again with the same origin returns the same request.
CREATE FUNCTION launch_api.new_request_v1(p_kind TEXT, p_input JSONB, p_requested_by TEXT, p_origin TEXT)
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF p_kind IS NULL OR p_kind NOT IN ('pause', 'pause_ads', 'change', 'duplicate', 'move') THEN
        RAISE EXCEPTION 'kind must be pause, pause_ads, change, duplicate or move, not %', p_kind;
    END IF;
    IF jsonb_typeof(p_input) IS DISTINCT FROM 'object'
       OR COALESCE(p_input->>'network', '') = '' OR COALESCE(p_input->>'account', '') = ''
       OR jsonb_typeof(p_input->'campaigns') IS DISTINCT FROM 'array' OR jsonb_array_length(p_input->'campaigns') = 0 THEN
        RAISE EXCEPTION 'input needs network, account and campaigns';
    END IF;
    IF COALESCE(p_origin, '') = '' THEN
        RAISE EXCEPTION 'origin is needed';
    END IF;
    INSERT INTO launch.request (kind, input, requested_by, origin)
    VALUES (p_kind, p_input, COALESCE(p_requested_by, ''), p_origin)
    ON CONFLICT (origin) DO NOTHING
    RETURNING id INTO v_id;
    IF v_id IS NULL THEN
        SELECT id INTO v_id FROM launch.request WHERE origin = p_origin;
    END IF;
    RETURN v_id;
END;
$$;

-- Readers get the published views through the launch_api_read role, which
-- the box setup creates and grants to each reading service's login.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'launch_api_read') THEN
        GRANT USAGE ON SCHEMA launch_api TO launch_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA launch_api TO launch_api_read;
        GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA launch_api TO launch_api_read;
    END IF;
END;
$$;
REVOKE EXECUTE ON FUNCTION launch_api.new_request_v1(TEXT, JSONB, TEXT, TEXT) FROM PUBLIC;
