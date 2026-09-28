package load

import (
	"context"
	"crypto/md5"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/tracks/parse"
)

// The lookup upserts, ported from the collector's db.SpyWriter (e20148c,
// internal/db/spywriter.go). Ids resolve from in-memory caches; a row seen
// again within touchEvery is not updated in live loading, which saves most
// upserts. A replay always updates, so first_seen_at can move back in time.

type cached struct {
	id      int32
	touched time.Time
}

const (
	touchEvery    = 10 * time.Minute
	maxCacheItems = 500000
)

type adKey struct {
	creativeID int32
	headline   string
}

// caches hold only committed ids.
type caches struct {
	networks   map[string]int32
	devices    map[string]int32
	placements map[string]int32
	proxyLines map[string]int32
	brands     map[string]int32
	publishers map[string]*cached
	accounts   map[string]*cached // netKey
	campaigns  map[string]*cached // netKey
	creatives  map[string]*cached
	ads        map[adKey]*cached
	links      map[uuid.UUID]*cached
	networkAds map[string]*cached // netKey of the network's ad id
}

func newCaches() *caches {
	return &caches{
		networks: map[string]int32{}, devices: map[string]int32{}, placements: map[string]int32{},
		proxyLines: map[string]int32{}, brands: map[string]int32{},
		publishers: map[string]*cached{}, accounts: map[string]*cached{}, campaigns: map[string]*cached{},
		creatives: map[string]*cached{}, ads: map[adKey]*cached{}, links: map[uuid.UUID]*cached{},
		networkAds: map[string]*cached{},
	}
}

// commit moves ids learned in a committed transaction into c. The big maps
// start over when they outgrow maxCacheItems.
func (c *caches) commit(p *caches) {
	if len(c.ads) > maxCacheItems || len(c.links) > maxCacheItems {
		c.ads, c.links = map[adKey]*cached{}, map[uuid.UUID]*cached{}
		c.creatives, c.campaigns, c.networkAds = map[string]*cached{}, map[string]*cached{}, map[string]*cached{}
	}
	for _, m := range []struct{ dst, src map[string]int32 }{
		{c.networks, p.networks}, {c.devices, p.devices}, {c.placements, p.placements},
		{c.proxyLines, p.proxyLines}, {c.brands, p.brands},
	} {
		for k, v := range m.src {
			m.dst[k] = v
		}
	}
	for _, m := range []struct{ dst, src map[string]*cached }{
		{c.publishers, p.publishers}, {c.accounts, p.accounts}, {c.campaigns, p.campaigns},
		{c.creatives, p.creatives}, {c.networkAds, p.networkAds},
	} {
		for k, v := range m.src {
			m.dst[k] = v
		}
	}
	for k, v := range p.ads {
		c.ads[k] = v
	}
	for k, v := range p.links {
		c.links[k] = v
	}
}

// ids looks a key up in the transaction's new ids first, then the cache.
func id32(fresh, cache map[string]int32, k string) int32 {
	if v, ok := fresh[k]; ok {
		return v
	}
	return cache[k]
}

func idCached(fresh, cache map[string]*cached, k string) int32 {
	if k == "" {
		return 0
	}
	if v, ok := fresh[k]; ok {
		return v.id
	}
	if v, ok := cache[k]; ok {
		return v.id
	}
	return 0
}

// skip reports whether a cached row was written recently enough to leave it.
func skip(live bool, c *cached, last time.Time) bool {
	return live && c != nil && !last.After(c.touched.Add(touchEvery))
}

// netKey is the cache key of an account or campaign: the ad network id plus
// the id on that network. It matches network_id || '|' || external_id.
func netKey(network int32, ext string) string { return strconv.Itoa(int(network)) + "|" + ext }

func optNetKey(network int32, ext string) string {
	if ext == "" {
		return ""
	}
	return netKey(network, ext)
}

func splitNetKey(k string) (int32, string) {
	net, ext, _ := strings.Cut(k, "|")
	id, _ := strconv.Atoi(net)
	return int32(id), ext
}

