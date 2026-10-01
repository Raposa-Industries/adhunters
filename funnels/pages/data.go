package pages

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Windows are the periods a page can show, in page order. Days are UTC
// days, as the hours are.
var Windows = []string{"today", "yesterday", "7d", "30d"}

var windowNames = map[string]string{"today": "Hoje", "yesterday": "Ontem", "7d": "7 dias", "30d": "30 dias"}

// span is a window's hours: [from, to).
func span(w string, now time.Time) (time.Time, time.Time) {
	day := now.UTC().Truncate(24 * time.Hour)
	end := now.UTC().Truncate(time.Hour).Add(time.Hour)
	switch w {
	case "today":
		return day, end
	case "yesterday":
		return day.AddDate(0, 0, -1), day
	case "30d":
		return day.AddDate(0, 0, -29), end
	}
	return day.AddDate(0, 0, -6), end
}

// counts reads a count table: the closed hours' rows and, for the hours
// still open, their drafts. An hour's drafts go when it closes, so the two
// never hold the same hour.
func counts(table string) string {
	return "(SELECT * FROM funnels." + table + " UNION ALL SELECT * FROM funnels_draft." + table + ")"
}

// journeys reads every journey, the drafts of open hours included.
const journeys = "(SELECT *, false AS draft FROM funnels.journey UNION ALL SELECT *, true FROM funnels_draft.journey)"

// Filter narrows a page's counts.
type Filter struct {
	From, To time.Time
	Device   string // phone, tablet, desktop, or "" for all
	Sub1     string // a Taboola campaign, or "" for all
}

// where adds the filter's conditions; args holds $1, $2 already.
func (f Filter) where(alias string, args *[]any) string {
	var b strings.Builder
	if f.Device != "" {
		*args = append(*args, f.Device)
		b.WriteString(" AND " + alias + "device = $" + itoa(int64(len(*args))))
	}
	if f.Sub1 != "" {
		*args = append(*args, f.Sub1)
		b.WriteString(" AND " + alias + "sub1 = $" + itoa(int64(len(*args))))
	}
	return b.String()
}

type store struct{ db *pgxpool.Pool }

// Freshness says how current a window's numbers are.
type Freshness struct {
	LastClosed *time.Time // the last hour whose counts are final
	OpenHours  int        // hours in the window counted as drafts
	DraftAt    *time.Time // when the newest draft was counted
}

func (s store) freshness(ctx context.Context, f Filter) (Freshness, error) {
	var fr Freshness
	err := s.db.QueryRow(ctx, `
		SELECT (SELECT max(hour) FROM funnels.hour_state WHERE closed_at IS NOT NULL),
		       count(*) FILTER (WHERE closed_at IS NULL AND draft_at IS NOT NULL),
		       max(draft_at) FILTER (WHERE closed_at IS NULL)
		FROM funnels.hour_state WHERE hour >= $1 AND hour < $2`, f.From, f.To).Scan(&fr.LastClosed, &fr.OpenHours, &fr.DraftAt)
	return fr, err
}

// Traffic is journeys and what they did, for one site or landing page.
type Traffic struct {
	Site, LP    string
	Journeys    int64
	WithClickID int64
	WithInput   int64
	VisibleMS   int64
	Bots        int64
}

// AvgSeconds is the average time in view per journey.
func (t Traffic) AvgSeconds() float64 {
	if t.Journeys == 0 {
		return 0
	}
	return float64(t.VisibleMS) / float64(t.Journeys) / 1000
}

// BotShare is the share of all journeys flagged as bots.
func (t Traffic) BotShare() float64 {
	if t.Journeys+t.Bots == 0 {
		return 0
	}
	return float64(t.Bots) / float64(t.Journeys+t.Bots)
}

// traffic sums journeys by site (lp empty) or, for one site, by first
// landing page.
func (s store) traffic(ctx context.Context, f Filter, site string) ([]Traffic, error) {
	args := []any{f.From, f.To}
	group, sel := "site", "site, ''"
	cond := f.where("", &args)
	if site != "" {
		args = append(args, site)
		cond += " AND site = $" + itoa(int64(len(args)))
		group, sel = "site, first_lp", "site, first_lp"
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+sel+`, sum(journeys), sum(with_click_id), sum(with_input), sum(visible_ms), sum(bots)
		FROM `+counts("journey_hourly")+` c
		WHERE hour >= $1 AND hour < $2`+cond+`
		GROUP BY `+group+` ORDER BY sum(journeys) DESC, `+group, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Traffic, error) {
		var t Traffic
		err := r.Scan(&t.Site, &t.LP, &t.Journeys, &t.WithClickID, &t.WithInput, &t.VisibleMS, &t.Bots)
		return t, err
	})
}

