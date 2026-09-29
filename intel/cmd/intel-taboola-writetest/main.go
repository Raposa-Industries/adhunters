// Command intel-taboola-writetest runs the approved Taboola write tests
// (research/taboola-api/write-test-plan.md in the project files) through the
// guarded write client, intel/taboola/act.
//
//	intel-taboola-writetest paused     -account X -url URL -out DIR   # T1 to T11, campaigns never turned on
//	intel-taboola-writetest live-start -account X -url URL -out DIR   # T12: one paused campaign, two items
//	intel-taboola-writetest live-on    -account X -out DIR            # T12: once approved, turn it on ($20 total at most)
//	intel-taboola-writetest live-cut   -account X -out DIR            # T12: pause one of its two items
//	intel-taboola-writetest live-pause -account X -out DIR            # T12: pause the campaign, keep everything
//	intel-taboola-writetest live-resume -account X -out DIR           # T12: turn a paused campaign back on
//	intel-taboola-writetest live-end   -account X -out DIR            # T12: pause, read reports, delete
//	intel-taboola-writetest cleanup    -account X -out DIR            # delete everything the tests made
//	intel-taboola-writetest purge      -account X -out DIR [-groups a,b] # delete items and AutoGen groups left by our deleted campaigns
//
// Every request and answer is saved under DIR/raw before it is read, and
// what each test showed goes to DIR/results.md. DIR/state.json lists what
// the tests created; the client refuses to touch anything else. The money
// ceiling is $20 for all campaigns ever turned on, across every run that
// shares the state file.
//
// Credentials come from TABOOLA_CLIENT_ID and TABOOLA_CLIENT_SECRET.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/taboola/act"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/run"
)

var version = "dev"

