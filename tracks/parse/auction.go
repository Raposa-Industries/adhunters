package parse

// Taboola's auction telemetry on a card. Ported from adhunters-collector
// e20148c, internal/openrtb/extractor.go.

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	rxAuctionPrice = regexp.MustCompile(`auctionPrice=([0-9.]+)`)
	rxBval         = regexp.MustCompile(`bval=([0-9.]+)`)
	rxAuctionID    = regexp.MustCompile(`auctionId=([0-9a-zA-Z_-]+)`)
	rxCampaignID   = regexp.MustCompile(`campaignId=([0-9]+)`)
)

func extractCardAuctionTelemetry(card map[string]any, placementLabel, pubDomain, pubName string) *auctionTelemetry {
	if card == nil {
		return nil
	}

	isRtb := false
	if v, ok := card["is-rtb"]; ok {
		if b, ok := v.(bool); ok {
			isRtb = b
		} else if s, ok := v.(string); ok && s == "true" {
			isRtb = true
		}
	}

	seatPublisher := ""
	if v, ok := card["publisher"].(string); ok {
		seatPublisher = v
	}
	isCompetingSeat := strings.Contains(seatPublisher, "googleadx") ||
		strings.Contains(seatPublisher, "rtb") ||
		strings.Contains(seatPublisher, "seat")

	var auctionID string
	var clearingPrice *float64
	var bidValue *float64
	var capAuctionPrice *float64
	currency := "USD"
	seatID := seatPublisher
	if seatID == "" {
		if isRtb {
			seatID = "taboola-rtb"
		} else {
			seatID = "taboola-native"
		}
	}

	// 1. Inspect itp array
	if itpRaw, ok := card["itp"].([]any); ok {
		for _, entry := range itpRaw {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				continue
			}

			rawURL := ""
			if u, ok := entryMap["u"].(string); ok {
				rawURL = u
			} else if u, ok := entryMap["url"].(string); ok {
				rawURL = u
			}

			if !strings.Contains(rawURL, "auctionPrice") &&
				!strings.Contains(rawURL, "auctionId") &&
				!strings.Contains(rawURL, "bval") {
				continue
			}

			targetURL := rawURL
			if !strings.HasPrefix(targetURL, "http") {
				targetURL = "https://" + targetURL
			}

			u, err := url.Parse(targetURL)
			if err == nil {
				q := u.Query()
				if auctionID == "" && q.Get("auctionId") != "" {
					auctionID = q.Get("auctionId")
				}
				if clearingPrice == nil && q.Get("auctionPrice") != "" {
					if p, err := strconv.ParseFloat(q.Get("auctionPrice"), 64); err == nil {
						clearingPrice = &p
					}
				}
				if bidValue == nil && q.Get("bval") != "" {
					if b, err := strconv.ParseFloat(q.Get("bval"), 64); err == nil {
						bidValue = &b
					}
				}
				if capAuctionPrice == nil && q.Get("capAuctionPrice") != "" {
					if c, err := strconv.ParseFloat(q.Get("capAuctionPrice"), 64); err == nil {
						capAuctionPrice = &c
					}
				}
				if q.Get("auctionCurrency") != "" {
					currency = q.Get("auctionCurrency")
				}
				if q.Get("auctionSeatId") != "" {
					seatID = q.Get("auctionSeatId")
				}
			} else {
				// Fallback regex
				if m := rxAuctionPrice.FindStringSubmatch(rawURL); len(m) > 1 && clearingPrice == nil {
					if p, err := strconv.ParseFloat(m[1], 64); err == nil {
						clearingPrice = &p
					}
				}
				if m := rxBval.FindStringSubmatch(rawURL); len(m) > 1 && bidValue == nil {
					if b, err := strconv.ParseFloat(m[1], 64); err == nil {
						bidValue = &b
					}
				}
				if m := rxAuctionID.FindStringSubmatch(rawURL); len(m) > 1 && auctionID == "" {
					auctionID = m[1]
				}
			}
		}
	}

	// Fallback top-level card attribute
	if clearingPrice == nil {
		if ap, ok := card["auctionPrice"]; ok {
			switch v := ap.(type) {
			case float64:
				clearingPrice = &v
			case string:
				if p, err := strconv.ParseFloat(v, 64); err == nil {
					clearingPrice = &p
				}
			}
		}
	}

	if clearingPrice == nil && bidValue == nil && !isRtb && !isCompetingSeat {
		return nil
	}

	// A card without an auction id gets one from its scrape when it becomes an
	// Ad (Parse), so a replay gives it the same id. The collector made up a
	// random one here.

	cleanDomain := strings.ToLower(strings.TrimPrefix(pubDomain, "www."))

	return &auctionTelemetry{
		AuctionID:       auctionID,
		PublisherDomain: cleanDomain,
		PublisherName:   pubName,
		Placement:       placementLabel,
		ClearingPrice:   clearingPrice,
		BidValue:        bidValue,
		CapAuctionPrice: capAuctionPrice,
		Currency:        currency,
		WinningSeat:     seatID,
		IsRtb:           isRtb,
	}
}
