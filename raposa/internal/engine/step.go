package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
	"github.com/Raposa-Industries/adhunters/shared/page"
)

// An investigation climbs a ladder of disguises, cheapest first, and once a
// rung breaks through it repeats that rung many times to measure which
// variants the operator is split testing. It moves one visit at a time: a
// worker claims it, makes one visit, and writes the visit and everything
// that follows from it in one transaction together with the progress and
// the time of the next visit. Nothing about an investigation lives in a
// worker between two visits.

// notRunningAfter is how long without a sighting makes an ad not running.
const notRunningAfter = time.Hour

// Waits that record no visit.
const (
	linkWaitStep   = 15 * time.Second // no live link on hand yet
	runnerWaitStep = 20 * time.Second // the browser runner could not take it
	// After this many runner waits in a row (about ten minutes) the visit is
	// recorded as an error, so a runner that stays down shows on the screen.
	maxRunnerWaits = 30
	// After this many failed steps in a row the investigation fails.
	maxStepErrors = 5
)

// errWait says the visit did not happen and should be tried again after
// the wait: nothing was learnt about the page.
type errWait struct {
	after time.Duration
	why   string
}

func (e *errWait) Error() string { return e.why }

// op is one write of the step, run in its transaction in order.
type op func(ctx context.Context, tx pgx.Tx) error

// run is one step of one investigation.
type run struct {
	e      *Engine
	inv    *Investigation
	p      *Progress
	set    Settings
	ladder []Disguise
	burned map[string]string

	ops    []op
	logs   []string
	events [][3]string // kind, title, body

	stage, stageNote string
	rung             int16
	next             time.Duration
	finished         bool
	lastLine         string      // the line the last visit took
	visited          [][2]string // engine and outcome of each visit, for the metrics
}

// ad is the context resolved when the investigation started.
func (r *run) ad() *AdContext {
	if r.p.Ad == nil {
		r.p.Ad = &AdContext{Device: "desktop"}
	}
	return r.p.Ad
}

func (r *run) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	r.logs = append(r.logs, line)
	r.e.log.Debug("investigation", "id", r.inv.ID, "line", line)
}

func (r *run) setStage(stage, note string, rung int16) {
	r.stage, r.stageNote = stage, note
	if rung > r.rung {
		r.rung = rung
	}
}

func (r *run) emit(kind, title, body string) {
	r.events = append(r.events, [3]string{kind, title, body})
}

func (r *run) do(o op) { r.ops = append(r.ops, o) }

// advance makes the investigation's next visit.
func (r *run) advance(ctx context.Context) error {
	if r.inv.StopRequested {
		r.logf("stop requested")
		return r.finish("stopped", "")
	}
	switch r.p.Phase {
	case "":
		return r.prepare(ctx)
	case "baseline":
		return r.baseline(ctx)
	case "baseline2":
		return r.baselineAgain(ctx)
	case "climb":
		return r.climb(ctx)
	case "sample":
		return r.sample(ctx)
	case "twin":
		return r.twin(ctx)
	}
	return r.finish("failed", fmt.Sprintf("the investigation is in a phase this engine does not know: %q", r.p.Phase))
}

