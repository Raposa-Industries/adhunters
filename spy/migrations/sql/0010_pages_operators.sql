-- lint: new-table
-- Landing pages and operators from Tracks' walks (tracks_api.walk_page_v1
-- and page_version_v1). Ported from the collector at e20148c: sites, clues
-- and sellers as its funnel walker filled them (013, 015, 021), and
-- spy.regroup_operators as in 016 and 033.
--
-- spy.refresh_pages reads the walks since its last run into sites (a landing
-- page's registrable domain), the clues each site's pages carry (pixel ids,
-- emails, company names), sellers, which accounts' clicks reached which
-- sites, and each creative's newest landing page for the classifier.
--
-- spy.regroup_operators groups sites and accounts into operators by the
-- collector's rules. It runs in shadow first: it writes its answer to
-- spy.grouping_group and spy.grouping_member and compares it with the
-- operators import-old copies, and changes spy.operator and
-- spy.account_operator only when the setting operators_from is 'grouping'.
-- Then import-old stops copying operators.

INSERT INTO spy.setting (name, value, text_value, note) VALUES
    ('operators_from', NULL, 'import',
     'Where operators come from: import (import-old copies the collector''s) or grouping (spy.regroup_operators applies its own). Until grouping, regrouping only proposes.'),
    ('clue_max_sites', 20, NULL,
     'A strong clue found on more sites than this joins nothing: it is a template, a platform or a shared tool, not one business.');

-- Operator fields the grouping fills.
ALTER TABLE spy.operator ADD COLUMN seller TEXT;                                    -- "ClickBank slimpro", behind most of its pages
ALTER TABLE spy.operator ADD COLUMN name_is_manual BOOLEAN NOT NULL DEFAULT FALSE;  -- display_name typed by hand; regrouping keeps it

-- The classifier reads a creative again when its landing page changes.
ALTER TABLE spy.creative_class ADD COLUMN input_page_at TIMESTAMPTZ;

-- Domains many businesses share. A hosting domain's sites are its hosts
-- (shop-a.myshopify.com and shop-b.myshopify.com are two sites); a platform
-- domain is never a site (a ClickBank order page is not the operator's).
-- Add a row here, no deploy needed; pages already read keep their site until
-- read again (refresh_pages with p_since).
CREATE TABLE spy.shared_domain (
    domain TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('hosting', 'platform')),
    note TEXT
);
INSERT INTO spy.shared_domain (domain, kind) VALUES
    ('myshopify.com', 'hosting'), ('vercel.app', 'hosting'), ('netlify.app', 'hosting'), ('pages.dev', 'hosting'),
    ('github.io', 'hosting'), ('webflow.io', 'hosting'), ('wixsite.com', 'hosting'), ('herokuapp.com', 'hosting'),
    ('lovable.app', 'hosting'), ('squarespace.com', 'hosting'), ('godaddysites.com', 'hosting'),
    ('blogspot.com', 'hosting'), ('wordpress.com', 'hosting'), ('web.app', 'hosting'), ('firebaseapp.com', 'hosting'),
    ('azurewebsites.net', 'hosting'), ('amazonaws.com', 'hosting'), ('cloudfront.net', 'hosting'),
    ('onrender.com', 'hosting'), ('carrd.co', 'hosting'), ('framer.website', 'hosting'), ('systeme.io', 'hosting'),
    ('clickfunnels.com', 'hosting'), ('myclickfunnels.com', 'hosting'), ('leadconnectorhq.com', 'hosting'),
    ('kajabi.com', 'hosting'), ('mykajabi.com', 'hosting'),
    ('clickbank.net', 'platform'), ('clickbank.com', 'platform'), ('digistore24.com', 'platform'),
    ('buygoods.com', 'platform'), ('jvzoo.com', 'platform'), ('maxweb.com', 'platform'), ('hotmart.com', 'platform'),
    ('stripe.com', 'platform'), ('paypal.com', 'platform'), ('shopify.com', 'platform'),
    ('checkoutchamp.com', 'platform'), ('ultracart.com', 'platform'), ('samcart.com', 'platform'),
    ('thrivecart.com', 'platform'), ('cartpanda.com', 'platform'), ('amazon.com', 'platform'),
    ('google.com', 'platform'), ('youtube.com', 'platform'), ('facebook.com', 'platform'),
    ('instagram.com', 'platform'), ('tiktok.com', 'platform'), ('taboola.com', 'platform'),
    ('newsbreak.com', 'platform'), ('apple.com', 'platform'), ('bit.ly', 'platform'), ('linktr.ee', 'platform');

-- Registrable domain of a host: "www.go.example.co.uk" -> "example.co.uk".
-- The collector's spy.site_domain (013).
CREATE FUNCTION spy.site_domain(p_host TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE
        WHEN n >= 3 AND p[n - 1] IN ('co', 'com', 'ac', 'org', 'net', 'gov', 'edu') AND length(p[n]) = 2
            THEN array_to_string(p[n - 2:n], '.')
        WHEN n >= 2
            THEN array_to_string(p[n - 1:n], '.')
        ELSE array_to_string(p, '.')
    END
    FROM (SELECT p, COALESCE(array_length(p, 1), 0) AS n
          FROM (SELECT string_to_array(regexp_replace(lower(split_part(p_host, ':', 1)), '^(\*\.|www\.)', ''), '.') AS p) y) x
$$;

-- The site a host belongs to: its registrable domain, the whole host on a
-- hosting domain or an address, NULL on a platform domain or no host.
CREATE FUNCTION spy.site_of(p_host TEXT)
RETURNS TEXT
LANGUAGE sql STABLE
AS $$
    SELECT CASE
        WHEN h ~ '^[0-9.]+$' THEN h
        WHEN d.kind = 'platform' THEN NULL
        WHEN d.kind = 'hosting' THEN h
        ELSE r.dom
    END
    FROM (SELECT regexp_replace(lower(split_part(p_host, ':', 1)), '^www\.', '') AS h, spy.site_domain(p_host) AS dom) r
    LEFT JOIN spy.shared_domain d ON d.domain = r.dom
    WHERE COALESCE(p_host, '') <> ''
$$;

-- What one thing read on a page proves, as a clue: its kind, its value, and
-- whether it is strong (one business) or only a hint. The collector's
-- spy.clue_from_legacy (015, 021), for what Tracks' page reader finds: pixel
-- ids by pixel (facebook, google, tiktok, ...), emails and company names.
CREATE FUNCTION spy.clue_of(p_type TEXT, p_value TEXT)
RETURNS TABLE (kind TEXT, value TEXT, strong BOOLEAN)
LANGUAGE sql STABLE
AS $$
    SELECT k.kind, k.value, k.strong FROM (
        SELECT CASE
            WHEN p_type = 'google' AND p_value ~ '^AW-\d{9,11}$' THEN 'google_ads'
            WHEN p_type = 'google' AND p_value ~ '^(UA-\d{6,10}-\d{1,3}|G-[A-Z0-9]{8,12})$' THEN 'google_analytics'
            WHEN p_type = 'google' AND p_value ~ '^GTM-[A-Z0-9]{5,8}$' THEN 'google_tag_manager'
            WHEN p_type = 'facebook' AND p_value ~ '^\d{15,16}$' THEN 'meta_pixel'
            WHEN p_type = 'tiktok' AND upper(p_value) ~ '^[A-Z0-9]{20}$' THEN 'tiktok_pixel'
            WHEN p_type = 'pinterest' AND p_value ~ '^\d{10,16}$' THEN 'pinterest_tag'
            WHEN p_type = 'snapchat' AND lower(p_value) ~ '^[0-9a-f-]{36}$' THEN 'snapchat_pixel'
            WHEN p_type = 'clarity' AND lower(p_value) ~ '^[a-z0-9]{8,12}$' THEN 'clarity'
            WHEN p_type = 'newsbreak' AND p_value ~ '^\d{15,20}$' THEN 'newsbreak_pixel'
            WHEN p_type = 'email' AND lower(p_value) ~ '^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$'
                 AND lower(p_value) !~ '\.(png|jpe?g|gif|webp|svg|css|js)$' THEN 'support_email'
            WHEN p_type = 'company' AND length(btrim(p_value)) BETWEEN 4 AND 120 THEN 'legal_text'
        END AS kind,
        CASE WHEN p_type = 'email' THEN lower(p_value)
             WHEN p_type IN ('tiktok') THEN upper(p_value)
             WHEN p_type IN ('snapchat', 'clarity') THEN lower(p_value)
             WHEN p_type = 'company' THEN btrim(p_value)
             ELSE p_value END AS value,
        CASE
            -- Free mail is shared by strangers, and a platform's or a tool's
            -- address shows on every page that uses it.
            WHEN p_type = 'email' THEN
                lower(split_part(p_value, '@', 2)) NOT IN
                    ('gmail.com', 'googlemail.com', 'yahoo.com', 'outlook.com', 'hotmail.com', 'live.com', 'icloud.com',
                     'aol.com', 'proton.me', 'protonmail.com', 'gmx.com', 'mail.com', 'yandex.com', 'zoho.com',
                     'example.com', 'domain.com', 'email.com', 'yourdomain.com', 'yoursite.com', 'company.com',
                     'sentry.io', 'wixpress.com', 'sentry.wixpress.com', 'sentry-next.wixpress.com')
                AND NOT EXISTS (SELECT 1 FROM spy.shared_domain d
                                WHERE d.domain = spy.site_domain(split_part(p_value, '@', 2)))
            -- The page reader's company names are loose page text.
            WHEN p_type = 'company' THEN FALSE
            ELSE TRUE
        END AS strong
    ) k
    WHERE k.kind IS NOT NULL
$$;

-- Sites: where landing pages are. operator_id and group_reason are set only
-- while operators come from grouping.
CREATE TABLE spy.site (
    id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    domain TEXT NOT NULL UNIQUE,
    operator_id INTEGER REFERENCES spy.operator(id) ON DELETE SET NULL,
    group_reason TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL
);

-- Clues: things on a page that name who runs it.
CREATE TABLE spy.clue (
    id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    kind TEXT NOT NULL,
    value TEXT NOT NULL,
    strong BOOLEAN NOT NULL,
    UNIQUE (kind, value)
);

CREATE TABLE spy.site_clue (
    site_id INTEGER NOT NULL REFERENCES spy.site(id) ON DELETE CASCADE,
    clue_id INTEGER NOT NULL REFERENCES spy.clue(id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (site_id, clue_id)
);
CREATE INDEX site_clue_clue_idx ON spy.site_clue (clue_id);

-- Sellers: the merchant account on a checkout platform.
CREATE TABLE spy.seller (
    id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    platform TEXT NOT NULL,
    account TEXT NOT NULL,
    UNIQUE (platform, account)
);

-- Sellers a site's walks reached, on the landing page or its next step.
CREATE TABLE spy.site_seller (
    site_id INTEGER NOT NULL REFERENCES spy.site(id) ON DELETE CASCADE,
    seller_id INTEGER NOT NULL REFERENCES spy.seller(id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (site_id, seller_id)
);

-- Which account's click reached which site (the collector's
-- account_landing_page, 033): the account of the sighting walked.
CREATE TABLE spy.account_site (
    account_id INTEGER NOT NULL,
    site_id INTEGER NOT NULL REFERENCES spy.site(id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (account_id, site_id)
);
CREATE INDEX account_site_site_idx ON spy.account_site (site_id);

-- Each creative's newest landing page that answered, for the classifier.
CREATE TABLE spy.creative_page (
    creative_id INTEGER PRIMARY KEY,
    version_hash UUID NOT NULL,       -- tracks_api.page_version_v1
    site_id INTEGER REFERENCES spy.site(id) ON DELETE SET NULL,
    url TEXT,
    walked_at TIMESTAMPTZ NOT NULL,   -- the newest walk that reached it
    changed_at TIMESTAMPTZ NOT NULL   -- when it last showed other content
);

-- How far refresh_pages has read.
CREATE TABLE spy.page_mark (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    read_to TIMESTAMPTZ NOT NULL
);

-- Hand fixes, applied last: join puts a site or an account in an operator,
-- split keeps it out of every automatic group.
CREATE TABLE spy.grouping_fix (
    id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    site_id INTEGER REFERENCES spy.site(id) ON DELETE CASCADE,
    account_id INTEGER,
    action TEXT NOT NULL CHECK (action IN ('join', 'split')),
    operator_id INTEGER REFERENCES spy.operator(id) ON DELETE CASCADE,
    note TEXT,
    made_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((site_id IS NULL) <> (account_id IS NULL)),
    CHECK (action = 'split' OR operator_id IS NOT NULL)
);

-- Agencies: account name roots that buy for 3 or more site groups.
CREATE TABLE spy.agency (
    id INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    name_root TEXT NOT NULL UNIQUE,
    operator_count INTEGER NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The last grouping: one row per group, and one per site and account in a
-- group. operator_id is the operator the group keeps (most of its members
-- already had it), or NULL for a new one until the grouping is applied.
CREATE TABLE spy.grouping_group (
    grp BIGINT PRIMARY KEY,
    operator_id INTEGER,
    display_name TEXT,
    kind TEXT NOT NULL,
    seller TEXT,
    accounts INTEGER NOT NULL,
    sites INTEGER NOT NULL
);

CREATE TABLE spy.grouping_member (
    member TEXT NOT NULL CHECK (member IN ('site', 'account')),
    member_id INTEGER NOT NULL,
    grp BIGINT,                       -- NULL: kept out by a split fix, or joined by hand
    operator_id INTEGER,              -- the group's, or a join fix's
    reason TEXT,                      -- the first rule that tied it in
    agency TEXT,                      -- an account's agency name root
    PRIMARY KEY (member, member_id)
);
CREATE INDEX grouping_member_grp_idx ON spy.grouping_member (grp);

-- Each regrouping, and how its accounts compare with the operators in use.
CREATE TABLE spy.grouping_run (
    id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    at TIMESTAMPTZ NOT NULL,
    applied BOOLEAN NOT NULL,
    groups INTEGER NOT NULL,
    sites INTEGER NOT NULL,
    accounts INTEGER NOT NULL,
    agencies INTEGER NOT NULL,
    accounts_same INTEGER NOT NULL,   -- grouped under the operator they have now
    accounts_moved INTEGER NOT NULL,  -- have an operator now, grouped under another or none
    accounts_new INTEGER NOT NULL,    -- no operator now, grouped
    took_ms INTEGER NOT NULL
);

-- Reads the walks since the last run (15 minutes back, for walks saved late)
-- or since p_since, to read a range again after a Tracks replay or a
-- shared_domain change. Everything it writes is kept as first and last
-- seen, so reading a walk twice changes nothing. Returns the pages read.
CREATE FUNCTION spy.refresh_pages(p_now TIMESTAMPTZ DEFAULT now(), p_since TIMESTAMPTZ DEFAULT NULL) RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_from TIMESTAMPTZ;
    n BIGINT;
BEGIN
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.refresh_pages')) THEN
        RETURN 0;
    END IF;
    SELECT read_to - interval '15 minutes' INTO v_from FROM spy.page_mark;
    v_from := COALESCE(p_since, v_from, '-infinity');

    CREATE TEMP TABLE IF NOT EXISTS spy_wp (walk_id BIGINT, at TIMESTAMPTZ, creative_id INTEGER, account_id INTEGER,
        step SMALLINT, final_url TEXT, host TEXT, status INTEGER, checkout_platform TEXT, seller_account TEXT,
        version_hash UUID) ON COMMIT DROP;
    CREATE TEMP TABLE IF NOT EXISTS spy_ws (walk_id BIGINT, at TIMESTAMPTZ, creative_id INTEGER, account_id INTEGER,
        domain TEXT, final_url TEXT, version_hash UUID) ON COMMIT DROP;
    TRUNCATE spy_wp, spy_ws;

    INSERT INTO spy_wp
    SELECT w.walk_id, w.at, w.creative_id, w.account_id, w.step, w.final_url, w.host, w.status,
           w.checkout_platform, w.seller_account, w.version_hash
    FROM tracks_api.walk_page_v1 w
    WHERE w.at > v_from AND w.at <= p_now;
    GET DIAGNOSTICS n = ROW_COUNT;

    -- Landing pages that answered, with their site (NULL on a platform).
    INSERT INTO spy_ws
    SELECT walk_id, at, creative_id, account_id, spy.site_of(host), final_url, version_hash
    FROM spy_wp
    WHERE step = 0 AND status BETWEEN 200 AND 299;

    INSERT INTO spy.site (domain, first_seen_at, last_seen_at)
    SELECT domain, min(at), max(at) FROM spy_ws WHERE domain IS NOT NULL GROUP BY domain
    ON CONFLICT (domain) DO UPDATE SET
        first_seen_at = LEAST(spy.site.first_seen_at, EXCLUDED.first_seen_at),
        last_seen_at = GREATEST(spy.site.last_seen_at, EXCLUDED.last_seen_at);

    INSERT INTO spy.account_site (account_id, site_id, first_seen_at, last_seen_at)
    SELECT w.account_id, s.id, min(w.at), max(w.at)
    FROM spy_ws w JOIN spy.site s ON s.domain = w.domain
    WHERE w.account_id IS NOT NULL
    GROUP BY 1, 2
    ON CONFLICT (account_id, site_id) DO UPDATE SET
        first_seen_at = LEAST(spy.account_site.first_seen_at, EXCLUDED.first_seen_at),
        last_seen_at = GREATEST(spy.account_site.last_seen_at, EXCLUDED.last_seen_at);

    -- Clues on each site's landing pages.
    CREATE TEMP TABLE IF NOT EXISTS spy_wc (site_id INTEGER, kind TEXT, value TEXT, strong BOOLEAN,
        first_at TIMESTAMPTZ, last_at TIMESTAMPTZ) ON COMMIT DROP;
    TRUNCATE spy_wc;
    INSERT INTO spy_wc
    SELECT s.id, c.kind, c.value, c.strong, min(x.first_at), max(x.last_at)
    FROM (SELECT domain, version_hash, min(at) AS first_at, max(at) AS last_at
          FROM spy_ws WHERE domain IS NOT NULL AND version_hash IS NOT NULL GROUP BY 1, 2) x
    JOIN spy.site s ON s.domain = x.domain
    JOIN tracks_api.page_version_v1 v ON v.hash = x.version_hash
    CROSS JOIN LATERAL (
        SELECT p.key AS t, i.value AS val
        FROM jsonb_each(v.pixels) p
        CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(p.value) = 'array' THEN p.value ELSE '[]' END) i
        UNION ALL SELECT 'email', e FROM unnest(v.emails) e
        UNION ALL SELECT 'company', co FROM unnest(v.companies) co
    ) raw
    CROSS JOIN LATERAL spy.clue_of(raw.t, raw.val) c
    GROUP BY 1, 2, 3, 4;

    INSERT INTO spy.clue (kind, value, strong)
    SELECT DISTINCT kind, value, strong FROM spy_wc
    ON CONFLICT (kind, value) DO UPDATE SET strong = EXCLUDED.strong
    WHERE spy.clue.strong IS DISTINCT FROM EXCLUDED.strong;

    INSERT INTO spy.site_clue (site_id, clue_id, first_seen_at, last_seen_at)
    SELECT w.site_id, c.id, w.first_at, w.last_at
    FROM spy_wc w JOIN spy.clue c ON c.kind = w.kind AND c.value = w.value
    ON CONFLICT (site_id, clue_id) DO UPDATE SET
        first_seen_at = LEAST(spy.site_clue.first_seen_at, EXCLUDED.first_seen_at),
        last_seen_at = GREATEST(spy.site_clue.last_seen_at, EXCLUDED.last_seen_at);

    -- Sellers, on the landing page or its next step, for the landing page's site.
    INSERT INTO spy.seller (platform, account)
    SELECT DISTINCT checkout_platform, seller_account FROM spy_wp
    WHERE checkout_platform IS NOT NULL AND seller_account IS NOT NULL
    ON CONFLICT (platform, account) DO NOTHING;

    INSERT INTO spy.site_seller (site_id, seller_id, first_seen_at, last_seen_at)
    SELECT s.id, se.id, min(p.at), max(p.at)
    FROM spy_wp p
    JOIN spy_ws w ON w.walk_id = p.walk_id
    JOIN spy.site s ON s.domain = w.domain
    JOIN spy.seller se ON se.platform = p.checkout_platform AND se.account = p.seller_account
    GROUP BY 1, 2
    ON CONFLICT (site_id, seller_id) DO UPDATE SET
        first_seen_at = LEAST(spy.site_seller.first_seen_at, EXCLUDED.first_seen_at),
        last_seen_at = GREATEST(spy.site_seller.last_seen_at, EXCLUDED.last_seen_at);

    -- Each creative's newest landing page.
    INSERT INTO spy.creative_page AS cp (creative_id, version_hash, site_id, url, walked_at, changed_at)
    SELECT DISTINCT ON (w.creative_id) w.creative_id, w.version_hash, s.id, w.final_url, w.at, w.at
    FROM spy_ws w LEFT JOIN spy.site s ON s.domain = w.domain
    WHERE w.version_hash IS NOT NULL
    ORDER BY w.creative_id, w.at DESC, w.walk_id DESC
    ON CONFLICT (creative_id) DO UPDATE SET
        version_hash = EXCLUDED.version_hash,
        site_id = EXCLUDED.site_id,
        url = EXCLUDED.url,
        walked_at = EXCLUDED.walked_at,
        changed_at = CASE WHEN cp.version_hash = EXCLUDED.version_hash THEN cp.changed_at ELSE EXCLUDED.walked_at END
    WHERE EXCLUDED.walked_at >= cp.walked_at;

    INSERT INTO spy.page_mark (id, read_to) VALUES (TRUE, p_now)
    ON CONFLICT (id) DO UPDATE SET read_to = GREATEST(spy.page_mark.read_to, EXCLUDED.read_to);
    RETURN n;
END;
$$;

-- Name root of an account: the first dash-separated part, digits at the end
-- removed. Short or generic roots return the whole account, so they group
-- nothing. The collector's (016).
CREATE FUNCTION spy.account_root(p_external_id TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE
        WHEN length(r) >= 5 AND r NOT IN ('taboolaaccount', 'taboola', 'account', 'media', 'digital',
                                          'global', 'native', 'group', 'agency', 'ads', 'adv')
            THEN r
        ELSE lower(p_external_id)
    END
    FROM (SELECT regexp_replace(split_part(lower(p_external_id), '-', 1), '\d+$', '') AS r) x
$$;

-- The email in an account name, as Taboola writes it (no @, no dots), without
-- the account number after it. NULL when the name has none. The collector's (033).
--   taboolaaccount-tiktokgringo45hotmailcom                 -> tiktokgringo45hotmailcom
--   charonmarketingcorporation-tiktokgringo45hotmailcom2-sc -> tiktokgringo45hotmailcom
CREATE FUNCTION spy.account_email(p_external_id TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT (regexp_match(lower(p_external_id),
        '(?:^|-)([a-z0-9]{3,}(?:gmail|googlemail|hotmail|outlook|live|yahoo|ymail|icloud|aol|protonmail|proton|msn|gmx|yandex|uol|bol)com(?:br)?)[0-9]*(?:-|$)'))[1]
$$;

-- Account name as shown in an operator name. The collector's (033).
CREATE FUNCTION spy.account_display(p_external_id TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT regexp_replace(regexp_replace(p_external_id, '^taboolaaccount-', ''), '-sc$', '')
$$;

-- Connected groups by label spreading: every node takes the smallest label
-- among its neighbours until nothing changes. Nodes are site id * 4,
-- account id * 4 + 1, clue id * 4 + 2. Reads spy_node and spy_edge, writes
-- spy_label. The collector's (016).
CREATE FUNCTION spy.spread_labels()
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    rounds INTEGER := 0;
    changed BIGINT;
BEGIN
    TRUNCATE spy_label;
    INSERT INTO spy_label (node, label)
    SELECT n, n FROM (SELECT a AS n FROM spy_edge UNION SELECT b FROM spy_edge UNION SELECT node FROM spy_node) x;
    LOOP
        UPDATE spy_label l SET label = m.lbl
        FROM (SELECT e.a AS node, min(l2.label) AS lbl
              FROM spy_edge e JOIN spy_label l2 ON l2.node = e.b
              GROUP BY e.a) m
        WHERE l.node = m.node AND m.lbl < l.label;
        GET DIAGNOSTICS changed = ROW_COUNT;
        rounds := rounds + 1;
        EXIT WHEN changed = 0 OR rounds > 200;
    END LOOP;
    RETURN rounds;
END;
$$;

-- Groups sites and accounts into operators. The collector's rules (016, 033):
--   1. Sites that share a strong clue belong together (a clue on more than
--      clue_max_sites sites joins nothing).
--   2. An account whose clicks reached at most 2 of those site groups belongs
--      with them. One that reached 3 or more is its own operator, arbitrage.
--   3. Accounts sharing a name root ("hearstmagsus-prevention-sc",
--      "hearstmagsus-elle2-sc") belong together, unless the root reached 3 or
--      more site groups: then the root is an agency.
--   4. grouping_fix rows are applied last and win.
--   5. Accounts with the same email in their name belong together.
-- Brand names never group: unrelated accounts share generic brands.
-- A group keeps the operator most of its members have now, so OP codes stay.
-- Writes spy.grouping_group, spy.grouping_member and a spy.grouping_run row;
-- applies them to spy.operator, spy.account_operator and spy.site only when
-- operators_from is 'grouping'. Returns the groups, 0 while no page was read.
CREATE FUNCTION spy.regroup_operators(p_now TIMESTAMPTZ DEFAULT now()) RETURNS BIGINT
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

    -- Rule 3: agency roots buy for 3 or more site groups across their accounts.
    INSERT INTO spy_agency_root
    SELECT a.root, count(DISTINCT x.grp)
    FROM spy_acct a JOIN spy_acct_site x ON x.account_id = a.account_id
    WHERE a.root <> lower(a.external_id)
    GROUP BY a.root
    HAVING count(DISTINCT a.account_id) >= 2 AND count(DISTINCT x.grp) >= 3;

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

        DELETE FROM spy.account_operator ao WHERE NOT EXISTS (
            SELECT 1 FROM spy.grouping_member m
            WHERE m.member = 'account' AND m.member_id = ao.account_id AND m.operator_id IS NOT NULL);
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
                                  accounts_new, took_ms)
    SELECT p_now, v_apply, v_groups,
           (SELECT count(*) FROM spy.grouping_member WHERE member = 'site' AND grp IS NOT NULL),
           (SELECT count(*) FROM spy.grouping_member WHERE member = 'account' AND grp IS NOT NULL),
           (SELECT count(*) FROM spy_agency_root),
           count(*) FILTER (WHERE ao.operator_id IS NOT NULL AND ao.operator_id = m.operator_id),
           count(*) FILTER (WHERE ao.operator_id IS NOT NULL AND m.operator_id IS DISTINCT FROM ao.operator_id),
           count(*) FILTER (WHERE ao.operator_id IS NULL AND (m.grp IS NOT NULL OR m.operator_id IS NOT NULL)),
           (extract(epoch FROM clock_timestamp() - v_start) * 1000)::int
    FROM spy.account_operator ao
    FULL JOIN (SELECT member_id, grp, operator_id FROM spy.grouping_member WHERE member = 'account') m
      ON m.member_id = ao.account_id;
    DELETE FROM spy.grouping_run WHERE at < p_now - interval '30 days';
    RETURN v_groups;
END;
$$;
