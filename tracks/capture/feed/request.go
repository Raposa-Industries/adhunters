package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
)

// Request is one scrape's request, with the body kept so it can be written
// to the raw file next to the answer.
type Request struct {
	HTTP *http.Request
	Body []byte // POST body; nil for GET
}

// Build returns the request for one scrape of t as device.
func Build(ctx context.Context, t Target, device string) (Request, error) {
	switch t.NetworkName() {
	case Taboola:
		return taboola(ctx, t, device)
	case NewsBreak:
		return newsbreak(ctx, t, device)
	}
	return Request{}, fmt.Errorf("unknown network %q", t.Network)
}

func isMobile(device string) bool {
	return device == "phone" || device == "mobile" || device == "android"
}

// Taboola's recommendations endpoint, asked the way a publisher page does.

type taboolaData struct {
	II  string        `json:"ii"`
	IT  string        `json:"it"`
	U   string        `json:"u"`
	Uad taboolaUad    `json:"uad"`
	R   []taboolaSlot `json:"r"`
}

type taboolaUad struct {
	Mobile   bool   `json:"mobile"`
	Platform string `json:"platform"`
}

type taboolaSlot struct {
	LI  string `json:"li"`
	UIP string `json:"uip"`
	S   int    `json:"s"`
}

func taboola(ctx context.Context, t Target, device string) (Request, error) {
	mobile := isMobile(device)
	platform := "Windows"
	if mobile {
		platform = "iOS"
		if device == "android" {
			platform = "Android"
		}
	}
	batch := t.BatchSize
	if batch <= 0 {
		batch = 30
	}
	placement := t.Placement
	if placement == "" {
		placement = "rbox-t2m"
	}
	path := t.Path
	if path == "" {
		path = "/"
	}
	itemType := t.ItemType
	if itemType == "" {
		itemType = "text"
	}
	uip := [3]string{"Below Article", "Right Rail", "Feed"}
	if mobile {
		uip = [3]string{"Mobile Feed", "Mobile Stream", "Mobile Below Article"}
	}
	data, err := json.Marshal(taboolaData{
		II:  path,
		IT:  itemType,
		U:   t.URL,
		Uad: taboolaUad{Mobile: mobile, Platform: platform},
		R: []taboolaSlot{
			{LI: placement, UIP: uip[0], S: batch},
			{LI: placement, UIP: uip[1], S: 25},
			{LI: placement, UIP: uip[2], S: 20},
		},
	})
	if err != nil {
		return Request{}, err
	}
	endpoint := fmt.Sprintf("https://trc.taboola.com/%s/trc/3/json?llvl=2&pubit=i&t=1&data=%s",
		t.TaboolaAccount, url.QueryEscape(string(data)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Request{}, err
	}
	req.Header.Set("User-Agent", UserAgent(device))
	req.Header.Set("Referer", t.URL)
	req.Header.Set("Accept", "*/*")
	return Request{HTTP: req}, nil
}

// NewsBreak Ads is NewsBreak's own ad network. Its web SDK (mspai.js) asks a
// Prebid Server for one ad per slot; the winning "msp_nova" bid carries the ad.
const (
	newsbreakAuctionURL = "https://prebid-server.newsbreak.com/openrtb2/auction"
	// Public account token from newsbreak.com's page code (mspai init).
	newsbreakToken = "75392df6-fb96-4337-b33f-c54721451f20"
)

// Slots newsbreak.com fills on its own pages, used when a target lists none.
var newsbreakDefaultGroups = [][]string{
	{
		"psm-pmi-nbcom-web-articleinside-prod",
		"psm-pmi-nbcom-web-articlebottom-prod",
		"psm-pmi-nbcom-web-articleright-prod",
		"psm-pmi-nbcom-web-articlerelated-prod",
		"psm-pmi-nbcom-web-foryoularge-prod",
	},
	{
		"nbnlv-web-articleinfeed-prod",
		"nbnlv-web-articleinside-prod",
		"nbnlv-web-articlerelated-prod",
		"nbnlv-web-left-prod",
		"nbnlv-web-right-prod",
		"nbnlv-web-tier1-prod",
		"nbnlv-web-tier2-prod",
		"nbnlv-web-tier3-prod",
		"nbnlv-web-tier4-prod",
	},
}

func newsbreak(ctx context.Context, t Target, device string) (Request, error) {
	placements := t.Placements
	groups := t.PlacementGroups
	if len(placements) == 0 && len(groups) == 0 {
		groups = newsbreakDefaultGroups
	}
	if len(groups) > 0 {
		placements = groups[rand.IntN(len(groups))]
	}
	body, err := newsbreakBody(t, device, placements)
	if err != nil {
		return Request{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, newsbreakAuctionURL, bytes.NewReader(body))
	if err != nil {
		return Request{}, err
	}
	page := t.URL
	if page == "" {
		page = "https://www.newsbreak.com/"
	}
	req.Header.Set("User-Agent", UserAgent(device))
	req.Header.Set("Content-Type", "text/plain;charset=utf-8")
	req.Header.Set("Origin", "https://www.newsbreak.com")
	req.Header.Set("Referer", page)
	req.Header.Set("Accept", "*/*")
	return Request{HTTP: req, Body: body}, nil
}

// newsbreakBody builds the OpenRTB auction body the way mspai.js does: the
// account token as the stored request, one native slot per placement.
func newsbreakBody(t Target, device string, placements []string) ([]byte, error) {
	dev := map[string]any{
		"ua": UserAgent(device), "language": "en",
		"w": 1920, "h": 1080, "os": "Windows", "osv": "10", "devicetype": 2,
	}
	if device == "phone" || device == "mobile" {
		dev = map[string]any{
			"ua": UserAgent(device), "language": "en",
			"w": 390, "h": 844, "os": "iOS", "osv": "17.5", "make": "Apple", "model": "iPhone", "devicetype": 1,
		}
	}
	native, err := json.Marshal(map[string]any{
		"ver": "1.2",
		"assets": []map[string]any{
			{"id": 0, "required": 1, "title": map[string]any{"len": 140}},
			{"id": 1, "required": 1, "img": map[string]any{"type": 3, "w": 1200, "h": 627}},
			{"id": 2, "required": 0, "data": map[string]any{"type": 1}},
		},
	})
	if err != nil {
		return nil, err
	}
	imps := make([]map[string]any, 0, len(placements))
	for i, pl := range placements {
		imps = append(imps, map[string]any{
			"id":     "imp" + strconv.Itoa(i),
			"secure": 1,
			"native": map[string]any{"request": string(native), "ver": "1.2"},
			"ext": map[string]any{
				"gpid":    pl,
				"prebid":  map[string]any{"storedrequest": map[string]any{"id": pl}},
				"context": map[string]any{"data": map[string][]string{"orgID": {"0"}, "appID": {"3"}, "enableNovaVideo": {"true"}}},
			},
		})
	}
	page := t.URL
	if page == "" {
		page = "https://www.newsbreak.com/"
	}
	// The ad server answers a numeric NewsBreak user id and ignores other
	// shapes, so each scrape poses as a new numeric user.
	userID := strconv.FormatInt(1_000_000_000+rand.Int64N(9_000_000_000), 10)
	return json.Marshal(map[string]any{
		"id":     uuid.NewString(),
		"imp":    imps,
		"source": map[string]any{"tid": uuid.NewString()},
		"site":   map[string]any{"page": page, "domain": "www.newsbreak.com", "publisher": map[string]any{"domain": "newsbreak.com"}},
		"device": dev,
		"user":   map[string]any{"id": userID},
		"tmax":   3000,
		"cur":    []string{"USD"},
		"ext": map[string]any{
			"prebid": map[string]any{
				"storedrequest": map[string]any{"id": newsbreakToken},
				"channel":       map[string]any{"name": "pbjs", "version": "9.37.0"},
				"targeting":     map[string]any{"includebidderkeys": true, "includewinners": true},
			},
		},
	})
}

var desktopAgents = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:135.0) Gecko/20100101 Firefox/135.0",
}

var mobileAgents = []string{
	"Mozilla/5.0 (iPhone; CPU iPhone OS 18_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 15; Pixel 9 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_7 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/134.0.0.0 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 14; SM-S928B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Mobile Safari/537.36",
}

// UserAgent picks a browser user agent for the device.
func UserAgent(device string) string {
	if device == "mobile" || device == "phone" || device == "tablet" {
		return mobileAgents[rand.IntN(len(mobileAgents))]
	}
	return desktopAgents[rand.IntN(len(desktopAgents))]
}
