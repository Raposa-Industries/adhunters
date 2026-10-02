package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/raposa/internal/pagever"
)

// Store is Raposa's side of the database: its own schema, and the Tracks
// views it reads (tracks_api).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// lease is how long one claim holds an investigation. A visit takes at most
// two and a half minutes; a worker that dies holding one frees it after this.
const lease = 5 * time.Minute

// LoadSettings reads the tunables. A key that is missing or unreadable keeps
// the value the migration seeded.
func (s *Store) LoadSettings(ctx context.Context) (Settings, error) {
	set := defaultSettings()
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM raposa.setting`)
	if err != nil {
		return set, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return set, fmt.Errorf("load settings: %w", err)
		}
		n := func(fallback int) int { return settingInt(value, fallback) }
		switch key {
		case "visits_target":
			set.VisitsTarget = n(set.VisitsTarget)
		case "visits_window_minutes":
			set.VisitsWindowMinutes = n(set.VisitsWindowMinutes)
		case "ladder_tries_per_rung":
			set.LadderTriesPerRung = n(set.LadderTriesPerRung)
		case "avoid_places":
			set.AvoidPlaces = settingList(value)
		case "avoid_regions":
			set.AvoidRegions = settingList(value)
		case "sample_states":
			set.SampleStates = settingList(value)
		case "max_page_bytes":
			set.MaxPageBytes = n(set.MaxPageBytes)
		case "funnel_max_steps":
			set.FunnelMaxSteps = n(set.FunnelMaxSteps)
		case "browser_dwell_ms":
			set.BrowserDwellMs = n(set.BrowserDwellMs)
		case "residential_budget_bytes":
			set.ResidentialBudgetBytes = n(set.ResidentialBudgetBytes)
		case "max_active":
			set.MaxActive = n(set.MaxActive)
		case "quick_max_running":
			set.QuickMaxRunning = n(set.QuickMaxRunning)
		case "live_link_wait_seconds":
			set.LiveLinkWaitSeconds = n(set.LiveLinkWaitSeconds)
		}
	}
	return set, rows.Err()
}

func settingInt(value string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// settingList splits a comma separated setting, dropping blanks.
func settingList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LoadLadder reads the enabled disguises, cheapest rung first.
func (s *Store) LoadLadder(ctx context.Context) ([]Disguise, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, rung, code, name, engine, line_role, device_rule, link_kind,
		       pin_place, load_assets, human_dwell, cost_kb, is_baseline
		FROM raposa.disguise WHERE enabled ORDER BY rung`)
	if err != nil {
		return nil, fmt.Errorf("load ladder: %w", err)
	}
	defer rows.Close()
	var out []Disguise
	for rows.Next() {
		var d Disguise
		if err := rows.Scan(&d.ID, &d.Rung, &d.Code, &d.Name, &d.Engine, &d.LineRole, &d.DeviceRule,
			&d.LinkKind, &d.PinPlace, &d.LoadAssets, &d.HumanDwell, &d.CostKB, &d.IsBaseline); err != nil {
			return nil, fmt.Errorf("load ladder: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReleaseNode lets go of every investigation this node held, so a restart
// resumes them at once instead of after their lease.
func (s *Store) ReleaseNode(ctx context.Context, node string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE raposa.investigation
		SET claimed_by = NULL, claim_token = NULL, claimed_until = NULL
		WHERE claimed_by = $1`, node)
	if err != nil {
		return 0, fmt.Errorf("release this node's investigations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Claim takes one investigation whose next visit is due, for one visit.
// Running ones come first, so a started investigation is never starved by
// new ones; a new one starts only while fewer than max_active run, and a
// quick one only while fewer than quick_max_running quick ones run, so a
// deep one someone asked for always finds room. Returns nil, nil when none
// is due.
func (s *Store) Claim(ctx context.Context, node string) (*Investigation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Claims go one at a time: two workers counting the running ones at once
	// would each start one more than max_active allows.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('raposa claim'))`); err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	token := uuid.New()
	var inv Investigation
	var progress []byte
	err = tx.QueryRow(ctx, `
		WITH active AS (
			SELECT count(*) FILTER (WHERE TRUE) AS n,
			       count(*) FILTER (WHERE mode = 'quick') AS quick
			FROM raposa.investigation
			WHERE status = 'running' OR (status = 'waiting' AND claimed_until > now())
		)
		UPDATE raposa.investigation
		SET claimed_by = $1, claim_token = $2, claimed_until = now() + $3::interval
		WHERE id = (
			SELECT i.id FROM raposa.investigation i, active a
			WHERE i.status IN ('waiting', 'running')
			  AND i.next_visit_at <= now()
			  AND (i.claimed_until IS NULL OR i.claimed_until < now())
			  AND (i.status = 'running' OR i.stop_requested
			       OR (a.n < raposa.setting_int('max_active', 10)
			           AND (i.mode <> 'quick' OR a.quick < raposa.setting_int('quick_max_running', 6))))
			ORDER BY (i.status = 'running') DESC, (i.mode = 'quick'), i.next_visit_at, i.id
			FOR UPDATE OF i SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, creative_id, ad_id, mode, status, stop_requested, target_click_url, publisher_referer,
		          burn_scope, visits_target, retry_of, attempt, started_at, progress, COALESCE(start_rung, 0)`,
		node, token, fmt.Sprintf("%d seconds", int(lease.Seconds()))).
		Scan(&inv.ID, &inv.CreativeID, &inv.AdID, &inv.Mode, &inv.Status, &inv.StopRequested,
			&inv.TargetClickURL, &inv.PublisherReferer, &inv.BurnScope, &inv.VisitsTarget,
			&inv.RetryOf, &inv.Attempt, &inv.StartedAt, &progress, &inv.StartRung)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	if err := json.Unmarshal(progress, &inv.Progress); err != nil {
		return nil, fmt.Errorf("claim: investigation %d has progress that does not read: %w", inv.ID, err)
	}
	inv.Token = token
	return &inv, tx.Commit(ctx)
}

// Release lets go of one investigation without recording anything, so its
// visit runs again: this node is stopping, or the visit could not start.
func (s *Store) Release(ctx context.Context, inv *Investigation, after time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE raposa.investigation
		SET claimed_by = NULL, claim_token = NULL, claimed_until = NULL,
		    next_visit_at = now() + $3::interval
		WHERE id = $1 AND claim_token = $2`, inv.ID, inv.Token, interval(after))
	return err
}

func interval(d time.Duration) string {
	return fmt.Sprintf("%d milliseconds", d.Milliseconds())
}

// ResolveAd reads what Tracks says about the ad: the device it targets, the
// publisher it ran on last, where its link goes and which campaign pays.
// Every field may come back empty when Tracks has not seen the creative for
// 30 days.
func (s *Store) ResolveAd(ctx context.Context, creativeID int32, adID *int32) (AdContext, error) {
	var c AdContext
	var device, pubName, pubDomain, host, saved, campaign *string
	var account *int32
	c.AccountByCampaign = map[string]int32{}
	c.CampaignByDevice = map[string]string{}

	// The daily counts, not the sightings: they outlive the 3 days of single
	// sightings Tracks publishes.
	err := s.pool.QueryRow(ctx, `
		WITH days AS (
			SELECT d.device_id, d.publisher_id, d.sightings, d.day
			FROM tracks_api.ad_daily_v1 d
			WHERE d.creative_id = $1 AND ($2::int IS NULL OR d.ad_id = $2) AND d.day > CURRENT_DATE - 30
		), pub AS (
			SELECT publisher_id FROM days GROUP BY publisher_id ORDER BY max(day) DESC, sum(sightings) DESC LIMIT 1
		), lnk AS (
			SELECT l.host, l.sample_url
			FROM tracks_api.creative_link_daily_v1 cl
			JOIN tracks_api.link_v1 l ON l.id = cl.link_id
			WHERE cl.creative_id = $1 AND cl.day > CURRENT_DATE - 30
			ORDER BY cl.last_seen_at DESC, cl.sightings DESC
			LIMIT 1
		), cmp AS (
			SELECT cp.external_id, cp.account_id
			FROM tracks_api.creative_campaign_daily_v1 cc
			JOIN tracks_api.campaign_v1 cp ON cp.id = cc.campaign_id
			WHERE cc.creative_id = $1 AND cc.day > CURRENT_DATE - 30
			ORDER BY cc.last_seen_at DESC, cc.sightings DESC
			LIMIT 1
		)
		SELECT
			(SELECT dv.code FROM days d JOIN tracks_api.device_v1 dv ON dv.id = d.device_id
			 GROUP BY dv.code ORDER BY sum(d.sightings) DESC LIMIT 1),
			(SELECT publisher_id FROM pub),
			(SELECT p.name FROM pub JOIN tracks_api.publisher_v1 p ON p.id = pub.publisher_id),
			(SELECT p.domain FROM pub JOIN tracks_api.publisher_v1 p ON p.id = pub.publisher_id),
			(SELECT host FROM lnk),
			(SELECT sample_url FROM lnk),
			(SELECT external_id FROM cmp),
			(SELECT account_id FROM cmp)`, creativeID, adID).
		Scan(&device, &c.PublisherID, &pubName, &pubDomain, &host, &saved, &campaign, &account)
	if err != nil {
		return c, fmt.Errorf("read the ad from Tracks: %w", err)
	}
	c.Device = "desktop"
	c.DeviceKnown = device != nil
	if device != nil && *device != "desktop" {
		c.Device = "phone"
	}
	c.PublisherName, c.PublisherDomain = deref(pubName), deref(pubDomain)
	c.LandingHost, c.SavedLink, c.CampaignID = deref(host), deref(saved), deref(campaign)
	if campaign != nil && account != nil {
		c.AccountByCampaign[*campaign] = *account
	}

	// When Tracks last saw it: the one ad when the investigation names one,
	// since another headline of the same creative can still run after this
	// one stopped; else any ad of the creative.
	if adID != nil {
		err = s.pool.QueryRow(ctx, `SELECT last_seen_at FROM tracks_api.ad_v1 WHERE id = $1`, *adID).Scan(&c.LastSeenAt)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT max(last_seen_at) FROM tracks_api.ad_v1 WHERE creative_id = $1`, creativeID).Scan(&c.LastSeenAt)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("read when Tracks last saw the ad: %w", err)
	}

	// The devices and campaigns of the last day. A creative that runs in a
	// desktop campaign and a phone campaign has a different link on each,
	// and the ad network writes the device into it, so each device goes with
	// its own campaign.
	rows, err := s.pool.Query(ctx, `
		SELECT dv.code, cp.external_id, COALESCE(cp.account_id, 0), count(*) AS n
		FROM tracks_api.sighting_v1 si
		JOIN tracks_api.device_v1 dv ON dv.id = si.device_id
		JOIN tracks_api.campaign_v1 cp ON cp.id = si.campaign_id
		WHERE si.creative_id = $1 AND ($2::int IS NULL OR si.ad_id = $2)
		  AND si.seen_at > now() - interval '24 hours'
		GROUP BY 1, 2, 3
		ORDER BY n DESC`, creativeID, adID)
	if err != nil {
		return c, fmt.Errorf("read the ad's devices from Tracks: %w", err)
	}
	perDevice := map[string]int64{}
	for rows.Next() {
		var code, campaignID string
		var acct int32
		var n int64
		if err := rows.Scan(&code, &campaignID, &acct, &n); err != nil {
			rows.Close()
			return c, fmt.Errorf("read the ad's devices from Tracks: %w", err)
		}
		if code != "desktop" {
			code = "phone"
		}
		if acct != 0 {
			c.AccountByCampaign[campaignID] = acct
		}
		if _, seen := perDevice[code]; !seen {
			c.Devices = append(c.Devices, code)
			c.CampaignByDevice[code] = campaignID // busiest first
		}
		perDevice[code] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, fmt.Errorf("read the ad's devices from Tracks: %w", err)
	}
	if len(c.Devices) > 0 {
		// The last day says which device the ad runs on now, which the 30 day
		// counts can get wrong for an ad that moved.
		if len(c.Devices) > 1 && perDevice[c.Devices[1]] > perDevice[c.Devices[0]] {
			c.Devices[0], c.Devices[1] = c.Devices[1], c.Devices[0]
		}
		c.Device = c.Devices[0]
		c.DeviceKnown = true
		c.CampaignID = c.CampaignByDevice[c.Device]
	}

	pubs, err := s.pool.Query(ctx, `
		SELECT p.name
		FROM tracks_api.ad_daily_v1 d
		JOIN tracks_api.publisher_v1 p ON p.id = d.publisher_id
		WHERE d.creative_id = $1 AND ($2::int IS NULL OR d.ad_id = $2) AND d.day >= CURRENT_DATE - 1
		GROUP BY p.name
		ORDER BY sum(d.sightings) DESC`, creativeID, adID)
	if err != nil {
		return c, fmt.Errorf("read the ad's publishers from Tracks: %w", err)
	}
	for pubs.Next() {
		var name string
		if err := pubs.Scan(&name); err != nil {
			pubs.Close()
			return c, err
		}
		c.Publishers = append(c.Publishers, name)
	}
	pubs.Close()
	return c, pubs.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// liveLink is a live link and the publisher page that served it.
type liveLink struct {
	URL       string
	Campaign  string
	Device    string
	Publisher string
	PageURL   string
	SeenAt    time.Time
}

// TakeLiveLink takes one live link to host from Tracks, and Tracks never
// hands it out again. ok is false when none is on hand.
func (s *Store) TakeLiveLink(ctx context.Context, host, campaign, device string, anyDevice bool) (liveLink, bool, error) {
	var l liveLink
	var camp, pageURL *string
	err := s.pool.QueryRow(ctx,
		`SELECT url, campaign_external_id, device, publisher, page_url, seen_at
		 FROM tracks_api.take_live_link_v1($1, $2, $3, $4)`, host, campaign, device, anyDevice).
		Scan(&l.URL, &camp, &l.Device, &l.Publisher, &pageURL, &l.SeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, false, nil
	}
	if err != nil {
		return l, false, fmt.Errorf("take a live link from Tracks: %w", err)
	}
	l.Campaign, l.PageURL = deref(camp), deref(pageURL)
	return l, true, nil
}

// BurnedLines lists the lines a visit past the baseline must not use, with
// why: the ones burned for this site, and the ones burned in general.
func (s *Store) BurnedLines(ctx context.Context, scope string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.line_key,
		       format('burned for this site: 0 dark pages in %s visits on rung %s, while the other lines saw %s in %s',
		              b.visits, b.rung, b.other_dark, b.other_visits)
		FROM raposa.line_burn b
		WHERE b.ended_at IS NULL AND b.scope = $1 AND $1 <> ''
		UNION ALL
		SELECT s.line_key, format('burned in general: burned for %s sites', s.sites_burned)
		FROM raposa.line_status s
		WHERE s.burned_in_general`, scope)
	if err != nil {
		return nil, fmt.Errorf("read burned lines: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, why string
		if err := rows.Scan(&key, &why); err != nil {
			return nil, fmt.Errorf("read burned lines: %w", err)
		}
		if _, seen := out[key]; !seen {
			out[key] = why
		}
	}
	return out, rows.Err()
}

// RefreshLineBurns rebuilds the burned lines and returns how many are active.
func (s *Store) RefreshLineBurns(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT raposa.refresh_line_burns()`).Scan(&n)
	return n, err
}