// StepRow is one step of a landing page: how many journeys reached it and
// how many stopped there.
type StepRow struct {
	LP, Step         string
	Reached, Stopped int64
	Of               int64 // the page's views, for the share
}

// Share is the step's reach against the page's views.
func (r StepRow) Share() float64 {
	if r.Of == 0 {
		return 0
	}
	return float64(r.Reached) / float64(r.Of)
}

// DropShare is how many of those who reached it stopped there.
func (r StepRow) DropShare() float64 {
	if r.Reached == 0 {
		return 0
	}
	return float64(r.Stopped) / float64(r.Reached)
}

// steps reads a site's steps, per landing page, in funnel order: view
// first, then by reach.
func (s store) steps(ctx context.Context, f Filter, site, lp string) (map[string][]StepRow, []string, error) {
	args := []any{f.From, f.To, site}
	cond := f.where("", &args)
	if lp != "" {
		args = append(args, lp)
		cond += " AND lp = $" + itoa(int64(len(args)))
	}
	rows, err := s.db.Query(ctx, `
		SELECT lp, step, sum(reached), sum(stopped)
		FROM `+counts("step_hourly")+` c
		WHERE hour >= $1 AND hour < $2 AND site = $3`+cond+`
		GROUP BY lp, step`, args...)
	if err != nil {
		return nil, nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StepRow, error) {
		var x StepRow
		err := r.Scan(&x.LP, &x.Step, &x.Reached, &x.Stopped)
		return x, err
	})
	if err != nil {
		return nil, nil, err
	}
	by := map[string][]StepRow{}
	views := map[string]int64{}
	for _, x := range list {
		by[x.LP] = append(by[x.LP], x)
		if x.Step == "view" {
			views[x.LP] = x.Reached
		}
	}
	var lps []string
	for k, l := range by {
		lps = append(lps, k)
		for i := range l {
			l[i].Of = views[k]
		}
		sort.SliceStable(l, func(i, j int) bool { return stepBefore(l[i], l[j]) })
	}
	sort.Slice(lps, func(i, j int) bool {
		if views[lps[i]] != views[lps[j]] {
			return views[lps[i]] > views[lps[j]]
		}
		return lps[i] < lps[j]
	})
	return by, lps, nil
}

// stepBefore orders a funnel: the view, scroll marks and the 10 s stay in
// their natural order, then the rest by reach.
func stepBefore(a, b StepRow) bool {
	ra, rb := stepRank(a.Step), stepRank(b.Step)
	if ra != rb {
		return ra < rb
	}
	if a.Reached != b.Reached {
		return a.Reached > b.Reached
	}
	return a.Step < b.Step
}

func stepRank(s string) int {
	switch s {
	case "view":
		return 0
	case "scroll25":
		return 1
	case "stay10s":
		return 2
	case "scroll50":
		return 3
	case "scroll75":
		return 4
	case "scroll100":
		return 5
	}
	return 6
}

// CampaignRow is one Taboola campaign's journeys on a landing page and how
// far they went.
type CampaignRow struct {
	Sub1    string
	Views   int64
	Reached []int64 // per column step
}