// prepare reads the ad from Tracks and starts the investigation.
func (r *run) prepare(ctx context.Context) error {
	if len(r.ladder) == 0 {
		return r.finish("failed", "the ladder is empty: no enabled disguise in raposa.disguise")
	}
	ad, err := r.e.store.ResolveAd(ctx, r.inv.CreativeID, r.inv.AdID)
	if err != nil {
		return err
	}
	r.p.Ad = &ad
	r.logf("investigation %d on creative %d, %s, attempt %d", r.inv.ID, r.inv.CreativeID, r.inv.Mode, r.inv.Attempt)

	if ad.LandingHost == "" {
		return r.finish("failed", "Tracks has not seen this creative's link in the last 30 days, so there is nothing to visit")
	}
	// Every rung past the reviewer baseline clicks a live link, and only an
	// ad that runs has one. Capture sees a running ad many times an hour, so
	// one not seen for an hour is not running.
	if ad.LastSeenAt != nil && time.Since(*ad.LastSeenAt) > notRunningAfter {
		return r.finish("failed", fmt.Sprintf("the ad is not running: Tracks last saw it %s ago (%s UTC), so there is no live link to click, and the saved link only ever gets the white page",
			time.Since(*ad.LastSeenAt).Round(time.Minute), ad.LastSeenAt.UTC().Format("2006-01-02 15:04")))
	}

	device := ad.Device
	if !ad.DeviceKnown {
		device += " (assumed: Tracks does not say which device this ad runs on)"
	}
	r.logf("target device %s, publisher %s, landing host %s, campaign %s",
		device, orDash(ad.PublisherName), orDash(ad.LandingHost), orDash(ad.CampaignID))
	if len(ad.Devices) > 1 {
		parts := make([]string, 0, len(ad.Devices))
		for _, dv := range ad.Devices {
			parts = append(parts, dv+" in campaign "+orDash(ad.CampaignByDevice[dv]))
		}
		r.logf("the ad ran on %s in the last day: the rungs that follow the campaign try each device", strings.Join(parts, " and "))
	}

	scope := SiteScope(ad.LandingHost)
	r.inv.BurnScope = scope
	burned, err := r.e.store.BurnedLines(ctx, scope)
	if err != nil {
		return err
	}
	for key, why := range burned {
		r.logf("skipping line %s: %s", key, why)
	}

	referer := ""
	if pub := findTarget(r.e.targets, ad.PublisherName, ad.PublisherDomain); pub != nil && pub.URL != "" {
		referer = pub.URL
	} else if ad.PublisherDomain != "" {
		referer = "https://" + ad.PublisherDomain + "/"
	}
	r.inv.PublisherReferer = referer
	r.inv.TargetClickURL = browserURL(ad.SavedLink)

	id := r.inv.ID
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE raposa.investigation
			SET status = 'running', started_at = COALESCE(started_at, now()),
			    target_click_url = $2, publisher_referer = $3, target_device = $4,
			    publisher_id = $5, burn_scope = $6
			WHERE id = $1`, id, clean(r.inv.TargetClickURL), clean(referer), ad.Device, ad.PublisherID, scope)
		return err
	})
	r.emit("started", fmt.Sprintf("Raposa started on creative %d", r.inv.CreativeID),
		fmt.Sprintf("%s investigation of %s", r.inv.Mode, ad.LandingHost))
	r.p.Phase = "baseline"
	r.setStage("baseline", "reading the white page a reviewer sees", 0)
	return nil
}

// baseline runs the reviewer rung: the saved link, from an ad network
// office city or a datacenter, which is exactly what a reviewer does. A line
// can refuse a host (one ISP line answers 403 to some tracker domains), so
// it tries up to three lines, one per visit.
func (r *run) baseline(ctx context.Context) error {
	d := r.baselineRung()
	if d == nil {
		return r.finish("failed", "the ladder has no baseline rung")
	}
	r.setStage("baseline", "reading the white page a reviewer sees", d.Rung)
	if r.p.BaselineTries == 0 {
		r.p.RungsTried++
		r.p.LastRung = d.Rung
	}
	r.p.BaselineTries++
	out, err := r.visit(ctx, *d, deviceFor(d.DeviceRule, r.ad().Device), "ladder", r.p.BaselineTries)
	if w := asWait(err); w != nil {
		r.p.BaselineTries--
		return err
	}
	if err != nil {
		if r.lastLine != "" && r.p.BaselineTries < 3 {
			r.logf("reviewer baseline on %s failed (%v): trying another line", r.lastLine, err)
			r.p.BadLines = append(r.p.BadLines, r.lastLine)
			return nil
		}
		return r.finish("failed", fmt.Sprintf("the reviewer baseline failed: %v", err))
	}
	if out.Verdict.Outcome == "error" {
		// A white page that is only a server error would make every later
		// visit look dark.
		return r.finish("failed", fmt.Sprintf("the reviewer baseline is not a page: %s", out.Verdict.Reason))
	}
	r.p.White = out.Landing
	side := out.side(*d)
	r.p.WhiteSide = &side
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE raposa.investigation SET white_page_id = $2 WHERE id = $1`, r.inv.ID, out.pageIDs[0])
		return err
	})
	r.logf("white page: %s (%s)", out.Landing.URL, orDash(out.Landing.Title))
	r.p.Phase = "baseline2"
	return nil
}

// baselineAgain reads the white page a second time. The baseline costs
// nothing, and a page that does not hash the same twice in a row would make
// every later visit read as dark on its HTML alone, which is a verdict
// nobody measured.
func (r *run) baselineAgain(ctx context.Context) error {
	d := r.baselineRung()
	if d == nil {
		return r.finish("failed", "the ladder has no baseline rung")
	}
	r.setStage("baseline", "reading the white page again, to see whether it changes on every load", d.Rung)
	again, err := r.visit(ctx, *d, deviceFor(d.DeviceRule, r.ad().Device), "ladder", r.p.BaselineTries+1)
	if asWait(err) != nil {
		return err
	}
	switch {
	case err != nil:
		r.p.WhiteStable = false
		r.logf("the second reviewer load failed (%v): comparing on the domain and the title only", err)
	case again.Landing.Hash != r.p.White.Hash:
		r.p.WhiteStable = false
		r.logf("the white page is not the same twice in a row: comparing on the domain and the title only")
	default:
		r.p.WhiteStable = true
		r.logf("the white page is the same twice in a row: comparing on its whole HTML")
	}
	r.p.Phase = "climb"
	return nil
}

