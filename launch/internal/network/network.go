// Package network is what Launch asks of an ad network, in words that fit
// every network: accounts hold groups, groups hold campaigns, campaigns hold
// ads (GLOSSARY.md). Each network's adapter is a subpackage (taboola; later
// newsbreak, where a group is NewsBreak's campaign and a campaign its ad
// set). Launch picks the adapter by the first segment of a path
// (/launch/taboola/<account>/g/<group>/c/<campaign>).
//
// Nothing an adapter offers can start spending: groups, campaigns and ads
// are made paused, and only a person turns them on, in the network's own
// dashboard.
package network

import (
	"context"
	"errors"
)

// Network is one ad network, through one login.
type Network interface {
	// Name is the path segment: "taboola".
	Name() string
	// Available says whether calls can be made, and why not in pt-BR.
	Available() (bool, string)
	Accounts(ctx context.Context) ([]Account, error)
	Groups(ctx context.Context, account string) ([]Group, error)
	// Campaigns lists every campaign of the account, each with its group.
	Campaigns(ctx context.Context, account string) ([]Campaign, error)
	Campaign(ctx context.Context, account, campaign string) (Campaign, error)
	Ads(ctx context.Context, account, campaign string) ([]Ad, error)

	CreateGroup(ctx context.Context, account string, g NewGroup) (Group, error)
	// CreateCampaign makes one paused campaign with its ads, all paused.
	// Images are uploaded once however many campaigns use them, so a pair
	// passes the same Uploads. It returns what was made even on an error.
	CreateCampaign(ctx context.Context, account string, c NewCampaign, up *Uploads) (Made, error)
	// Copy makes a paused copy of a campaign, with its ads, in the same
	// group or in another (how a campaign moves: a group is fixed once a
	// campaign is made).
	Copy(ctx context.Context, account, campaign string, to CopyTo) (Made, error)
	// AddAds makes more ads, paused, in a campaign that exists.
	AddAds(ctx context.Context, account, campaign string, ads []NewAd, up *Uploads) (Made, error)
	Pause(ctx context.Context, account, campaign string) error
	// PauseAd pauses one ad of a campaign.
	PauseAd(ctx context.Context, account, campaign, ad string) error
	Change(ctx context.Context, account, campaign string, ch Change) (Campaign, error)
}

// Account is one advertiser account.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Group is a campaign group: Taboola's campaign group, NewsBreak's campaign.
type Group struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Budget float64 `json:"budget"`
	// BudgetModel is MONTHLY, ENTIRE or "" (none of its own).
	BudgetModel string `json:"budget_model"`
	// Objective is the network's objective for the group; its campaigns
	// must have the same.
	Objective string `json:"objective,omitempty"`
}

// Device is where a campaign shows: Launch makes every new campaign as a
// desktop and mobile pair. Mobile is phones and tablets.
type Device string

const (
	Desktop Device = "desktop"
	Mobile  Device = "mobile"
	// Both is a campaign made elsewhere that targets several devices.
	Both Device = "both"
)

// Campaign is one campaign as Launch shows it. Money is USD; 0 means none.
type Campaign struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	GroupID   string   `json:"group_id"`
	Status    string   `json:"status"`
	Active    bool     `json:"active"`
	Device    Device   `json:"device"`
	Settings  Settings `json:"settings"`
	Objective string   `json:"objective"`
}

// Settings are what a pair shares, and what a campaign preset holds.
type Settings struct {
	Brand         string   `json:"brand"`
	CPC           float64  `json:"cpc"`
	BidStrategy   string   `json:"bid_strategy"`
	DailyCap      float64  `json:"daily_cap"`
	SpendingLimit float64  `json:"spending_limit"`
	Countries     []string `json:"countries"`
	TrackingCode  string   `json:"tracking_code"`
	Objective     string   `json:"objective"`
	StartDate     string   `json:"start_date"`
	EndDate       string   `json:"end_date"`
	// TargetCPA, with bid strategy MAX_CONVERSIONS, is the cost per
	// conversion to aim for; 0 lets the network maximize conversions.
	TargetCPA float64 `json:"target_cpa,omitempty"`
	// ExcludeCities are the network's city values not to show in.
	ExcludeCities []string `json:"exclude_cities,omitempty"`
	// AdDelivery is OPTIMIZED (the best ads get more) or EVEN (A/B).
	AdDelivery string `json:"ad_delivery,omitempty"`
}

// Ad is one ad in a campaign (Taboola's item).
type Ad struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	ImageURL    string `json:"image_url"`
	CTA         string `json:"cta"`
	AdID        string `json:"ad_id"` // our ad id, in the network's custom id
	AI          bool   `json:"ai"`
	Status      string `json:"status"`
	Approval    string `json:"approval"`
	Active      bool   `json:"active"`
	// Name is the team's name for an ad Launch made (AdName). Taboola's
	// items have no name, so it is only Launch's: History and the pages.
	Name string `json:"name,omitempty"`
}

// NewGroup is a group with a budget its campaigns share.
type NewGroup struct {
	Name        string  `json:"name"`
	Budget      float64 `json:"budget"`
	BudgetModel string  `json:"budget_model"`
	Objective   string  `json:"objective"`
}

// NewAd is one ad to make. Image is the picture's sha256 in Launch's image
// store; Uploads turns it into the network's own address once.
type NewAd struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Image       string `json:"image"`
	CTA         string `json:"cta"`
	AdID        string `json:"ad_id"`
	AI          bool   `json:"ai"`
}

// NewCampaign is one campaign to make, paused, with its ads.
type NewCampaign struct {
	Name     string   `json:"name"`
	GroupID  string   `json:"group_id"`
	Device   Device   `json:"device"`
	Settings Settings `json:"settings"`
	Ads      []NewAd  `json:"ads"`
}

// Made is what one create or copy made. Err says what failed after the
// campaign was made (an ad refused, an ad left active), in pt-BR.
type Made struct {
	Campaign Campaign `json:"campaign"`
	Ads      []Ad     `json:"ads"`
	Problems []string `json:"problems,omitempty"`
}

// CopyTo is where a copy goes and what it changes.
type CopyTo struct {
	Name    string `json:"name"`
	GroupID string `json:"group_id"` // "" keeps the original's group
}

// Change is what may change on a campaign; zero fields stay.
type Change struct {
	Name          string  `json:"name,omitempty"`
	CPC           float64 `json:"cpc,omitempty"`
	DailyCap      float64 `json:"daily_cap,omitempty"`
	SpendingLimit float64 `json:"spending_limit,omitempty"`
}

// Uploads turns Launch's images into the network's addresses, one upload
// per image however many campaigns use it.
type Uploads struct {
	// Read returns an image's bytes and a file name by its sha256.
	Read func(sha string) ([]byte, string, error)
	done map[string]string
}

// Once returns the network's address for image sha, uploading it with up
// the first time.
func (u *Uploads) Once(sha string, up func(name string, data []byte) (string, error)) (string, error) {
	if u.done == nil {
		u.done = map[string]string{}
	}
	if url, ok := u.done[sha]; ok {
		return url, nil
	}
	if u.Read == nil {
		return "", errors.New("network: no image store")
	}
	data, name, err := u.Read(sha)
	if err != nil {
		return "", err
	}
	url, err := up(name, data)
	if err != nil {
		return "", err
	}
	u.done[sha] = url
	return url, nil
}

// Refused is a request refused before it reached the network; Message is
// one pt-BR line for the person.
type Refused struct{ Message string }

func (e *Refused) Error() string { return e.Message }
