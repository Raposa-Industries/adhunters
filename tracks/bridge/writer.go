// The collector's spy writer (adhunters-collector e20148c,
// internal/db/spywriter.go), moved here so Tracks' scrapes reach the
// collector's database exactly as its sweeper's did. What changed: it is
// called directly instead of through a channel, the ad type is parse.Ad
// (the same fields; the loader ported them from the writer's ScrapeAd), and
// each batch also files its ads' click links in spy.walk_queue for the
// collector's landing page walker (fileWalks). Everything between is the
// collector's code as it was, so the counts come out the same.

package bridge

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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/tracks/parse"
)

// SpyWriter writes scrapes into the collector's spy schema (013). One
// goroutine owns it: lookups resolve from in-memory caches, and the counts,
// creative_link and creative_campaign are updated in the same transaction as
// the scrapes.
type SpyWriter struct {
	pool *pgxpool.Pool

	// Caches, owned by the writer goroutine. Only committed ids go in.
	// Accounts and campaigns are keyed by netKey (ad network plus external id).
	networks   map[string]int32
	devices    map[string]int32
	publishers map[string]int32 // raw publisher string (alias) -> publisher id
	placements map[string]int32
	proxyLines map[string]int32
	brands     map[string]int32
	accounts   map[string]*cached
	campaigns  map[string]*cached
	creatives  map[string]*cached
	ads        map[adKey]*cached
	links      map[uuid.UUID]*cached
	networkAds map[string]*cached // netKey of the network's ad id
	partitions map[string]bool
	walks      map[int32]time.Time // ad id -> when its link was last filed in spy.walk_queue
}

// cached is a row id plus the newest last_seen_at written for it. Rows seen
// again within touchEvery are not updated, which saves most upserts.
type cached struct {
	id      int32
	touched time.Time
}

const (
	touchEvery    = 10 * time.Minute
	walkEvery     = 10 * time.Minute // an ad's link is filed for the walker at most this often
	maxBatch      = 64
	maxCacheItems = 500000
)

type adKey struct {
	creativeID int32
	headline   string
}

// Scrape is one fetch of one publisher page, on one device, through one proxy
// line. Ads is empty when the page returned no ads.
type Scrape struct {
	BatchID         uuid.UUID
	Network         string // spy.network code; empty means taboola
	ScrapedAt       time.Time
	Publisher       string
	Device          string
	ProxyLine       string
	LatencyMs       int
	GeoCountry      string
	TrcRoute        string
	WorkerNode      string
	PipelineVersion string
	PayloadChecksum string
	Referer         string // the publisher page, sent to the walker as the click's referer
	Ads             []ScrapeAd
}

// ScrapeAd is one sighting: one ad seen once in the scrape. The loader's
// parse.Ad is the collector's ScrapeAd plus the live link and the auction,
// which the collector's writer never stored.
type ScrapeAd = parse.Ad

// WrittenAd holds the ids one ScrapeAd was stored under.
type WrittenAd struct {
	CreativeID int32
	AdID       int32
	AccountID  int32 // 0: no account on the ad
}

// NewSpyWriter returns a writer for the collector's database at pool.
func NewSpyWriter(pool *pgxpool.Pool) *SpyWriter {
	return &SpyWriter{
		pool:       pool,
		networks:   map[string]int32{},
		devices:    map[string]int32{},
		publishers: map[string]int32{},
		placements: map[string]int32{},
		proxyLines: map[string]int32{},
		brands:     map[string]int32{},
		accounts:   map[string]*cached{},
		campaigns:  map[string]*cached{},
		creatives:  map[string]*cached{},
		ads:        map[adKey]*cached{},
		links:      map[uuid.UUID]*cached{},
		networkAds: map[string]*cached{},
		partitions: map[string]bool{},
		walks:      map[int32]time.Time{},
	}
}

// Write stores scrapes in batches of up to 64 (the collector's group commit)
// and returns the ids of each ad, lined up with scrapes. A scrape already
// stored, by its BatchID, is skipped with its sightings, so writing the same
// scrapes again counts nothing twice. When a batch fails, its scrapes are
// written one by one, as the collector did, and the first error is returned.
func (w *SpyWriter) Write(ctx context.Context, scrapes []Scrape) ([][]WrittenAd, error) {
	out := make([][]WrittenAd, 0, len(scrapes))
	for start := 0; start < len(scrapes); start += maxBatch {
		batch := scrapes[start:min(start+maxBatch, len(scrapes))]
		ids, err := w.writeBatch(ctx, batch)
		if err == nil {
			out = append(out, ids...)
			continue
		}
		if len(batch) == 1 || ctx.Err() != nil {
			return out, err
		}
		for _, s := range batch {
			ids, err := w.writeBatch(ctx, []Scrape{s})
			if err != nil {
				return out, err
			}
			out = append(out, ids[0])
		}
	}
	return out, nil
}

// pending holds ids learned inside the current transaction. They reach the
// caches only after commit, so a rollback never leaves a wrong id behind.
type pending struct {
	devices, publishers, placements, proxyLines, brands map[string]int32
	accounts, campaigns, creatives                      map[string]*cached
	ads                                                 map[adKey]*cached
	links                                               map[uuid.UUID]*cached
	networkAds                                          map[string]*cached
}

func newPending() *pending {
	return &pending{
		devices: map[string]int32{}, publishers: map[string]int32{}, placements: map[string]int32{},
		proxyLines: map[string]int32{}, brands: map[string]int32{},
		accounts: map[string]*cached{}, campaigns: map[string]*cached{}, creatives: map[string]*cached{},
		ads: map[adKey]*cached{}, links: map[uuid.UUID]*cached{}, networkAds: map[string]*cached{},
	}
}

func (w *SpyWriter) commitCaches(p *pending) {
	if len(w.ads) > maxCacheItems || len(w.links) > maxCacheItems {
		w.ads = map[adKey]*cached{}
		w.links = map[uuid.UUID]*cached{}
		w.creatives = map[string]*cached{}
		w.campaigns = map[string]*cached{}
		w.networkAds = map[string]*cached{}
	}
	if len(w.walks) > maxCacheItems {
		w.walks = map[int32]time.Time{}
	}
	for _, m := range []struct{ dst, src map[string]int32 }{
		{w.devices, p.devices}, {w.publishers, p.publishers}, {w.placements, p.placements},
		{w.proxyLines, p.proxyLines}, {w.brands, p.brands},
	} {
		for k, v := range m.src {
			m.dst[k] = v
		}
	}
	for _, m := range []struct{ dst, src map[string]*cached }{
		{w.accounts, p.accounts}, {w.campaigns, p.campaigns}, {w.creatives, p.creatives},
		{w.networkAds, p.networkAds},
	} {
		for k, v := range m.src {
			m.dst[k] = v
		}
	}
	for k, v := range p.ads {
		w.ads[k] = v
	}
	for k, v := range p.links {
		w.links[k] = v
	}
}

