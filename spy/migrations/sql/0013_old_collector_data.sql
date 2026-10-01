-- lint: new-table (the only index here is on a temporary table inside the function)
-- Room for what spy-numbers import-old now copies from the collector, so
-- nothing it gathered is lost when prodbox goes:
--   grouping_fix.old_id, made_by   a hand fix copied from the collector (its id
--                                  there, and who made it); import-old replaces
--                                  these rows on each run, never the new ones.
--   agency.name, imported          the collector's agencies, with its names; an
--                                  imported agency's root never joins accounts.
--   direction_event.old_id         a change of direction from the collector's
--                                  history (its id there).
--   price_day.imported             a day of prices copied from the collector's
--                                  auction log and sightings; import-old
--                                  replaces these rows on each run.
-- regroup_operators counts the imported agencies, and refresh_prices leaves
-- the copied prices alone, except where Tracks has a row for the same ad,
-- publisher, device and day: Tracks' row replaces the copied one.

ALTER TABLE spy.grouping_fix ADD COLUMN old_id INTEGER;
ALTER TABLE spy.grouping_fix ADD COLUMN made_by TEXT;
ALTER TABLE spy.agency ADD COLUMN name TEXT;
ALTER TABLE spy.agency ADD COLUMN imported BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE spy.direction_event ADD COLUMN old_id BIGINT;
ALTER TABLE spy.price_day ADD COLUMN imported BOOLEAN NOT NULL DEFAULT FALSE;
COMMENT ON COLUMN spy.grouping_fix.old_id IS 'the collector''s grouping_fix id; set by import-old only';
COMMENT ON COLUMN spy.agency.imported IS 'named by the collector (import-old); always an agency';
COMMENT ON COLUMN spy.price_day.imported IS 'copied from the collector by import-old';
COMMENT ON COLUMN spy.direction_event.old_id IS 'the collector''s direction_event id; set by import-old only';

CREATE OR REPLACE FUNCTION spy.regroup_operators(p_now TIMESTAMPTZ DEFAULT now()) RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_cfg JSONB := spy.cfg();
    v_max_sites INTEGER := COALESCE((v_cfg->>'clue_max_sites')::numeric, 20)::int;
    v_apply BOOLEAN := COALESCE(v_cfg->>'operators_from', 'import') = 'grouping';
    v_start TIMESTAMPTZ := clock_timestamp();
    v_groups BIGINT;
