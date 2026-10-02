package walk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OldWalks says what ImportOld copied.
type OldWalks struct {
	Versions int // the collector's landing page versions read
	Pages    int // distinct page versions they became here
	Uses     int // the collector's rows of which creative (and ad) led to which landing page
	Walks    int // walks written
	Skipped  int // uses whose creative Tracks lacks, or that has no ad or link here
}

// ImportOld copies the collector's landing pages into walks, so the pages
// its funnel walker and its quick investigations read before the switch-over
// stay usable here (and in Spy, which reads walks). It reads the collector's
// database (old) and never writes it.
//
// The collector kept a landing page's versions (one per distinct content)
// and, apart, which creative and ad led to the page, with first and last
// seen; it did not keep single walks. So each use of a page becomes one walk
// per version seen while that use was, dated the last time both were seen
// (a use no version overlaps gets the page's last version). The walk's step
// 0 is the landing page as the version saw it; step 1 is the first page
// past it the collector recorded, without its content. A use without an ad
// gets its creative's most seen ad here, and every walk its creative's most
// clicked link. Versions are hashed as tracks-walker hashes its own, so a
// page Tracks walks again with the same content is the same row.
//
// Running it again is safe: the copied walks (record ids "old-…") are
// replaced, and a page version already here only widens its first and last
// seen.
func ImportOld(ctx context.Context, db, old *pgxpool.Pool) (OldWalks, error) {
	var res OldWalks
	tx, err := db.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE old_version (old_id INTEGER, landing_page_id INTEGER, host TEXT, final_url TEXT, hops INTEGER,
		    page_type TEXT, checkout_platform TEXT, seller_account TEXT, hash UUID, title TEXT, word_count INTEGER,
		    headings JSONB, meta JSONB, favicon_url TEXT, pixels JSONB, emails TEXT[], phones TEXT[], companies TEXT[],
		    disclaimers TEXT[], vsl JSONB, text TEXT, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ,
		    s1_url TEXT, s1_final_url TEXT, s1_host TEXT, s1_status INTEGER, s1_hops INTEGER, s1_page_type TEXT,
		    s1_checkout_platform TEXT, s1_seller_account TEXT) ON COMMIT DROP;
		CREATE TEMP TABLE old_use (id INTEGER, creative_key TEXT, headline TEXT, landing_page_id INTEGER,
		    first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ) ON COMMIT DROP`); err != nil {
		return res, err
	}

	n, err := copyOldVersions(ctx, old, tx)
	if err != nil {
		return res, fmt.Errorf("old landing page versions: %w", err)
	}
	res.Versions = int(n)
	rows, err := old.Query(ctx, `
		SELECT u.id, c.creative_key, a.headline, u.landing_page_id, u.first_seen_at, u.last_seen_at
		FROM spy.landing_page_use u
		JOIN spy.creative c ON c.id = u.creative_id
		LEFT JOIN spy.ad a ON a.id = u.ad_id`)
	if err != nil {
		return res, fmt.Errorf("old landing page uses: %w", err)
	}
	n, err = tx.CopyFrom(ctx, pgx.Identifier{"old_use"},
		[]string{"id", "creative_key", "headline", "landing_page_id", "first_seen_at", "last_seen_at"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if !rows.Next() {
				return nil, rows.Err()
			}
			return rows.Values()
		}))
	rows.Close()
	if err != nil {
		return res, fmt.Errorf("old landing page uses: %w", err)
	}
	res.Uses = int(n)

	steps := []string{
		`CREATE INDEX ON old_version (landing_page_id)`,
		`ANALYZE old_version`,
		`ANALYZE old_use`,
		`INSERT INTO tracks.page_version (hash, title, word_count, headings, meta, favicon_url, pixels, emails, phones,
		                                  companies, disclaimers, vsl, text, first_seen_at, last_seen_at)
		 SELECT DISTINCT ON (hash) hash, title, word_count, headings, meta, favicon_url, pixels, emails, phones,
		        companies, disclaimers, vsl, text, min(first_seen_at) OVER w, max(last_seen_at) OVER w
		 FROM old_version
		 WINDOW w AS (PARTITION BY hash)
		 ORDER BY hash
		 ON CONFLICT (hash) DO UPDATE SET
		     first_seen_at = LEAST(tracks.page_version.first_seen_at, EXCLUDED.first_seen_at),
		     last_seen_at = GREATEST(tracks.page_version.last_seen_at, EXCLUDED.last_seen_at)`,
		`CREATE TEMP TABLE m_creative ON COMMIT DROP AS
		 SELECT DISTINCT c.id, c.creative_key FROM old_use u JOIN tracks.creative c ON c.creative_key = u.creative_key`,
		// Each creative's most seen ad, else its first; its most clicked link.
		`CREATE TEMP TABLE m_lead_ad ON COMMIT DROP AS
		 SELECT DISTINCT ON (ad.creative_id) ad.creative_id, ad.id AS ad_id
		 FROM tracks.ad ad
		 LEFT JOIN (SELECT ad_id, sum(sightings) AS n FROM tracks.ad_daily
		            WHERE creative_id IN (SELECT id FROM m_creative) GROUP BY 1) s ON s.ad_id = ad.id
		 WHERE ad.creative_id IN (SELECT id FROM m_creative)
		 ORDER BY ad.creative_id, s.n DESC NULLS LAST, ad.id`,
		`CREATE TEMP TABLE m_link ON COMMIT DROP AS
		 SELECT DISTINCT ON (creative_id) creative_id, link_id
		 FROM (SELECT creative_id, link_id, sum(sightings) AS n FROM tracks.creative_link_daily
		       WHERE creative_id IN (SELECT id FROM m_creative) GROUP BY 1, 2) x
		 ORDER BY creative_id, n DESC, link_id`,
		`CREATE TEMP TABLE m_use ON COMMIT DROP AS
		 SELECT u.id, u.landing_page_id, u.first_seen_at, u.last_seen_at, c.id AS creative_id,
		        COALESCE(ad.id, l.ad_id) AS ad_id, k.link_id
		 FROM old_use u
		 JOIN m_creative c ON c.creative_key = u.creative_key
		 LEFT JOIN tracks.ad ad ON ad.creative_id = c.id AND ad.headline = u.headline
		 LEFT JOIN m_lead_ad l ON l.creative_id = c.id
		 LEFT JOIN m_link k ON k.creative_id = c.id
		 WHERE COALESCE(ad.id, l.ad_id) IS NOT NULL AND k.link_id IS NOT NULL`,
		`CREATE TEMP TABLE old_walk ON COMMIT DROP AS
		 SELECT 'old-' || u.id || '-' || v.old_id AS record_id, LEAST(u.last_seen_at, v.last_seen_at) AS at,
		        u.ad_id, u.creative_id, u.link_id, v.old_id AS version_id
		 FROM m_use u
		 JOIN old_version v ON v.landing_page_id = u.landing_page_id
		                   AND v.first_seen_at <= u.last_seen_at AND v.last_seen_at >= u.first_seen_at
		 UNION ALL
		 SELECT 'old-' || u.id || '-' || v.old_id, LEAST(u.last_seen_at, v.last_seen_at), u.ad_id, u.creative_id,
		        u.link_id, v.old_id
		 FROM m_use u
		 CROSS JOIN LATERAL (SELECT v.old_id, v.last_seen_at FROM old_version v WHERE v.landing_page_id = u.landing_page_id
		                     ORDER BY v.last_seen_at DESC, v.old_id DESC LIMIT 1) v
		 WHERE NOT EXISTS (SELECT 1 FROM old_version o WHERE o.landing_page_id = u.landing_page_id
		                   AND o.first_seen_at <= u.last_seen_at AND o.last_seen_at >= u.first_seen_at)`,
		`DELETE FROM tracks.walk WHERE record_id LIKE 'old-%'`,
		`INSERT INTO tracks.walk (record_id, at, ad_id, creative_id, account_id, link_id, outcome, ms)
		 SELECT o.record_id, o.at, o.ad_id, o.creative_id, ad.account_id, o.link_id, 'ok', 0
		 FROM old_walk o JOIN tracks.ad ad ON ad.id = o.ad_id`,
		// The collector stored a version only for a page that answered.
		`INSERT INTO tracks.walk_step (walk_id, step, url_id, final_url_id, host, status, hops, version_hash, page_type,
		                               checkout_platform, seller_account)
		 SELECT x.walk_id, x.step, tracks.url_id(x.url), tracks.url_id(x.final_url), x.host, x.status, x.hops, x.hash,
		        x.page_type, x.checkout_platform, x.seller_account
		 FROM (SELECT w.id AS walk_id, 0 AS step, v.final_url AS url, v.final_url, v.host, 200 AS status, v.hops, v.hash,
		              v.page_type, v.checkout_platform, v.seller_account
		       FROM tracks.walk w JOIN old_walk o ON o.record_id = w.record_id JOIN old_version v ON v.old_id = o.version_id
		       UNION ALL
		       SELECT w.id, 1, v.s1_url, v.s1_final_url, v.s1_host, v.s1_status, v.s1_hops, NULL, v.s1_page_type,
		              v.s1_checkout_platform, v.s1_seller_account
		       FROM tracks.walk w JOIN old_walk o ON o.record_id = w.record_id JOIN old_version v ON v.old_id = o.version_id
		       WHERE v.s1_url IS NOT NULL) x`,
	}
	for _, q := range steps {
		if _, err := tx.Exec(ctx, q); err != nil {
			return res, fmt.Errorf("import old walks: %w", err)
		}
	}
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(DISTINCT hash) FROM old_version)::int, (SELECT count(*) FROM old_walk)::int,
		       (SELECT count(*) FROM old_use u WHERE NOT EXISTS (SELECT 1 FROM m_use m WHERE m.id = u.id))::int`).
		Scan(&res.Pages, &res.Walks, &res.Skipped); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// oldStep is one page past the landing page, as the collector's funnel