// climb makes one attempt on the current rung, moving up when the rung has
// had its tries.
func (r *run) climb(ctx context.Context) error {
	tries := max(r.set.LadderTriesPerRung, 1)
	d := r.rungAt(r.p.Rung)
	if d == nil || r.p.Attempt >= tries {
		d = r.nextRung(r.p.Rung)
		if d == nil {
			return r.ladderRanOut()
		}
		r.p.Rung, r.p.Attempt, r.p.RungTries, r.p.RungDark = d.Rung, 0, 0, 0
		r.p.RungsTried++
		r.p.LastRung = d.Rung
	}
	r.setStage("climbing", fmt.Sprintf("rung %d: %s", d.Rung, d.Name), d.Rung)

	r.p.Attempt++
	device := r.climbDevice(*d, r.p.Attempt)
	out, err := r.visit(ctx, *d, device, "ladder", r.p.Attempt)
	if asWait(err) != nil {
		r.p.Attempt--
		return err
	}
	if err != nil {
		r.logf("rung %d attempt %d (%s): %v", d.Rung, r.p.Attempt, device, err)
		return nil
	}
	r.p.RungTries++
	r.p.LadderLoads++
	r.logf("rung %d attempt %d (%s): %s (%s)", d.Rung, r.p.Attempt, device, out.Verdict.Outcome, out.Verdict.Reason)
	if out.Verdict.Outcome != "dark" {
		return nil
	}

	r.p.RungDark++
	r.p.TriesAtBreach, r.p.DarkAtBreach = r.p.RungTries, r.p.RungDark
	breach := *d
	r.p.Breach = &breach
	r.p.BreachDevice = device
	side := out.side(*d)
	r.p.DarkSide = &side
	r.noteCheckout(out)
	r.logf("rung %d (%s) broke through on %s", d.Rung, d.Code, device)
	id := r.inv.ID
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		// The screen shows the device the dark page was served to.
		_, err := tx.Exec(ctx, `
			UPDATE raposa.investigation SET breach_rung = COALESCE(breach_rung, $2), target_device = $3
			WHERE id = $1`, id, d.Rung, device)
		return err
	})
	r.emit("dark_found", fmt.Sprintf("Dark page on creative %d", r.inv.CreativeID),
		fmt.Sprintf("rung %d (%s) got %s on %s", d.Rung, d.Name, out.Landing.URL, device))

	if r.inv.Mode != "deep" {
		return r.finish("completed", "")
	}
	r.p.Phase = "sample"
	r.setStage("sampling", fmt.Sprintf("%d visits on rung %d, one every %s", r.sampleTarget(), d.Rung, r.sampleGap().Round(time.Second)), d.Rung)
	return nil
}

// ladderRanOut ends an investigation whose ladder saw no dark page.
func (r *run) ladderRanOut() error {
	if r.p.LadderLoads == 0 {
		// Nothing past the reviewer baseline ever loaded a page, so the ad
		// was not tested. Writing "not cloaked" here would be a verdict
		// nobody measured.
		return r.finish("failed", "no rung past the reviewer baseline loaded a page: the ad was not tested")
	}
	if r.inv.Mode == "quick" {
		r.logf("the free rungs ran out with no dark page over %d visits (quick investigation: the paid rungs were not tried)", r.p.LadderLoads)
	} else {
		r.logf("the ladder ran out with no dark page over %d visits: the ad is not cloaked", r.p.LadderLoads)
	}
	return r.finish("completed", "")
}

// climbDevice is the device one ladder attempt claims. A rung that follows
// the campaign takes turns over the devices the ad ran on in the last day,
// so a creative running in a desktop campaign and a phone campaign is tried
// on both: one operator family serves its dark page to phones only, even
// from a desktop campaign's creative. Every other rule is fixed.
func (r *run) climbDevice(d Disguise, attempt int) string {
	ad := r.ad()
	if d.DeviceRule == "campaign" && len(ad.Devices) > 1 {
		return ad.Devices[(attempt-1)%len(ad.Devices)]
	}
	return deviceFor(d.DeviceRule, ad.Device)
}

func (r *run) sampleTarget() int {
	if r.inv.VisitsTarget > 0 {
		return r.inv.VisitsTarget
	}
	return r.set.VisitsTarget
}

// sampleGap spreads the sample over visits_window_minutes, so the operator
// sees a normal trickle.
func (r *run) sampleGap() time.Duration {
	target := max(r.sampleTarget(), 1)
	gap := time.Duration(r.set.VisitsWindowMinutes) * time.Minute / time.Duration(target)
	return max(gap, time.Second)
}

// sample makes one visit on the rung that broke through.
func (r *run) sample(ctx context.Context) error {
	d := r.p.Breach
	if d == nil {
		return r.finish("failed", "sampling with no rung that broke through")
	}
	target := r.sampleTarget()
	if r.p.SampleDone >= target {
		return r.finish("completed", "")
	}
	if d.LineRole == "residential" && r.set.ResidentialBudgetBytes > 0 && r.p.ResBytes >= int64(r.set.ResidentialBudgetBytes) {
		r.logf("sample stopped after %d visits: the residential traffic of this investigation reached its budget (%d of %d bytes)",
			r.p.SampleDone, r.p.ResBytes, r.set.ResidentialBudgetBytes)
		r.setStage("sampling", fmt.Sprintf("stopped at visit %d of %d: residential traffic budget reached", r.p.SampleDone, target), d.Rung)
		return r.finish("completed", "")
	}
	n := r.p.SampleDone + 1
	out, err := r.visit(ctx, *d, r.p.BreachDevice, "sample", 1)
	if asWait(err) != nil {
		return err
	}
	if err != nil {
		r.logf("sample %d/%d: %v", n, target, err)
		if strings.HasPrefix(err.Error(), "no fresh link") {
			r.p.NoLink++
		}
		if r.p.NoLink >= 3 {
			r.logf("sample stopped after %d visits: three visits in a row found no live link, so the ad has stopped running", n)
			r.setStage("sampling", fmt.Sprintf("stopped at visit %d of %d: the ad stopped running", n, target), d.Rung)
			return r.finish("completed", "")
		}
	} else {
		r.p.NoLink = 0
		r.p.TriesAtBreach++
		if out.Verdict.Outcome == "dark" {
			r.p.DarkAtBreach++
			if r.p.DarkSide == nil {
				side := out.side(*d)
				r.p.DarkSide = &side
			}
			r.noteCheckout(out)
		}
	}
	r.p.Phase = "twin"
	return nil
}