BEGIN
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.regroup_operators')) THEN
        RETURN 0;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM spy.site) THEN
        RETURN 0;
    END IF;

    CREATE TEMP TABLE IF NOT EXISTS spy_node (node BIGINT PRIMARY KEY) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_edge (a BIGINT, b BIGINT, reason TEXT) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_label (node BIGINT PRIMARY KEY, label BIGINT) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_split (node BIGINT PRIMARY KEY) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_acct_site (account_id INTEGER, site_id INTEGER, grp BIGINT) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_acct (account_id INTEGER PRIMARY KEY, external_id TEXT, root TEXT, email TEXT,
        groups INTEGER, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_agency_root (root TEXT PRIMARY KEY, groups INTEGER) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_group (node BIGINT PRIMARY KEY, label BIGINT, old_operator INTEGER) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_group_op (label BIGINT PRIMARY KEY, operator_id INTEGER) ON COMMIT DROP;
    TRUNCATE spy_node, spy_edge, spy_label, spy_split, spy_acct_site, spy_acct, spy_agency_root, spy_group, spy_group_op;
    CREATE INDEX IF NOT EXISTS spy_edge_a ON spy_edge (a);

    INSERT INTO spy_split
    SELECT DISTINCT COALESCE(site_id::bigint * 4, account_id::bigint * 4 + 1)
    FROM spy.grouping_fix WHERE action = 'split';

    INSERT INTO spy_acct (account_id, external_id, root, email, groups, first_seen_at, last_seen_at)
    SELECT a.id, a.external_id, spy.account_root(a.external_id), spy.account_email(a.external_id), 0,
           a.first_seen_at, a.last_seen_at
    FROM tracks_api.account_v1 a;

    INSERT INTO spy_node SELECT id::bigint * 4 FROM spy.site;
    INSERT INTO spy_node SELECT account_id::bigint * 4 + 1 FROM spy_acct;

    -- Rule 1: site <-> strong clue.
    INSERT INTO spy_edge (a, b, reason)
    SELECT sc.site_id::bigint * 4, sc.clue_id::bigint * 4 + 2, c.kind || ' ' || c.value
    FROM spy.site_clue sc
    JOIN spy.clue c ON c.id = sc.clue_id
    JOIN (SELECT clue_id FROM spy.site_clue GROUP BY clue_id HAVING count(*) BETWEEN 2 AND v_max_sites) k
      ON k.clue_id = sc.clue_id
    WHERE c.strong;
    DELETE FROM spy_edge WHERE a IN (SELECT node FROM spy_split);
    INSERT INTO spy_edge (a, b, reason) SELECT b, a, reason FROM spy_edge;
    ANALYZE spy_edge;
    PERFORM spy.spread_labels();

    -- Where each account's clicks lead, as site groups from rule 1.
    INSERT INTO spy_acct_site
    SELECT DISTINCT x.account_id, x.site_id, l.label
    FROM spy.account_site x
    JOIN spy_label l ON l.node = x.site_id::bigint * 4
    JOIN spy_acct a ON a.account_id = x.account_id;
    UPDATE spy_acct a SET groups = x.n
    FROM (SELECT account_id, count(DISTINCT grp)::int AS n FROM spy_acct_site GROUP BY 1) x
    WHERE x.account_id = a.account_id;

    -- Rule 3: agency roots buy for 3 or more site groups across their
    -- accounts, or their accounts belong to 3 or more operators today, or
    -- the collector named them (import-old copies its agencies). The
    -- walker has not reached every account yet, so without the second test
    -- an agency whose clients' pages are still unread looks like one
    -- operator and all its accounts would join.
    INSERT INTO spy_agency_root
    SELECT root, max(n)::int FROM (
        SELECT a.root, count(DISTINCT x.grp) AS n
        FROM spy_acct a JOIN spy_acct_site x ON x.account_id = a.account_id
        WHERE a.root <> lower(a.external_id)
        GROUP BY a.root
        HAVING count(DISTINCT a.account_id) >= 2 AND count(DISTINCT x.grp) >= 3
        UNION ALL
        SELECT a.root, count(DISTINCT ao.operator_id)
        FROM spy_acct a JOIN spy.account_operator ao ON ao.account_id = a.account_id
        WHERE a.root <> lower(a.external_id)
        GROUP BY a.root
        HAVING count(DISTINCT a.account_id) >= 2 AND count(DISTINCT ao.operator_id) >= 3
        UNION ALL
        SELECT name_root, operator_count FROM spy.agency WHERE imported
    ) r
    GROUP BY root;

    -- Rule 2: accounts that reach at most 2 site groups join them.
    INSERT INTO spy_edge (a, b, reason)
    SELECT x.account_id::bigint * 4 + 1, x.site_id::bigint * 4, 'buys ads only for this operator''s sites'
    FROM spy_acct_site x JOIN spy_acct a ON a.account_id = x.account_id
    WHERE a.groups BETWEEN 1 AND 2;

    -- Rule 3: accounts sharing a name root that is not an agency's join each other.
    INSERT INTO spy_edge (a, b, reason)
    SELECT a.account_id::bigint * 4 + 1, r.first_id::bigint * 4 + 1, 'same account name root "' || a.root || '"'
    FROM spy_acct a
    JOIN (SELECT root, min(account_id) AS first_id FROM spy_acct GROUP BY root HAVING count(*) >= 2) r USING (root)
    WHERE a.account_id <> r.first_id
      AND a.root NOT IN (SELECT root FROM spy_agency_root);

    -- Rule 5: accounts with the same email in their name join each other,
    -- agency or not: an email is one person.
    INSERT INTO spy_edge (a, b, reason)
    SELECT a.account_id::bigint * 4 + 1, r.first_id::bigint * 4 + 1, 'same email in account name "' || a.email || '"'
    FROM spy_acct a
    JOIN (SELECT email, min(account_id) AS first_id FROM spy_acct WHERE email IS NOT NULL
          GROUP BY email HAVING count(*) >= 2) r USING (email)
    WHERE a.account_id <> r.first_id;

    DELETE FROM spy_edge WHERE a IN (SELECT node FROM spy_split) OR b IN (SELECT node FROM spy_split);
    INSERT INTO spy_edge (a, b, reason)
    SELECT b, a, reason FROM spy_edge e
    WHERE e.a % 4 <> 2 AND e.b % 4 <> 2 AND NOT EXISTS (SELECT 1 FROM spy_edge r WHERE r.a = e.b AND r.b = e.a);
    ANALYZE spy_edge;
    PERFORM spy.spread_labels();

    -- Groups, and the operator each one keeps: the one most of its members
    -- have now. A lone account with no sites and no root or email partner is
    -- not an operator yet.
    INSERT INTO spy_group
    SELECT l.node, l.label,
           CASE WHEN l.node % 4 = 0 THEN (SELECT operator_id FROM spy.site WHERE id = l.node / 4)
                ELSE (SELECT operator_id FROM spy.account_operator WHERE account_id = (l.node - 1) / 4) END
    FROM spy_label l
    WHERE l.node % 4 IN (0, 1)
      AND l.node NOT IN (SELECT node FROM spy_split)
      AND NOT (l.node % 4 = 1 AND NOT EXISTS (SELECT 1 FROM spy_label o WHERE o.label = l.label AND o.node <> l.node)
               AND COALESCE((SELECT groups FROM spy_acct WHERE account_id = (l.node - 1) / 4), 0) = 0);

    INSERT INTO spy_group_op
    SELECT DISTINCT ON (label) label, old_operator
    FROM (SELECT label, old_operator, count(*) AS c FROM spy_group
          WHERE old_operator IS NOT NULL AND old_operator IN (SELECT id FROM spy.operator)
          GROUP BY 1, 2) x
    ORDER BY label, c DESC, old_operator;
    -- An operator can be kept by one group only.
    DELETE FROM spy_group_op g WHERE EXISTS (
        SELECT 1 FROM spy_group_op o WHERE o.operator_id = g.operator_id AND o.label < g.label);
    INSERT INTO spy_group_op (label, operator_id)
    SELECT DISTINCT label, NULL::int FROM spy_group
    WHERE label NOT IN (SELECT label FROM spy_group_op);

    -- Members, with the first edge that tied each in, and hand fixes last.
    DELETE FROM spy.grouping_member;
    DELETE FROM spy.grouping_group;
    INSERT INTO spy.grouping_member (member, member_id, grp, operator_id, reason, agency)
    SELECT CASE WHEN g.node % 4 = 0 THEN 'site' ELSE 'account' END,
           CASE WHEN g.node % 4 = 0 THEN g.node / 4 ELSE (g.node - 1) / 4 END,
           g.label, go.operator_id,
           (SELECT e.reason FROM spy_edge e WHERE e.a = g.node ORDER BY e.reason LIMIT 1),
           CASE WHEN g.node % 4 = 1 THEN (SELECT r.root FROM spy_acct a JOIN spy_agency_root r USING (root)
                                          WHERE a.account_id = (g.node - 1) / 4) END
    FROM spy_group g JOIN spy_group_op go USING (label);
    INSERT INTO spy.grouping_member (member, member_id, grp, operator_id, reason)
    SELECT CASE WHEN f.site_id IS NOT NULL THEN 'site' ELSE 'account' END, COALESCE(f.site_id, f.account_id),
           NULL, f.operator_id,
           CASE f.action WHEN 'join' THEN 'fixed by hand: ' ELSE 'split by hand: ' END || COALESCE(f.note, '')
    FROM (SELECT DISTINCT ON (site_id, account_id) * FROM spy.grouping_fix ORDER BY site_id, account_id, made_at DESC, id DESC) f
    ON CONFLICT (member, member_id) DO UPDATE SET
        grp = CASE WHEN EXCLUDED.operator_id IS NULL THEN NULL ELSE spy.grouping_member.grp END,
        operator_id = EXCLUDED.operator_id, reason = EXCLUDED.reason;

    INSERT INTO spy.grouping_group (grp, operator_id, kind, accounts, sites)
    SELECT go.label, go.operator_id, 'direct',
           count(*) FILTER (WHERE g.node % 4 = 1), count(*) FILTER (WHERE g.node % 4 = 0)
    FROM spy_group_op go JOIN spy_group g USING (label)
    GROUP BY 1, 2;

    -- Kind: an account reaching 3 or more site groups is arbitrage; a site
    -- selling through an affiliate network is affiliate; else direct.
    UPDATE spy.grouping_group gg SET kind = CASE
        WHEN EXISTS (SELECT 1 FROM spy.grouping_member m JOIN spy_acct a ON a.account_id = m.member_id
                     WHERE m.grp = gg.grp AND m.member = 'account' AND a.groups >= 3) THEN 'arbitrage'
        WHEN EXISTS (SELECT 1 FROM spy.grouping_member m
                     JOIN spy.site_seller ss ON ss.site_id = m.member_id
                     JOIN spy.seller se ON se.id = ss.seller_id
                     WHERE m.grp = gg.grp AND m.member = 'site'
                       AND se.platform IN ('ClickBank', 'BuyGoods', 'MaxBounty', 'Digistore24', 'JVZoo')) THEN 'affiliate'
        ELSE 'direct' END;

    -- Display name: typed by hand, else the oldest account running in the
    -- last 7 days, else the oldest account, else the site seen last.
    -- Seller: the one behind most of its sites, never Custom Checkout or
    -- Shopify's "gid" (neither names a seller).
    UPDATE spy.grouping_group gg SET
        display_name = COALESCE(
            (SELECT o.display_name FROM spy.operator o WHERE o.id = gg.operator_id AND o.name_is_manual),
            (SELECT spy.account_display(a.external_id) FROM spy.grouping_member m JOIN spy_acct a ON a.account_id = m.member_id
             WHERE m.grp = gg.grp AND m.member = 'account'
             ORDER BY a.last_seen_at >= p_now - interval '7 days' DESC, a.first_seen_at, a.account_id LIMIT 1),
            (SELECT s.domain FROM spy.grouping_member m JOIN spy.site s ON s.id = m.member_id
             WHERE m.grp = gg.grp AND m.member = 'site' ORDER BY s.last_seen_at DESC, s.domain LIMIT 1)),
        seller = (SELECT se.platform || ' ' || se.account
                  FROM spy.grouping_member m
                  JOIN spy.site_seller ss ON ss.site_id = m.member_id
                  JOIN spy.seller se ON se.id = ss.seller_id
                  WHERE m.grp = gg.grp AND m.member = 'site' AND se.platform <> 'Custom Checkout'
                    AND NOT (se.platform = 'Shopify' AND se.account = 'gid')
                  GROUP BY se.id, se.platform, se.account
                  ORDER BY count(*) DESC, max(ss.last_seen_at) DESC, se.id
                  LIMIT 1);

    -- Agencies.
    INSERT INTO spy.agency (name_root, operator_count, updated_at)
    SELECT root, groups, p_now FROM spy_agency_root
    ON CONFLICT (name_root) DO UPDATE SET operator_count = EXCLUDED.operator_count, updated_at = EXCLUDED.updated_at;

    IF v_apply THEN
        -- New groups get new operators.
        WITH created AS (
            INSERT INTO spy.operator (name)
            SELECT 'group ' || grp FROM spy.grouping_group WHERE operator_id IS NULL
            RETURNING id, name
        )
        UPDATE spy.grouping_group g SET operator_id = c.id
        FROM created c WHERE g.operator_id IS NULL AND c.name = 'group ' || g.grp;
        UPDATE spy.grouping_member m SET operator_id = g.operator_id
        FROM spy.grouping_group g
        WHERE g.grp = m.grp AND m.operator_id IS NULL;

        UPDATE spy.operator o SET
            display_name = CASE WHEN o.name_is_manual THEN o.display_name ELSE COALESCE(g.display_name, o.display_name) END,
            kind = g.kind,
            seller = g.seller,
            name = concat_ws(' · ', CASE WHEN o.name_is_manual THEN o.display_name ELSE COALESCE(g.display_name, o.display_name) END,
                             g.seller, o.code),
            updated_at = p_now
        FROM spy.grouping_group g WHERE g.operator_id = o.id;

        -- An account on no landing page yet keeps its operator.
        DELETE FROM spy.account_operator ao WHERE EXISTS (
            SELECT 1 FROM spy.grouping_member m
            WHERE m.member = 'account' AND m.member_id = ao.account_id AND m.operator_id IS NULL);
        INSERT INTO spy.account_operator (account_id, operator_id)
        SELECT member_id, operator_id FROM spy.grouping_member WHERE member = 'account' AND operator_id IS NOT NULL
        ON CONFLICT (account_id) DO UPDATE SET operator_id = EXCLUDED.operator_id
        WHERE spy.account_operator.operator_id <> EXCLUDED.operator_id;
        UPDATE spy.site s SET operator_id = m.operator_id, group_reason = m.reason
        FROM spy.grouping_member m WHERE m.member = 'site' AND m.member_id = s.id
          AND (s.operator_id, s.group_reason) IS DISTINCT FROM (m.operator_id, m.reason);
        UPDATE spy.site s SET operator_id = NULL, group_reason = NULL
        WHERE s.operator_id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM spy.grouping_member m WHERE m.member = 'site' AND m.member_id = s.id);

        -- Operators holding nothing go, unless a hand fix names them.
        DELETE FROM spy.operator o
        WHERE NOT EXISTS (SELECT 1 FROM spy.account_operator WHERE operator_id = o.id)
          AND NOT EXISTS (SELECT 1 FROM spy.site WHERE operator_id = o.id)
          AND NOT EXISTS (SELECT 1 FROM spy.grouping_fix WHERE operator_id = o.id);
    END IF;

    SELECT count(*) INTO v_groups FROM spy.grouping_group;
    INSERT INTO spy.grouping_run (at, applied, groups, sites, accounts, agencies, accounts_same, accounts_moved,
                                  accounts_new, accounts_unseen, took_ms)
    SELECT p_now, v_apply, v_groups,
           (SELECT count(*) FROM spy.grouping_member WHERE member = 'site' AND grp IS NOT NULL),
           (SELECT count(*) FROM spy.grouping_member WHERE member = 'account' AND grp IS NOT NULL),
           (SELECT count(*) FROM spy_agency_root),
           count(*) FILTER (WHERE ao.operator_id IS NOT NULL AND ao.operator_id = m.operator_id),
           count(*) FILTER (WHERE ao.operator_id IS NOT NULL AND m.member_id IS NOT NULL
                                  AND m.operator_id IS DISTINCT FROM ao.operator_id),
           count(*) FILTER (WHERE ao.operator_id IS NULL AND (m.grp IS NOT NULL OR m.operator_id IS NOT NULL)),
           count(*) FILTER (WHERE ao.operator_id IS NOT NULL AND m.member_id IS NULL),
           (extract(epoch FROM clock_timestamp() - v_start) * 1000)::int
    FROM spy.account_operator ao
    FULL JOIN (SELECT member_id, grp, operator_id FROM spy.grouping_member WHERE member = 'account') m
      ON m.member_id = ao.account_id;
    DELETE FROM spy.grouping_run WHERE at < p_now - interval '30 days';
    RETURN v_groups;