func uniqueMissing(keys []string, cache map[string]int32) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if _, ok := cache[k]; ok || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolveNames finds (or adds) rows of a small name table.
func resolveNames(ctx context.Context, tx pgx.Tx, table, col string, keys []string, cache, out map[string]int32) error {
	missing := uniqueMissing(keys, cache)
	if len(missing) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`INSERT INTO tracks.%s (%s) SELECT unnest($1::text[]) ON CONFLICT (%s) DO NOTHING`, table, col, col), missing); err != nil {
		return fmt.Errorf("add %s: %w", table, err)
	}
	return scanNames(ctx, tx, fmt.Sprintf(`SELECT %s, id::int FROM tracks.%s WHERE %s = ANY($1)`, col, table, col), missing, out)
}

// resolveNetworks looks up ad network ids by code. Networks are added by
// migrations only, so an unknown code fails the load.
func resolveNetworks(ctx context.Context, tx pgx.Tx, codes []string, cache, out map[string]int32) error {
	missing := uniqueMissing(codes, cache)
	if len(missing) == 0 {
		return nil
	}
	if err := scanNames(ctx, tx, `SELECT code, id::int FROM tracks.network WHERE code = ANY($1)`, missing, out); err != nil {
		return fmt.Errorf("look up networks: %w", err)
	}
	for _, c := range missing {
		if _, ok := out[c]; !ok {
			return fmt.Errorf("unknown ad network %q", c)
		}
	}
	return nil
}

func scanNames(ctx context.Context, tx pgx.Tx, query string, keys []string, out map[string]int32) error {
	rows, err := tx.Query(ctx, query, keys)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var id int32
		if err := rows.Scan(&k, &id); err != nil {
			return err
		}
		out[k] = id
	}
	return rows.Err()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type seenRange struct{ first, last time.Time }

func (r *seenRange) widen(t time.Time) { widenPair(&r.first, &r.last, t) }

func widenPair(first, last *time.Time, t time.Time) {
	if t.Before(*first) {
		*first = t
	}
	if t.After(*last) {
		*last = t
	}
}

type publisherRow struct {
	network     int32
	domain      string
	first, last time.Time
}

func upsertPublishers(ctx context.Context, tx pgx.Tx, live bool, m map[string]*publisherRow, c, p *caches) error {
	var names, domains []string
	var nets []int32
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		if skip(live, c.publishers[k], m[k].last) {
			continue
		}
		names = append(names, k)
		nets = append(nets, m[k].network)
		domains = append(domains, m[k].domain)
		first = append(first, m[k].first)
		last = append(last, m[k].last)
	}
	if len(names) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO tracks.publisher AS p (name, network_id, domain, first_seen_at, last_seen_at)
		SELECT n, nw, NULLIF(d, ''), f, l
		FROM unnest($1::text[], $2::smallint[], $3::text[], $4::timestamptz[], $5::timestamptz[]) AS u(n, nw, d, f, l)
		ON CONFLICT (name) DO UPDATE SET
			domain = COALESCE(EXCLUDED.domain, p.domain),
			first_seen_at = LEAST(p.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(p.last_seen_at, EXCLUDED.last_seen_at)
		RETURNING name, id`, names, nets, domains, first, last)
	if err != nil {
		return fmt.Errorf("upsert publishers: %w", err)
	}
	return scanCached(rows, m, p.publishers, func(r *publisherRow) time.Time { return r.last })
}

func upsertAccounts(ctx context.Context, tx pgx.Tx, live bool, m map[string]*seenRange, orgs map[string]string, c, p *caches) error {
	var nets []int32
	var ext, orgIDs []string
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		if skip(live, c.accounts[k], m[k].last) {
			continue
		}
		net, e := splitNetKey(k)
		nets = append(nets, net)
		ext = append(ext, e)
		orgIDs = append(orgIDs, orgs[k])
		first = append(first, m[k].first)
		last = append(last, m[k].last)
	}
	if len(ext) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO tracks.account AS a (network_id, external_id, org_external_id, first_seen_at, last_seen_at)
		SELECT nw, e, NULLIF(o, ''), f, l
		FROM unnest($1::smallint[], $2::text[], $3::text[], $4::timestamptz[], $5::timestamptz[]) AS u(nw, e, o, f, l)
		ON CONFLICT (network_id, external_id) DO UPDATE SET
			org_external_id = COALESCE(EXCLUDED.org_external_id, a.org_external_id),
			first_seen_at = LEAST(a.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(a.last_seen_at, EXCLUDED.last_seen_at)
		RETURNING network_id || '|' || external_id, id`, nets, ext, orgIDs, first, last)
	if err != nil {
		return fmt.Errorf("upsert accounts: %w", err)
	}
	return scanCached(rows, m, p.accounts, func(r *seenRange) time.Time { return r.last })
}