const (
	prefix   = "AH-TEST"
	maxMoney = 20.0
	maxCPC   = 0.5
	bid      = 0.10
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	account := fs.String("account", "", "advertiser account (not the network account)")
	landing := fs.String("url", "", "landing page for the test items")
	out := fs.String("out", "", "directory for raw answers, state.json and results.md")
	sites := fs.String("sites", "", "two site names to block and unblock in T8, comma-separated")
	brand := fs.String("brand", "", "live-start: the brand shown on the ads (a real advertiser name)")
	imagePath := fs.String("image", "", "live-start: photo for the ads (JPEG or PNG the owner has rights to)")
	aiImage := fs.Bool("ai", false, "live-start: the photo is AI-made, so the ads carry Taboola's AI label")
	titles := fs.String("titles", "", "live-start: the two headlines, separated by |, matching the landing page")
	tracking := fs.String("tracking", "", "campaign tracking code (query string with Taboola macros such as {campaign_id}) that Taboola appends to every item URL")
	groups := fs.String("groups", "", "cleanup, purge: more AutoGen campaign group ids to delete, comma-separated (for campaigns made before groups were recorded)")
	fs.Parse(os.Args[2:])
	if *account == "" || *out == "" {
		usage()
	}
	base := os.Getenv("TABOOLA_BASE")
	if base == "" {
		base = "https://backstage.taboola.com"
	}
	if err := os.MkdirAll(filepath.Join(*out, "raw"), 0o750); err != nil {
		fail(err)
	}
	c, err := act.New(base, os.Getenv("TABOOLA_CLIENT_ID"), os.Getenv("TABOOLA_CLIENT_SECRET"), act.Guard{
		Account: *account, NamePrefix: prefix, MaxCPC: maxCPC, MaxMoney: maxMoney,
		StateFile: filepath.Join(*out, "state.json"),
	})
	if err != nil {
		fail(err)
	}
	t := &tester{c: c, dir: *out, account: *account, url: *landing, tracking: *tracking, sites: splitList(*sites),
		brand: *brand, imagePath: *imagePath, aiImage: *aiImage, titles: splitBar(*titles), groups: splitList(*groups), wait: 10 * time.Second}
	// Carry on the raw file numbers of earlier runs sharing -out.
	if old, err := os.ReadDir(filepath.Join(*out, "raw")); err == nil {
		t.n = len(old)
	}
	c.Record = t.save
	log := logx.New("intel-taboola-writetest", version)
	err = run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		defer t.writeResults(cmd)
		switch cmd {
		case "paused":
			return t.paused(ctx)
		case "live-start":
			return t.liveStart(ctx)
		case "live-on":
			return t.liveOn(ctx)
		case "live-cut":
			return t.liveCut(ctx)
		case "live-pause":
			return t.livePause(ctx)
		case "live-resume":
			return t.liveResume(ctx)
		case "live-end":
			return t.liveEnd(ctx)
		case "cleanup":
			return t.cleanup(ctx)
		case "purge":
			return t.purgeDeleted(ctx)
		}
		usage()
		return nil
	})
	if err != nil {
		fail(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: intel-taboola-writetest paused|live-start|live-on|live-cut|live-pause|live-resume|live-end|cleanup|purge -account X -out DIR [-url URL] [-sites a,b]")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "intel-taboola-writetest:", err)
	os.Exit(1)
}

type tester struct {
	c        *act.Client
	dir      string
	account  string
	url      string
	tracking string
	sites    []string
	// live-start only: a served ad must pass Taboola's review, so it gets
	// real content, never the placeholders the paused tests use.
	brand     string
	imagePath string
	aiImage   bool
	titles    []string
	groups    []string // AutoGen group ids named on the command line
	wait      time.Duration
	n         int
	lines     []string
}

// note records one finding for results.md.
func (t *tester) note(test, format string, a ...any) {
	line := fmt.Sprintf("| %s | %s |", test, strings.ReplaceAll(fmt.Sprintf(format, a...), "|", "/"))
	t.lines = append(t.lines, line)
	fmt.Println(line)
}

func (t *tester) save(e act.Exchange) {
	t.n++
	name := fmt.Sprintf("%03d-%s-%s.json", t.n, strings.ToLower(e.Method), safe(e.Path))
	rec := map[string]any{"time": e.Time, "method": e.Method, "path": e.Path, "status": e.Status,
		"request": rawOrString(e.RequestBody), "response": rawOrString(e.Body)}
	b, _ := json.MarshalIndent(rec, "", "  ")
	os.WriteFile(filepath.Join(t.dir, "raw", name), b, 0o640)
}

func (t *tester) writeResults(cmd string) {
	f, err := os.OpenFile(filepath.Join(t.dir, "results.md"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n## %s, %s, account `%s`\n\n| Test | What happened |\n|---|---|\n", cmd, time.Now().UTC().Format(time.RFC3339), t.account)
	for _, l := range t.lines {
		fmt.Fprintln(f, l)
	}
}

// campaignBody is a paused test campaign: fixed $0.10 bid, $20 in total at
// most, $10 a day at most, US desktop and phone. Taboola fills macros only
// in the campaign's tracking code: in an item URL it escapes the braces
// (seen 2026-09-29), so the macros go in -tracking, not in -url.
func (t *tester) campaignBody(name string) act.Obj {
	b := t.baseCampaign(name)
	if t.tracking != "" {
		b["tracking_code"] = t.tracking
	}
	return b
}

func (t *tester) baseCampaign(name string) act.Obj {
	return act.Obj{
		"name": prefix + " " + name, "branding_text": "AH Test", "is_active": false,
		"marketing_objective": "DRIVE_WEBSITE_TRAFFIC", "bid_strategy": "FIXED", "cpc": bid,
		"spending_limit_model": "ENTIRE", "spending_limit": maxMoney, "daily_cap": 10.0, "daily_ad_delivery_model": "STRICT",
		"country_targeting":  act.Obj{"type": "INCLUDE", "value": []string{"US"}},
		"platform_targeting": act.Obj{"type": "INCLUDE", "value": []string{"DESK", "PHON"}},
	}
}

// paused runs T1 to T11. No campaign is ever turned on. Whatever stops it
// early, everything it made is deleted before it returns.
func (t *tester) paused(ctx context.Context) error {
	err := t.pausedTests(ctx)
	if cerr := t.cleanup(ctx); cerr != nil {
		return errors.Join(err, cerr)
	}
	return err
}

func (t *tester) pausedTests(ctx context.Context) error {
	if t.url == "" {
		return errors.New("-url is required")
	}
	camp, err := t.c.CreateCampaign(ctx, t.campaignBody("T1 "+time.Now().UTC().Format("2006-01-02 15:04")))
	if err != nil {
		t.note("T1", "create failed: %v", err)
		return err
	}
	cid := s(camp["id"])
	t.note("T1", "created campaign %s: status %s, approval %s, is_active %v, bid %s %v, budget %v %v, daily cap %v",
		cid, s(camp["status"]), s(camp["approval_state"]), camp["is_active"], s(camp["bid_strategy"]), camp["cpc"],
		s(camp["spending_limit_model"]), camp["spending_limit"], camp["daily_cap"])

	end := time.Now().UTC().AddDate(0, 0, 14).Format(time.DateOnly)
	if got, err := t.c.UpdateCampaign(ctx, cid, act.Obj{"traffic_allocation_mode": "EVEN", "traffic_allocation_ab_test_end_date": end}); err != nil {
		t.note("T2", "A/B mode refused: %v", err)
	} else {
		t.note("T2", "traffic_allocation_mode %s, A/B end %s", s(got["traffic_allocation_mode"]), s(got["traffic_allocation_ab_test_end_date"]))
	}

	img, err := t.c.UploadImage(ctx, "ah-test.png", testImage())
	if err != nil {
		t.note("T3", "image upload failed: %v", err)
		return err
	}
	t.note("T3", "image uploaded to %s", img)
	var items []string
	for i, extra := range []act.Obj{
		{"custom_data": act.Obj{"custom_id": "AH-T3-A"}},
		{"custom_data": act.Obj{"custom_id": "AH-T3-B"}, "ai_disclosure": act.Obj{"status": "AI_GENERATED"}},
	} {
		it, err := t.c.CreateItem(ctx, cid, act.Obj{"url": t.url})
		if err != nil {
			t.note("T3", "item create failed: %v", err)
			return err
		}
		iid := s(it["id"])
		it, err = t.untilCrawled(ctx, cid, iid)
		if err != nil {
			t.note("T3", "item %s never left CRAWLING: %v", iid, err)
			return err
		}
		t.note("T3", "item %s crawled: status %s, crawled title %q", iid, s(it["status"]), s(it["title"]))
		upd := act.Obj{"title": fmt.Sprintf("A plain test headline %c", 'A'+i), "thumbnail_url": img}
		for k, v := range extra {
			upd[k] = v
		}
		got, err := t.c.UpdateItem(ctx, cid, iid, upd)
		if err != nil {
			t.note("T3", "item %s update refused: %v", iid, err)
			continue
		}
		t.note("T3", "item %s: status %s, approval %s, custom_data %s, ai_disclosure %s", iid, s(got["status"]), s(got["approval_state"]), js(got["custom_data"]), js(got["ai_disclosure"]))
		items = append(items, iid)
	}
	if len(items) < 2 {
		return errors.New("T3 did not leave two items")
	}

	mass, err := t.c.MassCreateItems(ctx, cid, []act.Obj{
		{"url": t.url, "title": "A plain test headline C", "thumbnail_url": img, "custom_data": act.Obj{"custom_id": "AH-T4-C"}},
		{"url": t.url, "title": "A plain test headline D", "thumbnail_url": img, "custom_data": act.Obj{"custom_id": "AH-T4-D"}},
	})
	if err != nil {
		t.note("T4", "mass create failed: %v", err)
	} else {
		rows, _ := mass["results"].([]any)
		for _, r := range rows {
			o, _ := r.(act.Obj)
			t.note("T4", "item %s: status %s, custom_data %s", s(o["id"]), s(o["status"]), js(o["custom_data"]))
		}
	}

	if got, err := t.c.UpdateItem(ctx, cid, items[0], act.Obj{"title": "A plain test headline A, edited"}); err != nil {
		t.note("T5", "edit in place refused: %v", err)
	} else {
		t.note("T5", "edit in place: item %s status %s, approval %s", items[0], s(got["status"]), s(got["approval_state"]))
	}
	if v2, err := t.c.MassCreateItems(ctx, cid, []act.Obj{{"url": t.url, "title": "A plain test headline A, version 2", "thumbnail_url": img, "custom_data": act.Obj{"custom_id": "AH-T5-A2"}}}); err != nil {
		t.note("T5", "new version failed: %v", err)
	} else {
		rows, _ := v2["results"].([]any)
		if len(rows) > 0 {
			o, _ := rows[0].(act.Obj)
			t.note("T5", "new version is item %s (status %s)", s(o["id"]), s(o["status"]))
		}
		if got, err := t.c.UpdateItem(ctx, cid, items[0], act.Obj{"is_active": false}); err != nil {
			t.note("T5", "pausing the old version refused: %v", err)
		} else {
			t.note("T5", "old version %s paused: status %s, is_active %v", items[0], s(got["status"]), got["is_active"])
		}
	}

	for _, on := range []bool{false, true} {
		if got, err := t.c.UpdateItem(ctx, cid, items[1], act.Obj{"is_active": on}); err != nil {
			t.note("T6", "item is_active %v refused: %v", on, err)
		} else {
			t.note("T6", "item %s is_active %v: status %s", items[1], got["is_active"], s(got["status"]))
		}
	}
	if got, err := t.c.Get(ctx, t.account+"/campaigns/"+cid+"/"); err == nil {
		t.note("T6", "campaign stays paused: is_active %v, status %s", got["is_active"], s(got["status"]))
	}

	if got, err := t.c.UpdateCampaign(ctx, cid, act.Obj{"daily_cap": 5.0, "spending_limit": 15.0}); err != nil {
		t.note("T7", "budget change refused: %v", err)
	} else {
		t.note("T7", "daily cap %v, total %v", got["daily_cap"], got["spending_limit"])
	}

	if len(t.sites) >= 2 {
		for _, op := range []string{"ADD", "REMOVE"} {
			if got, err := t.c.PatchCampaign(ctx, cid, act.Obj{"patch_operation": op, "publisher_targeting": act.Obj{"publishers": t.sites[:2]}}); err != nil {
				t.note("T8", "%s blocked sites refused: %v", op, err)
			} else {
				t.note("T8", "%s blocked sites: %s", op, js(got["publisher_targeting"]))
			}
		}
	} else {
		t.note("T8", "skipped: no -sites given")
	}

	for _, withItems := range []bool{true, false} {
		name := fmt.Sprintf("%s T9 copy (items %v)", prefix, withItems)
		got, err := t.c.DuplicateCampaign(ctx, cid, act.Obj{"name": name, "duplicate_settings": act.Obj{"include_items": withItems}})
		if err != nil {
			t.note("T9", "copy (items %v) refused: %v", withItems, err)
			continue
		}
		copied := s(got["id"])
		n := -1
		if its, err := t.c.Get(ctx, t.account+"/campaigns/"+copied+"/items/"); err == nil {
			rows, _ := its["results"].([]any)
			n = len(rows)
		}
		t.note("T9", "copy %s (items %v): status %s, is_active %v, approval %s, %d items", copied, withItems, s(got["status"]), got["is_active"], s(got["approval_state"]), n)
	}

	if got, err := t.c.UpdateCampaign(ctx, cid, act.Obj{"cpc": 0.12}); err != nil {
		t.note("T10", "bid change refused: %v", err)
	} else {
		t.note("T10", "bid now %v", got["cpc"])
	}

	if _, err := t.c.DeleteItem(ctx, cid, items[1]); err != nil {
		t.note("T11", "item delete failed: %v", err)
	} else if got, err := t.c.Get(ctx, t.account+"/campaigns/"+cid+"/items/"+items[1]+"/"); err != nil {
		t.note("T11", "deleted item %s now answers: %v", items[1], err)
	} else {
		t.note("T11", "deleted item %s still answers: status %s", items[1], s(got["status"]))
	}
	return nil
}

// cleanup deletes every campaign the tests made that is not deleted yet,
// then checks that each answers 404. Items go first: Taboola leaves a
// deleted campaign's items waiting in its review queue.
func (t *tester) cleanup(ctx context.Context) error {
	st := t.c.State()
	var errs []error
	for id := range st.Campaigns {
		if err := t.purgeItems(ctx, id); err != nil {
			errs = append(errs, err)
		}
		if st.Deleted[id] {
			continue
		}
		got, err := t.c.DeleteCampaign(ctx, id)
		if err != nil {
			t.note("T11", "delete campaign %s failed: %v", id, err)
			errs = append(errs, err)
			continue
		}
		after := "still readable"
		if _, err := t.c.Get(ctx, t.account+"/campaigns/"+id+"/"); err != nil {
			after = "then: " + err.Error()
		}
		t.note("T11", "deleted campaign %s: status %s; %s", id, s(got["status"]), after)
	}
	errs = append(errs, t.deleteGroups(ctx))
	return errors.Join(errs...)
}

// deleteGroups deletes the "AutoGen" campaign groups Taboola made for our
// campaigns, which it keeps after the campaigns are deleted. Groups go last,
// after their campaigns; the guard refuses any group a campaign still uses.
func (t *tester) deleteGroups(ctx context.Context) error {
	st := t.c.State()
	ids := map[string]bool{}
	for cid, g := range st.Groups {
		if st.Deleted[cid] {
			ids[g] = true
		}
	}
	for _, g := range t.groups {
		ids[g] = true
	}
	var errs []error
	for g := range ids {
		if st.Deleted["group:"+g] {
			continue
		}
		if _, err := t.c.DeleteCampaignGroup(ctx, g); err != nil {
			t.note("T11", "delete campaign group %s: %v", g, err)
			if !errors.Is(err, act.ErrRefused) {
				errs = append(errs, err)
			}
			continue
		}
		t.note("T11", "deleted campaign group %s", g)
	}
	return errors.Join(errs...)
}

// purgeDeleted deletes the leftover items and AutoGen groups of our already
// deleted campaigns only, leaving a live test alone.
func (t *tester) purgeDeleted(ctx context.Context) error {
	st := t.c.State()
	var errs []error
	for id := range st.Campaigns {
		if st.Deleted[id] {
			if err := t.purgeItems(ctx, id); err != nil {
				errs = append(errs, err)
			}
		}
	}
	errs = append(errs, t.deleteGroups(ctx))
	return errors.Join(errs...)
}

// purgeItems deletes every item Taboola still lists under one of our
// campaigns, deleted or not.
func (t *tester) purgeItems(ctx context.Context, cid string) error {
	its, err := t.c.Get(ctx, t.account+"/campaigns/"+cid+"/items/")
	if err != nil {
		return err
	}
	rows, _ := its["results"].([]any)
	gone := t.c.State().Deleted
	var errs []error
	for _, r := range rows {
		o, _ := r.(act.Obj)
		iid := s(o["id"])
		if gone[iid] {
			continue
		}
		if _, err := t.c.DeleteLeftoverItem(ctx, cid, iid); err != nil {
			t.note("T11", "delete item %s of campaign %s failed: %v", iid, cid, err)
			errs = append(errs, err)
			continue
		}
		t.note("T11", "deleted item %s (%q) of campaign %s", iid, s(o["title"]), cid)
	}
	return errors.Join(errs...)
}

// liveStart is T12's start: one fresh campaign in A/B mode with two items,
// turned on once both items are approved. It refuses if any campaign it did
// not create is running in the account, so it cannot compete with one.
func (t *tester) liveStart(ctx context.Context) error {
	if t.url == "" {
		return errors.New("-url is required")
	}
	if t.brand == "" || t.imagePath == "" || len(t.titles) != 2 {
		return errors.New("live-start needs -brand, -image and two -titles: served ads get real content, not placeholders")
	}
	photo, err := os.ReadFile(t.imagePath)
	if err != nil {
		return err
	}
	if err := t.noOtherRunning(ctx); err != nil {
		t.note("T12", "not started: %v", err)
		return err
	}
	body := t.campaignBody("T12 live " + time.Now().UTC().Format("2006-01-02 15:04"))
	body["branding_text"] = t.brand
	body["traffic_allocation_mode"] = "EVEN"
	body["end_date"] = time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
	camp, err := t.c.CreateCampaign(ctx, body)
	if err != nil {
		t.note("T12", "create failed: %v", err)
		return err
	}
	cid := s(camp["id"])
	img, err := t.c.UploadImage(ctx, filepath.Base(t.imagePath), photo)
	if err != nil {
		t.note("T12", "image upload failed: %v", err)
		return errors.Join(err, t.cleanup(ctx))
	}
	var ads []act.Obj
	for i, title := range t.titles {
		ad := act.Obj{"url": t.url, "title": title, "thumbnail_url": img, "custom_data": act.Obj{"custom_id": fmt.Sprintf("AH-T12-%d", i+1)}}
		if t.aiImage {
			ad["ai_disclosure"] = act.Obj{"status": "AI_GENERATED"}
		}
		ads = append(ads, ad)
	}
	if _, err := t.c.MassCreateItems(ctx, cid, ads); err != nil {
		t.note("T12", "items failed: %v", err)
		return err
	}
	if err := os.WriteFile(filepath.Join(t.dir, "live-campaign.txt"), []byte(cid), 0o640); err != nil {
		return err
	}
	t.note("T12", "campaign %s created paused with 2 items; run live-on once Taboola approves them", cid)
	return nil
}

// liveOn turns the T12 campaign on once Taboola approved it and its items,
// waiting up to 20 minutes for the review. It re-checks that no other
// campaign runs in the account first.
func (t *tester) liveOn(ctx context.Context) error {
	cid, err := t.pendingLive()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Minute)
	for {
		camp, err := t.c.Get(ctx, t.account+"/campaigns/"+cid+"/")
		if err != nil {
			return err
		}
		its, err := t.c.Get(ctx, t.account+"/campaigns/"+cid+"/items/")
		if err != nil {
			return err
		}
		rows, _ := its["results"].([]any)
		approved := 0
		for _, r := range rows {
			if o, _ := r.(act.Obj); s(o["approval_state"]) == "APPROVED" {
				approved++
			}
		}
		if s(camp["approval_state"]) == "APPROVED" && approved == len(rows) && approved > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.note("T12", "still in review after 20 minutes (campaign %s, %d of %d items approved); left paused", s(camp["approval_state"]), approved, len(rows))
			return nil
		}
		if err := sleep(ctx, time.Minute); err != nil {
			return err
		}
	}
	if err := t.noOtherRunning(ctx); err != nil {
		t.note("T12", "not turned on: %v", err)
		return err
	}
	got, err := t.c.UpdateCampaign(ctx, cid, act.Obj{"is_active": true})
	if err != nil {
		t.note("T12", "turn on refused: %v", err)
		return err
	}
	t.note("T12", "campaign %s ON at %s: status %s, total cap %v, daily cap %v", cid, time.Now().UTC().Format(time.RFC3339), s(got["status"]), got["spending_limit"], got["daily_cap"])
	return nil
}

// pendingLive is the T12 campaign live-start made, if it is not on or
// deleted yet.
func (t *tester) pendingLive() (string, error) {
	b, err := os.ReadFile(filepath.Join(t.dir, "live-campaign.txt"))
	if err != nil {
		return "", errors.New("no T12 campaign: run live-start first")
	}
	id := strings.TrimSpace(string(b))
	st := t.c.State()
	if _, on := st.Activated[id]; on || st.Deleted[id] {
		return "", fmt.Errorf("T12 campaign %s is already on or deleted", id)
	}
	return id, nil
}

// livePause pauses the live campaign and leaves everything in place, for
// when a person asks to hold the test.
func (t *tester) livePause(ctx context.Context) error {
	cid, _, err := t.live()
	if err != nil {
		return err
	}
	got, err := t.c.UpdateCampaign(ctx, cid, act.Obj{"is_active": false})
	if err != nil {
		t.note("T12", "pause refused: %v", err)
		return err
	}
	t.note("T12", "campaign %s paused at %s: status %s, spent %v", cid, time.Now().UTC().Format(time.RFC3339), s(got["status"]), got["spent"])
	return nil
}

// liveResume turns a paused live campaign back on as it was. The guard
// still counts its budget once against the money ceiling, and nothing else
// may be running in the account.
func (t *tester) liveResume(ctx context.Context) error {
	cid, _, err := t.live()
	if err != nil {
		return err
	}
	if err := t.noOtherRunning(ctx); err != nil {
		t.note("T12", "not resumed: %v", err)
		return err
	}
	got, err := t.c.UpdateCampaign(ctx, cid, act.Obj{"is_active": true})
	if err != nil {
		t.note("T12", "resume refused: %v", err)
		return err
	}
	t.note("T12", "campaign %s resumed at %s: status %s, spent %v, total cap %v", cid, time.Now().UTC().Format(time.RFC3339), s(got["status"]), got["spent"], got["spending_limit"])
	return nil
}

// liveCut pauses the second item of the live campaign (a cut while serving).
func (t *tester) liveCut(ctx context.Context) error {
	cid, items, err := t.live()
	if err != nil {
		return err
	}
	got, err := t.c.UpdateItem(ctx, cid, items[len(items)-1], act.Obj{"is_active": false})
	if err != nil {
		t.note("T12", "cut refused: %v", err)
		return err
	}
	t.note("T12", "cut item %s at %s: status %s", items[len(items)-1], time.Now().UTC().Format(time.RFC3339), s(got["status"]))
	return nil
}

// liveEnd pauses the live campaign, saves what the reports show for it, and
// deletes everything.
func (t *tester) liveEnd(ctx context.Context) error {
	cid, _, err := t.live()
	if err != nil {
		return err
	}
	if got, err := t.c.UpdateCampaign(ctx, cid, act.Obj{"is_active": false}); err != nil {
		t.note("T12", "pause failed: %v", err)
		return err
	} else {
		t.note("T12", "campaign %s paused at %s: spent %v", cid, time.Now().UTC().Format(time.RFC3339), got["spent"])
	}
	day := time.Now().UTC()
	from, to := day.AddDate(0, 0, -2).Format(time.DateOnly), day.Format(time.DateOnly)
	for _, p := range []string{
		"reports/campaign-summary/dimensions/campaign_breakdown?start_date=" + from + "&end_date=" + to + "&campaign=" + cid,
		"reports/top-campaign-content/dimensions/item_breakdown?start_date=" + from + "&end_date=" + to + "&campaign=" + cid,
		"reports/realtime-campaign-summary/dimensions/by_campaign?start_date=" + from + "&end_date=" + to,
		"reports/realtime-top-campaign-content/dimensions/by_item?start_date=" + from + "&end_date=" + to,
	} {
		if got, err := t.c.Get(ctx, t.account+"/"+p); err != nil {
			t.note("T12", "%s: %v", p[:strings.Index(p, "?")], err)
		} else {
			rows, _ := got["results"].([]any)
			t.note("T12", "%s: %d rows (raw saved)", p[:strings.Index(p, "?")], len(rows))
		}
	}
	return t.cleanup(ctx)
}

// live is the T12 campaign once it was turned on, with its items.
func (t *tester) live() (string, []string, error) {
	b, err := os.ReadFile(filepath.Join(t.dir, "live-campaign.txt"))
	if err != nil {
		return "", nil, errors.New("no T12 campaign: run live-start first")
	}
	id := strings.TrimSpace(string(b))
	st := t.c.State()
	if st.Deleted[id] {
		return "", nil, fmt.Errorf("T12 campaign %s is deleted", id)
	}
	var items []string
	for it, c := range st.Items {
		if c == id {
			items = append(items, it)
		}
	}
	sort.Strings(items)
	return id, items, nil
}

// noOtherRunning refuses when a campaign we did not create is running.
func (t *tester) noOtherRunning(ctx context.Context) error {
	got, err := t.c.Get(ctx, t.account+"/campaigns/")
	if err != nil {
		return err
	}
	ours := t.c.State().Campaigns
	rows, _ := got["results"].([]any)
	for _, r := range rows {
		o, _ := r.(act.Obj)
		if _, mine := ours[s(o["id"])]; !mine && s(o["status"]) == "RUNNING" {
			return fmt.Errorf("campaign %s (%s) is running in %s", s(o["id"]), s(o["name"]), t.account)
		}
	}
	return nil
}

// untilCrawled polls an item until Taboola has crawled its URL.
func (t *tester) untilCrawled(ctx context.Context, cid, iid string) (act.Obj, error) {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		it, err := t.c.Get(ctx, t.account+"/campaigns/"+cid+"/items/"+iid+"/")
		if err != nil {
			return nil, err
		}
		if s(it["status"]) != "CRAWLING" {
			return it, nil
		}
		if time.Now().After(deadline) {
			return it, errors.New("timed out")
		}
		if err := sleep(ctx, t.wait); err != nil {
			return nil, err
		}
	}
}

// testImage is a plain 1200 by 674 picture: two soft colour fields, no text.
func testImage() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 674))
	for y := 0; y < 674; y++ {
		for x := 0; x < 1200; x++ {
			c := color.RGBA{uint8(60 + x/10), uint8(120 + y/8), 170, 255}
			if (x-600)*(x-600)+(y-337)*(y-337) < 180*180 {
				c = color.RGBA{240, 200, 90, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func s(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	}
	return fmt.Sprint(v)
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func rawOrString(b []byte) any {
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}

func splitBar(v string) []string {
	var out []string
	for _, p := range strings.Split(v, "|") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func safe(p string) string {
	if i := strings.Index(p, "?"); i >= 0 {
		p = p[:i]
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '_'
	}, strings.Trim(p, "/"))
}