// ensurePartitions creates the daily sighting partitions for the batch's days
// (and the next day), outside the write transaction.
func (w *SpyWriter) ensurePartitions(ctx context.Context, scrapes []Scrape) error {
	for _, s := range scrapes {
		day := s.ScrapedAt.UTC().Format("2006-01-02")
		if w.partitions[day] {
			continue
		}
		next := s.ScrapedAt.UTC().AddDate(0, 0, 1).Format("2006-01-02")
		if _, err := w.pool.Exec(ctx, `SELECT spy.ensure_sighting_partitions($1::date, $2::date)`, day, next); err != nil {
			return fmt.Errorf("ensure sighting partitions %s: %w", day, err)
		}
		w.partitions[day] = true
	}
	return nil
}

// line is one sighting with every id resolved.
type line struct {
	scrapeIdx int
	ad        *ScrapeAd
	seenAt    time.Time
	publisher int32
	device    int32
	creative  int32
	adID      int32
	placement int32
	campaign  int32
	link      int32
	account   int32 // 0: no account on the ad
	brand     int32 // 0: no brand on the ad
}

func (w *SpyWriter) writeBatch(ctx context.Context, scrapes []Scrape) ([][]WrittenAd, error) {
	if err := w.ensurePartitions(ctx, scrapes); err != nil {
		return nil, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p := newPending()

	// 1. Lookups by name.
	var nets, devs, pubs, places, proxies, brands []string
	for _, s := range scrapes {
		nets = append(nets, networkCode(s))
		devs = append(devs, s.Device)
		pubs = append(pubs, s.Publisher)
		if s.ProxyLine != "" {
			proxies = append(proxies, s.ProxyLine)
		}
		for i := range s.Ads {
			a := &s.Ads[i]
			if a.Placement != "" {
				places = append(places, a.Placement)
			}
			if b := strings.TrimSpace(a.Brand); b != "" {
				brands = append(brands, b)
			}
		}
	}
	if err := w.resolveNetworks(ctx, tx, nets); err != nil {
		return nil, err
	}
	if err := resolveNames(ctx, tx, "device", "code", devs, w.devices, p.devices); err != nil {
		return nil, err
	}
	if err := resolveNames(ctx, tx, "placement", "name", places, w.placements, p.placements); err != nil {
		return nil, err
	}
	if err := resolveNames(ctx, tx, "proxy_line", "code", proxies, w.proxyLines, p.proxyLines); err != nil {
		return nil, err
	}
	if err := resolveNames(ctx, tx, "brand", "name", brands, w.brands, p.brands); err != nil {
		return nil, err
	}
	if err := resolvePublishers(ctx, tx, pubs, w.publishers, p.publishers); err != nil {
		return nil, err
	}
	name := func(cache, fresh map[string]int32, k string) int32 {
		if v, ok := fresh[k]; ok {
			return v
		}
		return cache[k]
	}

	// 2. Accounts, campaigns, creatives.
	accounts := map[string]*seenRange{}
	orgs := map[string]string{} // account netKey -> org id
	campaigns := map[string]*campaignRow{}
	creatives := map[string]*creativeRow{}
	for _, s := range scrapes {
		t := s.ScrapedAt
		net := w.networks[networkCode(s)]
		for i := range s.Ads {
			a := &s.Ads[i]
			if a.Account != "" {
				widen(accounts, netKey(net, a.Account), t)
				if a.OrgID != "" {
					orgs[netKey(net, a.Account)] = a.OrgID
				}
			}
			if a.CampaignID != "" {
				ck := netKey(net, a.CampaignID)
				c := campaigns[ck]
				if c == nil {
					c = &campaignRow{first: t, last: t}
					campaigns[ck] = c
				}
				c.widen(t)
				if a.CampaignName != "" {
					c.name = a.CampaignName
				}
				if a.ParentCampaignID != "" {
					c.parent = a.ParentCampaignID
				}
				if a.ParentCampaign != "" {
					c.parentName = a.ParentCampaign
				}
				if a.Objective != "" {
					c.objective = a.Objective
				}
				if a.Account != "" {
					c.account = netKey(net, a.Account)
				}
			}
			c := creatives[a.CreativeKey]
			if c == nil {
				c = &creativeRow{ad: a, first: t, last: t}
				creatives[a.CreativeKey] = c
			}
			c.widen(t)
			if a.FormatType == "video" {
				c.video = true
			}
		}
	}
	if err := w.upsertAccounts(ctx, tx, accounts, orgs, p); err != nil {
		return nil, err
	}
	// accountID and campaignID take a netKey, or "" for none.
	accountID := func(key string) int32 {
		if key == "" {
			return 0
		}
		if c, ok := p.accounts[key]; ok {
			return c.id
		}
		return w.accounts[key].id
	}
	if err := w.upsertCampaigns(ctx, tx, campaigns, accountID, p); err != nil {
		return nil, err
	}
	if err := w.upsertCreatives(ctx, tx, creatives, p); err != nil {
		return nil, err
	}
	creativeID := func(key string) int32 {
		if c, ok := p.creatives[key]; ok {
			return c.id
		}
		return w.creatives[key].id
	}
	campaignID := func(key string) int32 {
		if key == "" {
			return 0
		}
		if c, ok := p.campaigns[key]; ok {
			return c.id
		}
		return w.campaigns[key].id
	}

	// 3. Ads and links.
	ads := map[adKey]*adRow{}
	links := map[uuid.UUID]*linkRow{}
	var lines []line
	for si, s := range scrapes {
		t := s.ScrapedAt
		net := w.networks[networkCode(s)]
		for i := range s.Ads {
			a := &s.Ads[i]
			ln := line{
				scrapeIdx: si, ad: a, seenAt: t,
				publisher: name(w.publishers, p.publishers, s.Publisher),
				device:    name(w.devices, p.devices, s.Device),
				creative:  creativeID(a.CreativeKey),
				placement: name(w.placements, p.placements, a.Placement),
				campaign:  campaignID(optNetKey(net, a.CampaignID)),
			}
			if b := strings.TrimSpace(a.Brand); b != "" {
				ln.brand = name(w.brands, p.brands, b)
			}
			if a.Account != "" {
				ln.account = accountID(netKey(net, a.Account))
			}
			k := adKey{ln.creative, a.Headline}
			r := ads[k]
			if r == nil {
				r = &adRow{first: t, last: t}
				ads[k] = r
			}
			r.widen(t)
			// Latest non-empty values win, as in the old loader.
			if a.Description != "" {
				r.description = a.Description
			}
			if a.Cta != "" {
				r.cta = a.Cta
			}
			if ln.brand != 0 {
				r.brand = ln.brand
			}
			if ln.account != 0 {
				r.account = ln.account
			}
			if a.ClickURL != "" {
				lk := LinkKey(a.ClickURL, a.CampaignID, a.ItemID)
				l := links[lk]
				if l == nil {
					l = &linkRow{ad: a, first: t, last: t}
					links[lk] = l
				}
				l.widen(t)
				if !t.Before(l.last) {
					l.ad = a
				}
			}
			lines = append(lines, ln)
		}
	}
	if err := w.upsertAds(ctx, tx, ads, p); err != nil {
		return nil, err
	}
	if err := w.upsertLinks(ctx, tx, links, p); err != nil {
		return nil, err
	}
	for i := range lines {
		ln := &lines[i]
		k := adKey{ln.creative, ln.ad.Headline}
		if c, ok := p.ads[k]; ok {
			ln.adID = c.id
		} else {
			ln.adID = w.ads[k].id
		}
		if ln.ad.ClickURL != "" {
			lk := LinkKey(ln.ad.ClickURL, ln.ad.CampaignID, ln.ad.ItemID)
			if c, ok := p.links[lk]; ok {
				ln.link = c.id
			} else {
				ln.link = w.links[lk].id
			}
		}
	}

	if err := w.upsertNetworkAds(ctx, tx, scrapes, lines, p); err != nil {
		return nil, err
	}

	// 4. Scrapes. A scrape already stored (a retry after a lost reply) is
	// skipped with its sightings, so nothing is counted twice.
	scrapeIDs, err := insertScrapes(ctx, tx, scrapes, func(s Scrape) (int32, int32, int32) {
		var pl int32
		if s.ProxyLine != "" {
			pl = name(w.proxyLines, p.proxyLines, s.ProxyLine)
		}
		return name(w.publishers, p.publishers, s.Publisher), name(w.devices, p.devices, s.Device), pl
	})
	if err != nil {
		return nil, err
	}

	// 5. Sightings.
	var rows [][]any
	var kept []line
	for _, ln := range lines {
		sid, ok := scrapeIDs[ln.scrapeIdx]
		if !ok {
			continue
		}
		kept = append(kept, ln)
		rows = append(rows, []any{
			ln.seenAt, sid, ln.adID, ln.creative, ln.publisher, nullInt(ln.placement),
			nullInt(ln.campaign), nullInt(ln.link), siteID(ln.ad.SiteID), optReal(ln.ad.EcpaPercentile),
			int16(ln.device), int16(ln.ad.FeedPosition), int16(ln.ad.BlockPosition),
			optReal(ln.ad.BidPrice), optReal(ln.ad.SecondPrice),
		})
	}
	if len(rows) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"spy", "sighting"},
			[]string{"seen_at", "scrape_id", "ad_id", "creative_id", "publisher_id", "placement_id",
				"campaign_id", "link_id", "site_id", "ecpa_percentile", "device_id", "feed_position", "block_position",
				"bid_price", "second_price"},
			pgx.CopyFromRows(rows)); err != nil {
			return nil, fmt.Errorf("copy sightings: %w", err)
		}
	}

	// 6. Counts.
	if err := writeCounts(ctx, tx, scrapes, scrapeIDs, kept, func(s Scrape) (int32, int32) {
		return name(w.publishers, p.publishers, s.Publisher), name(w.devices, p.devices, s.Device)
	}); err != nil {
		return nil, err
	}

	// 7. The walker's links (tracks-bridge only).
	walks, err := w.fileWalks(ctx, tx, scrapes, kept)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	w.commitCaches(p)
	for ad, t := range walks {
		w.walks[ad] = t
	}

	out := make([][]WrittenAd, len(scrapes))
	for si, s := range scrapes {
		out[si] = make([]WrittenAd, 0, len(s.Ads))
	}
	for _, ln := range lines {
		out[ln.scrapeIdx] = append(out[ln.scrapeIdx], WrittenAd{CreativeID: ln.creative, AdID: ln.adID, AccountID: ln.account})
	}
	return out, nil
}

