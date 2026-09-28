package engine

import (
	"time"
)

// Progress is everything the next visit of an investigation needs, kept in
// raposa.investigation.progress between visits. A restart, or another
// worker, picks the investigation up at its next visit from here.
type Progress struct {
	// Where it is: "" (not started), baseline, baseline2 (the second load of
	// the white page), climb, sample, twin (the reviewer visit that goes with
	// each sample visit).
	Phase string     `json:"phase"`
	Ad    *AdContext `json:"ad,omitempty"`

	// The reviewer baseline: lines tried, lines that refused the target, the
	// white page and whether it hashed the same twice in a row.
	BaselineTries int       `json:"baseline_tries,omitempty"`
	BadLines      []string  `json:"bad_lines,omitempty"`
	White         landing   `json:"white"`
	WhiteStable   bool      `json:"white_stable"`
	WhiteSide     *sideData `json:"white_side,omitempty"`

	// The climb: the rung being tried and its attempt, and what it saw.
	Rung        int16 `json:"rung"`
	Attempt     int   `json:"attempt"`
	RungTries   int   `json:"rung_tries"`
	RungDark    int   `json:"rung_dark"`
	LadderLoads int   `json:"ladder_loads"`
	RungsTried  int   `json:"rungs_tried"`
	LastRung    int16 `json:"last_rung"`

	// The rung that broke through, the device it did it on, and every visit
	// on it (ladder attempts included) with how many saw a dark page. The
	// confidence is this ratio and nothing else.
	Breach        *Disguise      `json:"breach,omitempty"`
	BreachDevice  string         `json:"breach_device,omitempty"`
	DarkSide      *sideData      `json:"dark_side,omitempty"`
	Checkout      map[string]any `json:"checkout,omitempty"`
	DarkAtBreach  int            `json:"dark_at_breach"`
	TriesAtBreach int            `json:"tries_at_breach"`

	// The sample: visits made, visits in a row that found no live link, and
	// the metered residential traffic spent.
	SampleDone int   `json:"sample_done"`
	NoLink     int   `json:"no_link"`
	ResBytes   int64 `json:"res_bytes"`

	// Round robin cursors over sample_states and the fixed lines.
	Place      int `json:"place"`
	LineCursor int `json:"line_cursor"`

	// Waits that record nothing: since when no live link was on hand, how
	// often in a row the browser runner could not take the visit, and how
	// often in a row a step failed for a reason that is not the page's.
	LinkWaitSince *time.Time `json:"link_wait_since,omitempty"`
	RunnerWaits   int        `json:"runner_waits,omitempty"`
	StepErrors    int        `json:"step_errors,omitempty"`
}

// sideData is the summary of one side, white or dark, kept in reviewer_data
// and raposa_data for the screens.
type sideData struct {
	URL              string        `json:"url"`
	FinalURL         string        `json:"finalUrl"`
	Domain           string        `json:"domain"`
	Title            string        `json:"title"`
	MetaDescription  string        `json:"metaDescription"`
	PageKind         string        `json:"pageKind"`
	CheckoutPlatform string        `json:"checkoutPlatform,omitempty"`
	BodySnippet      string        `json:"bodySnippet"`
	HTMLLength       int           `json:"htmlLength"`
	LineKey          string        `json:"lineKey"`
	Place            string        `json:"place,omitempty"`
	Disguise         string        `json:"disguise"`
	DisguiseName     string        `json:"disguiseName"`
	DisguisesTested  int           `json:"disguisesTested,omitempty"`
	FunnelSteps      []stepData    `json:"funnelSteps,omitempty"`
	Variants         []variantData `json:"variants,omitempty"`
}

// stepData is one page of the dark funnel, in order.
type stepData struct {
	StepNo      int    `json:"stepNo"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	PageKind    string `json:"pageKind"`
	ReachedBy   string `json:"reachedBy"`
	ClickedText string `json:"clickedText,omitempty"`
}

// variantData is one distinct dark funnel and its share.
type variantData struct {
	Label    string  `json:"label"`
	Visits   int     `json:"visits"`
	SharePct float64 `json:"sharePct"`
}