// ReleaseFollows queues the follow-up runs that are due
// (raposa.release_follows) and returns how many.
func (s *Store) ReleaseFollows(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT raposa.release_follows()`).Scan(&n)
	return n, err
}

// QueueQuick tops up the automatic quick queue (raposa.queue_quick) and
// returns how many investigations it queued.
func (s *Store) QueueQuick(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT raposa.queue_quick()`).Scan(&n)
	return n, err
}

// Request asks for an investigation through the published function, as any
// other service would.
func (s *Store) Request(ctx context.Context, creativeID int32, mode string, adID *int32, by string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT raposa_api.request_investigation_v1($1, $2, $3, $4)`,
		creativeID, mode, adID, by).Scan(&id)
	return id, err
}

// Stop asks an investigation to stop. False when it had already ended.
func (s *Store) Stop(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT raposa_api.stop_investigation_v1($1)`, id).Scan(&ok)
	return ok, err
}

// upsertPages stores the pages of one visit as versions and returns their
// ids, in order. A page is its host and path (pagever.Key); a capture whose
// words match a version already stored (pagever.Match) is that version and
// only bumps its counters. keep queues a new version for the keeper, which a
// deep investigation's pages are and a quick one's are not.
func upsertPages(ctx context.Context, tx pgx.Tx, pages []Page, keep bool) ([]int32, error) {
	// Two visits meeting the same new page at once would store it twice, so
	// each page key is locked, in one order, so two visits never wait on
	// each other in a circle.
	keys := make([]string, len(pages))
	for i, p := range pages {
		keys[i] = pagever.Key(p.Host, p.Path)
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for i, k := range sorted {
		if i > 0 && sorted[i-1] == k {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('raposa page ' || $1))`, k); err != nil {
			return nil, fmt.Errorf("lock page %s: %w", k, err)
		}
	}
	ids := make([]int32, len(pages))
	for i, p := range pages {
		id, err := upsertPage(ctx, tx, p, keys[i], keep)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

func upsertPage(ctx context.Context, tx pgx.Tx, p Page, key string, keep bool) (int32, error) {
	var id int32
	err := tx.QueryRow(ctx, `SELECT id FROM raposa.page WHERE content_hash = $1`, p.ContentHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = matchVersion(ctx, tx, key, p.BodyText)
	}
	if err != nil {
		return 0, fmt.Errorf("find the version of %s: %w", p.URL, err)
	}
	if id != 0 {
		_, err := tx.Exec(ctx, `
			UPDATE raposa.page
			SET times_seen = times_seen + 1, last_seen_at = now(), is_dark = is_dark OR $2,
			    capture_state = CASE WHEN capture_state = 'html' AND $3 AND page_kind IS DISTINCT FROM 'error'
			                         THEN 'queued' ELSE capture_state END,
			    video_links = CASE WHEN $4::text[] <@ video_links THEN video_links
			                       ELSE (SELECT array_agg(DISTINCT l) FROM unnest(video_links || $4::text[]) l) END
			WHERE id = $1`, id, p.IsDark, keep, videoOrEmpty(p.VideoLinks))
		if err != nil {
			return 0, fmt.Errorf("bump page %d: %w", id, err)
		}
		return id, nil
	}
	state := "html"
	if keep && p.PageKind != "error" {
		state = "queued"
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO raposa.page
			(content_hash, page_key, text_digest, url, host, path, title, page_kind, word_count, html, body_text,
			 html_bytes, headings, meta_tags, pixels, checkout_platform, checkout_merchant_id,
			 outbound_links, is_dark, capture_state, video_links)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11, $12,
		        $13::jsonb, $14::jsonb, $15::jsonb, NULLIF($16, ''), NULLIF($17, ''), $18::jsonb, $19, $20, $21)
		RETURNING id`,
		p.ContentHash, key, pagever.TextDigest(p.BodyText), clean(p.URL), p.Host, p.Path, clean(p.Title),
		p.PageKind, p.WordCount, clean(p.HTML), clean(p.BodyText), p.HTMLBytes,
		jsonOr(p.Headings, "{}"), jsonOr(p.MetaTags, "{}"), jsonOr(p.Pixels, "{}"),
		clean(p.CheckoutPlatform), clean(p.CheckoutMerchantID), jsonOr(p.OutboundLinks, "[]"), p.IsDark, state,
		videoOrEmpty(p.VideoLinks)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert page %s: %w", p.URL, err)
	}
	return id, nil
}

// matchVersion finds the stored version of one page whose words match,
// newest first, or 0 when the words are new.
func matchVersion(ctx context.Context, tx pgx.Tx, key, text string) (int32, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, text_digest, COALESCE(body_text, '')
		FROM raposa.page WHERE page_key = $1
		ORDER BY id DESC LIMIT 50`, key)
	if err != nil {
		return 0, err
	}
	var versions []pagever.Version
	for rows.Next() {
		var v pagever.Version
		if err := rows.Scan(&v.ID, &v.TextDigest, &v.Text); err != nil {
			rows.Close()
			return 0, err
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	id, _ := pagever.Match(versions, text)
	return id, nil
}

// videoOrEmpty is the links as a text array, never NULL.
func videoOrEmpty(links []string) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, clean(l))
	}
	return out
}

// clean drops the NUL bytes Postgres refuses in text, and anything that is
// not UTF-8.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.ToValidUTF8(s, "")
}

// jsonOr encodes v, or returns fallback for nil or what does not encode.
func jsonOr(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return fallback
	}
	return string(b)
}
