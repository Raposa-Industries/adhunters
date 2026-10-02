package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// who is the person every request comes from, as Cloudflare Access names
// them to the apps.
const who = "e2e@adhunters.test"

// accounts are the fake Taboola login's advertiser accounts.
var accounts = []string{"zoltagroup-1-sc", "zoltagroup-2-sc"}

// TestFirstCampaignPath walks one Spy ad through Create, the library,
// Launch and Intel, through the real binaries, and checks every Taboola
// request Launch sends against the owner's rules (2026-10-01): nothing is
// ever turned on, and every campaign has at most $20 a day and $20 in all.
func TestFirstCampaignPath(t *testing.T) {
	if os.Getenv("E2E") == "" {
		t.Skip("E2E not set: the path test runs in its own CI job (E2E=1 go test ./... in e2e/)")
	}
	bins := build(t)
	dbURL, db := database(t)
	ctx := context.Background()
	env := []string{"DATABASE_URL=" + dbURL}
	bin := func(name string) string { return filepath.Join(bins, name) }
	root := repoRoot(t)

	// The deploy's order: every schema migrated by its own service.
	run(t, bin("tracks-loader"), env, "migrate")
	run(t, bin("spy-numbers"), env, "migrate")
	run(t, bin("intel-numbers"), env, "migrate")

	taboola := newFakeTaboola(t, accounts...)
	openai := newFakeOpenAI(t)
	drive := newFakeDrive(t)
	data := t.TempDir()

	libAddr := freeAddr(t)
	start(t, bin("library"), freeAddr(t), append(env,
		"LIBRARY_ADDR="+libAddr,
		"LIBRARY_DRIVE_FOLDER="+driveRoot,
		"LIBRARY_GOOGLE_CLIENT_ID=fake-client",
		"LIBRARY_GOOGLE_CLIENT_SECRET=fake-secret",
		"LIBRARY_GOOGLE_TOKEN_URL="+drive.URL()+"/token",
		"LIBRARY_GOOGLE_API_URL="+drive.URL(),
	), "-drive-every=1s")
	// What library drive-login leaves behind once the folder's owner signs in.
	if _, err := db.Exec(ctx, `INSERT INTO library.drive_login (account, client_id, refresh_token)
		VALUES ('adhuntertech@gmail.test', 'fake-client', 'fake-refresh')`); err != nil {
		t.Fatal(err)
	}
	createAddr := freeAddr(t)
	start(t, bin("create"), freeAddr(t), append(env,
		"CREATE_ADDR="+createAddr,
		"CREATE_FILES=file://"+filepath.Join(data, "create-files"),
		"CREATE_KEEP_DIR="+filepath.Join(data, "create-kept"),
		"LIBRARY_URL=http://"+libAddr,
		"OPENAI_API_KEY=sk-fake",
		"OPENAI_BASE_URL="+openai.URL(),
	))
	launchAddr := freeAddr(t)
	// The box's settings since the owner's word of 2026-10-02: $500 a day,
	// no spending limit, new campaigns go up running.
	start(t, bin("launch-web"), freeAddr(t), append(env,
		fmt.Sprintf("TABOOLA_MAX_DAILY_CAP=%v", ceiling),
		"TABOOLA_MAX_SPEND_LIMIT=0",
		"TABOOLA_CREATE_ACTIVE=1",
		"LAUNCH_WEB_ADDR="+launchAddr,
		"LAUNCH_DATA_DIR="+filepath.Join(data, "launch"),
		"LAUNCH_LIBRARY_URL=http://"+libAddr,
		"TABOOLA_BASE_URL="+taboola.URL(),
		"TABOOLA_CLIENT_ID=fake-id",
		"TABOOLA_CLIENT_SECRET=fake-secret",
		"TABOOLA_ACCOUNTS="+strings.Join(accounts, ","),
	))
	create := "http://" + createAddr
	launch := "http://" + launchAddr

	// ---- 1. Spy: an ad Spy found, and its "Criar variações" link -------------
	var creative int64
	if err := db.QueryRow(ctx, `
		WITH b AS (INSERT INTO tracks.brand (name) VALUES ('Hearing Health Digest') RETURNING id),
		     c AS (INSERT INTO tracks.creative (creative_key, image_url, format_type, first_seen_at, last_seen_at)
		           VALUES ('e2e-ear.jpg', 'https://pictures.spy.invalid/e2e-ear.jpg', 'image', now() - interval '9 days', now())
		           RETURNING id),
		     a AS (INSERT INTO tracks.ad (creative_id, headline, brand_id, first_seen_at, last_seen_at)
		           SELECT c.id, 'Ringing In Your Ears? Try This Tonight', b.id, now() - interval '9 days', now() FROM c, b)
		SELECT id FROM c`).Scan(&creative); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO spy.creative_class (creative_id, category_id, vertical_id, confidence, source, rules_hash, input_ad_id, classified_at, needs_model)
		VALUES ($1, 'health', 'tinnitus', 0.9, 'rules', 'e2e', 0, now(), false)`, creative); err != nil {
		t.Fatal(err)
	}
	pageHas(t, root, "spy/web/pages/ad.js", `'/create/?from=spy&creative=' + encodeURIComponent(id)`, "Criar variações")
	pageHas(t, root, "create/internal/site/pages/static/app.js", "/create/api/spy/${creative}/session")
	res := call(t, "GET", create+"/create/?from=spy&creative="+strconv.FormatInt(creative, 10), nil, nil)
	if res != http.StatusOK {
		t.Fatalf("Create's page from Spy's link: %d", res)
	}

	// ---- 2. Create: a session from the ad, one turn, a save ------------------
	var spyAd struct {
		Ad struct {
			Headline   string `json:"headline"`
			VerticalID string `json:"vertical_id"`
		} `json:"ad"`
		VerticalName string `json:"vertical_name"`
	}
	mustCall(t, "GET", fmt.Sprintf("%s/create/api/spy/%d", create, creative), nil, &spyAd)
	if spyAd.Ad.VerticalID != "tinnitus" || spyAd.VerticalName == "" || spyAd.Ad.Headline != "Ringing In Your Ears? Try This Tonight" {
		t.Fatalf("Create read Spy's ad as %+v", spyAd)
	}
	var opened struct {
		Session struct {
			ID         int64  `json:"id"`
			VerticalID string `json:"vertical_id"`
		} `json:"session"`
		Picked  []int64 `json:"picked"`
		Warning string  `json:"warning"`
	}
	mustCall(t, "POST", fmt.Sprintf("%s/create/api/spy/%d/session", create, creative), map[string]any{}, &opened)
	sid := opened.Session.ID
	if sid == 0 || opened.Session.VerticalID != "tinnitus" || len(opened.Picked) == 0 {
		t.Fatalf("session from Spy: %+v", opened)
	}
	// Create fetches only public pictures, so the ad's picture comes in the
	// way the page offers when that fails: from the person's computer.
	if opened.Warning == "" {
		t.Logf("Spy's picture came in without a warning")
	}
	upload := uploadPicture(t, create+fmt.Sprintf("/create/api/sessions/%d/items", sid), picture(t, 1200, 628))
	picked := append(append([]int64(nil), opened.Picked...), upload)

	mustCall(t, "POST", fmt.Sprintf("%s/create/api/sessions/%d/turns", create, sid),
		map[string]any{"prompt": "Same ad, an older man at night", "picked": picked, "images": 2, "headlines": 3}, nil)
	var detail struct {
		Turns []struct {
			State string `json:"state"`
			Error string `json:"error"`
		} `json:"turns"`
		Items []struct {
			ID     int64  `json:"id"`
			TurnID *int64 `json:"turn_id"`
			Kind   string `json:"kind"`
			Text   string `json:"text"`
			State  string `json:"state"`
			Error  string `json:"error"`
		} `json:"items"`
		Saves []struct {
			State        string `json:"state"`
			Error        string `json:"error"`
			LibrarySetID *int64 `json:"library_set_id"`
		} `json:"saves"`
	}
	var made []int64
	eventually(t, "Create's turn to finish", func() (bool, string) {
		mustCall(t, "GET", fmt.Sprintf("%s/create/api/sessions/%d", create, sid), nil, &detail)
		made = made[:0]
		var states []string
		for _, it := range detail.Items {
			if it.TurnID == nil {
				continue
			}
			states = append(states, it.Kind+":"+it.State+" "+it.Error)
			if it.State == "done" {
				made = append(made, it.ID)
			}
		}
		return len(made) == 5, strings.Join(states, ", ")
	})
	if openai.count("/v1/chat/completions") == 0 || openai.count("/v1/images/generations")+openai.count("/v1/images/edits") != 2 {
		t.Errorf("OpenAI calls: %v", openai.calls)
	}
	mustCall(t, "POST", fmt.Sprintf("%s/create/api/sessions/%d/saves", create, sid), map[string]any{"item_ids": made, "ai_label": "ai"}, nil)
	var setID int64
	eventually(t, "Create's save to reach the library", func() (bool, string) {
		mustCall(t, "GET", fmt.Sprintf("%s/create/api/sessions/%d", create, sid), nil, &detail)
		if len(detail.Saves) == 0 {
			return false, "no save"
		}
		s := detail.Saves[len(detail.Saves)-1]
		if s.LibrarySetID != nil && s.State == "done" {
			setID = *s.LibrarySetID
			return true, ""
		}
		return false, s.State + " " + s.Error
	})
	pageHas(t, root, "create/internal/site/pages/static/app.js", "/launch/new?set=")
	pageHas(t, root, "launch/web/pages/newpair.js", "q.get('set')")

	// ---- 3. Launch: the set from the library, a paused pair ------------------
	var set struct {
		Set struct {
			Name       string `json:"name"`
			VerticalID string `json:"vertical_id"`
		} `json:"set"`
		Creatives []struct {
			ID         int64  `json:"id"`
			SHA256     string `json:"sha256"`
			AILabel    string `json:"ai_label"`
			DriveState string `json:"drive_state"`
		} `json:"creatives"`
		Headlines []struct {
			Text string `json:"text"`
		} `json:"headlines"`
	}
	// The library's Drive pass uploads the saved pictures into the folder,
	// under the vertical and the session's set.
	eventually(t, "the saved pictures to reach Drive", func() (bool, string) {
		mustCall(t, "GET", fmt.Sprintf("%s/launch/api/library/set?id=%d", launch, setID), nil, &set)
		var states []string
		for _, c := range set.Creatives {
			if c.DriveState != "in_drive" {
				states = append(states, c.DriveState)
			}
		}
		return len(set.Creatives) > 0 && len(states) == 0, strings.Join(states, ", ")
	})
	inDrive := drive.stored()
	for _, c := range set.Creatives {
		path, ok := inDrive[c.SHA256]
		if !ok {
			t.Errorf("creative %d (sha %s) is not in Drive; Drive has %v", c.ID, c.SHA256, inDrive)
		} else if !strings.HasPrefix(path, "StepNutra / ") || !strings.Contains(path, " / "+set.Set.Name+" / ") {
			t.Errorf("creative %d is at %q in Drive, want under StepNutra and the set %q", c.ID, path, set.Set.Name)
		}
	}
	var waiting int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM library.creative WHERE pending IS NOT NULL`).Scan(&waiting); err != nil || waiting != 0 {
		t.Errorf("%d creatives still hold their bytes in the library after the Drive pass (%v)", waiting, err)
	}
	if set.Set.VerticalID != "tinnitus" || len(set.Creatives) != 2 || len(set.Headlines) != 3 {
		t.Fatalf("Launch read the library set as %+v", set)
	}
	var ads []map[string]any
	team := teamDefaults(t, root)
	for i, c := range set.Creatives {
		if c.AILabel != "ai" {
			t.Errorf("creative %d lost its AI label: %q", c.ID, c.AILabel)
		}
		var used struct {
			Image struct {
				SHA string `json:"sha256"`
			} `json:"image"`
		}
		// Only Drive has the picture now: Launch's copy comes from there.
		mustCall(t, "POST", fmt.Sprintf("%s/launch/api/library/use?id=%d", launch, c.ID), map[string]any{}, &used)
		if used.Image.SHA != c.SHA256 {
			t.Fatalf("library picture %d arrived as %q, not %q", c.ID, used.Image.SHA, c.SHA256)
		}
		for j, h := range set.Headlines {
			if (i+j)%2 == 0 || len(ads) < 3 {
				ads = append(ads, map[string]any{"title": h.Text, "url": "https://offer.example.com/lp", "image": used.Image.SHA,
					"cta": "LEARN_MORE", "ad_id": fmt.Sprintf("ah-%s-%d", used.Image.SHA[:10], j), "ai": true})
			}
		}
	}
	settings := map[string]any{
		"brand": "Hearing Daily", "bid_strategy": team.BidStrategy, "objective": team.Objective,
		"daily_cap": team.DailyCap, "spending_limit": team.SpendingLimit, "countries": []string{"US"},
		"exclude_cities": team.ExcludeCities, "ad_delivery": team.AdDelivery, "tracking_code": team.TrackingCode,
	}
	var next struct {
		Group   string `json:"group"`
		Desktop string `json:"desktop"`
		Mobile  string `json:"mobile"`
	}
	mustCall(t, "GET", launch+"/launch/api/taboola/"+accounts[0]+"/next", nil, &next)
	// The mobile half has its own daily budget and start (the page's
	// "Mobile com valores próprios"); everything else is the desktop's.
	mobile := map[string]any{}
	for k, v := range settings {
		mobile[k] = v
	}
	mobileCap := team.DailyCap / 2
	mobile["daily_cap"], mobile["start_date"] = mobileCap, "2030-01-05"
	pair := map[string]any{
		"network": "taboola", "account": accounts[0], "devices": "both", "group_id": "",
		"new_group": map[string]any{"name": "", "budget": 0, "budget_model": "", "objective": team.Objective},
		"settings":  settings, "mobile": mobile, "ads": ads, "key": "e2e-first-pair",
	}
	result := sendPair(t, launch, pair)
	if result.Result != "done" || result.Desktop == nil || result.Mobile == nil {
		t.Fatalf("pair not made: %+v", result)
	}
	if result.Desktop.Campaign.Name != next.Desktop || result.Mobile.Campaign.Name != next.Mobile {
		t.Errorf("campaign names %q / %q, Launch said next would be %q / %q", result.Desktop.Campaign.Name, result.Mobile.Campaign.Name, next.Desktop, next.Mobile)
	}

	// ---- 4. The rules, on every request Launch sent to Taboola ---------------
	reqs := taboola.requests()
	var groups, camps, massAds int
	platforms := map[string]bool{}
	for _, r := range reqs {
		b, _ := r.Body.(map[string]any)
		switch {
		case r.Method == "POST" && r.Path == accounts[0]+"/campaigns_group/":
			groups++
			// No end date sent: Taboola's default, 9999-12-31, is no end.
			if _, ok := b["end_date"]; ok {
				t.Errorf("group sent with an end date: %v", b["end_date"])
			}
		case r.Method == "POST" && r.Path == accounts[0]+"/campaigns/":
			camps++
			if g, _ := b["campaign_group_id"].(string); g != result.GroupID || g == "" {
				t.Errorf("campaign %v sent to group %q, not the new group %q", b["name"], g, result.GroupID)
			}
			pt, _ := b["platform_targeting"].(map[string]any)
			vals, _ := pt["value"].([]any)
			platforms[fmt.Sprint(vals)] = true
			// The mobile campaign carries its own daily budget and start.
			wantCap, wantStart := team.DailyCap, any(nil)
			if fmt.Sprint(vals) == "[PHON TBLT]" {
				wantCap, wantStart = mobileCap, "2030-01-05"
			}
			if b["daily_cap"] != wantCap || b["start_date"] != wantStart {
				t.Errorf("campaign %v (%v): daily cap %v, start %v; want %v and %v", b["name"], vals, b["daily_cap"], b["start_date"], wantCap, wantStart)
			}
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/items/mass"):
			coll, _ := b["collection"].([]any)
			massAds += len(coll)
		}
	}
	if groups != 1 || camps != 2 || massAds != 2*len(ads) {
		t.Errorf("Launch sent %d groups, %d campaigns, %d ads; want 1, 2 and %d", groups, camps, massAds, 2*len(ads))
	}
	if !platforms["[DESK]"] || !platforms["[PHON TBLT]"] {
		t.Errorf("devices: %v, want one desktop (DESK) and one mobile (PHON and TBLT) campaign", platforms)
	}

	// Asking for more than the ceilings is refused before anything reaches
	// Taboola: a daily cap over $500, or a typed total over 30 daily caps.
	before := len(taboola.requests())
	for _, over := range []map[string]any{{"daily_cap": ceiling + 1}, {"spending_limit": 30*ceiling + 1}, {"spending_limit": 0.0, "daily_cap": ceiling + 0.5}} {
		s := map[string]any{}
		for k, v := range settings {
			s[k] = v
		}
		for k, v := range over {
			s[k] = v
		}
		p := map[string]any{"network": "taboola", "account": accounts[0], "devices": "desktop", "group_id": result.GroupID,
			"name": "over the ceiling", "settings": s, "ads": ads[:1], "key": fmt.Sprint("over-", over)}
		if r := sendPair(t, launch, p); r.Result == "done" {
			t.Errorf("a campaign with %v was made", over)
		}
	}
	// A cap raise on a made campaign too (Launch's change, as Intel's and
	// Desk's links open it).
	var changed struct {
		Done []struct {
			Error string `json:"error"`
		} `json:"done"`
	}
	if code := call(t, "POST", launch+"/launch/api/taboola/"+accounts[0]+"/change",
		map[string]any{"campaigns": []string{result.Desktop.Campaign.ID}, "change": map[string]any{"daily_cap": ceiling + 100}}, &changed); code == http.StatusOK &&
		(len(changed.Done) == 0 || changed.Done[0].Error == "") {
		t.Errorf("a daily cap over $%v was accepted: %+v", ceiling, changed)
	}
	for _, r := range taboola.requests()[before:] {
		if r.Method != "GET" {
			t.Errorf("a refused ask still reached Taboola: %s %s %s", r.Method, r.Path, r.Raw)
		}
	}

	// The rules hold on everything sent, the refused asks included.
	checkRules(t, taboola.requests(), team)

	// ---- 5. Intel: RedTrack's and Taboola's numbers in Launch's tables -------
	// RedTrack reports by the tracking code's sub slots. Fill them the way a
	// click does, from the code Launch sent, so Intel only joins when Launch's
	// code and Intel agree on which sub carries which id.
	code := sentTrackingCode(t, reqs)
	itemID := func(m *made1) string {
		if len(m.Ads) == 0 {
			t.Fatalf("campaign %s has no ads in Launch's answer", m.Campaign.ID)
		}
		return m.Ads[0].ID
	}
	seedIntel(t, db, accounts[0], result.GroupID, []seedCampaign{
		{ID: result.Desktop.Campaign.ID, Item: itemID(result.Desktop), Spent: 14.5, Sales: 3, Revenue: 147},
		{ID: result.Mobile.Campaign.ID, Item: itemID(result.Mobile), Spent: 9.25, Sales: 0, Revenue: 0},
	}, code)
	run(t, bin("intel-numbers"), env, "once")

	var numbers struct {
		Available bool `json:"available"`
		Campaigns map[string]struct {
			Spent   float64 `json:"spent"`
			Sales   float64 `json:"sales"`
			Revenue float64 `json:"revenue"`
			Profit  float64 `json:"profit"`
		} `json:"campaigns"`
		Ads map[string]struct {
			Spent float64 `json:"spent"`
			Sales float64 `json:"sales"`
			Word  string  `json:"word"`
		} `json:"ads"`
	}
	mustCall(t, "GET", launch+"/launch/api/numbers?window=today&accounts="+accounts[0], nil, &numbers)
	d, m := numbers.Campaigns[result.Desktop.Campaign.ID], numbers.Campaigns[result.Mobile.Campaign.ID]
	switch {
	case !numbers.Available:
		t.Errorf("Launch says Intel's views are not there")
	case d.Spent != 14.5 || d.Sales != 3 || d.Revenue != 147 || d.Profit != 132.5:
		t.Errorf("desktop campaign in Launch's table: %+v; want spent 14.5, 3 sales, revenue 147, profit 132.5 (sub slots: %s)", d, code)
	case m.Spent != 9.25 || m.Sales != 0:
		t.Errorf("mobile campaign in Launch's table: %+v", m)
	}
	if a := numbers.Ads[itemID(result.Desktop)]; a.Spent != 14.5 || a.Sales != 3 || a.Word == "" {
		t.Errorf("desktop ad in Launch's table: %+v", a)
	}
}