// twin makes the reviewer visit that goes with each sample visit: the saved
// link from an ad network office city, at the same moment. Repeated a
// hundred times, these show whether the operator also split tests the white
// page. They go out on free lines and do not count toward the sample.
func (r *run) twin(ctx context.Context) error {
	target := r.sampleTarget()
	n := r.p.SampleDone + 1
	if wd := r.baselineRung(); wd != nil {
		_, err := r.visit(ctx, *wd, deviceFor(wd.DeviceRule, r.ad().Device), "sample", 1)
		if asWait(err) != nil {
			return err
		}
		if err != nil {
			r.logf("reviewer sample %d/%d: %v", n, target, err)
		}
	}
	r.p.SampleDone = n
	r.p.Phase = "sample"
	id := r.inv.ID
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT raposa.refresh_variants($1)`, id)
		return err
	})
	var rung int16
	if r.p.Breach != nil {
		rung = r.p.Breach.Rung
	}
	r.setStage("sampling", fmt.Sprintf("visit %d of %d on rung %d", n, target, rung), rung)
	if n < target {
		r.next = r.sampleGap()
	}
	return nil
}

// finish writes the verdict. status is completed for an investigation that
// ran to its end, stopped for one someone asked to stop, which keeps
// everything it found, and failed for one that could not test the ad, with
// why.
func (r *run) finish(status, why string) error {
	r.finished = true
	p := r.p
	isCloaked := p.Breach != nil
	confidence := 0.0
	var breachRung *int16
	if p.Breach != nil {
		breachRung = &p.Breach.Rung
		// The share of the visits on the rung that broke through which saw a
		// dark page. A rung with no visit behind it reports no confidence
		// rather than a number chosen for it.
		if p.TriesAtBreach > 0 {
			confidence = float64(p.DarkAtBreach) * 100 / float64(p.TriesAtBreach)
		}
	}
	note := why
	if status != "failed" {
		note = verdictNote(r.inv.Mode, p.Breach, status)
	} else {
		r.logf("%s", why)
	}
	r.setStage("done", truncate(note, 300), p.LastRung)
	if status != "failed" {
		r.logf("verdict: cloaked=%v, %d of %d visits on rung %s saw a dark page (%.1f%%)",
			isCloaked, p.DarkAtBreach, p.TriesAtBreach, breachRungText(p.Breach), confidence)
	}

	white := sideData{}
	if p.WhiteSide != nil {
		white = *p.WhiteSide
	}
	dark := sideData{}
	if p.DarkSide != nil {
		dark = *p.DarkSide
	}
	// The rungs the ladder actually visited, not the length of the ladder:
	// a breakthrough on rung 2 tested two rungs, not eight.
	dark.DisguisesTested = p.RungsTried
	id := r.inv.ID
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT raposa.refresh_variants($1)`, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT label, visits, share_pct::float8 FROM raposa.variant
			WHERE investigation_id = $1 ORDER BY share_pct DESC, id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v variantData
			if err := rows.Scan(&v.Label, &v.Visits, &v.SharePct); err != nil {
				rows.Close()
				return err
			}
			dark.Variants = append(dark.Variants, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE raposa.investigation
			SET status = $2, is_cloaked = $3, cloaked_confidence = $4, breach_rung = COALESCE($5, breach_rung),
			    reviewer_data = $6::jsonb, raposa_data = $7::jsonb, checkout_data = $8::jsonb,
			    completed_at = now(), started_at = COALESCE(started_at, now())
			WHERE id = $1`,
			id, status, isCloaked, confidence, breachRung, jsonOr(white, "{}"), jsonOr(dark, "{}"), jsonOr(p.Checkout, "{}"))
		return err
	})
	title := map[string]string{
		"completed": "Investigation finished",
		"stopped":   "Investigation stopped",
		"failed":    "Investigation failed",
	}[status]
	r.emit("finished", fmt.Sprintf("%s on creative %d", title, r.inv.CreativeID), note)
	// No dark page: the paced queue tries again later.
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		var retry *int64
		if err := tx.QueryRow(ctx, `SELECT raposa.schedule_retry($1)`, id).Scan(&retry); err != nil {
			return err
		}
		if retry != nil {
			_, err := tx.Exec(ctx, `INSERT INTO raposa.log (investigation_id, line) VALUES ($1, $2)`,
				id, fmt.Sprintf("no dark page: investigation %d queued to try again", *retry))
			return err
		}
		return nil
	})
	return nil
}

func breachRungText(d *Disguise) string {
	if d == nil {
		return "-"
	}
	return fmt.Sprintf("%d", d.Rung)
}

func verdictNote(mode string, breach *Disguise, status string) string {
	switch {
	case status == "stopped" && breach != nil:
		return fmt.Sprintf("stopped after a dark funnel was found on rung %d (%s)", breach.Rung, breach.Code)
	case status == "stopped":
		return "stopped before any rung saw a dark page"
	case breach == nil && mode == "quick":
		return "no dark page on the free rungs (quick: the paid rungs were not tried)"
	case breach == nil:
		return "the ladder ran out with no dark page"
	default:
		return fmt.Sprintf("dark funnel found on rung %d (%s)", breach.Rung, breach.Code)
	}
}

// visitOutcome is what one visit came back with.
type visitOutcome struct {
	Verdict verdict
	Landing landing
	Pages   []Page
	Steps   []VisitStep
	Bytes   int
	LineKey string
	Place   string
	// The link the visit asked for. The landing URL is where it ended after
	// the redirects, which is not the same thing.
	TargetURL string
	// Filled when the step commits.
	pageIDs []int32
}

// visitRow is one raposa.visit row.
type visitRow struct {
	Purpose    string
	Rung       int16
	DisguiseID int16
	Attempt    int
	LineKey    string
	Place      string
	ExitIP     string
	Device     string
	Engine     string
	LinkKind   string
	TargetURL  string
	Referer    string
	Outcome    string
	StepsCount int
	StatusCode *int16
	Hops       []VisitHop
	Bytes      int
	DurationMs int
	Error      string
	StartedAt  time.Time
}

// visit runs one disguise once: it takes a live link when the rung wants
// one, picks the line and the place, loads the page, captures every page it
// reaches and queues the visit's writes.
func (r *run) visit(ctx context.Context, d Disguise, device, purpose string, attempt int) (*visitOutcome, error) {
	ad := r.ad()
	if device == "" {
		device = deviceFor(d.DeviceRule, ad.Device)
	}
	v := visitRow{
		Purpose:    purpose,
		Rung:       d.Rung,
		DisguiseID: d.ID,
		Attempt:    attempt,
		Device:     device,
		Engine:     d.Engine,
		LinkKind:   d.LinkKind,
		TargetURL:  browserURL(r.inv.TargetClickURL),
		Referer:    r.inv.PublisherReferer,
		Outcome:    "error",
		StartedAt:  time.Now(),
	}

	// A browser visit needs a free slot first: a link taken while the
	// browser is busy would be wasted.
	if d.Engine == "browser" {
		release, ok := r.e.browser.TryAcquire()
		if !ok {
			return nil, r.runnerWait(v, "every browser slot on this box is busy")
		}
		defer release()
	}

	// The link. A live one still carries the ad network's click values; the
	// saved one has them stripped and always gets the white page. A rung
	// that asks for a live link waits for one rather than spending a visit
	// on a link that cannot get through and reading the white page it comes
	// back with as "not cloaked".
	if d.LinkKind == "live" {
		link, err := r.takeLiveLink(ctx, device)
		if err != nil {
			return nil, err
		}
		if link == nil {
			waited := time.Duration(0)
			if r.p.LinkWaitSince == nil {
				now := time.Now()
				r.p.LinkWaitSince = &now
			} else {
				waited = time.Since(*r.p.LinkWaitSince)
			}
			limit := time.Duration(r.set.LiveLinkWaitSeconds) * time.Second
			if waited < limit {
				return nil, &errWait{after: linkWaitStep, why: "waiting for Tracks to see the ad again"}
			}
			r.p.LinkWaitSince = nil
			return r.record(v, nil, fmt.Sprintf("no fresh link for this visit: Tracks did not see %s in any feed for %s", ad.LandingHost, limit.Round(time.Second)))
		}
		r.p.LinkWaitSince = nil
		v.TargetURL = browserURL(link.URL)
		v.Referer = r.clickReferer(*link, v.TargetURL)
	}
	if v.TargetURL == "" {
		return r.record(v, nil, "no link to visit")
	}

	// The line the disguise asks for. The baseline takes a line exiting in an
	// avoided place, because that is what a reviewer sees; every other rung
	// stays away from those places and from burned lines.
	var line *lines.Line
	if r.inv.Mode == "quick" && d.LineRole == "residential" {
		return r.record(v, nil, "a quick investigation never uses the residential line")
	}
	if d.LineRole != "direct" {
		all := r.usableLines()
		if !d.IsBaseline {
			all = r.unburned(all)
		}
		line = chooseLine(all, d.LineRole, r.set.AvoidPlaces, d.IsBaseline, r.p.LineCursor)
		r.p.LineCursor++
		if line == nil {
			return r.record(v, nil, fmt.Sprintf("no %s line on this box", d.LineRole))
		}
	}
	// A residential visit is pinned to a state away from the places an
	// operator watches, with a session id of its own, so each visit arrives
	// from a different home address.
	if line != nil && d.PinPlace && line.Role == "residential" {
		v.Place = pickPlace(r.set.SampleStates, r.set.AvoidPlaces, r.set.AvoidRegions, r.p.Place)
		r.p.Place++
		pinned := proxyState(*line, v.Place)
		line = &pinned
	}
	if line != nil {
		v.LineKey = line.Key
		r.lastLine = line.Key
	}

	plan := visitPlan{
		URL:        v.TargetURL,
		Referer:    v.Referer,
		Line:       line,
		Device:     v.Device,
		Timezone:   timezoneFor(v.Place, v.LineKey),
		LoadAssets: d.LoadAssets,
		HumanDwell: d.HumanDwell,
		DwellMs:    r.set.BrowserDwellMs,
	}
	// The ladder only needs the landing page to tell white from dark. The
	// sample visits walk the whole funnel.
	if purpose == "sample" && !d.IsBaseline {
		plan.MaxSteps = r.set.FunnelMaxSteps
	}

	visitCtx, cancel := context.WithTimeout(ctx, visitBudget(d))
	defer cancel()
	var res *VisitResult
	var err error
	if d.Engine == "browser" {
		res, err = r.e.browser.Visit(visitCtx, plan)
	} else {
		res, err = r.e.fetcher.Visit(visitCtx, plan)
	}
	if ctx.Err() != nil {
		// This node is stopping: the visit runs again elsewhere.
		return nil, ctx.Err()
	}
	if errors.Is(err, errRunnerBusy) {
		return nil, r.runnerWait(v, err.Error())
	}
	r.p.RunnerWaits = 0
	if err != nil && res != nil && len(res.Steps) > 0 && strings.Contains(err.Error(), "timed out") {
		// The time ran out part way down the funnel: the pages it reached are
		// real, and the landing page is what tells white from dark.
		r.logf("%s visit ran out of time after %d pages: keeping them", d.Engine, len(res.Steps))
		err = nil
	}
	if err != nil {
		if line != nil {
			r.e.lines.Failed(line.Key)
		}
		return r.record(v, res, err.Error())
	}
	if line != nil {
		r.e.lines.Worked(line.Key)
	}
	if len(res.Steps) == 0 {
		return r.record(v, res, "the visit returned no page")
	}
	return r.store(v, res, d)
}

// runnerWait puts the visit off while the browser runner cannot take it,
// and records it as an error once that has lasted too long.
func (r *run) runnerWait(v visitRow, why string) error {
	r.p.RunnerWaits++
	if r.p.RunnerWaits < maxRunnerWaits {
		return &errWait{after: runnerWaitStep, why: why}
	}
	r.p.RunnerWaits = 0
	_, err := r.record(v, nil, "the browser runner could not take a visit for ten minutes: "+why)
	return err
}

func asWait(err error) *errWait {
	var w *errWait
	if errors.As(err, &w) {
		return w
	}
	return nil
}

// store captures every page of one visit, judges the landing page against
// the white page, and queues the visit, its steps, its pages and its
// evidence.
func (r *run) store(v visitRow, res *VisitResult, d Disguise) (*visitOutcome, error) {
	pages := make([]Page, 0, len(res.Steps))
	for _, step := range res.Steps {
		pages = append(pages, capture(step, r.set.MaxPageBytes))
	}
	first := res.Steps[0]
	land := landing{URL: first.URL, Status: first.Status, Title: pages[0].Title, Hash: pages[0].ContentHash, Engine: d.Engine}

	vd := judge(r.p.White, land, r.p.WhiteStable)
	if d.IsBaseline {
		// The baseline is the white page by definition. Only a server error
		// makes it useless.
		vd = verdict{Outcome: "white", Reason: "the reviewer baseline"}
		if isErrorPage(land.Status, land.Title) {
			vd = verdict{Outcome: "error", Reason: fmt.Sprintf("the server answered %d (%s)", land.Status, land.Title)}
		}
	}
	// Once a visit is dark, every page it walked through belongs to the dark
	// funnel.
	if vd.Outcome == "dark" {
		for i := range pages {
			pages[i].IsDark = true
		}
	}

	out := &visitOutcome{
		Verdict: vd, Landing: land, Pages: pages, Steps: res.Steps, Bytes: res.Bytes,
		LineKey: v.LineKey, Place: v.Place, TargetURL: v.TargetURL,
	}
	status := int16(first.Status)
	v.Outcome = vd.Outcome
	v.StepsCount = len(res.Steps)
	v.StatusCode = &status
	v.Hops = first.Hops
	v.Bytes = res.Bytes
	v.DurationMs = res.DurationMs
	v.ExitIP = res.ExitIP
	if vd.Outcome == "error" {
		v.Error = vd.Reason
	}

	r.visited = append(r.visited, [2]string{d.Engine, vd.Outcome})
	keep := r.inv.Mode == "deep"
	evidence := r.isEvidence(vd.Outcome, first.URL)
	counted := 0
	if v.Purpose == "sample" && !d.IsBaseline {
		// Only the disguised sample visits count toward visits_done; the
		// reviewer visits made alongside them are the white side.
		counted = 1
	}
	r.countMetered(v.LineKey, res.Bytes)
	id := r.inv.ID
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		ids, err := upsertPages(ctx, tx, pages, keep)
		if err != nil {
			return err
		}
		out.pageIDs = ids
		visitID, err := insertVisit(ctx, tx, id, v, &ids[0])
		if err != nil {
			return err
		}
		for i, step := range res.Steps {
			if _, err := tx.Exec(ctx, `
				INSERT INTO raposa.step (visit_id, step_no, page_id, reached_by, clicked_text, clicked_url)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))`,
				visitID, i+1, ids[i], orDefault(step.ReachedBy, "cta"), clean(step.ClickedText), clean(step.ClickedURL)); err != nil {
				return fmt.Errorf("record step %d: %w", i+1, err)
			}
		}
		if evidence {
			if err := r.storeEvidence(ctx, tx, out, v.Device); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `
			UPDATE raposa.investigation SET visits_done = visits_done + $2, bytes_used = bytes_used + $3
			WHERE id = $1`, id, counted, res.Bytes)
		return err
	})
	return out, nil
}

// record queues a visit that never reached a page, so the ladder leaves a
// trail even when a line or the runner was not there, and returns the
// reason as the error.
func (r *run) record(v visitRow, res *VisitResult, reason string) (*visitOutcome, error) {
	v.Outcome = "error"
	v.Error = reason
	if res != nil {
		v.Bytes = res.Bytes
		v.DurationMs = res.DurationMs
		v.ExitIP = res.ExitIP
	}
	r.visited = append(r.visited, [2]string{v.Engine, "error"})
	// A failed visit on the residential line still spent metered traffic.
	r.countMetered(v.LineKey, v.Bytes)
	id := r.inv.ID
	r.do(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := insertVisit(ctx, tx, id, v, nil); err != nil {
			return err
		}
		if v.Bytes > 0 {
			_, err := tx.Exec(ctx, `UPDATE raposa.investigation SET bytes_used = bytes_used + $2 WHERE id = $1`, id, v.Bytes)
			return err
		}
		return nil
	})
	return nil, errors.New(reason)
}

func insertVisit(ctx context.Context, tx pgx.Tx, investigationID int64, v visitRow, landed *int32) (int64, error) {
	var id int64
	hops := v.Hops
	if hops == nil {
		hops = []VisitHop{}
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO raposa.visit
			(investigation_id, purpose, rung, disguise_id, attempt, line_key, place, exit_ip, device, engine,
			 link_kind, target_url, referer, outcome, landed_page_id, steps_count, status_code,
			 redirect_hops, bytes_used, duration_ms, error, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10,
		        $11, $12, $13, $14, $15, $16, $17, $18::jsonb, $19, $20, NULLIF($21, ''), $22)
		RETURNING id`,
		investigationID, v.Purpose, v.Rung, v.DisguiseID, v.Attempt, v.LineKey, v.Place, v.ExitIP, v.Device,
		v.Engine, v.LinkKind, clean(v.TargetURL), clean(v.Referer), v.Outcome, landed, v.StepsCount, v.StatusCode,
		jsonOr(hops, "[]"), v.Bytes, v.DurationMs, clean(v.Error), v.StartedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record visit: %w", err)
	}
	return id, nil
}

