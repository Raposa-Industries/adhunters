package parse

import (
	"testing"
)

func TestCleanCanonicalURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Strip tblci and fragment",
			input:    "https://example.com/lander?tblci=GiD123#tblci456",
			expected: "https://example.com/lander",
		},
		{
			name:     "Preserve tracker routing parameters",
			input:    "https://track.domain.com/click?campaign_id=12345&sub1=test&tblci=abc",
			expected: "https://track.domain.com/click?campaign_id=12345&sub1=test",
		},
		{
			name:     "Strip click ids in any parameter",
			input:    "https://example.com/tb/?camp_id=50347138&cid=tbl4LahDwAGYCiC6ln3fftswH4wDskjBit38WAXqbBwg&pid=1151445",
			expected: "https://example.com/tb/?camp_id=50347138&pid=1151445",
		},
		{
			name:     "Strip unexpanded macros",
			input:    "https://example.com/lander?utm_campaign={campaign}&cost=%7Bcpc%7D&valid=1",
			expected: "https://example.com/lander?valid=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanCanonicalURL(tt.input)
			if got != tt.expected {
				t.Errorf("CleanCanonicalURL() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDecodeTaboolaURL(t *testing.T) {
	rawURL := "https://trc.taboola.com/nbc-today/log/3/click?p=nbc-today&campaign_id=98765&campaign_item_id=112233&site=today.com&platform=desktop"
	decoded := DecodeTaboolaURL(rawURL)

	if decoded == nil {
		t.Fatalf("expected non-nil decoded tracking")
	}
	if decoded.AccountID != "nbc-today" {
		t.Errorf("expected account ID nbc-today, got %s", decoded.AccountID)
	}
	if decoded.CampaignID != "98765" {
		t.Errorf("expected campaign ID 98765, got %s", decoded.CampaignID)
	}
	if decoded.CampaignItemID != "112233" {
		t.Errorf("expected campaign item ID 112233, got %s", decoded.CampaignItemID)
	}
	if decoded.Site != "today.com" {
		t.Errorf("expected site today.com, got %s", decoded.Site)
	}
}

func TestDecodeSkipsClickIDs(t *testing.T) {
	rawURL := "https://example.com/tb/?camp_id=50347138&cid=tbl4LahDwAGYCiC6ln3fftswH4wDskjBit38WAXqbBwg&sub2=tblh-fuowAGgCiDmPjs02Xa0v7ufVX6lqD"
	decoded := DecodeTaboolaURL(rawURL)
	if decoded.CampaignID != "50347138" {
		t.Errorf("expected campaign ID 50347138, got %s", decoded.CampaignID)
	}
	if decoded.CampaignItemID != "" {
		t.Errorf("expected no campaign item ID, got %s", decoded.CampaignItemID)
	}
}

func TestBrowserURLDropsWhatABrowserDrops(t *testing.T) {
	cases := map[string]string{
		// Investigation 3aeeeb0d: the operator's template ends in a line break
		// and the ad network appends the click id after it.
		"https://righttrack1.org/ln1odf8cwa?pid=4844&c=46830\n&tblci=tblX#tblcitblX": "https://righttrack1.org/ln1odf8cwa?pid=4844&c=46830&tblci=tblX#tblcitblX",
		"https://x.com/?a=1\r\n\r\n&tblci=t":                                         "https://x.com/?a=1&tblci=t",
		"  https://x.com/?a=b c\t&d=1 \n":                                            "https://x.com/?a=b%20c&d=1",
		"https://x.com/?a=1":                                                         "https://x.com/?a=1",
	}
	for in, want := range cases {
		if got := BrowserURL(in); got != want {
			t.Errorf("BrowserURL(%q) = %q, want %q", in, got, want)
		}
	}
}