type campaignRow struct {
	name, account                 string
	parent, parentName, objective string
	first, last                   time.Time
}

func upsertCampaigns(ctx context.Context, tx pgx.Tx, live bool, m map[string]*campaignRow, accountID func(string) int32, c, p *caches) error {
	var ext, names, parents, parentNames, objectives []string
	var nets, accs []int32
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		r := m[k]
		if skip(live, c.campaigns[k], r.last) {
			continue
		}
		net, e := splitNetKey(k)
		nets = append(nets, net)
		ext = append(ext, e)
		names = append(names, r.name)
		parents = append(parents, r.parent)
		parentNames = append(parentNames, r.parentName)
		objectives = append(objectives, r.objective)
		accs = append(accs, accountID(r.account))
		first = append(first, r.first)
		last = append(last, r.last)
	}
	if len(ext) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO tracks.campaign AS c (network_id, external_id, name, account_id, parent_external_id,
		                                  parent_name, objective, first_seen_at, last_seen_at)
		SELECT nw, e, NULLIF(n, ''), NULLIF(a, 0), NULLIF(pe, ''), NULLIF(pn, ''), NULLIF(o, ''), f, l
		FROM unnest($1::smallint[], $2::text[], $3::text[], $4::int[], $5::timestamptz[], $6::timestamptz[],
		            $7::text[], $8::text[], $9::text[]) AS u(nw, e, n, a, f, l, pe, pn, o)
		ON CONFLICT (network_id, external_id) DO UPDATE SET
			name = COALESCE(EXCLUDED.name, c.name),
			account_id = COALESCE(EXCLUDED.account_id, c.account_id),
			parent_external_id = COALESCE(EXCLUDED.parent_external_id, c.parent_external_id),
			parent_name = COALESCE(EXCLUDED.parent_name, c.parent_name),
			objective = COALESCE(EXCLUDED.objective, c.objective),
			first_seen_at = LEAST(c.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(c.last_seen_at, EXCLUDED.last_seen_at)
		RETURNING network_id || '|' || external_id, id`, nets, ext, names, accs, first, last,
		parents, parentNames, objectives)
	if err != nil {
		return fmt.Errorf("upsert campaigns: %w", err)
	}
	return scanCached(rows, m, p.campaigns, func(r *campaignRow) time.Time { return r.last })
}

type creativeRow struct {
	ad          *parse.Ad
	video       bool
	first, last time.Time
}

func upsertCreatives(ctx context.Context, tx pgx.Tx, live bool, m map[string]*creativeRow, c, p *caches) error {
	var keys, images, formats, thumbs, langs []string
	var durations []int32
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		r := m[k]
		if skip(live, c.creatives[k], r.last) && !r.video {
			continue
		}
		format := r.ad.FormatType
		if r.video {
			format = "video"
		}
		keys = append(keys, k)
		images = append(images, r.ad.ImageURL)
		formats = append(formats, format)
		durations = append(durations, int32(r.ad.VideoDuration))
		thumbs = append(thumbs, r.ad.ThumbDimensions)
		langs = append(langs, r.ad.Language)
		first = append(first, r.first)
		last = append(last, r.last)
	}
	if len(keys) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO tracks.creative AS c (creative_key, image_url, format_type, video_duration, thumb_dimensions,
		                                  language, first_seen_at, last_seen_at)
		SELECT k, i, NULLIF(fm, ''), NULLIF(d, 0), NULLIF(t, ''), NULLIF(lg, ''), f, l
		FROM unnest($1::text[], $2::text[], $3::text[], $4::int[], $5::text[], $6::text[],
		            $7::timestamptz[], $8::timestamptz[]) AS u(k, i, fm, d, t, lg, f, l)
		ON CONFLICT (creative_key) DO UPDATE SET
			format_type = CASE WHEN EXCLUDED.format_type = 'video' THEN 'video' ELSE c.format_type END,
			video_duration = GREATEST(c.video_duration, EXCLUDED.video_duration),
			thumb_dimensions = COALESCE(c.thumb_dimensions, EXCLUDED.thumb_dimensions),
			first_seen_at = LEAST(c.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(c.last_seen_at, EXCLUDED.last_seen_at)
		RETURNING creative_key, id`, keys, images, formats, durations, thumbs, langs, first, last)
	if err != nil {
		return fmt.Errorf("upsert creatives: %w", err)
	}
	return scanCached(rows, m, p.creatives, func(r *creativeRow) time.Time { return r.last })
}

