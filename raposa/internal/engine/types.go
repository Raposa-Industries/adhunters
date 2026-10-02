package engine

import (
	"time"

	"github.com/google/uuid"
)

// Disguise is one rung of the ladder: one way of looking like a real reader
// (raposa.disguise). The baseline rung looks like an ad network reviewer.
type Disguise struct {
	ID         int16  `json:"id"`
	Rung       int16  `json:"rung"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Engine     string `json:"engine"`      // fetch or browser
	LineRole   string `json:"line_role"`   // direct, dc, isp, residential
	DeviceRule string `json:"device_rule"` // campaign, other, desktop, phone
	LinkKind   string `json:"link_kind"`   // live or saved
	PinPlace   bool   `json:"pin_place"`
	LoadAssets bool   `json:"load_assets"`
	HumanDwell bool   `json:"human_dwell"`
	CostKB     int32  `json:"cost_kb"`
	IsBaseline bool   `json:"is_baseline"`
}

// Settings are the tunables in raposa.setting, read on every visit, so a
// change there needs no deploy.
type Settings struct {
	VisitsTarget        int
	VisitsWindowMinutes int
	LadderTriesPerRung  int
	AvoidPlaces         []string
	AvoidRegions        []string
	SampleStates        []string
	MaxPageBytes        int
	FunnelMaxSteps      int
	BrowserDwellMs      int
	// The metered residential traffic one investigation may spend. The
	// sample stops when it is reached.
	ResidentialBudgetBytes int
	MaxActive              int
	QuickMaxRunning        int
	LiveLinkWaitSeconds    int
}

// defaultSettings are the values the migration seeds, used when a key is
// missing or unreadable.
func defaultSettings() Settings {
	return Settings{
		VisitsTarget:           100,
		VisitsWindowMinutes:    120,
		LadderTriesPerRung:     2,
		MaxPageBytes:           5242880,
		FunnelMaxSteps:         6,
		BrowserDwellMs:         8000,
		ResidentialBudgetBytes: 209715200,
		MaxActive:              10,
		QuickMaxRunning:        6,
		LiveLinkWaitSeconds:    180,
	}
}

// AdContext is what Tracks says about where and how the ad runs: the device
// it targets, the publishers it ran on, where its link goes and which
// campaign pays. Read once, when the investigation starts.
type AdContext struct {
	Device string `json:"device"` // desktop or phone
	// False when nothing said which device the ad runs on. The ladder still
	// claims desktop, but nothing measured it.
	DeviceKnown     bool   `json:"device_known"`
	PublisherID     *int32 `json:"publisher_id,omitempty"`
	PublisherName   string `json:"publisher_name"`
	PublisherDomain string `json:"publisher_domain"`
	LandingHost     string `json:"landing_host"`
	SavedLink       string `json:"saved_link"`
	CampaignID      string `json:"campaign_id"`
	// The devices the ad ran on in the last day, busiest first, and the
	// campaign it ran in on each. One creative often runs in one campaign per
	// device, and the ad network writes the device into the link, so a visit
	// claims a device together with the campaign that device was served.
	Devices          []string          `json:"devices,omitempty"`
	CampaignByDevice map[string]string `json:"campaign_by_device,omitempty"`
	// The Tracks account that pays for each of those campaigns, so evidence
	// names who paid for the click.
	AccountByCampaign map[string]int32 `json:"account_by_campaign,omitempty"`
	// Every publisher the ad ran on in the last day, busiest first.
	Publishers []string `json:"publishers,omitempty"`
	// When Tracks last saw the ad. A live link exists only while it runs.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

// Page is one version of one page, as a visit captured it (raposa.page).
type Page struct {
	ContentHash        uuid.UUID
	URL                string
	Host               string
	Path               string
	Title              string
	PageKind           string
	WordCount          int
	HTML               string
	BodyText           string
	HTMLBytes          int
	Headings           map[string][]string
	MetaTags           map[string]string
	Pixels             map[string]any
	CheckoutPlatform   string
	CheckoutMerchantID string
	OutboundLinks      []string
	// The video player links the page carries (VTurb on converteai.net and
	// the like), so a VSL can be watched without opening its page.
	VideoLinks []string
	IsDark     bool
}

// Investigation is the row a worker claimed, with what the visit needs.
type Investigation struct {
	ID               int64
	CreativeID       int32
	AdID             *int32
	Mode             string
	Status           string
	StopRequested    bool
	TargetClickURL   string
	PublisherReferer string
	BurnScope        string
	VisitsTarget     int
	RetryOf          *int64
	Attempt          int
	// The rung a follow-up run starts its climb at: the one that broke
	// through the first time. 0 starts at the bottom.
	StartRung int16
	StartedAt *time.Time
	Token     uuid.UUID
	Progress  Progress
}