// takeLiveLink takes a live link served to device, in the campaign the ad
// ran in on that device. A device the ad did not run on in the last day (the
// "other device" rung) takes the ad's own campaign link, served to whichever
// device, since sending it to the device it was not served to is the point
// of that rung. nil when none is on hand.
func (r *run) takeLiveLink(ctx context.Context, device string) (*liveLink, error) {
	ad := r.ad()
	campaign := ad.CampaignByDevice[device]
	if campaign == "" {
		campaign = ad.CampaignID
	}
	anyDevice := !slices.Contains(ad.Devices, device)
	l, ok, err := r.e.store.TakeLiveLink(ctx, ad.LandingHost, campaign, device, anyDevice)
	if err != nil || !ok {
		return nil, err
	}
	return &l, nil
}

// clickReferer is the Referer of a visit on a live link: Taboola's click
// address when the link came from a Taboola publisher, which is what a
// reader's click sends; else the publisher page that served the link; else
// the publisher the investigation started from.
func (r *run) clickReferer(l liveLink, link string) string {
	if pub := findTarget(r.e.targets, l.Publisher, ""); pub != nil {
		if ref := taboolaClickReferer(*pub, link); ref != "" {
			return ref
		}
	}
	if l.PageURL != "" {
		return l.PageURL
	}
	return r.inv.PublisherReferer
}