type adRow struct {
	description, cta string
	brand, account   int32
	first, last      time.Time
}

func upsertAds(ctx context.Context, tx pgx.Tx, live bool, m map[adKey]*adRow, c, p *caches) error {
	keys := make([]adKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].creativeID != keys[j].creativeID {
			return keys[i].creativeID < keys[j].creativeID
		}
		return keys[i].headline < keys[j].headline
	})
	var creatives, brands, accounts []int32
	var headlines, descs, ctas []string
	var first, last []time.Time
	for _, k := range keys {
		r := m[k]
		if skip(live, c.ads[k], r.last) {
			continue
		}
		creatives = append(creatives, k.creativeID)
		headlines = append(headlines, k.headline)
		descs = append(descs, r.description)
		ctas = append(ctas, r.cta)
		brands = append(brands, r.brand)
		accounts = append(accounts, r.account)
		first = append(first, r.first)
		last = append(last, r.last)
	}
	if len(creatives) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO tracks.ad AS a (creative_id, headline, description, cta, brand_id, account_id, first_seen_at, last_seen_at)
		SELECT c, h, NULLIF(d, ''), NULLIF(ct, ''), NULLIF(b, 0), NULLIF(ac, 0), f, l
		FROM unnest($1::int[], $2::text[], $3::text[], $4::text[], $5::int[], $6::int[],
		            $7::timestamptz[], $8::timestamptz[]) AS u(c, h, d, ct, b, ac, f, l)
		ON CONFLICT (creative_id, md5(headline)) DO UPDATE SET
			description = COALESCE(EXCLUDED.description, a.description),
			cta = COALESCE(EXCLUDED.cta, a.cta),
			brand_id = COALESCE(EXCLUDED.brand_id, a.brand_id),
			account_id = COALESCE(EXCLUDED.account_id, a.account_id),
			first_seen_at = LEAST(a.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(a.last_seen_at, EXCLUDED.last_seen_at)
		RETURNING creative_id, headline, id`, creatives, headlines, descs, ctas, brands, accounts, first, last)
	if err != nil {
		return fmt.Errorf("upsert ads: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k adKey
		var id int32
		if err := rows.Scan(&k.creativeID, &k.headline, &id); err != nil {
			return err
		}
		p.ads[k] = &cached{id: id, touched: m[k].last}
	}
	return rows.Err()
}

type linkRow struct {
	ad          *parse.Ad
	first, last time.Time
}

func upsertLinks(ctx context.Context, tx pgx.Tx, live bool, m map[uuid.UUID]*linkRow, c, p *caches) error {
	keys := make([]uuid.UUID, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	var lk []uuid.UUID
	var hosts, paths, items, trackers, networks, params, samples []string
	var first, last []time.Time
	for _, k := range keys {
		r := m[k]
		if skip(live, c.links[k], r.last) {
			continue
		}
		lk = append(lk, k)
		hosts = append(hosts, URLHost(r.ad.ClickURL))
		paths = append(paths, URLPath(r.ad.ClickURL))
		items = append(items, r.ad.ItemID)
		trackers = append(trackers, r.ad.Tracker)
		networks = append(networks, r.ad.AffiliateNetwork)
		pj := string(r.ad.Params)
		if pj == "" {
			pj = "{}"
		}
		params = append(params, pj)
		samples = append(samples, r.ad.ClickURL)
		first = append(first, r.first)
		last = append(last, r.last)
	}
	if len(lk) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO tracks.link AS l (link_key, host, path, item_id, tracker, affiliate_network, params,
		                              sample_url, first_seen_at, last_seen_at)
		SELECT k, h, pa, NULLIF(i, ''), NULLIF(t, ''), NULLIF(n, ''), pr::jsonb, s, f, la
		FROM unnest($1::uuid[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[],
		            $8::text[], $9::timestamptz[], $10::timestamptz[]) AS u(k, h, pa, i, t, n, pr, s, f, la)
		ON CONFLICT (link_key) DO UPDATE SET
			sample_url = CASE WHEN EXCLUDED.last_seen_at > l.last_seen_at THEN EXCLUDED.sample_url ELSE l.sample_url END,
			first_seen_at = LEAST(l.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(l.last_seen_at, EXCLUDED.last_seen_at)
		RETURNING link_key, id`, lk, hosts, paths, items, trackers, networks, params, samples, first, last)
	if err != nil {
		return fmt.Errorf("upsert links: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k uuid.UUID
		var id int32
		if err := rows.Scan(&k, &id); err != nil {
			return err
		}
		p.links[k] = &cached{id: id, touched: m[k].last}
	}
	return rows.Err()
}

