package load

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/Raposa-Industries/adhunters/tracks/parse"
)

// line is one sighting with every id resolved.
type line struct {
	scrape    int // index into the file's scrapes
	ad        *parse.Ad
	seenAt    time.Time
	publisher int32
	device    int32
	creative  int32
	adID      int32
	placement int32
	campaign  int32
	link      int32
	account   int32
	brand     int32
}

// written is what one file's load stored.
type written struct {
	scrapes   int
	sightings int
	auctions  int
	liveLinks int
	hours     []time.Time
}

func networkCode(s *parse.Scrape) string {
	if s.Network == "" {
		return parse.Taboola
	}
	return s.Network
}

// write stores one raw file's scrapes in tx: it first removes whatever an
// earlier load of the same file stored, so loading a file again (a replay)
// replaces its facts instead of adding to them. Lookups only ever widen.
func (l *Loader) write(ctx context.Context, tx pgx.Tx, fileID int64, scrapes []parse.Scrape, live, fileLinks bool, p *caches) (written, error) {
	var w written
	c := l.caches
	hours := map[time.Time]bool{}

	// 0. What an earlier load of this file stored goes.
	var oldIDs []int64
	var oldFrom, oldTo *time.Time
	if err := tx.QueryRow(ctx, `
		WITH old AS (DELETE FROM tracks.scrape WHERE raw_file_id = $1 RETURNING id, at)
		SELECT COALESCE(array_agg(id), '{}'), min(at), max(at) FROM old`, fileID).Scan(&oldIDs, &oldFrom, &oldTo); err != nil {
		return w, fmt.Errorf("remove earlier load: %w", err)
	}
	if len(oldIDs) > 0 {
		for _, t := range []string{"sighting", "auction"} {
			if _, err := tx.Exec(ctx, `DELETE FROM tracks.`+t+` WHERE scrape_id = ANY($1) AND seen_at BETWEEN $2 AND $3`,
				oldIDs, *oldFrom, *oldTo); err != nil {
				return w, fmt.Errorf("remove earlier %ss: %w", t, err)
			}
		}
		for h := oldFrom.UTC().Truncate(time.Hour); !h.After(*oldTo); h = h.Add(time.Hour) {
			hours[h.UTC()] = true
		}
	}

	// 1. Lookups by name.
	var nets, devs, places, proxies, brands []string
	for i := range scrapes {
		s := &scrapes[i]
		nets = append(nets, networkCode(s))
		devs = append(devs, s.Device)
		if s.Line != "" {
			proxies = append(proxies, s.Line)
		}
		for j := range s.Ads {
			a := &s.Ads[j]
			if a.Placement != "" {
				places = append(places, a.Placement)
			}
			if b := strings.TrimSpace(a.Brand); b != "" {
				brands = append(brands, b)
			}
		}
	}
	if err := resolveNetworks(ctx, tx, nets, c.networks, p.networks); err != nil {
		return w, err
	}
	for _, r := range []struct {
		table, col string
		keys       []string
		cache, out map[string]int32
	}{
		{"device", "code", devs, c.devices, p.devices},
		{"placement", "name", places, c.placements, p.placements},
		{"proxy_line", "code", proxies, c.proxyLines, p.proxyLines},
		{"brand", "name", brands, c.brands, p.brands},
	} {
		if err := resolveNames(ctx, tx, r.table, r.col, r.keys, r.cache, r.out); err != nil {
			return w, err
		}
	}
	network := func(s *parse.Scrape) int32 { return id32(p.networks, c.networks, networkCode(s)) }

	// 2. Publishers, accounts, campaigns, creatives.
	pubs := map[string]*publisherRow{}
	accounts := map[string]*seenRange{}
	orgs := map[string]string{}
	campaigns := map[string]*campaignRow{}
	creatives := map[string]*creativeRow{}
	for i := range scrapes {
		s := &scrapes[i]
		t := s.At
		net := network(s)
		if r := pubs[s.Publisher]; r == nil {
			pubs[s.Publisher] = &publisherRow{network: net, domain: s.Domain, first: t, last: t}
		} else {
			widenPair(&r.first, &r.last, t)
		}
		for j := range s.Ads {
			a := &s.Ads[j]
			if a.Account != "" {
				k := netKey(net, a.Account)
				if r := accounts[k]; r == nil {
					accounts[k] = &seenRange{t, t}
				} else {
					r.widen(t)
				}
				if a.OrgID != "" {
					orgs[k] = a.OrgID
				}
			}
			if a.CampaignID != "" {
				k := netKey(net, a.CampaignID)
				r := campaigns[k]
				if r == nil {
					r = &campaignRow{first: t, last: t}
					campaigns[k] = r
				}
				widenPair(&r.first, &r.last, t)
				if a.CampaignName != "" {
					r.name = a.CampaignName
				}
				if a.ParentCampaignID != "" {
					r.parent = a.ParentCampaignID
				}
				if a.ParentCampaign != "" {
					r.parentName = a.ParentCampaign
				}
				if a.Objective != "" {
					r.objective = a.Objective
				}
				if a.Account != "" {
					r.account = netKey(net, a.Account)
				}
			}
			r := creatives[a.CreativeKey]
			if r == nil {
				r = &creativeRow{ad: a, first: t, last: t}
				creatives[a.CreativeKey] = r
			}
			widenPair(&r.first, &r.last, t)
			if a.FormatType == "video" {
				r.video = true
			}
		}
	}
	if err := upsertPublishers(ctx, tx, live, pubs, c, p); err != nil {
		return w, err
	}
	if err := upsertAccounts(ctx, tx, live, accounts, orgs, c, p); err != nil {
		return w, err
	}
	accountID := func(k string) int32 { return idCached(p.accounts, c.accounts, k) }
	if err := upsertCampaigns(ctx, tx, live, campaigns, accountID, c, p); err != nil {
		return w, err
	}
	if err := upsertCreatives(ctx, tx, live, creatives, c, p); err != nil {
		return w, err
	}

	// 3. Ads and links.
	ads := map[adKey]*adRow{}
	links := map[uuid.UUID]*linkRow{}
	var lines []line
	for i := range scrapes {
		s := &scrapes[i]
		t := s.At
		net := network(s)
		for j := range s.Ads {
			a := &s.Ads[j]
			ln := line{
				scrape: i, ad: a, seenAt: t,
				publisher: idCached(p.publishers, c.publishers, s.Publisher),
				device:    id32(p.devices, c.devices, s.Device),
				creative:  idCached(p.creatives, c.creatives, a.CreativeKey),
				placement: id32(p.placements, c.placements, a.Placement),
				campaign:  idCached(p.campaigns, c.campaigns, optNetKey(net, a.CampaignID)),
			}
			if b := strings.TrimSpace(a.Brand); b != "" {
				ln.brand = id32(p.brands, c.brands, b)
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
			widenPair(&r.first, &r.last, t)
			// Latest non-empty values win.
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
				lr := links[lk]
				if lr == nil {
					lr = &linkRow{ad: a, first: t, last: t}
					links[lk] = lr
				}
				widenPair(&lr.first, &lr.last, t)
				if !t.Before(lr.last) {
					lr.ad = a
				}
			}
			lines = append(lines, ln)
		}
	}
	if err := upsertAds(ctx, tx, live, ads, c, p); err != nil {
		return w, err
	}
	if err := upsertLinks(ctx, tx, live, links, c, p); err != nil {
		return w, err
	}
	nads := map[string]*networkAdRow{}
	for i := range lines {
		ln := &lines[i]
		k := adKey{ln.creative, ln.ad.Headline}
		if v, ok := p.ads[k]; ok {
			ln.adID = v.id
		} else if v, ok := c.ads[k]; ok {
			ln.adID = v.id
		}
		if ln.ad.ClickURL != "" {
			lk := LinkKey(ln.ad.ClickURL, ln.ad.CampaignID, ln.ad.ItemID)
			if v, ok := p.links[lk]; ok {
				ln.link = v.id
			} else if v, ok := c.links[lk]; ok {
				ln.link = v.id
			}
		}
		if ln.adID == 0 || ln.creative == 0 || ln.publisher == 0 || ln.device == 0 {
			return w, fmt.Errorf("unresolved id in scrape %s", scrapes[ln.scrape].CaptureID)
		}
		if a := ln.ad; a.NetworkAd != nil && a.ItemID != "" {
			k := netKey(network(&scrapes[ln.scrape]), a.ItemID)
			r := nads[k]
			if r == nil {
				r = &networkAdRow{d: a.NetworkAd, first: ln.seenAt, last: ln.seenAt}
				nads[k] = r
			}
			r.ad, r.campaign = ln.adID, ln.campaign
			widenPair(&r.first, &r.last, ln.seenAt)
		}
	}
	if err := upsertNetworkAds(ctx, tx, live, nads, c, p); err != nil {
		return w, err
	}

	// 4. Scrapes, with ids taken up front so sightings can point to them.
	ids := make([]int64, 0, len(scrapes))
	if len(scrapes) > 0 {
		rows, err := tx.Query(ctx, `SELECT nextval('tracks.scrape_id_seq') FROM generate_series(1, $1)`, len(scrapes))
		if err != nil {
			return w, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return w, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return w, err
		}
	}
	scrapeRows := make([][]any, len(scrapes))
	for i := range scrapes {
		s := &scrapes[i]
		var proxy any
		if s.Line != "" {
			proxy = int16(id32(p.proxyLines, c.proxyLines, s.Line))
		}
		scrapeRows[i] = []any{
			ids[i], captureUUID(s.CaptureID), s.At, fileID, idCached(p.publishers, c.publishers, s.Publisher),
			int16(id32(p.devices, c.devices, s.Device)), proxy, s.Instance, nullText(s.Version), s.Outcome,
			int16(s.Status), int32(s.LatencyMS), int16(min(len(s.Ads), 32767)), nullText(s.GeoCountry),
			nullText(s.TrcRoute), nullText(s.BodySHA256), nullText(clip(s.Error, 1000)),
		}
		hours[s.At.UTC().Truncate(time.Hour)] = true
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"tracks", "scrape"},
		[]string{"id", "capture_id", "at", "raw_file_id", "publisher_id", "device_id", "proxy_line_id", "instance",
			"version", "outcome", "status", "latency_ms", "ad_count", "geo_country", "trc_route", "body_sha256", "error"},
		pgx.CopyFromRows(scrapeRows)); err != nil {
		return w, fmt.Errorf("copy scrapes: %w", err)
	}
	w.scrapes = len(scrapes)

	// 5. Sightings and auctions.
	sightings := make([][]any, 0, len(lines))
	var auctions [][]any
	for _, ln := range lines {
		a := ln.ad
		sid := ids[ln.scrape]
		sightings = append(sightings, []any{
			ln.seenAt, sid, ln.adID, ln.creative, ln.publisher, int16(ln.device), nullInt(ln.placement),
			nullInt(ln.campaign), nullInt(ln.link), nullInt(ln.account), nullInt(ln.brand), siteID(a.SiteID),
			optReal(a.EcpaPercentile), int16(a.FeedPosition), int16(a.BlockPosition), optReal(a.BidPrice),
			optReal(a.SecondPrice),
		})
		if au := a.Auction; au != nil {
			auctions = append(auctions, []any{
				ln.seenAt, sid, ln.adID, ln.publisher, int16(ln.device), au.AuctionID, nullText(au.Placement),
				optReal(au.ClearingPrice), optReal(au.BidValue), optReal(au.CapAuctionPrice), nullText(au.Currency),
				nullText(au.WinningSeat), au.IsRtb,
			})
		}
	}
	if len(sightings) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"tracks", "sighting"},
			[]string{"seen_at", "scrape_id", "ad_id", "creative_id", "publisher_id", "device_id", "placement_id",
				"campaign_id", "link_id", "account_id", "brand_id", "site_id", "ecpa_percentile", "feed_position",
				"block_position", "bid_price", "second_price"},
			pgx.CopyFromRows(sightings)); err != nil {
			return w, fmt.Errorf("copy sightings: %w", err)
		}
	}
	if len(auctions) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"tracks", "auction"},
			[]string{"seen_at", "scrape_id", "ad_id", "publisher_id", "device_id", "auction_id", "placement",
				"clearing_price", "bid_value", "cap_auction_price", "currency", "winning_seat", "is_rtb"},
			pgx.CopyFromRows(auctions)); err != nil {
			return w, fmt.Errorf("copy auctions: %w", err)
		}
	}
	w.sightings, w.auctions = len(sightings), len(auctions)

	// 6. Live links for Raposa, from files fresh enough to still be clickable.
	if fileLinks {
		var rows [][]any
		for _, ln := range lines {
			s := &scrapes[ln.scrape]
			host := BareHost(ln.ad.LiveURL)
			if host == "" {
				continue
			}
			dev := s.Device
			if dev != "desktop" {
				dev = "phone"
			}
			page := s.PageURL
			if page == "" && s.Domain != "" {
				page = "https://" + s.Domain + "/"
			}
			rows = append(rows, []any{host, ln.ad.LiveURL, networkCode(s), nullText(ln.ad.CampaignID), dev,
				s.Publisher, nullText(page), ln.adID, ln.creative, ln.seenAt})
		}
		if len(rows) > 0 {
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"tracks", "live_link"},
				[]string{"host", "url", "network", "campaign_external_id", "device", "publisher", "page_url", "ad_id",
					"creative_id", "seen_at"}, pgx.CopyFromRows(rows)); err != nil {
				return w, fmt.Errorf("copy live links: %w", err)
			}
		}
		w.liveLinks = len(rows)
	}

	// 7. The hours this file touched need closing (again).
	for h := range hours {
		w.hours = append(w.hours, h)
	}
	if len(w.hours) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tracks.hour_state (hour) SELECT unnest($1::timestamptz[])
			ON CONFLICT (hour) DO UPDATE SET dirty = TRUE`, w.hours); err != nil {
			return w, fmt.Errorf("mark hours: %w", err)
		}
	}
	return w, nil
}

// BareHost is a link's host without the prefixes that do not tell two hosts
// apart. Ported from the collector's LinkBook.
func BareHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	host = strings.TrimPrefix(host, "www.")
	return strings.TrimPrefix(host, "secure.")
}

// captureUUID stores a record's ULID as the UUID with the same 16 bytes. An
// id that is not a ULID (older test data) gets a stable UUID from its MD5.
func captureUUID(id string) uuid.UUID {
	if u, err := ulid.Parse(id); err == nil {
		return uuid.UUID(u)
	}
	return uuid.UUID(md5.Sum([]byte(id)))
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