// countMetered adds a visit's traffic to the residential budget when the
// line it took is the metered one.
func (r *run) countMetered(lineKey string, n int) {
	if n > 0 && lineKey != "" && r.e.lines.Role(lineKey) == "residential" {
		r.p.ResBytes += int64(n)
	}
}

// usableLines is the box's lines less the ones that refused this
// investigation's target.
func (r *run) usableLines() []lines.Line {
	all := r.e.lines.All()
	if len(r.p.BadLines) == 0 {
		return all
	}
	out := make([]lines.Line, 0, len(all))
	for _, l := range all {
		if !slices.Contains(r.p.BadLines, l.Key) {
			out = append(out, l)
		}
	}
	return out
}

// unburned drops the lines burned for this site or in general.
func (r *run) unburned(all []lines.Line) []lines.Line {
	if len(r.burned) == 0 {
		return all
	}
	out := make([]lines.Line, 0, len(all))
	for _, l := range all {
		if _, burned := r.burned[l.Key]; !burned {
			out = append(out, l)
		}
	}
	return out
}

func (r *run) baselineRung() *Disguise {
	for i := range r.ladder {
		if r.ladder[i].IsBaseline {
			return &r.ladder[i]
		}
	}
	return nil
}

func (r *run) rungAt(rung int16) *Disguise {
	for i := range r.ladder {
		if r.ladder[i].Rung == rung && !r.ladder[i].IsBaseline {
			return &r.ladder[i]
		}
	}
	return nil
}

