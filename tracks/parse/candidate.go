package parse

import (
	"encoding/json"
	"time"
)

// Publisher is what a parser needs to know about the page a scrape asked
// for. The loader reads it from the raw record itself, so a replay never
// depends on today's targets file.
type Publisher struct {
	Name   string
	Domain string // bare host, no "www."
	URL    string // the page the feed was asked for
}

// candidate is one card of a feed answer before it becomes an Ad. Ported from
// the collector's model.RawAdCandidate (e20148c), less the fields that now
// come from the raw record (batch, proxy line, time, latency, checksum).
type candidate struct {
	Title             string
	Branding          string
	Description       string
	CtaText           string
	ThumbnailURL      string
	ClickURL          string
	Publisher         string
	PublisherDomain   string
	Placement         string
	Device            string
	EcpaPercentile    *float64
	PublishedDate     *int64
	AdvertiserAccount string
	// Set by ad networks whose ids are not in the click link (NewsBreak Ads).
	Network          string
	CreativeKey      string
	CampaignID       string
	CampaignName     string
	NetworkAdID      string
	OrgID            string // NewsBreak org that owns the account
	ParentCampaignID string // NewsBreak campaign above the ad set, from the landing link
	ParentCampaign   string // its name
	Objective        string
	BidPrice         *float64
	SecondPrice      *float64
	NetworkAd        *NetworkAd
	FormatType       string
	VideoDuration    int
	ThumbDimensions  string
	Cropping         json.RawMessage
	Language         string
	Tat              string
	Sig              string
	Tblci            string
	FeedPosition     int
	BlockPosition    int
	AuctionTelemetry *auctionTelemetry
	TrcMeta          trcSessionMeta
}

// NetworkAd is what an ad network tells about one of its ad ids.
type NetworkAd struct {
	CreativeID    string
	Name          string
	StartedAt     *time.Time
	IconURL       string
	Layout        string
	MediaAspect   string
	LaunchOption  string
	IabTier1      string
	IabTier2      string
	LandingDomain string
	Disclaimer    string
	Extra         json.RawMessage
}

type auctionTelemetry struct {
	AuctionID       string
	PublisherDomain string
	PublisherName   string
	Placement       string
	ClearingPrice   *float64
	BidValue        *float64
	CapAuctionPrice *float64
	Currency        string
	WinningSeat     string
	IsRtb           bool
}

type trcSessionMeta struct {
	SessionID  string
	UserID     string
	GeoCountry string
	Route      string
}