// made1 is one campaign of a pair, as Launch's job answers it.
type made1 struct {
	Campaign struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"campaign"`
	Ads []struct {
		ID string `json:"id"`
	} `json:"ads"`
}

type pairResult struct {
	GroupID  string   `json:"group_id"`
	Desktop  *made1   `json:"desktop"`
	Mobile   *made1   `json:"mobile"`
	Problems []string `json:"problems"`
	Result   string   `json:"result"`
}

// sendPair sends a new pair the way the page does and waits for the job.
func sendPair(t *testing.T, launch string, body map[string]any) pairResult {
	t.Helper()
	var job struct {
		ID     string      `json:"id"`
		Done   bool        `json:"done"`
		Error  string      `json:"error"`
		Result *pairResult `json:"result"`
	}
	code := call(t, "POST", launch+"/launch/api/pairs", body, &job)
	if code == http.StatusBadRequest {
		return pairResult{Result: "refused"}
	}
	if code != http.StatusAccepted && code != http.StatusOK {
		t.Fatalf("POST pairs: %d", code)
	}
	eventually(t, "Launch's send to finish", func() (bool, string) {
		if !job.Done {
			mustCall(t, "GET", launch+"/launch/api/jobs/"+job.ID, nil, &job)
		}
		return job.Done, ""
	})
	if job.Result == nil {
		return pairResult{Result: "failed", Problems: []string{job.Error}}
	}
	if job.Error != "" {
		job.Result.Problems = append(job.Result.Problems, job.Error)
	}
	return *job.Result
}

// team is the new campaign defaults Launch's page fills in (TEAM in
// launch/web/pages/presets.js), read from the page so a change there is
// tested here.
type team struct {
	Objective, BidStrategy, AdDelivery, TrackingCode string
	DailyCap, SpendingLimit                          float64
	ExcludeCities                                    []string
}

func teamDefaults(t *testing.T, root string) team {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "launch/web/pages/presets.js"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "export const TEAM = {")
	if i < 0 {
		t.Fatal("presets.js has no TEAM")
	}
	src = src[i:]
	src = src[:strings.Index(src, "\n};")]
	str := func(k string) string {
		m := regexp.MustCompile(`\n\s*` + k + `: '([^']*)'`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("TEAM.%s not found", k)
		}
		return m[1]
	}
	num := func(k string) float64 {
		m := regexp.MustCompile(`\n\s*` + k + `: ([0-9.]+)`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("TEAM.%s not found", k)
		}
		f, _ := strconv.ParseFloat(m[1], 64)
		return f
	}
	tm := team{Objective: str("objective"), BidStrategy: str("bid_strategy"), AdDelivery: str("ad_delivery"),
		TrackingCode: str("tracking_code"), DailyCap: num("daily_cap"), SpendingLimit: num("spending_limit")}
	if m := regexp.MustCompile(`exclude_cities: \[([^\]]*)\]`).FindStringSubmatch(src); m != nil {
		for _, c := range strings.Split(m[1], ",") {
			if c = strings.Trim(strings.TrimSpace(c), "'"); c != "" {
				tm.ExcludeCities = append(tm.ExcludeCities, c)
			}
		}
	}
	return tm
}