// ------------------------------------------------------------------------------
// Rows collected per batch
// ------------------------------------------------------------------------------

type seenRange struct{ first, last time.Time }

func (r *seenRange) widen(t time.Time) {
	if t.Before(r.first) {
		r.first = t
	}
	if t.After(r.last) {
		r.last = t
	}
}

func widen(m map[string]*seenRange, k string, t time.Time) {
	r := m[k]
	if r == nil {
		m[k] = &seenRange{t, t}
		return
	}
	r.widen(t)
}

type campaignRow struct {
	name, account                 string
	parent, parentName, objective string
	first, last                   time.Time
}

func (r *campaignRow) widen(t time.Time) { widenPair(&r.first, &r.last, t) }

type creativeRow struct {
	ad          *ScrapeAd
	video       bool
	first, last time.Time
}

func (r *creativeRow) widen(t time.Time) { widenPair(&r.first, &r.last, t) }

type adRow struct {
	description, cta string
	brand, account   int32
	first, last      time.Time
}

func (r *adRow) widen(t time.Time) { widenPair(&r.first, &r.last, t) }

type linkRow struct {
	ad          *ScrapeAd
	first, last time.Time
}

func (r *linkRow) widen(t time.Time) { widenPair(&r.first, &r.last, t) }

func widenPair(first, last *time.Time, t time.Time) {
	if t.Before(*first) {
		*first = t
	}
	if t.After(*last) {
		*last = t
	}
}

