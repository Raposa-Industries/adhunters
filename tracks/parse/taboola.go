package parse

// Taboola's feed answer, parsed as the collector parsed it. Ported from
// adhunters-collector e20148c, internal/sweeper/client.go; the request half
// lives in tracks/capture/feed.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func parseTaboolaResponse(rawBytes []byte, pub Publisher, device string) ([]candidate, trcSessionMeta, error) {
	rawText := strings.TrimSpace(string(rawBytes))
	if rawText == "" {
		return nil, trcSessionMeta{}, nil
	}
	// Taboola uses this callback when a placement has no eligible inventory.
	if strings.HasPrefix(rawText, "TRC.callbacks.mute()") && !strings.Contains(rawText, "{") {
		return nil, trcSessionMeta{}, nil
	}
	// Some publisher placements return JavaScript statements before the actual
	// callback, and terminate that callback with a semicolon. Keep the payload
	// wrapper while discarding those leading statements.
	for _, marker := range []string{"trc_json_response =", "TFASC.trkCallback(", "window.taboola_response(", "taboola_callback("} {
		if idx := strings.Index(rawText, marker); idx > 0 {
			rawText = strings.TrimSpace(rawText[idx:])
			break
		}
	}
	rawText = strings.TrimSuffix(rawText, ";")
	// A few placements prepend callback-control statements. The response still
	// contains one JSON object; isolate it before applying callback wrappers.
	if start := strings.Index(rawText, "{"); start > 0 {
		if end := strings.LastIndex(rawText, "}"); end > start {
			rawText = rawText[start : end+1]
		}
	}

	if idx := strings.Index(rawText, "trc_json_response ="); idx != -1 {
		rawText = strings.TrimSpace(rawText[idx+len("trc_json_response ="):])
	}
	if strings.HasPrefix(rawText, "TFASC.trkCallback(") && strings.HasSuffix(rawText, ")") {
		rawText = strings.TrimSpace(rawText[len("TFASC.trkCallback(") : len(rawText)-1])
	} else if strings.HasPrefix(rawText, "window.taboola_response(") && strings.HasSuffix(rawText, ")") {
		rawText = strings.TrimSpace(rawText[len("window.taboola_response(") : len(rawText)-1])
	} else if strings.HasPrefix(rawText, "taboola_callback(") && strings.HasSuffix(rawText, ")") {
		rawText = strings.TrimSpace(rawText[len("taboola_callback(") : len(rawText)-1])
	}

	var root map[string]any
	if err := json.Unmarshal([]byte(rawText), &root); err != nil {
		return nil, trcSessionMeta{}, fmt.Errorf("json unmarshal taboola response error: %w", err)
	}

	var candidates []candidate
	cleanPubDomain := strings.ToLower(strings.TrimPrefix(pub.Domain, "www."))

	var trcMeta trcSessionMeta
	if trc, ok := root["trc"].(map[string]any); ok {
		if v, ok := trc["sd"].(string); ok {
			trcMeta.SessionID = v
		}
		if v, ok := trc["ui"].(string); ok {
			trcMeta.UserID = v
		}
		if v, ok := trc["cc"].(string); ok {
			trcMeta.GeoCountry = v
		}
		if v, ok := trc["route"].(string); ok {
			trcMeta.Route = v
		}

		if vl, ok := trc["vl"].([]any); ok {
			globalPos := 0
			for _, blockRaw := range vl {
				block, ok := blockRaw.(map[string]any)
				if !ok {
					continue
				}
				placementLabel := "Feed / In-Article"
				if uip, ok := block["uip"].(string); ok && uip != "" {
					placementLabel = uip
				} else if ppb, ok := block["ppb"].(string); ok && ppb != "" {
					placementLabel = ppb
				}

				if itemsList, ok := block["v"].([]any); ok {
					blockIndex := 0
					for _, itemRaw := range itemsList {
						item, ok := itemRaw.(map[string]any)
						if !ok {
							continue
						}
						blockIndex++
						globalPos++

						c := parseSingleCard(item, placementLabel, pub, cleanPubDomain, device, globalPos, blockIndex, trcMeta)
						if c != nil {
							candidates = append(candidates, *c)
						}
					}
				}
			}
		}
	}

	// Recommendations REST API list format (root["list"])
	if listRaw, ok := root["list"].([]any); ok {
		placementLabel := "Recommendations Feed"
		if p, ok := root["placement"].(string); ok && p != "" {
			placementLabel = p
		}
		globalPos := 0
		for _, itemRaw := range listRaw {
			item, ok := itemRaw.(map[string]any)
			if !ok {
				continue
			}
			globalPos++
			c := parseSingleCard(item, placementLabel, pub, cleanPubDomain, device, globalPos, globalPos, trcMeta)
			if c != nil {
				candidates = append(candidates, *c)
			}
		}
	}

	return candidates, trcMeta, nil
}