// nextRung is the first rung above after that this investigation climbs: a
// quick one climbs only the rungs that cost nothing, and never one on the
// metered residential line, whatever its cost_kb says.
func (r *run) nextRung(after int16) *Disguise {
	for i := range r.ladder {
		d := &r.ladder[i]
		if d.Rung <= after || d.IsBaseline || (r.inv.Mode == "quick" && (d.CostKB > 0 || d.LineRole == "residential")) {
			continue
		}
		return d
	}
	return nil
}

// noteCheckout keeps the seller found on the dark funnel.
func (r *run) noteCheckout(out *visitOutcome) {
	if r.p.Checkout != nil {
		return
	}
	for _, p := range out.Pages {
		if p.CheckoutPlatform == "" {
			continue
		}
		r.p.Checkout = map[string]any{
			"platform":   p.CheckoutPlatform,
			"merchantId": p.CheckoutMerchantID,
			"url":        p.URL,
			"title":      p.Title,
			"detectedAt": time.Now().UTC(),
		}
		return
	}
}

// side summarises one visit for the screens.
func (o *visitOutcome) side(d Disguise) sideData {
	first := o.Pages[0]
	s := sideData{
		URL:              o.TargetURL,
		FinalURL:         first.URL,
		Domain:           page.ExtractCanonicalDomain(first.URL),
		Title:            first.Title,
		MetaDescription:  first.MetaTags["description"],
		PageKind:         first.PageKind,
		CheckoutPlatform: first.CheckoutPlatform,
		BodySnippet:      truncate(first.BodyText, 1000),
		HTMLLength:       first.HTMLBytes,
		LineKey:          o.LineKey,
		Place:            o.Place,
		Disguise:         d.Code,
		DisguiseName:     d.Name,
	}
	for i, p := range o.Pages {
		step := stepData{StepNo: i + 1, URL: p.URL, Title: p.Title, PageKind: p.PageKind}
		if i < len(o.Steps) {
			step.ReachedBy = o.Steps[i].ReachedBy
			step.ClickedText = o.Steps[i].ClickedText
		}
		s.FunnelSteps = append(s.FunnelSteps, step)
	}
	return s
}

