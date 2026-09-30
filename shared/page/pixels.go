package page

import (
	"regexp"
	"strings"
)

var (
	rxFB = regexp.MustCompile(`fbq\(['"]init['"],\s*['"]([0-9]+)['"]`)
	// Only real Google id formats (same as spy.clue_from_legacy in 015). The old
	// loose pattern also caught pieces of click ids and random page text.
	rxGoogle  = regexp.MustCompile(`\b(AW-[0-9]{9,11}|G-[A-Z0-9]{8,12}|UA-[0-9]{6,10}-[0-9]{1,3}|GTM-[A-Z0-9]{5,8})\b`)
	rxTikTok  = regexp.MustCompile(`ttq\.load\(['"]([0-9a-zA-Z]+)['"]`)
	rxSnap    = regexp.MustCompile(`snaptr\(['"]init['"],\s*['"]([0-9a-zA-Z_-]+)['"]`)
	rxPint    = regexp.MustCompile(`pintrk\(['"]load['"],\s*['"]([0-9a-zA-Z]+)['"]`)
	rxClarity = regexp.MustCompile(`clarity\.ms/tag/([0-9a-zA-Z]+)`)
	// NewsBreak pixel: nbpix('init', 'ID-<NewsBreak account id>').
	rxNewsBreak = regexp.MustCompile(`nbpix\(\s*['"]init['"]\s*,\s*['"]ID-([0-9]{15,20})['"]`)
)

func ExtractPixels(htmlContent string) map[string]any {
	pixels := make(map[string]any)
	lower := strings.ToLower(htmlContent)

	// Facebook / Meta
	var fbIDs []string
	for _, m := range rxFB.FindAllStringSubmatch(htmlContent, -1) {
		if len(m) > 1 {
			fbIDs = append(fbIDs, m[1])
		}
	}
	if len(fbIDs) > 0 || strings.Contains(lower, "connect.facebook.net") {
		pixels["facebook"] = map[string]any{
			"detected": true,
			"ids":      fbIDs,
		}
	}

	// Google Ads / GA4
	var gIDs []string
	seenG := map[string]bool{}
	for _, m := range rxGoogle.FindAllStringSubmatch(htmlContent, -1) {
		if len(m) > 1 && !seenG[m[1]] {
			seenG[m[1]] = true
			gIDs = append(gIDs, m[1])
		}
	}
	if len(gIDs) > 0 || strings.Contains(lower, "googletagmanager.com") {
		pixels["google"] = map[string]any{
			"detected": true,
			"ids":      gIDs,
		}
	}

	// TikTok
	var ttIDs []string
	for _, m := range rxTikTok.FindAllStringSubmatch(htmlContent, -1) {
		if len(m) > 1 {
			ttIDs = append(ttIDs, m[1])
		}
	}
	if len(ttIDs) > 0 || strings.Contains(lower, "analytics.tiktok.com") {
		pixels["tiktok"] = map[string]any{
			"detected": true,
			"ids":      ttIDs,
		}
	}

	// Snapchat
	var snapIDs []string
	for _, m := range rxSnap.FindAllStringSubmatch(htmlContent, -1) {
		if len(m) > 1 {
			snapIDs = append(snapIDs, m[1])
		}
	}
	if len(snapIDs) > 0 || strings.Contains(lower, "sc-static.net/scevent.min.js") {
		pixels["snapchat"] = map[string]any{
			"detected": true,
			"ids":      snapIDs,
		}
	}

	// Pinterest
	var pintIDs []string
	for _, m := range rxPint.FindAllStringSubmatch(htmlContent, -1) {
		if len(m) > 1 {
			pintIDs = append(pintIDs, m[1])
		}
	}
	if len(pintIDs) > 0 || strings.Contains(lower, "pintrk") {
		pixels["pinterest"] = map[string]any{
			"detected": true,
			"ids":      pintIDs,
		}
	}

	// Clarity
	var clarityIDs []string
	for _, m := range rxClarity.FindAllStringSubmatch(htmlContent, -1) {
		if len(m) > 1 {
			clarityIDs = append(clarityIDs, m[1])
		}
	}
	if len(clarityIDs) > 0 || strings.Contains(lower, "clarity.ms") {
		pixels["clarity"] = map[string]any{
			"detected": true,
			"ids":      clarityIDs,
		}
	}

	// Microsoft UET
	if strings.Contains(lower, "bat.bing.com") || strings.Contains(lower, "uetq") {
		pixels["microsoft_uet"] = map[string]any{"detected": true}
	}

	// NewsBreak. The id is a NewsBreak account id.
	var nbIDs []string
	seenNB := map[string]bool{}
	for _, m := range rxNewsBreak.FindAllStringSubmatch(htmlContent, -1) {
		if !seenNB[m[1]] {
			seenNB[m[1]] = true
			nbIDs = append(nbIDs, m[1])
		}
	}
	if len(nbIDs) > 0 || strings.Contains(lower, "static.newsbreak.com/pixel") {
		pixels["newsbreak"] = map[string]any{
			"detected": true,
			"ids":      nbIDs,
		}
	}

	// Ad trackers loaded on the page itself.
	if strings.Contains(lower, "rdtk.io") || strings.Contains(lower, "redtrack") || strings.Contains(lower, "rtkcid") {
		pixels["redtrack"] = map[string]any{"detected": true}
	}
	if strings.Contains(lower, "voluum") {
		pixels["voluum"] = map[string]any{"detected": true}
	}

	// Taboola Pixel
	if strings.Contains(lower, "trc.taboola.com") || strings.Contains(lower, "_tb_v") {
		pixels["taboola"] = map[string]any{"detected": true}
	}

	// Outbrain
	if strings.Contains(lower, "outbrain.com") || strings.Contains(lower, "obapi") {
		pixels["outbrain"] = map[string]any{"detected": true}
	}

	return pixels
}