// pageHas checks a page's code still holds the link the next app expects.
func pageHas(t *testing.T, root, file string, wants ...string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wants {
		if !bytes.Contains(b, []byte(w)) {
			t.Errorf("%s no longer has %q: the link between the apps changed; update both sides and this test", file, w)
		}
	}
}

// call sends JSON (when body is not nil) as the signed-in person and
// decodes the answer into out; it returns the status.
func call(t *testing.T, method, u string, body, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Cf-Access-Authenticated-User-Email", who)
	c := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, u, err, raw)
		}
	}
	if res.StatusCode >= 300 {
		t.Logf("%s %s: %d %s", method, pathOf(u), res.StatusCode, bytes.TrimSpace(raw))
	}
	return res.StatusCode
}

// mustCall is call that fails the test on anything but a 2xx.
func mustCall(t *testing.T, method, u string, body, out any) {
	t.Helper()
	if code := call(t, method, u, body, out); code < 200 || code > 299 {
		t.Fatalf("%s %s: %d", method, pathOf(u), code)
	}
}

func pathOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	return p.RequestURI()
}

// uploadPicture adds a picture to a Create session as the page's
// "+ Imagem do computador" does, and returns the item's id.
func uploadPicture(t *testing.T, u string, pic []byte) int64 {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "spy-ad.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(pic)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", u, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Cf-Access-Authenticated-User-Email", who)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var it struct {
		ID int64 `json:"id"`
	}
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated || json.Unmarshal(raw, &it) != nil || it.ID == 0 {
		t.Fatalf("upload to Create: %d %s", res.StatusCode, raw)
	}
	return it.ID
}

// eventually polls until ok, failing after two minutes with the last note.
func eventually(t *testing.T, what string, ok func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		done, note := ok()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited 2 min for %s: %s", what, note)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