END;
$$;

-- As in 0008, but the rows import-old copied stay, unless Tracks has the
-- same ad, publisher, device and day.
CREATE OR REPLACE FUNCTION spy.refresh_prices(p_now TIMESTAMPTZ DEFAULT now()) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    today DATE := (p_now AT TIME ZONE 'UTC')::date;
    since DATE;
    n BIGINT;
BEGIN
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.refresh_prices')) THEN
        RETURN 0;
    END IF;
    SELECT LEAST(done_through, today - 1) INTO since FROM spy.price_state;
    since := COALESCE(since, today - 14);

    DELETE FROM spy.price_day WHERE day >= since AND NOT imported;
    WITH tab AS (
        SELECT (a.seen_at AT TIME ZONE 'UTC')::date AS day, a.ad_id, a.publisher_id, a.device_id,
               count(*) AS auctions, count(*) FILTER (WHERE a.is_rtb) AS rtb,
               count(a.clearing_price) AS clearing_n, COALESCE(sum(a.clearing_price), 0) AS clearing_sum,
               percentile_cont(0.25) WITHIN GROUP (ORDER BY a.clearing_price) AS clearing_p25,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY a.clearing_price) AS clearing_p50,
               percentile_cont(0.75) WITHIN GROUP (ORDER BY a.clearing_price) AS clearing_p75,
               count(a.bid_value) AS bid_n, COALESCE(sum(a.bid_value), 0) AS bid_sum,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY a.bid_value) AS bid_p50,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY a.cap_auction_price) AS cap_p50
        FROM tracks_api.auction_v1 a
        WHERE a.seen_at >= since::timestamp AT TIME ZONE 'UTC'
          AND a.seen_at < (today + 1)::timestamp AT TIME ZONE 'UTC'
        GROUP BY 1, 2, 3, 4
    ),
    nb AS (
        SELECT (s.seen_at AT TIME ZONE 'UTC')::date AS day, s.ad_id, s.publisher_id, s.device_id,
               count(s.bid_price) AS bid_n, COALESCE(sum(s.bid_price), 0) AS bid_sum,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY s.bid_price) AS bid_p50,
               count(s.second_price) AS second_n, COALESCE(sum(s.second_price), 0) AS second_sum,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY s.second_price) AS second_p50
        FROM tracks_api.sighting_v1 s
        WHERE s.seen_at >= since::timestamp AT TIME ZONE 'UTC'
          AND s.seen_at < (today + 1)::timestamp AT TIME ZONE 'UTC'
          AND (s.bid_price IS NOT NULL OR s.second_price IS NOT NULL)
        GROUP BY 1, 2, 3, 4
    )
    INSERT INTO spy.price_day (day, ad_id, creative_id, publisher_id, device_id, auctions, rtb, clearing_n,
                                clearing_sum, clearing_p25, clearing_p50, clearing_p75, bid_n, bid_sum, bid_p50,
                                cap_p50, second_n, second_sum, second_p50, imported)
    SELECT COALESCE(t.day, b.day), COALESCE(t.ad_id, b.ad_id), ad.creative_id,
           COALESCE(t.publisher_id, b.publisher_id), COALESCE(t.device_id, b.device_id),
           COALESCE(t.auctions, 0), COALESCE(t.rtb, 0), COALESCE(t.clearing_n, 0), COALESCE(t.clearing_sum, 0),
           t.clearing_p25, t.clearing_p50, t.clearing_p75,
           COALESCE(t.bid_n, 0) + COALESCE(b.bid_n, 0), COALESCE(t.bid_sum, 0) + COALESCE(b.bid_sum, 0),
           COALESCE(t.bid_p50, b.bid_p50), t.cap_p50,
           COALESCE(b.second_n, 0), COALESCE(b.second_sum, 0), b.second_p50, FALSE
    FROM tab t
    FULL JOIN nb b ON b.day = t.day AND b.ad_id = t.ad_id AND b.publisher_id = t.publisher_id AND b.device_id = t.device_id
    JOIN tracks_api.ad_v1 ad ON ad.id = COALESCE(t.ad_id, b.ad_id)
    ON CONFLICT (day, ad_id, publisher_id, device_id) DO UPDATE SET
        creative_id = EXCLUDED.creative_id, auctions = EXCLUDED.auctions, rtb = EXCLUDED.rtb,
        clearing_n = EXCLUDED.clearing_n, clearing_sum = EXCLUDED.clearing_sum, clearing_p25 = EXCLUDED.clearing_p25,
        clearing_p50 = EXCLUDED.clearing_p50, clearing_p75 = EXCLUDED.clearing_p75, bid_n = EXCLUDED.bid_n,
        bid_sum = EXCLUDED.bid_sum, bid_p50 = EXCLUDED.bid_p50, cap_p50 = EXCLUDED.cap_p50,
        second_n = EXCLUDED.second_n, second_sum = EXCLUDED.second_sum, second_p50 = EXCLUDED.second_p50,
        imported = FALSE;
    GET DIAGNOSTICS n = ROW_COUNT;

    INSERT INTO spy.price_state (done_through, refreshed_at) VALUES (today, now())
    ON CONFLICT (id) DO UPDATE SET done_through = EXCLUDED.done_through, refreshed_at = EXCLUDED.refreshed_at;
    RETURN GREATEST(n, 1);
END;
$$;
