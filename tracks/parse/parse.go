// Package parse turns one raw record (one scrape, as capture received it)
// into the scrape and the ads the loader stores.
//
// The parsers are the collector's, moved as they are (adhunters-collector
// e20148c: internal/sweeper client.go, newsbreak.go, decoder.go, filter.go,
// lists.go and internal/openrtb). What changed is only where their inputs
// come from: time, proxy line and publisher are read from the record, and
// nothing here is random, so parsing the same record twice gives the same
// result. That is what makes a replay safe.
package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
)

// Networks the loader knows how to parse.
const (
	Taboola   = "taboola"
	NewsBreak = "newsbreak"
)

// Outcomes of a scrape. Every scrape is stored with one, because errors count
// as scrapes.
const (
	OutcomeOK       = "ok"       // parsed; it may still hold no ads
	OutcomeEmpty    = "empty"    // a 2xx answer with no body
	OutcomeError    = "error"    // no answer: timeout, refused, no proxy line
	OutcomeHTTP     = "http"     // an answer outside 2xx; Status says which
	OutcomeUnparsed = "unparsed" // a 2xx answer the parser could not read (format drift)
)

// Scrape is one fetch of one publisher page, on one device, through one proxy
// line, with the ads it showed.
type Scrape struct {
	CaptureID  string // the record's ULID
	At         time.Time
	Network    string
	Publisher  string
	Domain     string
	PageURL    string
	Device     string
	Line       string
	Instance   string
	Version    string
	Status     int
	LatencyMS  int
	Outcome    string
	Error      string
	GeoCountry string
	TrcRoute   string
	BodySHA256 string
	Ads        []Ad
}

// Ad is one sighting: one ad seen once in the scrape. Ported from the
// collector's db.ScrapeAd.
type Ad struct {
	CreativeKey      string
	ImageURL         string
	FormatType       string
	VideoDuration    int
	ThumbDimensions  string
	Language         string
	Headline         string
	Description      string
	Cta              string
	Brand            string
	Account          string // account id on the scrape's ad network
	Placement        string
	CampaignID       string // campaign id on the scrape's ad network (NewsBreak: ad set id)
	CampaignName     string
	ItemID           string
	ClickURL         string // click link without one-time values
	LiveURL          string // click link as the feed served it, for Raposa
	Tracker          string
	AffiliateNetwork string
	Params           []byte // decoded tracking data (JSON), one-time keys removed
	SiteID           string // Taboola sub-site id
	EcpaPercentile   *float64
	FeedPosition     int
	BlockPosition    int

	// NewsBreak Ads only.
	OrgID            string // org that owns the account
	ParentCampaignID string // NewsBreak campaign above the ad set (CampaignID)
	ParentCampaign   string
	Objective        string
	BidPrice         *float64
	SecondPrice      *float64
	NetworkAd        *NetworkAd // ItemID is its external id

	// Taboola auction telemetry, when the card carried any.
	Auction *Auction
}

// Auction is what a Taboola card tells about the auction it won.
type Auction struct {
	AuctionID       string
	Placement       string
	ClearingPrice   *float64
	BidValue        *float64
	CapAuctionPrice *float64
	Currency        string
	WinningSeat     string
	IsRtb           bool
}

// Parse reads one record. It fails only when the record itself is unusable
// (no id, no time); an answer the parser cannot read becomes a scrape with
// OutcomeUnparsed, so one odd answer never stops a file from loading.
func Parse(rec *spool.Record) (Scrape, error) {
	if rec.ID == "" || rec.At.IsZero() {
		return Scrape{}, fmt.Errorf("record without id or time")
	}
	pub := publisherOf(rec)
	s := Scrape{
		CaptureID: rec.ID,
		At:        rec.At.UTC(),
		Network:   rec.Network,
		Publisher: rec.Publisher,
		Domain:    pub.Domain,
		PageURL:   pub.URL,
		Device:    rec.Device,
		Line:      rec.Line,
		Instance:  rec.Instance,
		Version:   rec.Version,
		Status:    rec.Status,
		LatencyMS: int(rec.LatencyMS),
		Error:     rec.Error,
	}
	body, err := rec.BodyBytes()
	if err != nil {
		s.Outcome, s.Error = OutcomeUnparsed, "body: "+err.Error()
		return s, nil
	}
	if len(body) > 0 {
		h := sha256.Sum256(body)
		s.BodySHA256 = hex.EncodeToString(h[:])
	}
	switch {
	case rec.Status == 0:
		s.Outcome = OutcomeError
		return s, nil
	case rec.Status < 200 || rec.Status > 299:
		s.Outcome = OutcomeHTTP
		return s, nil
	case rec.Error != "":
		// An answer that broke off while being read.
		s.Outcome = OutcomeError
		return s, nil
	case len(body) == 0:
		s.Outcome = OutcomeEmpty
		return s, nil
	}

	var cands []candidate
	switch rec.Network {
	case Taboola:
		var meta trcSessionMeta
		cands, meta, err = parseTaboolaResponse(body, pub, rec.Device)
		s.GeoCountry, s.TrcRoute = meta.GeoCountry, meta.Route
	case NewsBreak:
		cands, err = parseNewsBreakResponse(body, pub, rec.Device)
	default:
		err = fmt.Errorf("unknown network %q", rec.Network)
	}
	if err != nil {
		s.Outcome, s.Error = OutcomeUnparsed, err.Error()
		return s, nil
	}
	s.Outcome = OutcomeOK
	s.Ads = make([]Ad, 0, len(cands))
	for i, c := range cands {
		ad := buildAd(c)
		if t := c.AuctionTelemetry; t != nil {
			id := t.AuctionID
			if id == "" {
				// Same record, same position, same id: a replay adds nothing.
				id = fmt.Sprintf("inferred_%s_%d", rec.ID, i+1)
			}
			ad.Auction = &Auction{
				AuctionID: id, Placement: t.Placement, ClearingPrice: t.ClearingPrice, BidValue: t.BidValue,
				CapAuctionPrice: t.CapAuctionPrice, Currency: t.Currency, WinningSeat: t.WinningSeat, IsRtb: t.IsRtb,
			}
		}
		s.Ads = append(s.Ads, ad)
	}
	return s, nil
}