// walker recorded it (landing_page_version.funnel_steps).
type oldStep struct {
	CTAURL           string            `json:"cta_url"`
	FinalURL         string            `json:"final_url"`
	Domain           string            `json:"domain"`
	PageType         string            `json:"page_type"`
	CheckoutPlatform string            `json:"checkout_platform"`
	MerchantID       string            `json:"merchant_id"`
	RedirectChain    []json.RawMessage `json:"redirect_chain"`
	Status           int               `json:"status"`
}

// copyOldVersions streams the collector's landing page versions into
// old_version, each turned into a Version as tracks-walker keeps one.
func copyOldVersions(ctx context.Context, old *pgxpool.Pool, tx pgx.Tx) (int64, error) {
	rows, err := old.Query(ctx, `
		SELECT v.id, v.landing_page_id, lp.host, v.sample_final_url,
		       CASE WHEN jsonb_typeof(v.sample_redirect_chain) = 'array' THEN jsonb_array_length(v.sample_redirect_chain) ELSE 0 END,
		       v.page_type, COALESCE(se.platform, v.checkout_platform), se.account,
		       v.title, COALESCE(v.word_count, 0), v.headings::text, v.meta_tags::text, v.favicon_url,
		       v.tracking_pixels::text, v.legal::text, v.disclaimers::text, v.vsl::text,
		       COALESCE(NULLIF(v.body_text, ''), v.body_excerpt, ''),
		       CASE WHEN jsonb_typeof(v.funnel_steps) = 'array' THEN (v.funnel_steps -> 0)::text END,
		       v.first_seen_at, v.last_seen_at
		FROM spy.landing_page_version v
		JOIN spy.landing_page lp ON lp.id = v.landing_page_id
		LEFT JOIN spy.seller se ON se.id = v.seller_id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols := []string{"old_id", "landing_page_id", "host", "final_url", "hops", "page_type", "checkout_platform",
		"seller_account", "hash", "title", "word_count", "headings", "meta", "favicon_url", "pixels", "emails", "phones",
		"companies", "disclaimers", "vsl", "text", "first_seen_at", "last_seen_at", "s1_url", "s1_final_url", "s1_host",
		"s1_status", "s1_hops", "s1_page_type", "s1_checkout_platform", "s1_seller_account"}
	return tx.CopyFrom(ctx, pgx.Identifier{"old_version"}, cols, pgx.CopyFromFunc(func() ([]any, error) {
		if !rows.Next() {
			return nil, rows.Err()
		}
		var (
			id, pageID, hops, words                             int32
			host, finalURL                                      string
			pageType, checkout, seller, title, favicon, step    *string
			headings, meta, pixelsJSON, legal, disclaimers, vsl *string
			text                                                string
			first, last                                         any
		)
		if err := rows.Scan(&id, &pageID, &host, &finalURL, &hops, &pageType, &checkout, &seller, &title, &words,
			&headings, &meta, &favicon, &pixelsJSON, &legal, &disclaimers, &vsl, &text, &step, &first, &last); err != nil {
			return nil, err
		}
		v := Version{Title: clean(deref(title)), WordCount: int(words), FaviconURL: deref(favicon), Text: clip(clean(text), maxText),
			Headings: map[string][]string{}, Meta: map[string]string{}, VSL: map[string]any{}, Disclaimers: []string{}}
		decode(headings, &v.Headings)
		decode(meta, &v.Meta)
		decode(vsl, &v.VSL)
		decode(disclaimers, &v.Disclaimers)
		var px map[string]any
		decode(pixelsJSON, &px)
		v.Pixels = pixels(px)
		var lg map[string]any
		decode(legal, &lg)
		v.Emails, v.Phones, v.Companies = strs(lg["emails"]), strs(lg["phones"]), strs(lg["entities"])
		if v.Headings == nil {
			v.Headings = map[string][]string{}
		}
		if v.Meta == nil {
			v.Meta = map[string]string{}
		}
		if v.VSL == nil {
			v.VSL = map[string]any{}
		}
		if v.Disclaimers == nil {
			v.Disclaimers = []string{}
		}
		v.Hash = v.hash()
		j := func(x any) []byte { b, _ := json.Marshal(x); return b }

		var s1 oldStep
		decode(step, &s1)
		s1URL := s1.CTAURL
		if s1URL == "" {
			s1URL = s1.FinalURL
		}
		return []any{id, pageID, oldHost(host), finalURL, max(0, int(hops)-1), pageType, checkout, seller, v.Hash,
			nonEmpty(v.Title), v.WordCount, j(v.Headings), j(v.Meta), nonEmpty(v.FaviconURL), j(v.Pixels), v.Emails,
			v.Phones, v.Companies, v.Disclaimers, j(v.VSL), v.Text, first, last,
			nonEmpty(s1URL), nonEmpty(s1.FinalURL), nonEmpty(oldHost(s1.Domain)), nonZero(s1.Status),
			max(0, len(s1.RedirectChain)-1), nonEmpty(s1.PageType), nonEmpty(s1.CheckoutPlatform), nonEmpty(s1.MerchantID)}, nil
	}))
}

// decode reads a JSON column; one the collector wrote badly stays empty.
func decode(s *string, into any) {
	if s != nil && *s != "" {
		_ = json.Unmarshal([]byte(*s), into)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// oldHost is a host as tracks-walker keeps one: lowercase, without www.
func oldHost(h string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "www.")
}

// clean drops what Postgres text refuses (NUL, broken UTF-8).
func clean(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", ""))
}
