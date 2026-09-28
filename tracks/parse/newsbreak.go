package parse

// NewsBreak Ads' auction answer, parsed as the collector parsed it. Ported
// from adhunters-collector e20148c, internal/sweeper/newsbreak.go; the request
// half lives in tracks/capture/feed.
//
// NewsBreak Ads is NewsBreak's own ad network. The winning "msp_nova" bid
// carries the full ad as JSON: account, ad set, ad, creative, headline, image
// and a redirect link that holds the landing page address.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const newsbreakSeat = "msp_nova"

type nbAuctionReply struct {
	SeatBid []struct {
		Seat string `json:"seat"`
		Bid  []struct {
			Price float64 `json:"price"`
			Adm   string  `json:"adm"`
			Ext   struct {
				Nova struct {
					AdUnitID string `json:"ad_unit_id"`
				} `json:"nova"`
			} `json:"ext"`
		} `json:"bid"`
	} `json:"seatbid"`
}

type nbAdm struct {
	Ad []nbAd `json:"ad"`
}

type nbAd struct {
	AdAccountID string     `json:"adAccountId"`
	AdID        string     `json:"adId"`
	AdsetID     string     `json:"adsetId"`
	OrgID       string     `json:"orgId"`
	RequestID   string     `json:"requestId"`
	Price       *float64   `json:"price"`
	SecondPrice *float64   `json:"secondPrice"`
	StartTimeMs string     `json:"startTimeMs"`
	HighValue   bool       `json:"highValue"`
	Creative    nbCreative `json:"creative"`
}

type nbCreative struct {
	Advertiser        string          `json:"advertiser"`
	Headline          string          `json:"headline"`
	Body              string          `json:"body"`
	CallToAction      string          `json:"callToAction"`
	CreativeID        string          `json:"creativeId"`
	CreativeType      string          `json:"creativeType"`
	CtrURL            string          `json:"ctrUrl"`
	ImageURL          string          `json:"imageUrl"`
	ImageURLs         []string        `json:"imageUrls"`
	IconURL           string          `json:"iconUrl"`
	CampaignObjective string          `json:"campaignObjective"`
	Layout            string          `json:"layout"`
	MediaAspect       string          `json:"mediaAspect"`
	LaunchOption      string          `json:"launchOption"`
	IabTier1          string          `json:"iabTier1ContentCode"`
	IabTier2          string          `json:"iabTier2ContentCode"`
	LandingPageDomain string          `json:"landingPageDomain"`
	Disclaimer        string          `json:"disclaimer"`
	Address           string          `json:"address"`
	AppStoreID        string          `json:"appStoreId"`
	AdBundleID        string          `json:"adBundleId"`
	CtaStyle          string          `json:"ctaStyle"`
	CarouselItems     json.RawMessage `json:"carouselItems"`
	ProductItemList   json.RawMessage `json:"productItemList"`
	HTMLPageItems     json.RawMessage `json:"htmlPageItems"`
	ImpressionTrack   []string        `json:"thirdPartyImpressionTrackingUrls"`
	ClickTrack        []string        `json:"thirdPartyClickTrackingUrls"`
	ViewTrack         []string        `json:"thirdPartyViewTrackingUrls"`
	VideoItem         *struct {
		CoverURL string `json:"coverUrl"`
		VideoURL string `json:"videoUrl"`
	} `json:"videoItem"`
}

func parseNewsBreakResponse(raw []byte, pub Publisher, device string) ([]candidate, error) {
	var reply nbAuctionReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("json unmarshal newsbreak response error: %w", err)
	}

	var candidates []candidate
	seen := map[string]bool{}
	pos := 0
	for _, sb := range reply.SeatBid {
		if sb.Seat != newsbreakSeat {
			continue // other bidders return display banners, not native ads
		}
		for _, b := range sb.Bid {
			var adm nbAdm
			if err := json.Unmarshal([]byte(b.Adm), &adm); err != nil || len(adm.Ad) == 0 {
				continue
			}
			ad := adm.Ad[0]
			c := ad.Creative
			if ad.AdID == "" || seen[ad.AdID] {
				continue
			}
			seen[ad.AdID] = true

			// A video ad names its creative by the video file, and shows its
			// cover image (imageUrl then holds the video).
			image, asset, format := c.ImageURL, c.ImageURL, "image"
			if c.CreativeType == "VIDEO" && c.VideoItem != nil {
				format = "video"
				image = c.VideoItem.CoverURL
				if c.VideoItem.VideoURL != "" {
					asset = c.VideoItem.VideoURL
				}
			}
			landing := NewsBreakLandingURL(c.CtrURL, ad.RequestID)
			names := NewsBreakLinkNames(landing, ad)
			var bid *float64
			if b.Price > 0 {
				p := b.Price
				bid = &p
			}
			cand := candidate{
				Title:             c.Headline,
				Branding:          strings.TrimSpace(c.Advertiser),
				Description:       c.Body,
				CtaText:           c.CallToAction,
				ThumbnailURL:      image,
				ClickURL:          BrowserURL(landing),
				Publisher:         pub.Name,
				PublisherDomain:   pub.Domain,
				Placement:         b.Ext.Nova.AdUnitID,
				Device:            device,
				AdvertiserAccount: ad.AdAccountID,
				FormatType:        format,
				Language:          "en",
				Network:           "newsbreak",
				CreativeKey:       NewsBreakCreativeKey(asset),
				CampaignID:        ad.AdsetID,
				CampaignName:      names.AdsetName,
				NetworkAdID:       ad.AdID,
				OrgID:             ad.OrgID,
				ParentCampaignID:  names.CampaignID,
				ParentCampaign:    names.CampaignName,
				Objective:         c.CampaignObjective,
				BidPrice:          bid,
				SecondPrice:       ad.SecondPrice,
				NetworkAd:         newsbreakDetails(ad, names.AdName),
				BlockPosition:     1,
			}
			// Same kinds skipped as on Taboola: house ads, news, travel,
			// real estate, search arbitrage and big brands.
			if cat, _ := ClassifyAd(cand); cat != CategoryCompetitorOffer {
				continue
			}
			pos++
			cand.FeedPosition = pos
			candidates = append(candidates, cand)
		}
	}
	return candidates, nil
}

