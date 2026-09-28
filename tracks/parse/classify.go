package parse

// Which cards are competitors' direct-response offers, and the text and
// link helpers. Ported from adhunters-collector e20148c,
// internal/sweeper/filter.go.

import (
	"crypto/md5"
	"encoding/hex"
	"html"
	"net/url"
	"regexp"
	"strings"
)

const (
	CategoryCompetitorOffer   = "COMPETITOR_OFFER"
	CategorySystemFallback    = "SYSTEM_FALLBACK"
	CategoryNewsRecirculation = "NEWS_RECIRCULATION"
	CategorySearchArbitrage   = "SEARCH_ARBITRAGE"
	CategoryTravelHospitality = "TRAVEL_HOSPITALITY"
	CategoryRealEstatePortal  = "REAL_ESTATE_PORTAL"
	CategoryBigBrand          = "BIG_BRAND"
)

var (
	spaceRegex        = regexp.MustCompile(`\s+`)
	imageExtRegex     = regexp.MustCompile(`\.(png|jpe?g|gif|webp|svg|ico|bmp)($|\?|#)`)
	rawImgPathRegex   = regexp.MustCompile(`\.(png|jpe?g|gif|webp|svg|ico|bmp)$`)
	placeholderRegex  = regexp.MustCompile(`^(title_[-0-9]+|headline_[-0-9]+|\[placeholder\])`)
	realEstateRx      = regexp.MustCompile(`(?i)\b(view homes for sale|home listings? in|priciest home listing|house asks \$[0-9.]+|property in .* lists for \$[0-9.]+)\b`)
	travelTitleRx     = regexp.MustCompile(`(?i)\b(cruise packages?|norwegian fjords cruise|resort & spa|punta cana resort)\b`)
	travelBrandRx     = regexp.MustCompile(`(?i)\b(hotel group|signature resorts)\b`)
	geBrandRx         = regexp.MustCompile(`(?i)\b(water heater|hotpoint|appliances|ptac)\b`)
	arbitrageNetRx    = regexp.MustCompile(`(?i)\b(search|searches)\.net\b`)
	arbitragePipesRx  = regexp.MustCompile(`(?i)\|\s*(suvs?|properties|auctions?|pickups?|cruise|deals|jumbo cds|retirement rollover|foreclosure|distressed)\b`)
	arbitragePrefixRx = regexp.MustCompile(`(?i)^(searchlogik|smartsearches|searcharena|freshsearches|elitesearches|advisorhq)\b`)
)

func CleanText(text string) string {
	if text == "" {
		return ""
	}
	s := html.UnescapeString(text)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = spaceRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func ExtractHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "secure.")
	return host
}

func ExtractImageAssetKey(thumbnailURL string) string {
	if thumbnailURL == "" {
		return "no_image"
	}

	target := thumbnailURL
	if strings.HasPrefix(target, "//") {
		target = "https:" + target
	}

	parsed, err := url.Parse(target)
	if err == nil {
		pathname := parsed.Path
		parts := strings.Split(strings.Trim(pathname, "/"), "/")
		if len(parts) > 0 {
			filename := parts[len(parts)-1]
			if idx := strings.Index(filename, "?"); idx != -1 {
				filename = filename[:idx]
			}
			if len(filename) >= 3 && len(filename) <= 128 {
				return filename
			}
			if len(filename) > 128 {
				hasher := md5.New()
				hasher.Write([]byte(filename))
				return hex.EncodeToString(hasher.Sum(nil)) + ".jpg"
			}
		}
	}

	hasher := md5.New()
	hasher.Write([]byte(thumbnailURL))
	return hex.EncodeToString(hasher.Sum(nil)) + ".jpg"
}

