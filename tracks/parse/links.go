package parse

// Click links: cleaning, and reading tracker ids out of them. Ported from
// adhunters-collector e20148c, internal/sweeper/decoder.go.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// A Taboola click id: "tbl" followed by 20 or more URL-safe characters. It is a
// one-time value. Trackers often carry it in parameters that otherwise hold a
// campaign or item id (cid, sub1, ...), so it must never be read as one.
// Same rule as spy.is_click_id() (017).
var rxClickID = regexp.MustCompile(`^tbl[A-Za-z0-9_-]{20,}$`)

func IsClickID(v string) bool {
	return rxClickID.MatchString(v)
}

type DecodedTracking struct {
	Tracker          string `json:"tracker"`
	AffiliateNetwork string `json:"affiliate_network"`
	AccountID        string `json:"account_id,omitempty"`
	CampaignID       string `json:"campaign_id,omitempty"`
	CampaignItemID   string `json:"campaign_item_id,omitempty"`
	CampaignName     string `json:"campaign_name,omitempty"`
	Site             string `json:"site,omitempty"`
	SiteID           string `json:"site_id,omitempty"`
	Platform         string `json:"platform,omitempty"`
	Tblci            string `json:"tblci,omitempty"`
	UTMSource        string `json:"utm_source,omitempty"`
	UTMMedium        string `json:"utm_medium,omitempty"`
	UTMCampaign      string `json:"utm_campaign,omitempty"`
	UTMContent       string `json:"utm_content,omitempty"`
	UTMTerm          string `json:"utm_term,omitempty"`
	AffiliateID      string `json:"affiliate_id,omitempty"`
	BidCost          string `json:"bid_cost,omitempty"`
	TrcRoute         string `json:"trc_route,omitempty"`
	TrcGeoCountry    string `json:"trc_geo_country,omitempty"`
}

// BrowserURL cleans a link the way a browser's address parser does before it
// loads it: tabs and line breaks anywhere are dropped, leading and trailing
// spaces and control characters are cut, and any other space or control
// character left inside is percent-encoded. Some operators end their tracking
// template with a line break, and the ad network appends "&tblci=..." after
// it, so the feed serves a link with a newline inside. A reader's browser
// never sees that newline; Go's URL parser refuses the whole link.
func BrowserURL(raw string) string {
	s := strings.TrimFunc(raw, func(r rune) bool { return r <= 0x20 })
	if !strings.ContainsFunc(s, func(r rune) bool { return r <= 0x20 || r == 0x7f }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\t' || c == '\n' || c == '\r':
			// dropped, as a browser does
		case c <= 0x20 || c == 0x7f:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// CleanCanonicalURL strips ephemeral click hashes, embedded assets, unexpanded macros, and query bloat.
func CleanCanonicalURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// 1. Strip client-side fragment (#tblci...)
	u.Fragment = ""

	// 2. Filter query parameters
	q := u.Query()
	filtered := url.Values{}

	ignoredParams := map[string]bool{
		"tblci": true, "clickid": true, "click_id": true, "taboola_click_id": true,
		"utm_tci": true, "external_id": true, "ref_id": true, "aff_sub5": true,
		"tbclid": true, "tblcid": true,
		"thumb": true, "thumbnail": true, "title": true, "headline": true,
		"cost": true, "ts": true, "tb_timestamp": true, "tb_cachebuster": true,
		"tb_cpc": true, "tb_gdpr": true, "tb_custom_id": true, "timestamp": true,
		"sdomain": true, "site_domain": true, "site1": true, "site2": true,
	}

	for k, vals := range q {
		lowerK := strings.ToLower(k)
		if ignoredParams[lowerK] {
			continue
		}
		for _, v := range vals {
			v = strings.TrimSpace(v)
			// Drop empty values, click ids, and unexpanded template macros like "{cpc}" or "%7B...%7D"
			if v == "" || IsClickID(v) || (strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}")) || (strings.HasPrefix(v, "%7B") && strings.HasSuffix(v, "%7D")) {
				continue
			}
			filtered.Add(k, v)
		}
	}

	u.RawQuery = filtered.Encode()
	return u.String()
}