// NewsBreakCreativeKey names a creative by its image or video file. NewsBreak
// serves images through a resizer (img.particlenews.com/image.php?url=...), so
// the key comes from the original file inside it. Files are named by a content
// hash; a video path ends in a rendition name (<hash>_trans.mp4/720_30_6mbps_h264.mp4),
// so the key is the last path part that starts with a hash. The "nb:" prefix
// keeps it apart from Taboola image names.
func NewsBreakCreativeKey(imageURL string) string {
	if imageURL == "" {
		return "no_image"
	}
	target := imageURL
	if u, err := url.Parse(imageURL); err == nil {
		if inner := u.Query().Get("url"); inner != "" {
			target = inner
		}
	}
	u, err := url.Parse(target)
	if err != nil || u.Path == "" {
		return "nb:" + ExtractImageAssetKey(imageURL)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if rxHashPrefix.MatchString(parts[i]) {
			return "nb:" + parts[i]
		}
	}
	return "nb:" + path.Base(u.Path)
}

var rxHashPrefix = regexp.MustCompile(`^[0-9a-f]{32,}`)

// NewsBreakLandingURL reads the landing page address out of a NewsBreak click
// link (rd.newsbreak.com/r?p=...). The p value is a base64 protobuf message
// whose field 1 is the address. Values made from the ad request id (NewsBreak
// click ids like nbclid=nvss_...) are one-time, so they are removed. An
// unreadable link is returned as it is.
func NewsBreakLandingURL(ctrURL, requestID string) string {
	u, err := url.Parse(ctrURL)
	if err != nil {
		return ctrURL
	}
	p := u.Query().Get("p")
	if p == "" {
		return ctrURL
	}
	msg, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p, "="))
	if err != nil {
		return ctrURL
	}
	landing := protoString(msg, 1)
	if landing == "" {
		return ctrURL
	}
	return stripNewsBreakClickIDs(landing, requestID)
}