type networkAdRow struct {
	d            *parse.NetworkAd
	ad, campaign int32
	first, last  time.Time
}

// upsertNetworkAds stores each ad network ad id (NewsBreak adId) seen, with
// the ad and campaign it belongs to.
func upsertNetworkAds(ctx context.Context, tx pgx.Tx, live bool, m map[string]*networkAdRow, c, p *caches) error {
	var nets, adIDs, camps []int32
	var ext, crs, names, icons, layouts, aspects, launches, iab1, iab2, domains, discl, extras []string
	var started []*time.Time
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		r := m[k]
		if skip(live, c.networkAds[k], r.last) {
			continue
		}
		net, e := splitNetKey(k)
		d := r.d
		nets = append(nets, net)
		ext = append(ext, e)
		adIDs = append(adIDs, r.ad)
		camps = append(camps, r.campaign)
		crs = append(crs, d.CreativeID)
		names = append(names, d.Name)
		started = append(started, d.StartedAt)
		icons = append(icons, d.IconURL)
		layouts = append(layouts, d.Layout)
		aspects = append(aspects, d.MediaAspect)
		launches = append(launches, d.LaunchOption)
		iab1 = append(iab1, d.IabTier1)
		iab2 = append(iab2, d.IabTier2)
		domains = append(domains, d.LandingDomain)
		discl = append(discl, d.Disclaimer)
		x := string(d.Extra)
		if x == "" {
			x = "{}"
		}
		extras = append(extras, x)
		first = append(first, r.first)
		last = append(last, r.last)
	}
	if len(ext) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO tracks.network_ad AS n (network_id, external_id, ad_id, campaign_id, creative_external_id, name,
		                                    started_at, icon_url, layout, media_aspect, launch_option, iab_tier1,
		                                    iab_tier2, landing_domain, disclaimer, extra, first_seen_at, last_seen_at)
		SELECT nw, e, a, NULLIF(c, 0), NULLIF(cr, ''), NULLIF(nm, ''), st, NULLIF(ic, ''), NULLIF(ly, ''),
		       NULLIF(asp, ''), NULLIF(lo, ''), NULLIF(i1, ''), NULLIF(i2, ''), NULLIF(ld, ''), NULLIF(ds, ''),
		       x::jsonb, f, l
		FROM unnest($1::smallint[], $2::text[], $3::int[], $4::int[], $5::text[], $6::text[], $7::timestamptz[],
		            $8::text[], $9::text[], $10::text[], $11::text[], $12::text[], $13::text[], $14::text[],
		            $15::text[], $16::text[], $17::timestamptz[], $18::timestamptz[])
		     AS u(nw, e, a, c, cr, nm, st, ic, ly, asp, lo, i1, i2, ld, ds, x, f, l)
		ON CONFLICT (network_id, external_id) DO UPDATE SET
			ad_id = EXCLUDED.ad_id,
			campaign_id = COALESCE(EXCLUDED.campaign_id, n.campaign_id),
			creative_external_id = COALESCE(EXCLUDED.creative_external_id, n.creative_external_id),
			name = COALESCE(EXCLUDED.name, n.name),
			started_at = COALESCE(EXCLUDED.started_at, n.started_at),
			icon_url = COALESCE(EXCLUDED.icon_url, n.icon_url),
			layout = COALESCE(EXCLUDED.layout, n.layout),
			media_aspect = COALESCE(EXCLUDED.media_aspect, n.media_aspect),
			launch_option = COALESCE(EXCLUDED.launch_option, n.launch_option),
			iab_tier1 = COALESCE(EXCLUDED.iab_tier1, n.iab_tier1),
			iab_tier2 = COALESCE(EXCLUDED.iab_tier2, n.iab_tier2),
			landing_domain = COALESCE(EXCLUDED.landing_domain, n.landing_domain),
			disclaimer = COALESCE(EXCLUDED.disclaimer, n.disclaimer),
			extra = n.extra || EXCLUDED.extra,
			first_seen_at = LEAST(n.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(n.last_seen_at, EXCLUDED.last_seen_at)
		RETURNING network_id || '|' || external_id, id`,
		nets, ext, adIDs, camps, crs, names, started, icons, layouts, aspects, launches, iab1, iab2, domains,
		discl, extras, first, last)
	if err != nil {
		return fmt.Errorf("upsert network ads: %w", err)
	}
	return scanCached(rows, m, p.networkAds, func(r *networkAdRow) time.Time { return r.last })
}

func scanCached[R any](rows pgx.Rows, m map[string]R, out map[string]*cached, last func(R) time.Time) error {
	defer rows.Close()
	for rows.Next() {
		var k string
		var id int32
		if err := rows.Scan(&k, &id); err != nil {
			return err
		}
		out[k] = &cached{id: id, touched: last(m[k])}
	}
	return rows.Err()
}

var (
	rxURLHost = regexp.MustCompile(`^[a-zA-Z]+://([^/?#]+)`)
	rxURLPath = regexp.MustCompile(`^[a-zA-Z]+://[^/?#]+([^?#]*)`)
	digits    = regexp.MustCompile(`^\d{1,9}$`)
)

// URLHost matches tracks.url_host().
func URLHost(u string) string {
	m := rxURLHost.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// URLPath matches tracks.url_path().
func URLPath(u string) string {
	m := rxURLPath.FindStringSubmatch(u)
	if m == nil || m[1] == "" {
		return "/"
	}
	return m[1]
}

// LinkKey matches tracks.link_key(): host + path + campaign + item.
func LinkKey(clickURL, campaign, item string) uuid.UUID {
	return uuid.UUID(md5.Sum([]byte(URLHost(clickURL) + "|" + URLPath(clickURL) + "|" + campaign + "|" + item)))
}

// siteID keeps a Taboola sub-site id that fits an integer.
func siteID(s string) any {
	if !digits.MatchString(s) {
		return nil
	}
	n, _ := strconv.Atoi(s)
	return int32(n)
}

func nullInt(v int32) any {
	if v == 0 {
		return nil
	}
	return v
}

func optReal(v *float64) any {
	if v == nil {
		return nil
	}
	return float32(*v)
}
