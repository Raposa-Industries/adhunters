package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// StepSplit is one page met at one step of the dark funnel, with its share
// of the sample visits that reached that step.
type StepSplit struct {
	Step       int16
	PageID     int32
	Title      string
	Kind       string
	Address    string
	Visits     int32
	StepVisits int32
	Share      float64
	// How the share moved since the day before, on the days page: new,
	// +12, -5 or =. Empty on the first day.
	Change string
}

// StepGroup is one step and the pages met there.
type StepGroup struct {
	Step  int16
	Kind  string
	Pages []StepSplit
}

// VideoLink is one video player address a page of the investigation loads.
type VideoLink struct {
	URL    string
	PageID int32
	Title  string
	Dark   bool
}

// FollowInfo is the follow an investigation started, or belongs to.
type FollowInfo struct {
	ID          int64
	Root        int64
	Days, Done  int16
	EveryHours  int16
	NextAt      time.Time
	By          string
	Ended       *time.Time
	Investigate []int64
}

// stepKinds names the kinds of page in the order a funnel goes through
// them, for the headings of the step splits.
var stepKinds = map[string]string{
	"advertorial": "advertoriais", "vsl": "VSLs", "checkout": "checkouts", "quiz": "quizzes",
	"article": "artigos", "error": "páginas que não abriram", "unknown": "páginas",
}

func (s *Server) stepSplits(ctx context.Context, id int64) ([]StepSplit, error) {
	rows, _ := s.db.Query(ctx, `
		SELECT step_no, page_id, COALESCE(title, ''), COALESCE(page_kind, 'unknown'), address, visits, step_visits,
		       share_pct::float8
		FROM raposa.step_split WHERE investigation_id = $1
		ORDER BY step_no, visits DESC, page_id`, id)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StepSplit, error) {
		var x StepSplit
		return x, r.Scan(&x.Step, &x.PageID, &x.Title, &x.Kind, &x.Address, &x.Visits, &x.StepVisits, &x.Share)
	})
}

// Grouped puts the splits under their step, each step named after the kind
// of page most of its visits met.
func Grouped(splits []StepSplit) []StepGroup {
	var out []StepGroup
	for _, x := range splits {
		if len(out) == 0 || out[len(out)-1].Step != x.Step {
			kind := stepKinds[x.Kind]
			if kind == "" {
				kind = "páginas"
			}
			out = append(out, StepGroup{Step: x.Step, Kind: kind})
		}
		out[len(out)-1].Pages = append(out[len(out)-1].Pages, x)
	}
	return out
}

// videos lists the video players the pages of these investigations load,
// the dark ones first.
func (s *Server) videos(ctx context.Context, ids []int64) ([]VideoLink, error) {
	rows, _ := s.db.Query(ctx, `
		SELECT DISTINCT ON (l) l, p.id, COALESCE(p.title, p.host || p.path), p.is_dark
		FROM raposa.visit v
		JOIN raposa.step st ON st.visit_id = v.id
		JOIN raposa.page p ON p.id = st.page_id
		CROSS JOIN LATERAL unnest(p.video_links) l
		WHERE v.investigation_id = ANY ($1)
		ORDER BY l, p.is_dark DESC, p.id`, ids)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (VideoLink, error) {
		var x VideoLink
		return x, r.Scan(&x.URL, &x.PageID, &x.Title, &x.Dark)
	})
	if err != nil {
		return nil, err
	}
	// The dark pages' players first: those are the VSLs the reviewer never sees.
	dark := list[:0:0]
	other := []VideoLink{}
	for _, x := range list {
		if x.Dark {
			dark = append(dark, x)
		} else {
			other = append(other, x)
		}
	}
	return append(dark, other...), nil
}