func stripNewsBreakClickIDs(rawURL, requestID string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	reqHex := strings.ReplaceAll(strings.ToLower(requestID), "-", "")
	if len(reqHex) > 24 {
		reqHex = reqHex[:24]
	}
	q := u.Query()
	for k, vals := range q {
		lk := strings.ToLower(k)
		if lk == "nbclid" || lk == "nb_clid" {
			q.Del(k)
			continue
		}
		for _, v := range vals {
			if reqHex != "" && strings.Contains(strings.ToLower(v), reqHex) {
				q.Del(k)
				break
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// protoString returns the first length-delimited field with the given number
// in a protobuf message, or "" when there is none.
func protoString(msg []byte, field uint64) string {
	for len(msg) > 0 {
		key, n := binary.Uvarint(msg)
		if n <= 0 {
			return ""
		}
		msg = msg[n:]
		switch key & 7 {
		case 0:
			_, n = binary.Uvarint(msg)
			if n <= 0 {
				return ""
			}
			msg = msg[n:]
		case 1:
			if len(msg) < 8 {
				return ""
			}
			msg = msg[8:]
		case 2:
			l, n := binary.Uvarint(msg)
			if n <= 0 || uint64(len(msg)-n) < l {
				return ""
			}
			if key>>3 == field {
				return string(msg[n : n+int(l)])
			}
			msg = msg[n+int(l):]
		case 5:
			if len(msg) < 4 {
				return ""
			}
			msg = msg[4:]
		default:
			return ""
		}
	}
	return ""
}

// NewsBreakNames are the names and ids an advertiser put in its landing link
// with NewsBreak's link macros. NewsBreak's auction gives none of them.
type NewsBreakNames struct {
	CampaignID   string
	CampaignName string
	AdsetName    string
	AdName       string
}

var rxNewsBreakID = regexp.MustCompile(`^\d{16,20}$`)

// NewsBreakLinkNames reads the NewsBreak campaign id and the campaign, ad set
// and ad names out of a landing link. Advertisers fill them with macros under
// their own parameter names. Seen shapes:
//
//	sub1..sub6            campaign id, campaign name, ad set id, ad set name, ad id, ad name
//	CAMPAIGN_ID, FLIGHT_NAME, CREATIVE_NAME (ad set = flight, ad = creative)
//	campaign, adset_name, ad_name, utm_campaign
//
// sub1..sub6 are trusted only when sub3 is the ad set id or sub5 the ad id.
func NewsBreakLinkNames(landingURL string, ad nbAd) NewsBreakNames {
	var n NewsBreakNames
	u, err := url.Parse(landingURL)
	if err != nil {
		return n
	}
	q := map[string]string{}
	for k, v := range u.Query() {
		if len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			q[strings.ToLower(k)] = strings.TrimSpace(v[0])
		}
	}
	known := map[string]bool{ad.AdsetID: true, ad.AdID: true, ad.AdAccountID: true, ad.OrgID: true, ad.Creative.CreativeID: true}
	isCampaignID := func(v string) bool { return rxNewsBreakID.MatchString(v) && !known[v] }
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := q[k]; v != "" {
				return v
			}
		}
		return ""
	}

	subs := q["sub3"] == ad.AdsetID || q["sub5"] == ad.AdID
	if subs && isCampaignID(q["sub1"]) {
		n.CampaignID = q["sub1"]
	}
	if n.CampaignID == "" {
		for _, k := range []string{"campaign_id", "campaignid", "campaign", "utm_campaign", "utm_id", "cid"} {
			if isCampaignID(q[k]) {
				n.CampaignID = q[k]
				break
			}
		}
	}
	n.CampaignName = first("campaign_name", "campaignname")
	n.AdsetName = first("adset_name", "adsetname", "flight_name", "ad_group_name")
	n.AdName = first("ad_name", "adname", "creative_name")
	if subs {
		if n.CampaignName == "" && n.CampaignID != "" && q["sub1"] == n.CampaignID {
			n.CampaignName = q["sub2"]
		}
		if n.AdsetName == "" && q["sub3"] == ad.AdsetID {
			n.AdsetName = q["sub4"]
		}
		if n.AdName == "" && q["sub5"] == ad.AdID {
			n.AdName = q["sub6"]
		}
	}
	// A name that is only an id or a macro left unfilled says nothing.
	for _, p := range []*string{&n.CampaignName, &n.AdsetName, &n.AdName} {
		if rxNewsBreakID.MatchString(*p) || strings.Contains(*p, "{") {
			*p = ""
		}
	}
	return n
}

// newsbreakDetails keeps what the auction tells about one NewsBreak ad id.
// Lists that are empty on most ads go in Extra.
func newsbreakDetails(ad nbAd, adName string) *NetworkAd {
	c := ad.Creative
	d := &NetworkAd{
		CreativeID:    c.CreativeID,
		Name:          adName,
		IconURL:       c.IconURL,
		Layout:        c.Layout,
		MediaAspect:   c.MediaAspect,
		LaunchOption:  c.LaunchOption,
		IabTier1:      c.IabTier1,
		IabTier2:      c.IabTier2,
		LandingDomain: c.LandingPageDomain,
		Disclaimer:    strings.TrimSpace(c.Disclaimer),
	}
	if ms, err := strconv.ParseInt(ad.StartTimeMs, 10, 64); err == nil && ms > 0 {
		t := time.UnixMilli(ms).UTC()
		d.StartedAt = &t
	}
	extra := map[string]any{}
	nonEmpty := func(k string, raw json.RawMessage) {
		s := strings.TrimSpace(string(raw))
		if s != "" && s != "null" && s != "[]" && s != "{}" {
			extra[k] = raw
		}
	}
	nonEmpty("carousel_items", c.CarouselItems)
	nonEmpty("product_items", c.ProductItemList)
	nonEmpty("html_page_items", c.HTMLPageItems)
	for k, v := range map[string][]string{
		"image_urls": c.ImageURLs, "impression_trackers": c.ImpressionTrack,
		"click_trackers": c.ClickTrack, "view_trackers": c.ViewTrack,
	} {
		if len(v) > 0 {
			extra[k] = v
		}
	}
	for k, v := range map[string]string{
		"creative_type": c.CreativeType, "address": c.Address, "app_store_id": c.AppStoreID,
		"ad_bundle_id": c.AdBundleID, "cta_style": c.CtaStyle,
	} {
		if v != "" {
			extra[k] = v
		}
	}
	if ad.HighValue {
		extra["high_value"] = true
	}
	if b, err := json.Marshal(extra); err == nil {
		d.Extra = b
	}
	return d
}
