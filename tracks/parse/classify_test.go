package parse

import (
	"testing"
)

func TestExtractImageAssetKey(t *testing.T) {
	url := "https://cdn.taboola.com/libtrc/static/thumbnails/341999900/asset_hash_123.jpg?v=1"
	key := ExtractImageAssetKey(url)
	if key != "asset_hash_123.jpg" {
		t.Fatalf("expected asset_hash_123.jpg, got %s", key)
	}
}

func TestCleanText(t *testing.T) {
	input := "This &amp; That &#39;Special&#39;  Offer   "
	expected := "This & That 'Special' Offer"
	got := CleanText(input)
	if got != expected {
		t.Errorf("CleanText() = %q, want %q", got, expected)
	}
}

func TestClassifyAd(t *testing.T) {
	ad := candidate{
		Title:        "The Secret Trick To Clean Your Gut Fast",
		Branding:     "Health Digest",
		ThumbnailURL: "https://cdn.taboola.com/thumb/gut.jpg",
		ClickURL:     "https://cleansepath.com/report?campaign_id=123",
	}

	cat, _ := ClassifyAd(ad)
	if cat != CategoryCompetitorOffer {
		t.Fatalf("expected %s, got %s", CategoryCompetitorOffer, cat)
	}

	// Test fallback filter
	fallbackAd := candidate{
		Title:    "default banner title",
		Branding: "-",
		ClickURL: "https://example.com/fallback.jpg",
	}
	fCat, _ := ClassifyAd(fallbackAd)
	if fCat != CategorySystemFallback {
		t.Fatalf("expected %s, got %s", CategorySystemFallback, fCat)
	}
}