func parseSingleCard(item map[string]any, placementLabel string, pub Publisher, cleanPubDomain, device string, globalPos, blockIdx int, trcMeta trcSessionMeta) *candidate {
	thumb := getString(item, "thumbnail")
	if thumb == "" {
		if allThumbs, ok := item["all-thumbnails"].([]any); ok && len(allThumbs) > 0 {
			if firstMap, ok := allThumbs[0].(map[string]any); ok {
				thumb = getString(firstMap, "url")
			} else if firstStr, ok := allThumbs[0].(string); ok {
				thumb = firstStr
			}
		}
	}

	// Cleaned as a browser would: some feeds serve links with a line break inside.
	clickURL := BrowserURL(getString(item, "url", "click_url"))
	title := CleanText(getString(item, "name", "title"))
	branding := CleanText(getString(item, "branding-text", "branding"))

	cand := candidate{
		Title:             title,
		Branding:          branding,
		ThumbnailURL:      thumb,
		ClickURL:          clickURL,
		Publisher:         pub.Name,
		PublisherDomain:   cleanPubDomain,
		Placement:         placementLabel,
		Device:            device,
		FeedPosition:      globalPos,
		BlockPosition:     blockIdx,
		TrcMeta:           trcMeta,
		Description:       CleanText(getString(item, "description", "summary")),
		CtaText:           CleanText(getString(item, "cta-text", "cta")),
		AdvertiserAccount: CleanText(getString(item, "publisher")),
		FormatType:        strings.ToLower(getString(item, "type")),
		Language:          getString(item, "lg"),
		Tat:               getString(item, "tat"),
		Sig:               getString(item, "sig"),
		Tblci:             getString(item, "tblci"),
		ThumbDimensions:   getString(item, "thumb-size"),
	}

	if cand.FormatType == "" {
		cand.FormatType = "text"
	}
	if cand.Language == "" {
		cand.Language = "en"
	}
	if cand.Tat == "" {
		cand.Tat = "TABOOLA"
	}

	if v, ok := item["duration"]; ok {
		switch dur := v.(type) {
		case float64:
			cand.VideoDuration = int(dur)
		case string:
			cand.VideoDuration, _ = strconv.Atoi(dur)
		}
	}

	if v, ok := item["ecpaPercentile"]; ok {
		switch ep := v.(type) {
		case float64:
			cand.EcpaPercentile = &ep
		case string:
			if f, err := strconv.ParseFloat(ep, 64); err == nil {
				cand.EcpaPercentile = &f
			}
		}
	}

	if v, ok := item["published-date"]; ok {
		switch pd := v.(type) {
		case float64:
			n := int64(pd)
			cand.PublishedDate = &n
		case string:
			if n, err := strconv.ParseInt(pd, 10, 64); err == nil {
				cand.PublishedDate = &n
			}
		}
	}

	if crop, ok := item["cropping"]; ok {
		if cropBytes, err := json.Marshal(crop); err == nil {
			cand.Cropping = cropBytes
		}
	}

	// Classify Ad: preserve genuine competitor direct-response offers
	cat, _ := ClassifyAd(cand)
	if cat != CategoryCompetitorOffer {
		return nil
	}

	// Extract OpenRTB auction telemetry
	cand.AuctionTelemetry = extractCardAuctionTelemetry(item, placementLabel, cleanPubDomain, pub.Name)

	return &cand
}

func getString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