// followOf reads the follow this investigation started (root) or belongs to.
func (s *Server) followOf(ctx context.Context, id int64, root *int64) (*FollowInfo, error) {
	r := id
	if root != nil {
		r = *root
	}
	var f FollowInfo
	err := s.db.QueryRow(ctx, `
		SELECT id, investigation_id, days, done, every_hours, next_at, created_by, ended_at
		FROM raposa.follow WHERE investigation_id = $1 ORDER BY id DESC LIMIT 1`, r).
		Scan(&f.ID, &f.Root, &f.Days, &f.Done, &f.EveryHours, &f.NextAt, &f.By, &f.Ended)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// follow sets an investigation to run again every 24 hours for 3 to 5 days
// (the form picks), each run a deep one that starts at the rung that broke
// through. Following again while a follow is open changes nothing.
func (s *Server) follow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	days, err := strconv.Atoi(r.FormValue("days"))
	if err != nil || days < 1 || days > 7 {
		http.Error(w, "pick how many days to follow, 1 to 7", http.StatusBadRequest)
		return
	}
	every := 24
	if h := r.FormValue("every"); h != "" {
		if every, err = strconv.Atoi(h); err != nil || every < 1 || every > 168 {
			http.Error(w, "every is a number of hours, 1 to 168", http.StatusBadRequest)
			return
		}
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO raposa.follow (investigation_id, creative_id, days, every_hours, next_at, created_by)
		SELECT id, creative_id, $2::smallint, $3::smallint, COALESCE(started_at, requested_at) + make_interval(hours => $3::int), $4
		FROM raposa.investigation WHERE id = $1
		ON CONFLICT (investigation_id) WHERE ended_at IS NULL DO NOTHING`, id, days, every, who(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, s.Base+fmt.Sprintf("/i/%d", id), http.StatusSeeOther)
}

func (s *Server) endFollow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var root int64
	err := s.db.QueryRow(r.Context(), `UPDATE raposa.follow SET ended_at = now() WHERE id = $1 AND ended_at IS NULL
		RETURNING investigation_id`, id).Scan(&root)
	if err == pgx.ErrNoRows {
		http.Redirect(w, r, s.Base+"/", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, s.Base+fmt.Sprintf("/i/%d", root), http.StatusSeeOther)
}

// Day is one run of a followed funnel.
type Day struct {
	Day         int16
	Row         Row
	Started     *time.Time
	Sightings   *int64
	DarkPct     float64
	Steps       []StepSplit
	Changes     []Change
	WhiteVisits int
	DarkVisits  int
}

// Change is one thing that moved from one day to the next, as the days page
// writes it: "Etapa 1 «Nurse reveals»: 33% → 67%". From and To are empty
// when the change is a page that came or went; Up says the share rose.
type Change struct {
	What     string
	From, To string
	Up       bool
}

func (c Change) String() string {
	if c.To == "" {
		return c.What
	}
	return c.What + ": " + c.From + " → " + c.To
}

// DaysPage is the days page: the follow, each day, and the days with
// splits laid out in two columns as the screen draws them (the first run
// and every second day on the left, the others and the videos on the right).
type DaysPage struct {
	Root    int64
	Follow  *FollowInfo
	Days    []Day
	Columns [2][]Day
	Videos  []VideoLink
	// Sightings says whether Tracks could be read for any day: the column
	// "Anúncio visto" is left out when it could not.
	Sightings bool
}

// days compares the runs of a followed funnel, day by day: how often the ad
// was seen, how many visits got past the white page, and how each step's
// split moved since the day before.
func (s *Server) days(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// The investigation followed: this one, or the one its follow follows.
	var root int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE((SELECT investigation_id FROM raposa.follow f WHERE f.id = i.follow_id), i.id)
		FROM raposa.investigation i WHERE id = $1`, id).Scan(&root)
	if err != nil {
		s.fail(w, err)
		return
	}
	follow, err := s.followOf(ctx, root, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The followed run is day 0; each day after is its latest attempt.
	rows, _ := s.db.Query(ctx, `
		SELECT 0::smallint, `+rowColumns+`, started_at FROM raposa.investigation WHERE id = $1
		UNION ALL
		SELECT * FROM (
			SELECT DISTINCT ON (i.follow_day) i.follow_day, `+qualified("i")+`, i.started_at
			FROM raposa.investigation i JOIN raposa.follow f ON f.id = i.follow_id
			WHERE f.investigation_id = $1
			ORDER BY i.follow_day, i.id DESC) d
		ORDER BY 1`, root)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Day, error) {
		var d Day
		vals := []any{&d.Day}
		x := &d.Row
		vals = append(vals, &x.ID, &x.CreativeID, &x.AdID, &x.Mode, &x.Origin, &x.Status, &x.Stage, &x.StageNote,
			&x.VisitsDone, &x.VisitsTarget, &x.Variants, &x.Cloaked, &x.Confidence, &x.BreachRung, &x.RequestedBy,
			&x.RequestedAt, &x.CompletedAt, &d.Started)
		return d, r.Scan(vals...)
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(list) == 0 {
		http.NotFound(w, r)
		return
	}
	ids := make([]int64, len(list))
	for i := range list {
		d := &list[i]
		ids[i] = d.Row.ID
		if d.Steps, err = s.stepSplits(ctx, d.Row.ID); err != nil {
			s.fail(w, err)
			return
		}
		if err := s.db.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE v.outcome = 'white'), count(*) FILTER (WHERE v.outcome = 'dark')
			FROM raposa.visit v JOIN raposa.disguise g ON g.id = v.disguise_id AND NOT g.is_baseline
			WHERE v.investigation_id = $1 AND v.purpose = 'sample'`, d.Row.ID).Scan(&d.WhiteVisits, &d.DarkVisits); err != nil {
			s.fail(w, err)
			return
		}
		if n := d.WhiteVisits + d.DarkVisits; n > 0 {
			d.DarkPct = float64(d.DarkVisits) * 100 / float64(n)
		}
		d.Sightings = s.sightings(ctx, d.Row.CreativeID, d.Started)
		if i > 0 {
			d.Changes = compareDays(&list[i-1], d)
		}
	}
	videos, err := s.videos(ctx, ids)
	if err != nil {
		s.fail(w, err)
		return
	}
	page := DaysPage{Root: root, Follow: follow, Days: list, Videos: videos}
	n := 0
	for _, d := range list {
		if d.Sightings != nil {
			page.Sightings = true
		}
		if len(d.Steps) > 0 {
			page.Columns[n%2] = append(page.Columns[n%2], d)
			n++
		}
	}
	s.render(w, r, "days.html", page)
}

// qualified is rowColumns read from the alias a.
func qualified(a string) string {
	return a + `.id, ` + a + `.creative_id, ` + a + `.ad_id, ` + a + `.mode, ` + a + `.origin, ` + a + `.status, ` +
		a + `.stage, ` + a + `.stage_note, ` + a + `.visits_done, ` + a + `.visits_target, ` + a + `.variants_count, ` +
		a + `.is_cloaked, ` + a + `.cloaked_confidence::float8, ` + a + `.breach_rung, ` + a + `.requested_by, ` +
		a + `.requested_at, ` + a + `.completed_at`
}

// sightings is how often Tracks saw the creative on the day a run started,
// or nil when Tracks cannot be read from here.
func (s *Server) sightings(ctx context.Context, creative int32, at *time.Time) *int64 {
	if at == nil {
		return nil
	}
	var n *int64
	err := s.db.QueryRow(ctx, `
		SELECT CASE WHEN to_regclass('tracks_api.ad_daily_v1') IS NOT NULL THEN
		       (SELECT sum(sightings)::bigint FROM tracks_api.ad_daily_v1 WHERE creative_id = $1 AND day = ($2::timestamptz AT TIME ZONE 'UTC')::date)
		       END`, creative, *at).Scan(&n)
	if err != nil {
		s.log.Warn("sightings for the days page", "creative", creative, "err", err)
		return nil
	}
	return n
}

// compareDays says what moved from one run to the next: the share of visits
// past the white page, and each step's pages that came, went or moved by 5
// points or more. It also fills each page's share the day before.
func compareDays(prev, cur *Day) []Change {
	var out []Change
	pct := func(v float64) string { return fmt.Sprintf("%.0f%%", v) }
	if prev.WhiteVisits+prev.DarkVisits > 0 && cur.WhiteVisits+cur.DarkVisits > 0 {
		if d := cur.DarkPct - prev.DarkPct; d >= 5 || d <= -5 {
			out = append(out, Change{What: "Passaram da página branca", From: pct(prev.DarkPct), To: pct(cur.DarkPct), Up: d > 0})
		}
	}
	type key struct {
		step  int16
		title string
		addr  string
	}
	before := map[key]float64{}
	for _, x := range prev.Steps {
		before[key{x.Step, x.Title, x.Address}] = x.Share
	}
	seen := map[key]bool{}
	for i := range cur.Steps {
		x := &cur.Steps[i]
		k := key{x.Step, x.Title, x.Address}
		seen[k] = true
		was, ok := before[k]
		switch {
		case !ok && len(prev.Steps) > 0:
			x.Change = "nova"
			out = append(out, Change{What: fmt.Sprintf("Etapa %d «%s» apareceu com %.0f%%", x.Step, label(x), x.Share)})
		case ok:
			x.Change = signed(was, x.Share)
			if d := x.Share - was; d >= 5 || d <= -5 {
				out = append(out, Change{What: fmt.Sprintf("Etapa %d «%s»", x.Step, label(x)), From: pct(was), To: pct(x.Share), Up: d > 0})
			}
		}
	}
	for _, x := range prev.Steps {
		k := key{x.Step, x.Title, x.Address}
		if !seen[k] && len(cur.Steps) > 0 {
			out = append(out, Change{What: fmt.Sprintf("Etapa %d «%s» (%.0f%% na véspera) não apareceu", x.Step, label(&x), x.Share)})
		}
	}
	if len(prev.Steps) > 0 && len(cur.Steps) == 0 && cur.Row.Status == "completed" {
		out = append(out, Change{What: "Nenhuma visita passou da página branca neste dia"})
	}
	return out
}

func label(x *StepSplit) string {
	if x.Title != "" {
		return x.Title
	}
	return x.Address
}

// Cloaked is one investigation that got past the white page, for the
// summary.
type Cloaked struct {
	Row
	Domain, Title, Kind, Checkout string
	WhiteDomain                   string
	Videos                        []VideoLink
}

// cloaked lists every investigation that got past the white page in the
// last days (1 by default): the cloaked-ads report, one line per ad, to
// read or print.
func (s *Server) cloaked(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days := 1
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d >= 1 && d <= 90 {
		days = d
	}
	rows, _ := s.db.Query(ctx, `SELECT `+rowColumns+`,
			COALESCE(raposa_data->>'domain', ''), COALESCE(raposa_data->>'title', ''), COALESCE(raposa_data->>'pageKind', ''),
			COALESCE(checkout_data->>'platform', raposa_data->>'checkoutPlatform', ''), COALESCE(reviewer_data->>'domain', '')
		FROM raposa.investigation
		WHERE is_cloaked AND completed_at > now() - make_interval(days => $1)
		ORDER BY completed_at DESC LIMIT 500`, days)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Cloaked, error) {
		var c Cloaked
		return c, scanRow(r, &c.Row, &c.Domain, &c.Title, &c.Kind, &c.Checkout, &c.WhiteDomain)
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	for i := range list {
		vs, err := s.videos(ctx, []int64{list[i].ID})
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, v := range vs {
			if v.Dark {
				list[i].Videos = append(list[i].Videos, v)
			}
		}
	}
	s.render(w, r, "cloaked.html", cloakedPage(list, days))
}

// weekdays are the days' short names in Portuguese, Sunday first.
var weekdays = [7]string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}

// day is a date, for the days page: sex 02/10.
func day(t any) string {
	var v time.Time
	switch x := t.(type) {
	case time.Time:
		v = x
	case *time.Time:
		if x == nil {
			return ""
		}
		v = *x
	default:
		return ""
	}
	v = v.UTC()
	return weekdays[v.Weekday()] + " " + v.Format("02/01")
}

// signed is a change in points, with its sign.
func signed(before, now float64) string {
	d := now - before
	switch {
	case d >= 0.5:
		return fmt.Sprintf("+%.0f", d)
	case d <= -0.5:
		return fmt.Sprintf("%.0f", d)
	}
	return "="
}
