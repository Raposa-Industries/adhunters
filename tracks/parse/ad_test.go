package parse

import (
	"encoding/json"
	"testing"
)

func TestBuildAd(t *testing.T) {
	raw := candidate{
		Title:             " Doctors Stunned ",
		ThumbnailURL:      "https://images.taboola.com/taboola/image/fetch/f_jpg/https%3A//cdn.example.com/abc123.jpg",
		ClickURL:          "https://theconsumerguide.co/senior/tb/?camp_id=50347138&cid=tbl4LahDwAGYCiC6ln3fftswH4wDskjBit38WAXqbBwg&pid=1151445&tblci=x#tblci123",
		AdvertiserAccount: "acme-sc",
		Placement:         "Below Article",
		FeedPosition:      4,
	}
	ad := buildAd(raw)
	if ad.CampaignID != "50347138" {
		t.Errorf("campaign = %q", ad.CampaignID)
	}
	if ad.ClickURL != "https://theconsumerguide.co/senior/tb/?camp_id=50347138&pid=1151445" {
		t.Errorf("click url = %q", ad.ClickURL)
	}
	var params map[string]any
	if err := json.Unmarshal(ad.Params, &params); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tblci", "trc_route", "trc_geo_country"} {
		if _, ok := params[k]; ok {
			t.Errorf("params keep one-time key %s", k)
		}
	}
	if ad.Headline != "Doctors Stunned" || ad.CreativeKey == "" {
		t.Errorf("headline %q, creative key %q", ad.Headline, ad.CreativeKey)
	}

	// No campaign in the link: the campaign stays empty.
	raw.ClickURL = "https://example.com/lp?utm_source=taboola"
	if got := buildAd(raw).CampaignID; got != "" {
		t.Errorf("campaign = %q, want empty", got)
	}
}