func DecodeTaboolaURL(rawURL string) *DecodedTracking {
	if rawURL == "" {
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}

	params := make(map[string]string)
	for k, v := range u.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}

	lowerURL := strings.ToLower(rawURL)

	// 1. Detect Tracker Infrastructure
	tracker := "Direct / Untracked"
	if params["vmid"] != "" || strings.Contains(lowerURL, "voluum") || params["v_id"] != "" || params["cep"] != "" || params["lptoken"] != "" {
		tracker = "Voluum (Ad Tracker)"
	} else if params["rdt_cid"] != "" || params["rtkcid"] != "" || params["rtkcmpid"] != "" || strings.Contains(lowerURL, "redtrack") || (params["sub1"] != "" && params["sub2"] != "" && params["sub3"] != "") {
		tracker = "RedTrack (Performance Tracker)"
	} else if params["b_id"] != "" || params["bna"] != "" || params["bin_id"] != "" || strings.Contains(lowerURL, "binom") {
		tracker = "Binom Tracker"
	} else if params["everflow"] != "" || params["ef_id"] != "" {
		tracker = "Everflow Partner Marketing"
	} else if params["thrive"] != "" || params["thr_id"] != "" {
		tracker = "Thrive Tracker"
	} else if params["flux"] != "" || params["fflux"] != "" || strings.Contains(lowerURL, "funnelflux") {
		tracker = "FunnelFlux"
	} else if params["at_id"] != "" || params["anytrack"] != "" || strings.Contains(lowerURL, "anytrack") {
		tracker = "AnyTrack"
	} else if params["cpv"] != "" || params["cpv_id"] != "" || strings.Contains(lowerURL, "cpvlab") {
		tracker = "CPVLab"
	} else if params["k_id"] != "" || strings.Contains(lowerURL, "keitaro") {
		tracker = "Keitaro Tracker"
	} else if params["f_id"] != "" || params["h_id"] != "" || strings.Contains(lowerURL, "hyros") {
		tracker = "Hyros Tracking"
	} else if strings.Contains(strings.ToLower(params["utm_source"]), "taboola") {
		tracker = "Taboola Native Pixel / S2S"
	}

	// 2. Detect Affiliate Network / Sales Gateway
	affiliateNetwork := "Direct DTC Advertiser"
	if strings.Contains(lowerURL, "clickbank.net") || params["tid"] != "" || params["cb_id"] != "" {
		affiliateNetwork = "ClickBank"
	} else if strings.Contains(lowerURL, "buygoods.com") || params["bg_id"] != "" || params["sessid"] != "" {
		affiliateNetwork = "BuyGoods"
	} else if strings.Contains(lowerURL, "maxweb") || params["mw_id"] != "" {
		affiliateNetwork = "MaxWeb"
	} else if strings.Contains(lowerURL, "digistore24") || strings.Contains(lowerURL, "digistore") {
		affiliateNetwork = "Digistore24"
	} else if strings.Contains(lowerURL, "giddyup") || params["gu_id"] != "" {
		affiliateNetwork = "GiddyUp"
	} else if strings.Contains(lowerURL, "advidi") {
		affiliateNetwork = "Advidi"
	} else if getAny(params, "affId", "aff_id", "affiliate_id", "aid", "pid") != "" {
		affiliateNetwork = "Affiliate Network (ID: " + getAny(params, "affId", "aff_id", "affiliate_id", "aid", "pid") + ")"
	}

	// 3. Extract Taboola Tokens
	accountID := getAny(params, "p", "account_id", "account")
	if accountID == "" && params["redir"] != "" {
		if nested, err := url.Parse(params["redir"]); err == nil {
			accountID = nested.Query().Get("p")
		}
	}

	campaignID := getID(params, "campaignid", "campaign_id", "camp_id", "cid", "nb_cid", "sub1", "utm_campaign_id")
	campaignItemID := getID(params, "campaignitemid", "campaign_item_id", "contentid", "nb_ciid", "sub2", "sub4", "utm_item_id")
	campaignName := getAny(params, "campaign_name", "campaignname", "utm_campaign", "cname")
	site := getAny(params, "site", "utm_medium", "pub", "publisherid", "nb_sid", "utm_publisher")
	siteID := getAny(params, "site_id", "siteid")
	if siteID == "" && params["sub8"] != "" && !strings.HasPrefix(params["sub8"], "http") {
		siteID = params["sub8"]
	}
	platform := getAny(params, "platform", "device", "nb_platform", "sub7", "utm_device")
	tblci := getAny(params, "tblci", "clickid", "click_id", "taboola_click_id", "utm_tci")
	affID := getAny(params, "affId", "aff_id", "affiliate_id", "aid", "pid")
	bidCost := getAny(params, "bidCost", "cpc", "bid", "cost")

	return &DecodedTracking{
		Tracker:          tracker,
		AffiliateNetwork: affiliateNetwork,
		AccountID:        accountID,
		CampaignID:       campaignID,
		CampaignItemID:   campaignItemID,
		CampaignName:     campaignName,
		Site:             site,
		SiteID:           siteID,
		Platform:         platform,
		Tblci:            tblci,
		UTMSource:        params["utm_source"],
		UTMMedium:        params["utm_medium"],
		UTMCampaign:      params["utm_campaign"],
		UTMContent:       params["utm_content"],
		UTMTerm:          params["utm_term"],
		AffiliateID:      affID,
		BidCost:          bidCost,
	}
}

func (d *DecodedTracking) ToJSON() json.RawMessage {
	b, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// getID is getAny for id fields: click ids are skipped.
func getID(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" && !IsClickID(v) {
			return v
		}
	}
	return ""
}

func getAny(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return ""
}
