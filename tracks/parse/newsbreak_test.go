package parse

import (
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseNewsBreakResponse(t *testing.T) {
	// A real auction reply (trimmed): the same ad won two slots, and a
	// pubmatic banner bid came back too.
	raw, err := os.ReadFile("testdata/newsbreak_auction.json")
	if err != nil {
		t.Fatal(err)
	}
	pub := Publisher{Name: "NewsBreak", Domain: "newsbreak.com"}

	ads, err := parseNewsBreakResponse(raw, pub, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if len(ads) != 1 {
		t.Fatalf("expected the repeated ad once and no banner, got %d ads", len(ads))
	}
	a := ads[0]
	checks := map[string][2]string{
		"account":  {a.AdvertiserAccount, "2053906549512589314"},
		"campaign": {a.CampaignID, "2100412018608353282"},
		"ad":       {a.NetworkAdID, "2100412029291245569"},
		"brand":    {a.Branding, "Sarah"},
		"creative": {a.CreativeKey, "nb:cfe1650dde76c912ce7b207f3d6718273cbd6cba.jpg"},
		"network":  {a.Network, "newsbreak"},
		"slot":     {a.Placement, "psm-pmi-nbcom-web-articlebottom-native-prod"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %q, want %q", name, c[0], c[1])
		}
	}
	if !strings.HasPrefix(a.Title, "Doctors Identify 10 Prescriptions") {
		t.Errorf("headline: got %q", a.Title)
	}
	if !strings.HasPrefix(a.ClickURL, "https://online.ustodayreport.com/blog/?") || strings.Contains(a.ClickURL, "nbclid") {
		t.Errorf("landing page: got %q", a.ClickURL)
	}
}

func TestNewsBreakCreativeKey(t *testing.T) {
	cases := map[string]string{
		"https://img.particlenews.com/image.php?type=webp_1200x000&url=https%3A%2F%2Fstatic.particlenews.com%2Fnova%2Fassets%2F205%2Fcfe1650dde76c912ce7b207f3d6718273cbd6cba.jpg": "nb:cfe1650dde76c912ce7b207f3d6718273cbd6cba.jpg",
		"https://static.particlenews.com/nova/assets/196/b269e75fae8ee027d62b77e79e4561cf5a041923_trans.mp4/720_30_6mbps_h264.mp4":                                                 "nb:b269e75fae8ee027d62b77e79e4561cf5a041923_trans.mp4",
		"": "no_image",
	}
	for in, want := range cases {
		if got := NewsBreakCreativeKey(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestNewsBreakLandingURL(t *testing.T) {
	// Protobuf: field 1 (string) is the landing page, field 3 (varint) is noise.
	landing := "https://try.example.com/lp?utm_source=newsbreak&nb_cid=e3e1b4d07ea7476680dbe64e_210277&utm_term=adset01&nbclid=nvss_x"
	msg := append([]byte{0x0a, byte(len(landing))}, landing...)
	msg = append(msg, 0x18, 0x96, 0x01)
	ctr := "https://rd.newsbreak.com/r?p=" + base64.RawURLEncoding.EncodeToString(msg)

	got := NewsBreakLandingURL(ctr, "e3e1b4d0-7ea7-4766-80db-e64ec607317f")
	want := "https://try.example.com/lp?utm_source=newsbreak&utm_term=adset01"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	if got := NewsBreakLandingURL("https://rd.newsbreak.com/r?p=%%%", ""); got != "https://rd.newsbreak.com/r?p=%%%" {
		t.Fatalf("unreadable link should come back as it is, got %q", got)
	}
}

func TestParseNewsBreakResponseKeepsEveryField(t *testing.T) {
	// Real auction replies (token trimmed): a health ad whose link names its
	// NewsBreak campaign, ad set and ad, and a NewsBreak house ad.
	raw, err := os.ReadFile("testdata/newsbreak_auction_full.json")
	if err != nil {
		t.Fatal(err)
	}
	pub := Publisher{Name: "NewsBreak", Domain: "newsbreak.com"}
	ads, err := parseNewsBreakResponse(raw, pub, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if len(ads) != 1 {
		t.Fatalf("expected the house ad skipped like on Taboola, got %d ads", len(ads))
	}
	a := ads[0]
	checks := map[string][2]string{
		"org":           {a.OrgID, "2092376173483810818"},
		"ad set":        {a.CampaignID, "2101537912278671362"},
		"ad set name":   {a.CampaignName, "[BID] [01] [Memória] [Leandro] [20/09]"},
		"campaign":      {a.ParentCampaignID, "2101537908369580033"},
		"objective":     {a.Objective, "WEB_CONVERSION"},
		"ad name":       {a.NetworkAd.Name, "copy 2 of 001"},
		"creative id":   {a.NetworkAd.CreativeID, "2101539148212854786"},
		"feed position": {strconv.Itoa(a.FeedPosition), "1"},
		"started":       {a.NetworkAd.StartedAt.Format(time.RFC3339), "2026-09-20T05:04:17Z"},
		"icon":          {strings.Split(a.NetworkAd.IconURL, "/assets/")[0], "https://static.particlenews.com/nova"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %q, want %q", name, c[0], c[1])
		}
	}
	if a.BidPrice == nil || a.SecondPrice == nil || *a.SecondPrice < 1.8 || *a.SecondPrice > 1.81 {
		t.Errorf("prices: got bid %v, second %v", a.BidPrice, a.SecondPrice)
	}
}

func TestNewsBreakLinkNames(t *testing.T) {
	ad := nbAd{AdAccountID: "1993724555765669890", AdsetID: "2101034543242805250", AdID: "2101034550584434689"}
	cases := []struct {
		link string
		want NewsBreakNames
	}{
		{ // CAMPAIGN_ID / FLIGHT_NAME / CREATIVE_NAME shape
			"https://ad.rejuvacare.com/x?OS=Windows&CAMPAIGN_ID=2087185815783710721&CAMPAIGN_NAME=BB+-+STN&FLIGHT_ID=2101034543242805250&FLIGHT_NAME=NB+%7C+BB&CREATIVE_ID=2101034550584434689&CREATIVE_NAME=copy+1&affId=056D1359",
			NewsBreakNames{"2087185815783710721", "BB - STN", "NB | BB", "copy 1"},
		},
		{ // utm shape: the campaign id twice, no names
			"https://americanhealthdaily.site/advertorial/?utm_id=2091687174724833281&utm_source=newsbreak&utm_medium=2101034543242805250&utm_campaign=2091687174724833281&utm_term=Blood+Sugar",
			NewsBreakNames{CampaignID: "2091687174724833281"},
		},
		{ // sub1..sub6 without the ad set in sub3 are not trusted
			"https://x.com/?sub1=2100411992024313858&sub2=name&sub3=1&sub4=adset",
			NewsBreakNames{},
		},
		{ // no macros
			"https://try.smoothspine.com/tfbm-coc-adv2-nb?is_nova=true",
			NewsBreakNames{},
		},
	}
	for _, c := range cases {
		if got := NewsBreakLinkNames(c.link, ad); got != c.want {
			t.Errorf("%s\n got %+v\nwant %+v", c.link, got, c.want)
		}
	}
}