// visitBudget is how long one visit may take. A browser loads scripts and
// sometimes video, so it gets far longer than a plain fetch.
func visitBudget(d Disguise) time.Duration {
	if d.Engine == "browser" {
		return 150 * time.Second
	}
	return 90 * time.Second
}

// commit writes the step: its visits and what follows from them, the log,
// the events, the progress and the next visit, and lets go of the claim. It
// writes nothing when the claim is no longer this worker's.
func (r *run) commit(ctx context.Context) error {
	progress, err := json.Marshal(r.p)
	if err != nil {
		return fmt.Errorf("encode progress: %w", err)
	}
	tx, err := r.e.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, o := range r.ops {
		if err := o(ctx, tx); err != nil {
			return err
		}
	}
	if len(r.logs) > 0 {
		clean := make([]string, len(r.logs))
		for i, l := range r.logs {
			clean[i] = cleanLog(l)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO raposa.log (investigation_id, line)
			SELECT $1, line FROM unnest($2::text[]) WITH ORDINALITY AS t(line, n) ORDER BY n`, r.inv.ID, clean); err != nil {
			return fmt.Errorf("write log: %w", err)
		}
	}
	for _, ev := range r.events {
		if _, err := tx.Exec(ctx, `SELECT raposa.emit($1, $2, $3, $4)`, r.inv.ID, ev[0], cleanLog(ev[1]), cleanLog(ev[2])); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
	var stage, note *string
	if r.stage != "" {
		stage, note = &r.stage, &r.stageNote
	}
	tag, err := tx.Exec(ctx, `
		UPDATE raposa.investigation
		SET progress = $3::jsonb,
		    stage = COALESCE($4, stage), stage_note = COALESCE($5, stage_note),
		    rung_reached = GREATEST(rung_reached, $6),
		    next_visit_at = now() + $7::interval,
		    claimed_by = NULL, claim_token = NULL, claimed_until = NULL
		WHERE id = $1 AND claim_token = $2`,
		r.inv.ID, r.inv.Token, string(progress), stage, note, r.rung, interval(r.next))
	if err != nil {
		return fmt.Errorf("write progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errLeaseLost
	}
	return tx.Commit(ctx)
}

// errLeaseLost says another worker took the investigation over: this step
// took longer than its lease. What it did is dropped; the visit runs again.
var errLeaseLost = errors.New("the claim ran out before the visit was written")

func cleanLog(s string) string { return truncate(clean(s), 4000) }