// publisherOf reads the publisher page out of the request capture sent:
// Taboola's data parameter names it in "u", NewsBreak's auction body in
// site.page. The domain is that page's host without "www.", which is what
// the collector's publishers.yaml held for every target.
func publisherOf(rec *spool.Record) Publisher {
	p := Publisher{Name: rec.Publisher}
	switch rec.Network {
	case Taboola:
		if u, err := url.Parse(rec.URL); err == nil {
			var data struct {
				U string `json:"u"`
			}
			if json.Unmarshal([]byte(u.Query().Get("data")), &data) == nil {
				p.URL = data.U
			}
		}
	case NewsBreak:
		var body struct {
			Site struct {
				Page      string `json:"page"`
				Publisher struct {
					Domain string `json:"domain"`
				} `json:"publisher"`
			} `json:"site"`
		}
		if json.Unmarshal([]byte(rec.RequestBody), &body) == nil {
			p.URL = body.Site.Page
			p.Domain = body.Site.Publisher.Domain
		}
	}
	if p.Domain == "" && p.URL != "" {
		p.Domain = ExtractHost(p.URL)
	}
	p.Domain = strings.TrimPrefix(strings.ToLower(p.Domain), "www.")
	return p
}

// buildAd turns one card into one sighting. Ported from the collector's
// sweeper.buildAd (engine.go).
func buildAd(raw candidate) Ad {
	ad := Ad{
		CreativeKey:     ExtractImageAssetKey(raw.ThumbnailURL),
		ImageURL:        raw.ThumbnailURL,
		FormatType:      raw.FormatType,
		VideoDuration:   raw.VideoDuration,
		ThumbDimensions: raw.ThumbDimensions,
		Language:        raw.Language,
		Headline:        CleanText(raw.Title),
		Description:     raw.Description,
		Cta:             raw.CtaText,
		Brand:           raw.Branding,
		Account:         raw.AdvertiserAccount,
		Placement:       raw.Placement,
		ClickURL:        CleanCanonicalURL(raw.ClickURL),
		LiveURL:         raw.ClickURL,
		EcpaPercentile:  raw.EcpaPercentile,
		FeedPosition:    raw.FeedPosition,
		BlockPosition:   raw.BlockPosition,
	}
	if raw.Network == NewsBreak {
		// Ids come from the ad itself. The link only adds tracker data.
		ad.CreativeKey = raw.CreativeKey
		ad.CampaignID = raw.CampaignID
		ad.CampaignName = raw.CampaignName
		ad.ItemID = raw.NetworkAdID
		ad.OrgID = raw.OrgID
		ad.ParentCampaignID = raw.ParentCampaignID
		ad.ParentCampaign = raw.ParentCampaign
		ad.Objective = raw.Objective
		ad.BidPrice = raw.BidPrice
		ad.SecondPrice = raw.SecondPrice
		ad.NetworkAd = raw.NetworkAd
		if decoded := DecodeTaboolaURL(raw.ClickURL); decoded != nil {
			ad.Tracker = decoded.Tracker
			ad.AffiliateNetwork = decoded.AffiliateNetwork
			decoded.Tblci, decoded.TrcRoute, decoded.TrcGeoCountry = "", "", ""
			ad.Params = decoded.ToJSON()
		}
		return ad
	}
	if decoded := DecodeTaboolaURL(raw.ClickURL); decoded != nil {
		ad.CampaignID = decoded.CampaignID
		ad.ItemID = decoded.CampaignItemID
		ad.CampaignName = decoded.CampaignName
		ad.SiteID = decoded.SiteID
		ad.Tracker = decoded.Tracker
		ad.AffiliateNetwork = decoded.AffiliateNetwork
		// Link data keeps no one-time values.
		decoded.Tblci, decoded.TrcRoute, decoded.TrcGeoCountry = "", "", ""
		ad.Params = decoded.ToJSON()
	}
	// A link without a campaign id leaves the campaign empty. The account is
	// stored on its own and is never a campaign.
	return ad
}