func ClassifyAd(ad candidate) (string, string) {
	branding := CleanText(ad.Branding)
	title := CleanText(ad.Title)
	clickURL := strings.TrimSpace(ad.ClickURL)
	thumbURL := strings.TrimSpace(ad.ThumbnailURL)
	clickHost := ExtractHost(clickURL)
	brandLower := strings.ToLower(branding)
	normTitle := strings.ToLower(spaceRegex.ReplaceAllString(title, " "))
	strippedTitle := strings.Trim(normTitle, ":!?,. ")

	// 1. System Fallback & Synthetic Placeholders
	if clickURL == "" || clickURL == "#" || strings.HasPrefix(strings.ToLower(clickURL), "javascript:") {
		return CategorySystemFallback, "Missing or invalid click URL"
	}

	if rawImgPathRegex.MatchString(strings.ToLower(clickURL)) || imageExtRegex.MatchString(strings.ToLower(clickURL)) {
		return CategorySystemFallback, "Click URL targets raw image asset"
	}

	lowerThumb := strings.ToLower(thumbURL)
	lowerClick := strings.ToLower(clickURL)
	if strings.Contains(lowerThumb, "fallback") || strings.Contains(lowerClick, "fallback") ||
		strings.Contains(lowerThumb, "/banner/banner-to-native") || strings.Contains(lowerClick, "/banner/banner-to-native") {
		return CategorySystemFallback, "System fallback asset pattern"
	}

	if clickHost != "" {
		if _, ok := InternalAssetDomains[clickHost]; ok {
			return CategorySystemFallback, "Click host is ad network asset delivery domain: " + clickHost
		}
	}

	if placeholderRegex.MatchString(normTitle) ||
		strings.HasPrefix(normTitle, "default banner") ||
		strings.HasPrefix(normTitle, "default title") {
		return CategorySystemFallback, "Synthetic widget placeholder headline: " + title
	}

	if _, ok := PlaceholderHeadlines[normTitle]; ok {
		return CategorySystemFallback, "Synthetic placeholder headline: " + title
	}
	if _, ok := PlaceholderHeadlines[strippedTitle]; ok {
		return CategorySystemFallback, "Synthetic placeholder headline: " + title
	}

	if branding == "-" || branding == "--" || branding == "" {
		return CategorySystemFallback, "Missing or placeholder branding: " + branding
	}

	// 2. Travel, Hospitality, Resorts, Cruises, Airlines
	if clickHost != "" {
		for td := range TravelDomains {
			if clickHost == td || strings.HasSuffix(clickHost, "."+td) {
				return CategoryTravelHospitality, "Travel destination host: " + clickHost
			}
		}
	}
	for _, pattern := range TravelBrandPatterns {
		if strings.Contains(brandLower, pattern) {
			return CategoryTravelHospitality, "Travel brand pattern: " + branding
		}
	}
	if travelTitleRx.MatchString(normTitle) || travelBrandRx.MatchString(brandLower) {
		return CategoryTravelHospitality, "Travel title pattern: " + title
	}

	// 3. Real Estate Portals & Listing Marketplaces
	if clickHost != "" {
		for rd := range RealEstateDomains {
			if clickHost == rd || strings.HasSuffix(clickHost, "."+rd) {
				return CategoryRealEstatePortal, "Real estate portal host: " + clickHost
			}
		}
	}
	for _, pattern := range RealEstateBrandPatterns {
		if brandLower == pattern || strings.HasPrefix(brandLower, pattern) || strings.Contains(brandLower, pattern) {
			return CategoryRealEstatePortal, "Real estate brand pattern: " + branding
		}
	}
	if realEstateRx.MatchString(normTitle) {
		return CategoryRealEstatePortal, "Real estate headline pattern: " + title
	}

	// 4. News Recirculation & Editorial Magazines
	if clickHost != "" {
		for nd := range NewsDomains {
			if clickHost == nd || strings.HasSuffix(clickHost, "."+nd) {
				return CategoryNewsRecirculation, "News publisher domain: " + clickHost
			}
		}
	}
	// Publisher domains come without a scheme ("newsbreak.com"), which
	// ExtractHost cannot read.
	pubHost := ExtractHost(ad.PublisherDomain)
	if pubHost == "" && ad.PublisherDomain != "" {
		pubHost = ExtractHost("https://" + ad.PublisherDomain)
	}
	if pubHost != "" && clickHost != "" && (pubHost == clickHost || strings.HasSuffix(clickHost, "."+pubHost)) {
		return CategoryNewsRecirculation, "Internal publisher recirculation: " + clickHost
	}
	for _, nb := range NewsBrandKeywords {
		if brandLower == nb || strings.Contains(brandLower, nb) {
			return CategoryNewsRecirculation, "Publisher branding: " + branding
		}
	}

	// 5. Search / MFA Arbitrage
	if clickHost != "" {
		for adHost := range ArbitrageDomains {
			if clickHost == adHost || strings.HasSuffix(clickHost, "."+adHost) {
				return CategorySearchArbitrage, "Arbitrage host: " + clickHost
			}
		}
		if strings.HasPrefix(clickHost, "lookfor.") || strings.HasPrefix(clickHost, "related.") || strings.HasPrefix(clickHost, "search.") {
			return CategorySearchArbitrage, "Arbitrage subdomain: " + clickHost
		}
	}
	for _, k := range ArbitrageBrandKeywords {
		if strings.Contains(brandLower, k) {
			return CategorySearchArbitrage, "Arbitrage branding: " + branding
		}
	}
	if arbitrageNetRx.MatchString(brandLower) || arbitragePipesRx.MatchString(branding) || arbitragePrefixRx.MatchString(brandLower) {
		return CategorySearchArbitrage, "Arbitrage branding: " + branding
	}
	if strings.HasPrefix(normTitle, "search for ") || strings.Contains(normTitle, " - search now") {
		return CategorySearchArbitrage, "Search query trigger headline"
	}

	// 6. Big Brands / Corporate National Brand Advertisers
	if clickHost != "" {
		for bb := range BigBrands {
			if clickHost == bb || strings.HasSuffix(clickHost, "."+bb) {
				return CategoryBigBrand, "Marketplace/corporate brand domain: " + bb
			}
		}
	}
	for _, bbn := range BigBrandNames {
		if brandLower == bbn || strings.Contains(brandLower, bbn) {
			return CategoryBigBrand, "Corporate big brand: " + branding
		}
	}
	if branding == "GE" && geBrandRx.MatchString(normTitle) {
		return CategoryBigBrand, "GE brand: " + title
	}

	// 7. Genuine Direct-Response Competitor Offer
	return CategoryCompetitorOffer, "Direct-Response Competitor Funnel"
}