// byCampaign splits a landing page's steps by Taboola campaign (sub1), for
// the given steps.
func (s store) byCampaign(ctx context.Context, f Filter, site, lp string, cols []string) ([]CampaignRow, error) {
	args := []any{f.From, f.To, site, lp, append([]string{"view"}, cols...)}
	cond := f.where("", &args)
	rows, err := s.db.Query(ctx, `
		SELECT sub1, step, sum(reached)
		FROM `+counts("step_hourly")+` c
		WHERE hour >= $1 AND hour < $2 AND site = $3 AND lp = $4 AND step = ANY($5)`+cond+`
		GROUP BY sub1, step`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*CampaignRow{}
	for rows.Next() {
		var sub1, step string
		var n int64
		if err := rows.Scan(&sub1, &step, &n); err != nil {
			return nil, err
		}
		c := by[sub1]
		if c == nil {
			c = &CampaignRow{Sub1: sub1, Reached: make([]int64, len(cols))}
			by[sub1] = c
		}
		if step == "view" {
			c.Views = n
			continue
		}
		for i, col := range cols {
			if col == step {
				c.Reached[i] = n
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CampaignRow, 0, len(by))
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Views != out[j].Views {
			return out[i].Views > out[j].Views
		}
		return out[i].Sub1 < out[j].Sub1
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

// campaigns lists the Taboola campaigns (sub1) that sent journeys to a
// site, most first, for the filter column.
func (s store) campaigns(ctx context.Context, f Filter, site string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sub1 FROM `+counts("journey_hourly")+` c
		WHERE hour >= $1 AND hour < $2 AND site = $3 AND sub1 <> ''
		GROUP BY sub1 ORDER BY sum(journeys) DESC, sub1 LIMIT 30`, f.From, f.To, site)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// VideoRow is one video (or one arm of it) over a window.
type VideoRow struct {
	Video, Arm, Device string
	Loads              int64
	Autoplays          int64
	Plays              int64
	WatchedS           int64
	LenS               int64
	ReachedPitch       int64
}

// PlayShare is the share of loads that someone played.
func (v VideoRow) PlayShare() float64 { return ratio(v.Plays, v.Loads) }

// PitchShare is the share of plays that heard the pitch.
func (v VideoRow) PitchShare() float64 { return ratio(v.ReachedPitch, v.Plays) }

// AvgWatched is the seconds heard per play.
func (v VideoRow) AvgWatched() float64 { return ratio(v.WatchedS, v.Plays) }

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// videos sums videos over a window, by video and, when by is "arm" or
// "device", by that too. video narrows to one.
func (s store) videos(ctx context.Context, f Filter, video, by string) ([]VideoRow, error) {
	args := []any{f.From, f.To}
	cond := f.where("", &args)
	if video != "" {
		args = append(args, video)
		cond += " AND video = $" + itoa(int64(len(args)))
	}
	arm, dev := "''", "''"
	group := "video"
	switch by {
	case "arm":
		arm, group = "arm", "video, arm"
	case "device":
		dev, group = "device", "video, device"
	}
	rows, err := s.db.Query(ctx, `
		SELECT video, `+arm+`, `+dev+`, sum(loads), sum(autoplays), sum(plays), sum(watched_s), max(len_s), sum(reached_pitch)
		FROM `+counts("video_hourly")+` c
		WHERE hour >= $1 AND hour < $2`+cond+`
		GROUP BY `+group+` ORDER BY sum(loads) DESC, `+group, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (VideoRow, error) {
		var v VideoRow
		err := r.Scan(&v.Video, &v.Arm, &v.Device, &v.Loads, &v.Autoplays, &v.Plays, &v.WatchedS, &v.LenS, &v.ReachedPitch)
		return v, err
	})
}

// Curve is one arm's retention: for each second, the plays that heard it.
type Curve struct {
	Arm   string
	Plays int64
	At    []int64 // index is the second
}

// curves reads a video's retention per arm. The device filter applies; the
// campaign filter cannot, since the curve is not kept per campaign.
func (s store) curves(ctx context.Context, f Filter, video string, plays map[string]int64) ([]Curve, error) {
	args := []any{f.From, f.To, video}
	cond := ""
	if f.Device != "" {
		args = append(args, f.Device)
		cond = " AND device = $4"
	}
	rows, err := s.db.Query(ctx, `
		SELECT arm, second, sum(watching)
		FROM `+counts("video_second_hourly")+` c
		WHERE hour >= $1 AND hour < $2 AND video = $3`+cond+` AND second < 21600
		GROUP BY arm, second ORDER BY arm, second`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*Curve{}
	var order []string
	for rows.Next() {
		var arm string
		var sec int
		var n int64
		if err := rows.Scan(&arm, &sec, &n); err != nil {
			return nil, err
		}
		c := by[arm]
		if c == nil {
			c = &Curve{Arm: arm, Plays: plays[arm]}
			by[arm] = c
			order = append(order, arm)
		}
		for len(c.At) <= sec {
			c.At = append(c.At, 0)
		}
		c.At[sec] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Curve, 0, len(order))
	for _, a := range order {
		out = append(out, *by[a])
	}
	return out, nil
}

// JourneyView is one journey with its steps and videos.
type JourneyView struct {
	ID, Site, FirstLP, LastLP, LastStep string
	ClickID, Sub1, Sub4, Sub8           string
	Device, Country, BotReason          string
	StartedAt, LastAt                   time.Time
	LPs                                 int
	VisibleMS                           int64
	MaxScroll                           int
	HadInput, BotSuspect, Draft         bool
	Steps                               []JourneyStep
	Videos                              []JourneyVideo
}

// JourneyStep is a step a journey reached.
type JourneyStep struct {
	Seq      int
	LP, Step string
	At       time.Time
}

// JourneyVideo is what a journey did with one video.
type JourneyVideo struct {
	Video, Arm, LP          string
	LenS, WatchedS, LastS   int
	PitchS                  *int
	Autoplayed, Played, Hit bool
}

// findJourneys looks journeys up by tracker click id or journey id.
func (s store) findJourneys(ctx context.Context, q string) ([]JourneyView, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, site, first_lp, last_lp, last_step, clickid, sub1, sub4, sub8, device, country, bot_reason,
		       started_at, last_at, lps, visible_ms, max_scroll, had_input, bot_suspect, draft
		FROM `+journeys+` j WHERE clickid = $1 OR id = $1
		ORDER BY started_at DESC LIMIT 20`, q)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (JourneyView, error) {
		var j JourneyView
		err := r.Scan(&j.ID, &j.Site, &j.FirstLP, &j.LastLP, &j.LastStep, &j.ClickID, &j.Sub1, &j.Sub4, &j.Sub8,
			&j.Device, &j.Country, &j.BotReason, &j.StartedAt, &j.LastAt, &j.LPs, &j.VisibleMS, &j.MaxScroll,
			&j.HadInput, &j.BotSuspect, &j.Draft)
		return j, err
	})
	if err != nil {
		return nil, err
	}
	for i := range list {
		j := &list[i]
		schema := "funnels"
		if j.Draft {
			schema = "funnels_draft"
		}
		rows, err := s.db.Query(ctx, `SELECT seq, lp, step, at FROM `+schema+`.journey_step WHERE journey = $1 ORDER BY seq`, j.ID)
		if err != nil {
			return nil, err
		}
		if j.Steps, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (JourneyStep, error) {
			var x JourneyStep
			err := r.Scan(&x.Seq, &x.LP, &x.Step, &x.At)
			return x, err
		}); err != nil {
			return nil, err
		}
		rows, err = s.db.Query(ctx, `
			SELECT video, arm, lp, len_s, watched_s, last_s, pitch_s, autoplayed, played, reached_pitch
			FROM `+schema+`.journey_video WHERE journey = $1 ORDER BY video`, j.ID)
		if err != nil {
			return nil, err
		}
		if j.Videos, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (JourneyVideo, error) {
			var x JourneyVideo
			err := r.Scan(&x.Video, &x.Arm, &x.LP, &x.LenS, &x.WatchedS, &x.LastS, &x.PitchS, &x.Autoplayed, &x.Played, &x.Hit)
			return x, err
		}); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// Found is one ⌘K result.
type Found struct {
	Title string `json:"title"`
	Sub   string `json:"sub"`
	Href  string `json:"href"`
}

// search finds sites, landing pages and videos seen in the last 30 days by
// name, and a journey by its exact click id.
func (s store) search(ctx context.Context, q string, now time.Time) ([]Found, error) {
	from := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -29)
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(q)) + "%"
	rows, err := s.db.Query(ctx, `
		(SELECT 'site', site, '' FROM `+counts("journey_hourly")+` c WHERE hour >= $1 AND lower(site) LIKE $2 GROUP BY site ORDER BY sum(journeys) DESC LIMIT 5)
		UNION ALL
		(SELECT 'lp', site, lp FROM `+counts("step_hourly")+` c WHERE hour >= $1 AND step = 'view' AND lower(lp) LIKE $2 GROUP BY site, lp ORDER BY sum(reached) DESC LIMIT 8)
		UNION ALL
		(SELECT 'video', video, '' FROM `+counts("video_hourly")+` c WHERE hour >= $1 AND lower(video) LIKE $2 GROUP BY video ORDER BY sum(loads) DESC LIMIT 5)
		UNION ALL
		(SELECT 'journey', id, clickid FROM `+journeys+` j WHERE clickid = $3 OR id = $3 LIMIT 3)`, from, like, q)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Found, error) {
		var kind, a, b string
		if err := r.Scan(&kind, &a, &b); err != nil {
			return Found{}, err
		}
		switch kind {
		case "site":
			return Found{Title: a, Sub: "Site", Href: sitePath(a)}, nil
		case "lp":
			return Found{Title: b, Sub: "Landing page · " + a, Href: lpPath(a, b)}, nil
		case "video":
			return Found{Title: a, Sub: "Vídeo", Href: videoPath(a)}, nil
		}
		sub := "Jornada"
		if b != "" {
			sub += " · click id " + b
		}
		return Found{Title: a, Sub: sub, Href: "/funnels/journeys?q=" + url.QueryEscape(a)}, nil
	})
}