// fresh reports whether a cached row was already written recently enough.
func fresh(c *cached, last time.Time) bool {
	return c != nil && !last.After(c.touched.Add(touchEvery))
}

// ------------------------------------------------------------------------------
// Lookups
// ------------------------------------------------------------------------------

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

// resolveNames finds (or adds) rows of a small name table and puts their ids in out.
func resolveNames(ctx context.Context, tx pgx.Tx, table, col string, keys []string, cache, out map[string]int32) error {
	missing := uniqueMissing(keys, cache)
	if len(missing) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`INSERT INTO spy.%s (%s) SELECT unnest($1::text[]) ON CONFLICT (%s) DO NOTHING`, table, col, col), missing); err != nil {
		return fmt.Errorf("add %s: %w", table, err)
	}
	return scanNames(ctx, tx, fmt.Sprintf(`SELECT %s, id::int FROM spy.%s WHERE %s = ANY($1)`, col, table, col), missing, out)
}

// resolvePublishers maps raw publisher strings to publishers through
// spy.publisher_alias. An unknown string becomes a new publisher of that name.
func resolvePublishers(ctx context.Context, tx pgx.Tx, keys []string, cache, out map[string]int32) error {
	missing := uniqueMissing(keys, cache)
	if len(missing) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO spy.publisher (name)
		SELECT a FROM unnest($1::text[]) a
		WHERE NOT EXISTS (SELECT 1 FROM spy.publisher_alias pa WHERE pa.alias = a)
		ON CONFLICT (name) DO NOTHING`, missing); err != nil {
		return fmt.Errorf("add publishers: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO spy.publisher_alias (alias, publisher_id)
		SELECT p.name, p.id FROM spy.publisher p WHERE p.name = ANY($1)
		ON CONFLICT (alias) DO NOTHING`, missing); err != nil {
		return fmt.Errorf("add publisher aliases: %w", err)
	}
	return scanNames(ctx, tx, `SELECT alias, publisher_id FROM spy.publisher_alias WHERE alias = ANY($1)`, missing, out)
}

// resolveNetworks looks up ad network ids by code. Networks are added by
// migrations only, so an unknown code fails the write.
func (w *SpyWriter) resolveNetworks(ctx context.Context, tx pgx.Tx, codes []string) error {
	missing := uniqueMissing(codes, w.networks)
	if len(missing) == 0 {
		return nil
	}
	found := map[string]int32{}
	if err := scanNames(ctx, tx, `SELECT code, id::int FROM spy.network WHERE code = ANY($1)`, missing, found); err != nil {
		return fmt.Errorf("look up networks: %w", err)
	}
	for _, c := range missing {
		if _, ok := found[c]; !ok {
			return fmt.Errorf("unknown ad network %q", c)
		}
	}
	// Network ids never change, so they go straight into the cache.
	for k, v := range found {
		w.networks[k] = v
	}
	return nil
}

func networkCode(s Scrape) string {
	if s.Network == "" {
		return "taboola"
	}
	return s.Network
}

// netKey is the cache key of an account or campaign: the ad network id plus
// the id on that network. It matches network_id || '|' || external_id.
func netKey(network int32, ext string) string {
	return strconv.Itoa(int(network)) + "|" + ext
}

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

// ------------------------------------------------------------------------------
// Upserts
// ------------------------------------------------------------------------------

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (w *SpyWriter) upsertAccounts(ctx context.Context, tx pgx.Tx, m map[string]*seenRange, orgs map[string]string, p *pending) error {
	var nets []int32
	var ext, orgIDs []string
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		if fresh(w.accounts[k], m[k].last) {
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
		INSERT INTO spy.account AS a (network_id, external_id, org_external_id, first_seen_at, last_seen_at)
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

func (w *SpyWriter) upsertCampaigns(ctx context.Context, tx pgx.Tx, m map[string]*campaignRow, accountID func(string) int32, p *pending) error {
	var ext, names, parents, parentNames, objectives []string
	var nets, accs []int32
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		r := m[k]
		if fresh(w.campaigns[k], r.last) {
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
		INSERT INTO spy.campaign AS c (network_id, external_id, name, account_id, parent_external_id,
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

func (w *SpyWriter) upsertCreatives(ctx context.Context, tx pgx.Tx, m map[string]*creativeRow, p *pending) error {
	var keys, images, formats, thumbs, langs []string
	var durations []int32
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		r := m[k]
		if fresh(w.creatives[k], r.last) && !r.video {
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
		INSERT INTO spy.creative AS c (creative_key, image_url, format_type, video_duration, thumb_dimensions,
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

func (w *SpyWriter) upsertAds(ctx context.Context, tx pgx.Tx, m map[adKey]*adRow, p *pending) error {
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
		if fresh(w.ads[k], r.last) {
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
		INSERT INTO spy.ad AS a (creative_id, headline, description, cta, brand_id, account_id, first_seen_at, last_seen_at)
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

func (w *SpyWriter) upsertLinks(ctx context.Context, tx pgx.Tx, m map[uuid.UUID]*linkRow, p *pending) error {
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
		if fresh(w.links[k], r.last) {
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
		INSERT INTO spy.link AS l (link_key, host, path, item_id, tracker, affiliate_network, params,
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

// upsertNetworkAds stores each ad network ad id (NewsBreak adId) seen in the
// batch, with the ad and campaign it belongs to.
func (w *SpyWriter) upsertNetworkAds(ctx context.Context, tx pgx.Tx, scrapes []Scrape, lines []line, p *pending) error {
	type row struct {
		ln          *line
		d           *parse.NetworkAd
		first, last time.Time
	}
	m := map[string]*row{}
	for i := range lines {
		ln := &lines[i]
		a := ln.ad
		if a.NetworkAd == nil || a.ItemID == "" {
			continue
		}
		k := netKey(w.networks[networkCode(scrapes[ln.scrapeIdx])], a.ItemID)
		r := m[k]
		if r == nil {
			r = &row{ln: ln, d: a.NetworkAd, first: ln.seenAt, last: ln.seenAt}
			m[k] = r
		}
		widenPair(&r.first, &r.last, ln.seenAt)
	}
	var nets, adIDs, camps []int32
	var ext, crs, names, icons, layouts, aspects, launches, iab1, iab2, domains, discl, extras []string
	var started []*time.Time
	var first, last []time.Time
	for _, k := range sortedKeys(m) {
		r := m[k]
		if fresh(w.networkAds[k], r.last) {
			continue
		}
		net, e := splitNetKey(k)
		d := r.d
		nets = append(nets, net)
		ext = append(ext, e)
		adIDs = append(adIDs, r.ln.adID)
		camps = append(camps, r.ln.campaign)
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
		INSERT INTO spy.network_ad AS n (network_id, external_id, ad_id, campaign_id, creative_external_id, name,
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
	return scanCached(rows, m, p.networkAds, func(r *row) time.Time { return r.last })
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

func insertScrapes(ctx context.Context, tx pgx.Tx, scrapes []Scrape, ids func(Scrape) (int32, int32, int32)) (map[int]int64, error) {
	var batch []uuid.UUID
	var at []time.Time
	var pubs, devs, proxies, counts, latency []int32
	var geo, route, worker, version, checksum []string
	for _, s := range scrapes {
		pub, dev, pl := ids(s)
		batch = append(batch, s.BatchID)
		at = append(at, s.ScrapedAt)
		pubs = append(pubs, pub)
		devs = append(devs, dev)
		proxies = append(proxies, pl)
		counts = append(counts, int32(len(s.Ads)))
		latency = append(latency, int32(s.LatencyMs))
		geo = append(geo, s.GeoCountry)
		route = append(route, s.TrcRoute)
		worker = append(worker, s.WorkerNode)
		version = append(version, s.PipelineVersion)
		checksum = append(checksum, s.PayloadChecksum)
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO spy.scrape (batch_uid, scraped_at, publisher_id, device_id, proxy_line_id, ad_count,
		                        latency_ms, geo_country, trc_route, worker_node, pipeline_version, payload_checksum)
		SELECT b, t, p, d::smallint, NULLIF(pl, 0)::smallint, n, lat, NULLIF(g, ''), NULLIF(r, ''),
		       NULLIF(w, ''), NULLIF(v, ''), NULLIF(c, '')
		FROM unnest($1::uuid[], $2::timestamptz[], $3::int[], $4::int[], $5::int[], $6::int[], $7::int[],
		            $8::text[], $9::text[], $10::text[], $11::text[], $12::text[]) AS u(b, t, p, d, pl, n, lat, g, r, w, v, c)
		ON CONFLICT (batch_uid) DO NOTHING
		RETURNING batch_uid, id`, batch, at, pubs, devs, proxies, counts, latency, geo, route, worker, version, checksum)
	if err != nil {
		return nil, fmt.Errorf("insert scrapes: %w", err)
	}
	defer rows.Close()
	byBatch := map[uuid.UUID]int64{}
	for rows.Next() {
		var b uuid.UUID
		var id int64
		if err := rows.Scan(&b, &id); err != nil {
			return nil, err
		}
		byBatch[b] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[int]int64{}
	for i, s := range scrapes {
		if id, ok := byBatch[s.BatchID]; ok {
			out[i] = id
		}
	}
	return out, nil
}

// ------------------------------------------------------------------------------
// Counts
// ------------------------------------------------------------------------------

type hourKey struct {
	hour               time.Time
	ad, publisher, dev int32
}

// ownerKey is one ad in one hour under one account and one brand, on one
// publisher and device (033).
type ownerKey struct {
	hour                               time.Time
	ad, account, brand, publisher, dev int32
}

// ownerDayKey is the same for one UTC day (033).
type ownerDayKey struct {
	day                                string
	ad, account, brand, publisher, dev int32
}

type placementKey struct {
	day                      string
	ad, publisher, placement int32
}

type campaignKey struct {
	day                 string
	campaign, publisher int32
}

type dayKey struct {
	day                string
	ad, publisher, dev int32
}

type count struct {
	sightings, posSum int64
	posMin, posMax    int32
	scrapes           map[int64]bool
	first, last       time.Time
	creative          int32
}

func (c *count) add(scrape int64, pos int32, t time.Time) {
	if c.scrapes == nil {
		c.scrapes = map[int64]bool{}
		c.posMin, c.posMax = pos, pos
		c.first, c.last = t, t
	}
	c.sightings++
	c.posSum += int64(pos)
	c.scrapes[scrape] = true
	if pos < c.posMin {
		c.posMin = pos
	}
	if pos > c.posMax {
		c.posMax = pos
	}
	widenPair(&c.first, &c.last, t)
}

func writeCounts(ctx context.Context, tx pgx.Tx, scrapes []Scrape, scrapeIDs map[int]int64, lines []line, ids func(Scrape) (int32, int32)) error {
	hourly := map[hourKey]*count{}
	daily := map[dayKey]*count{}
	placement := map[placementKey]*count{}
	campaign := map[campaignKey]*count{}
	pubHourly := map[hourKey]*count{}
	creativeLink := map[[2]int32]*count{}
	creativeCampaign := map[[2]int32]*count{}
	owners := map[ownerKey]int32{}
	ownerDays := map[ownerDayKey]*count{}

	for _, ln := range lines {
		sid := scrapeIDs[ln.scrapeIdx]
		pos := int32(ln.ad.FeedPosition)
		hour := ln.seenAt.UTC().Truncate(time.Hour)
		day := ln.seenAt.UTC().Format("2006-01-02")

		hk := hourKey{hour, ln.adID, ln.publisher, ln.device}
		if hourly[hk] == nil {
			hourly[hk] = &count{}
		}
		hourly[hk].add(sid, pos, ln.seenAt)
		owners[ownerKey{hour, ln.adID, ln.account, ln.brand, ln.publisher, ln.device}]++
		odk := ownerDayKey{day, ln.adID, ln.account, ln.brand, ln.publisher, ln.device}
		if ownerDays[odk] == nil {
			ownerDays[odk] = &count{creative: ln.creative}
		}
		ownerDays[odk].add(sid, pos, ln.seenAt)

		dk := dayKey{day, ln.adID, ln.publisher, ln.device}
		if daily[dk] == nil {
			daily[dk] = &count{creative: ln.creative}
		}
		daily[dk].add(sid, pos, ln.seenAt)

		if ln.placement != 0 {
			pk := placementKey{day, ln.adID, ln.publisher, ln.placement}
			if placement[pk] == nil {
				placement[pk] = &count{}
			}
			placement[pk].add(sid, pos, ln.seenAt)
		}
		if ln.campaign != 0 {
			ck := campaignKey{day, ln.campaign, ln.publisher}
			if campaign[ck] == nil {
				campaign[ck] = &count{}
			}
			campaign[ck].add(sid, pos, ln.seenAt)
			cc := [2]int32{ln.creative, ln.campaign}
			if creativeCampaign[cc] == nil {
				creativeCampaign[cc] = &count{}
			}
			creativeCampaign[cc].add(sid, pos, ln.seenAt)
		}
		if ln.link != 0 {
			cl := [2]int32{ln.creative, ln.link}
			if creativeLink[cl] == nil {
				creativeLink[cl] = &count{}
			}
			creativeLink[cl].add(sid, pos, ln.seenAt)
		}
	}
	// Every stored scrape counts toward its publisher, empty ones included.
	for i, s := range scrapes {
		sid, ok := scrapeIDs[i]
		if !ok {
			continue
		}
		pub, dev := ids(s)
		k := hourKey{s.ScrapedAt.UTC().Truncate(time.Hour), 0, pub, dev}
		c := pubHourly[k]
		if c == nil {
			c = &count{scrapes: map[int64]bool{}}
			pubHourly[k] = c
		}
		c.scrapes[sid] = true
		c.sightings += int64(len(s.Ads))
	}

	// ad_hourly
	{
		keys := make([]hourKey, 0, len(hourly))
		for k := range hourly {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessHour(keys[i], keys[j]) })
		var hours []time.Time
		var ads, pubs, devs, mins, maxs, sightings, scrapesN []int32
		var sums []int64
		for _, k := range keys {
			c := hourly[k]
			hours = append(hours, k.hour)
			ads = append(ads, k.ad)
			pubs = append(pubs, k.publisher)
			devs = append(devs, k.dev)
			mins = append(mins, c.posMin)
			maxs = append(maxs, c.posMax)
			sightings = append(sightings, int32(c.sightings))
			scrapesN = append(scrapesN, int32(len(c.scrapes)))
			sums = append(sums, c.posSum)
		}
		if len(keys) > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO spy.ad_hourly AS r (hour, ad_id, publisher_id, device_id, feed_position_min,
				                                feed_position_max, sightings, scrapes, feed_position_sum)
				SELECT h, a, p, d::smallint, mn::smallint, mx::smallint, s, sc, ps
				FROM unnest($1::timestamptz[], $2::int[], $3::int[], $4::int[], $5::int[], $6::int[],
				            $7::int[], $8::int[], $9::bigint[]) AS u(h, a, p, d, mn, mx, s, sc, ps)
				ON CONFLICT (hour, ad_id, publisher_id, device_id) DO UPDATE SET
					sightings = r.sightings + EXCLUDED.sightings,
					scrapes = r.scrapes + EXCLUDED.scrapes,
					feed_position_sum = r.feed_position_sum + EXCLUDED.feed_position_sum,
					feed_position_min = LEAST(r.feed_position_min, EXCLUDED.feed_position_min),
					feed_position_max = GREATEST(r.feed_position_max, EXCLUDED.feed_position_max)`,
				hours, ads, pubs, devs, mins, maxs, sightings, scrapesN, sums); err != nil {
				return fmt.Errorf("ad_hourly: %w", err)
			}
		}
	}

	// ad_account_brand_hourly (033)
	if len(owners) > 0 {
		keys := make([]ownerKey, 0, len(owners))
		for k := range owners {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if !a.hour.Equal(b.hour) {
				return a.hour.Before(b.hour)
			}
			if a.ad != b.ad {
				return a.ad < b.ad
			}
			if a.account != b.account {
				return a.account < b.account
			}
			if a.brand != b.brand {
				return a.brand < b.brand
			}
			if a.publisher != b.publisher {
				return a.publisher < b.publisher
			}
			return a.dev < b.dev
		})
		var hours []time.Time
		var ads, accounts, brands, pubs, devs, sightings []int32
		for _, k := range keys {
			hours = append(hours, k.hour)
			ads = append(ads, k.ad)
			accounts = append(accounts, k.account)
			brands = append(brands, k.brand)
			pubs = append(pubs, k.publisher)
			devs = append(devs, k.dev)
			sightings = append(sightings, owners[k])
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO spy.ad_account_brand_hourly AS r (hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings)
			SELECT h, a, NULLIF(ac, 0), NULLIF(b, 0), p, d::smallint, s
			FROM unnest($1::timestamptz[], $2::int[], $3::int[], $4::int[], $5::int[], $6::int[], $7::int[])
			     AS u(h, a, ac, b, p, d, s)
			ON CONFLICT ON CONSTRAINT ad_account_brand_hourly_key DO UPDATE SET
				sightings = r.sightings + EXCLUDED.sightings`,
			hours, ads, accounts, brands, pubs, devs, sightings); err != nil {
			return fmt.Errorf("ad_account_brand_hourly: %w", err)
		}
	}

	// ad_account_daily (033)
	if len(ownerDays) > 0 {
		keys := make([]ownerDayKey, 0, len(ownerDays))
		for k := range ownerDays {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.day != b.day {
				return a.day < b.day
			}
			if a.ad != b.ad {
				return a.ad < b.ad
			}
			if a.account != b.account {
				return a.account < b.account
			}
			if a.brand != b.brand {
				return a.brand < b.brand
			}
			if a.publisher != b.publisher {
				return a.publisher < b.publisher
			}
			return a.dev < b.dev
		})
		var days []string
		var ads, accounts, brands, pubs, devs, creatives, sightings []int32
		var first, last []time.Time
		for _, k := range keys {
			c := ownerDays[k]
			days = append(days, k.day)
			ads = append(ads, k.ad)
			accounts = append(accounts, k.account)
			brands = append(brands, k.brand)
			pubs = append(pubs, k.publisher)
			devs = append(devs, k.dev)
			creatives = append(creatives, c.creative)
			sightings = append(sightings, int32(c.sightings))
			first = append(first, c.first)
			last = append(last, c.last)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO spy.ad_account_daily AS r (day, ad_id, account_id, brand_id, publisher_id, device_id,
			                                       creative_id, sightings, first_seen_at, last_seen_at)
			SELECT dy, a, NULLIF(ac, 0), NULLIF(b, 0), p, d::smallint, c, s, f, l
			FROM unnest($1::date[], $2::int[], $3::int[], $4::int[], $5::int[], $6::int[], $7::int[], $8::int[],
			            $9::timestamptz[], $10::timestamptz[]) AS u(dy, a, ac, b, p, d, c, s, f, l)
			ON CONFLICT ON CONSTRAINT ad_account_daily_key DO UPDATE SET
				sightings = r.sightings + EXCLUDED.sightings,
				first_seen_at = LEAST(r.first_seen_at, EXCLUDED.first_seen_at),
				last_seen_at = GREATEST(r.last_seen_at, EXCLUDED.last_seen_at)`,
			days, ads, accounts, brands, pubs, devs, creatives, sightings, first, last); err != nil {
			return fmt.Errorf("ad_account_daily: %w", err)
		}
	}

	// ad_daily
	{
		keys := make([]dayKey, 0, len(daily))
		for k := range daily {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.day != b.day {
				return a.day < b.day
			}
			if a.ad != b.ad {
				return a.ad < b.ad
			}
			if a.publisher != b.publisher {
				return a.publisher < b.publisher
			}
			return a.dev < b.dev
		})
		var days []string
		var ads, pubs, devs, creatives, sightings, scrapesN []int32
		var sums []int64
		var first, last []time.Time
		for _, k := range keys {
			c := daily[k]
			days = append(days, k.day)
			ads = append(ads, k.ad)
			pubs = append(pubs, k.publisher)
			devs = append(devs, k.dev)
			creatives = append(creatives, c.creative)
			sightings = append(sightings, int32(c.sightings))
			scrapesN = append(scrapesN, int32(len(c.scrapes)))
			sums = append(sums, c.posSum)
			first = append(first, c.first)
			last = append(last, c.last)
		}
		if len(keys) > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO spy.ad_daily AS r (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes,
				                               feed_position_sum, first_seen_at, last_seen_at)
				SELECT dy, a, p, d::smallint, c, s, sc, ps, f, l
				FROM unnest($1::date[], $2::int[], $3::int[], $4::int[], $5::int[], $6::int[], $7::int[],
				            $8::bigint[], $9::timestamptz[], $10::timestamptz[]) AS u(dy, a, p, d, c, s, sc, ps, f, l)
				ON CONFLICT (day, ad_id, publisher_id, device_id) DO UPDATE SET
					sightings = r.sightings + EXCLUDED.sightings,
					scrapes = r.scrapes + EXCLUDED.scrapes,
					feed_position_sum = r.feed_position_sum + EXCLUDED.feed_position_sum,
					first_seen_at = LEAST(r.first_seen_at, EXCLUDED.first_seen_at),
					last_seen_at = GREATEST(r.last_seen_at, EXCLUDED.last_seen_at)`,
				days, ads, pubs, devs, creatives, sightings, scrapesN, sums, first, last); err != nil {
				return fmt.Errorf("ad_daily: %w", err)
			}
		}
	}

	// placement_daily
	if len(placement) > 0 {
		keys := make([]placementKey, 0, len(placement))
		for k := range placement {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.day != b.day {
				return a.day < b.day
			}
			if a.ad != b.ad {
				return a.ad < b.ad
			}
			if a.publisher != b.publisher {
				return a.publisher < b.publisher
			}
			return a.placement < b.placement
		})
		var days []string
		var ads, pubs, places, sightings []int32
		for _, k := range keys {
			days = append(days, k.day)
			ads = append(ads, k.ad)
			pubs = append(pubs, k.publisher)
			places = append(places, k.placement)
			sightings = append(sightings, int32(placement[k].sightings))
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO spy.placement_daily AS r (day, ad_id, publisher_id, placement_id, sightings)
			SELECT * FROM unnest($1::date[], $2::int[], $3::int[], $4::int[], $5::int[])
			ON CONFLICT (day, ad_id, publisher_id, placement_id) DO UPDATE SET
				sightings = r.sightings + EXCLUDED.sightings`,
			days, ads, pubs, places, sightings); err != nil {
			return fmt.Errorf("placement_daily: %w", err)
		}
	}

	// campaign_daily
	if len(campaign) > 0 {
		keys := make([]campaignKey, 0, len(campaign))
		for k := range campaign {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.day != b.day {
				return a.day < b.day
			}
			if a.campaign != b.campaign {
				return a.campaign < b.campaign
			}
			return a.publisher < b.publisher
		})
		var days []string
		var camps, pubs, sightings, scrapesN []int32
		var first, last []time.Time
		for _, k := range keys {
			c := campaign[k]
			days = append(days, k.day)
			camps = append(camps, k.campaign)
			pubs = append(pubs, k.publisher)
			sightings = append(sightings, int32(c.sightings))
			scrapesN = append(scrapesN, int32(len(c.scrapes)))
			first = append(first, c.first)
			last = append(last, c.last)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO spy.campaign_daily AS r (day, campaign_id, publisher_id, sightings, scrapes, first_seen_at, last_seen_at)
			SELECT * FROM unnest($1::date[], $2::int[], $3::int[], $4::int[], $5::int[], $6::timestamptz[], $7::timestamptz[])
			ON CONFLICT (day, campaign_id, publisher_id) DO UPDATE SET
				sightings = r.sightings + EXCLUDED.sightings,
				scrapes = r.scrapes + EXCLUDED.scrapes,
				first_seen_at = LEAST(r.first_seen_at, EXCLUDED.first_seen_at),
				last_seen_at = GREATEST(r.last_seen_at, EXCLUDED.last_seen_at)`,
			days, camps, pubs, sightings, scrapesN, first, last); err != nil {
			return fmt.Errorf("campaign_daily: %w", err)
		}
	}

	// publisher_hourly
	if len(pubHourly) > 0 {
		keys := make([]hourKey, 0, len(pubHourly))
		for k := range pubHourly {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessHour(keys[i], keys[j]) })
		var hours []time.Time
		var pubs, devs, scrapesN, sightings []int32
		for _, k := range keys {
			c := pubHourly[k]
			hours = append(hours, k.hour)
			pubs = append(pubs, k.publisher)
			devs = append(devs, k.dev)
			scrapesN = append(scrapesN, int32(len(c.scrapes)))
			sightings = append(sightings, int32(c.sightings))
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO spy.publisher_hourly AS r (hour, publisher_id, device_id, scrapes, sightings)
			SELECT h, p, d::smallint, sc, s
			FROM unnest($1::timestamptz[], $2::int[], $3::int[], $4::int[], $5::int[]) AS u(h, p, d, sc, s)
			ON CONFLICT (hour, publisher_id, device_id) DO UPDATE SET
				scrapes = r.scrapes + EXCLUDED.scrapes,
				sightings = r.sightings + EXCLUDED.sightings`,
			hours, pubs, devs, scrapesN, sightings); err != nil {
			return fmt.Errorf("publisher_hourly: %w", err)
		}
	}

	// creative_link and creative_campaign (kept forever, see 018)
	for _, t := range []struct {
		table, col string
		m          map[[2]int32]*count
	}{
		{"creative_link", "link_id", creativeLink},
		{"creative_campaign", "campaign_id", creativeCampaign},
	} {
		if len(t.m) == 0 {
			continue
		}
		keys := make([][2]int32, 0, len(t.m))
		for k := range t.m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i][0] != keys[j][0] {
				return keys[i][0] < keys[j][0]
			}
			return keys[i][1] < keys[j][1]
		})
		var creatives, others []int32
		var sightings []int64
		var first, last []time.Time
		for _, k := range keys {
			c := t.m[k]
			creatives = append(creatives, k[0])
			others = append(others, k[1])
			sightings = append(sightings, c.sightings)
			first = append(first, c.first)
			last = append(last, c.last)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			INSERT INTO spy.%[1]s AS r (creative_id, %[2]s, sightings, first_seen_at, last_seen_at)
			SELECT * FROM unnest($1::int[], $2::int[], $3::bigint[], $4::timestamptz[], $5::timestamptz[])
			ON CONFLICT (creative_id, %[2]s) DO UPDATE SET
				sightings = r.sightings + EXCLUDED.sightings,
				first_seen_at = LEAST(r.first_seen_at, EXCLUDED.first_seen_at),
				last_seen_at = GREATEST(r.last_seen_at, EXCLUDED.last_seen_at)`, t.table, t.col),
			creatives, others, sightings, first, last); err != nil {
			return fmt.Errorf("%s: %w", t.table, err)
		}
	}
	return nil
}

func lessHour(a, b hourKey) bool {
	if !a.hour.Equal(b.hour) {
		return a.hour.Before(b.hour)
	}
	if a.ad != b.ad {
		return a.ad < b.ad
	}
	if a.publisher != b.publisher {
		return a.publisher < b.publisher
	}
	return a.dev < b.dev
}

// ------------------------------------------------------------------------------
// The walker's links
// ------------------------------------------------------------------------------

// fileWalks files the newest click link of each ad in the written sightings
// in spy.walk_queue (collector 042), where the collector's landing page walker
// reads them now that its sweeper no longer feeds it. The sweeper handed the
// walker every sighting; the walker's queue decided what was due. Filing an
// ad at most every walkEvery keeps the writes small and loses nothing: the
// queue re-walks an ad after hours, not minutes. Returns the ads filed.
func (w *SpyWriter) fileWalks(ctx context.Context, tx pgx.Tx, scrapes []Scrape, lines []line) (map[int32]time.Time, error) {
	type walk struct {
		ln  *line
		ref string
	}
	newest := map[int32]walk{}
	for i := range lines {
		ln := &lines[i]
		if ln.ad.ClickURL == "" || ln.creative == 0 || ln.adID == 0 {
			continue
		}
		if t, ok := w.walks[ln.adID]; ok && ln.seenAt.Sub(t) < walkEvery {
			continue
		}
		if cur, ok := newest[ln.adID]; ok && !ln.seenAt.After(cur.ln.seenAt) {
			continue
		}
		newest[ln.adID] = walk{ln: ln, ref: scrapes[ln.scrapeIdx].Referer}
	}
	if len(newest) == 0 {
		return nil, nil
	}
	var ads, creatives, accounts []int32
	var urls, refs []string
	filed := make(map[int32]time.Time, len(newest))
	for ad, k := range newest {
		ads = append(ads, ad)
		creatives = append(creatives, k.ln.creative)
		accounts = append(accounts, k.ln.account)
		urls = append(urls, k.ln.ad.ClickURL)
		refs = append(refs, k.ref)
		filed[ad] = k.ln.seenAt
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO spy.walk_queue AS q (ad_id, creative_id, account_id, click_url, referer, queued_at)
		SELECT a, c, NULLIF(acc, 0), u, r, NOW()
		FROM unnest($1::int[], $2::int[], $3::int[], $4::text[], $5::text[]) AS x(a, c, acc, u, r)
		ON CONFLICT (ad_id) DO UPDATE SET creative_id = EXCLUDED.creative_id, account_id = EXCLUDED.account_id,
			click_url = EXCLUDED.click_url, referer = EXCLUDED.referer, queued_at = EXCLUDED.queued_at`,
		ads, creatives, accounts, urls, refs); err != nil {
		return nil, fmt.Errorf("file walks: %w", err)
	}
	return filed, nil
}

// ------------------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------------------

func nullInt(v int32) any {
	if v == 0 {
		return nil
	}
	return v
}

var digits = regexp.MustCompile(`^\d{1,9}$`)

func siteID(s string) any {
	if !digits.MatchString(s) {
		return nil
	}
	var n int32
	fmt.Sscan(s, &n)
	return n
}

// optReal stores a float as REAL, or NULL when missing.
func optReal(v *float64) any {
	if v == nil {
		return nil
	}
	return float32(*v)
}

var (
	rxURLHost = regexp.MustCompile(`^[a-zA-Z]+://([^/?#]+)`)
	rxURLPath = regexp.MustCompile(`^[a-zA-Z]+://[^/?#]+([^?#]*)`)
)

// URLHost matches spy.url_host() (013).
func URLHost(u string) string {
	m := rxURLHost.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// URLPath matches spy.url_path() (013).
func URLPath(u string) string {
	m := rxURLPath.FindStringSubmatch(u)
	if m == nil || m[1] == "" {
		return "/"
	}
	return m[1]
}

// LinkKey matches spy.link_key() (013): host + path + campaign + item.
func LinkKey(clickURL, campaign, item string) uuid.UUID {
	return uuid.UUID(md5.Sum([]byte(URLHost(clickURL) + "|" + URLPath(clickURL) + "|" + campaign + "|" + item)))
}
