// Package taboola is Launch's Taboola adapter: network.Network over the
// shared write client (shared/taboola/write), which holds the guards (only
// the allowed accounts, never a network account, ceilings on bids and caps,
// everything paused, only-own on a lent login) and keeps every exchange raw.
//
// A group here is Taboola's campaign group. A desktop campaign targets DESK
// and a mobile one PHON; a campaign made elsewhere with both is Both.
package taboola

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// Taboola is one Taboola login.
type Taboola struct{ c *write.Client }

// New wraps a write client (nil or not configured: every call says why).
func New(c *write.Client) *Taboola { return &Taboola{c: c} }

func (t *Taboola) Name() string { return "taboola" }

func (t *Taboola) Available() (bool, string) { return t.c.Available(), t.c.Why() }

// Message is err as one pt-BR line for the person.
func Message(err error) string {
	var r *network.Refused
	if errors.As(err, &r) {
		return r.Message
	}
	return write.Message(err)
}

func (t *Taboola) Accounts(ctx context.Context) ([]network.Account, error) {
	list, err := t.c.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]network.Account, len(list))
	for i, a := range list {
		out[i] = network.Account{ID: a.ID, Name: a.Name}
	}
	return out, nil
}

func (t *Taboola) Groups(ctx context.Context, account string) ([]network.Group, error) {
	list, err := t.c.Groups(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make([]network.Group, len(list))
	for i, g := range list {
		out[i] = group(g)
	}
	return out, nil
}

func group(g write.Group) network.Group {
	return network.Group{ID: g.ID, Name: g.Name, Status: g.Status, Budget: g.SpendingLimit, BudgetModel: g.SpendingLimitModel}
}

func (t *Taboola) Campaigns(ctx context.Context, account string) ([]network.Campaign, error) {
	list, err := t.c.Campaigns(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make([]network.Campaign, len(list))
	for i, c := range list {
		out[i] = campaign(c)
	}
	return out, nil
}

func (t *Taboola) Campaign(ctx context.Context, account, id string) (network.Campaign, error) {
	c, err := t.c.Campaign(ctx, account, id)
	if err != nil {
		return network.Campaign{}, err
	}
	return campaign(c), nil
}

// device reads a campaign's platforms: only DESK is desktop, only phones
// (and tablets) is mobile, anything else both.
func device(platforms []string) network.Device {
	switch {
	case len(platforms) == 1 && platforms[0] == "DESK":
		return network.Desktop
	case len(platforms) > 0 && !slices.Contains(platforms, "DESK"):
		return network.Mobile
	}
	return network.Both
}

func platforms(d network.Device) []string {
	switch d {
	case network.Desktop:
		return []string{"DESK"}
	case network.Mobile:
		return []string{"PHON"}
	}
	return nil
}

func campaign(c write.Campaign) network.Campaign {
	return network.Campaign{
		ID: c.ID, Name: c.Name, GroupID: c.CampaignGroupID, Status: c.Status, Active: c.IsActive,
		Device: device(c.Platforms), Objective: c.MarketingObjective,
		Settings: network.Settings{
			Brand: c.BrandingText, CPC: c.CPC, BidStrategy: c.BidStrategy, DailyCap: c.DailyCap,
			SpendingLimit: c.SpendingLimit, Countries: c.Countries, TrackingCode: c.TrackingCode,
			Objective: c.MarketingObjective, StartDate: c.StartDate, EndDate: c.EndDate,
		},
	}
}

func ad(a write.Ad) network.Ad {
	return network.Ad{
		ID: a.ID, Title: a.Title, Description: a.Description, URL: a.URL, ImageURL: a.ThumbnailURL,
		CTA: a.CTA, AdID: a.CustomID, AI: a.AI, Status: a.Status, Approval: a.Approval, Active: a.IsActive,
	}
}

func (t *Taboola) Ads(ctx context.Context, account, campaign string) ([]network.Ad, error) {
	list, err := t.c.Ads(ctx, account, campaign)
	if err != nil {
		return nil, err
	}
	out := make([]network.Ad, len(list))
	for i, a := range list {
		out[i] = ad(a)
	}
	return out, nil
}

func (t *Taboola) CreateGroup(ctx context.Context, account string, g network.NewGroup) (network.Group, error) {
	made, err := t.c.CreateGroup(ctx, account, write.NewGroup{Name: g.Name, SpendingLimit: g.Budget, Model: g.BudgetModel, MarketingObjective: g.Objective})
	if err != nil {
		return network.Group{}, err
	}
	return group(made), nil
}

// ctas are Taboola's cta_type values by the label people pick.
var ctas = map[string]string{
	"read more": "READ_MORE", "learn more": "LEARN_MORE", "shop now": "SHOP_NOW", "see more": "SEE_MORE",
	"discover more": "DISCOVER_MORE", "get offer": "GET_OFFER", "sign up": "SIGN_UP", "download": "DOWNLOAD",
	"watch now": "WATCH_NOW", "install now": "INSTALL_NOW", "get quote": "GET_QUOTE", "apply now": "APPLY_NOW",
	"buy now": "BUY_NOW", "contact us": "CONTACT_US", "try now": "TRY_NOW", "book now": "BOOK_NOW",
}

// CTAType is Taboola's cta_type for a label ("Read More") or a type
// already ("READ_MORE"); "" for none.
func CTAType(s string) string {
	s = strings.TrimSpace(s)
	if v, ok := ctas[strings.ToLower(s)]; ok {
		return v
	}
	return strings.ToUpper(strings.ReplaceAll(s, " ", "_"))
}

func (t *Taboola) CreateCampaign(ctx context.Context, account string, n network.NewCampaign, up *network.Uploads) (network.Made, error) {
	if n.Device != network.Desktop && n.Device != network.Mobile {
		return network.Made{}, &network.Refused{Message: "cada campanha nova é desktop ou mobile"}
	}
	if len(n.Ads) == 0 {
		return network.Made{}, &network.Refused{Message: "nenhum anúncio para criar"}
	}
	items := make([]write.NewItem, len(n.Ads))
	for i, a := range n.Ads {
		items[i] = write.NewItem{URL: a.URL, Title: a.Title, Description: a.Description, CTA: CTAType(a.CTA), CustomID: a.AdID, AI: a.AI, ThumbnailURL: "https://pending.invalid/"}
		if err := items[i].Check(); err != nil {
			return network.Made{}, &network.Refused{Message: "anúncio " + itoa(i+1) + ": " + write.Message(err)}
		}
	}
	s := n.Settings
	cp, err := t.c.CreateCampaign(ctx, account, write.NewCampaign{
		Name: n.Name, Brand: s.Brand, CPC: s.CPC, DailyCap: s.DailyCap, SpendingLimit: s.SpendingLimit,
		Countries: s.Countries, Platforms: platforms(n.Device), TrackingCode: s.TrackingCode,
		MarketingObjective: s.Objective, BidStrategy: s.BidStrategy, StartDate: s.StartDate, EndDate: s.EndDate,
		GroupID: n.GroupID,
	})
	if err != nil {
		return network.Made{}, err
	}
	made := network.Made{Campaign: campaign(cp)}
	for i, a := range n.Ads {
		url, err := up.Once(a.Image, func(name string, data []byte) (string, error) { return t.c.UploadImage(ctx, name, data) })
		if err != nil {
			return made, errors.New("campanha " + cp.ID + " criada, mas a imagem do anúncio " + itoa(i+1) + " não subiu: " + write.Message(err))
		}
		items[i].ThumbnailURL = url
	}
	got, err := t.c.MassCreateItems(ctx, account, cp.ID, items)
	for _, it := range got {
		made.Ads = append(made.Ads, network.Ad{ID: it.ID, Title: it.Title, Status: it.Status, AdID: it.CustomID, Active: !it.Paused})
	}
	return made, err
}

// Copy duplicates a campaign (settings only, as Taboola does), then makes
// its ads again in the copy, paused. Taboola's duplicate can carry items,
// but whether they keep their approval is not proven, so Launch copies them
// itself and they go through review again.
func (t *Taboola) Copy(ctx context.Context, account, from string, to network.CopyTo) (network.Made, error) {
	ads, err := t.c.Ads(ctx, account, from)
	if err != nil {
		return network.Made{}, err
	}
	cp, err := t.c.DuplicateCampaign(ctx, account, from, write.NewCampaign{Name: to.Name, GroupID: to.GroupID})
	if err != nil {
		return network.Made{}, err
	}
	made := network.Made{Campaign: campaign(cp)}
	var items []write.NewItem
	for _, a := range ads {
		items = append(items, a.NewItem())
	}
	if len(items) == 0 {
		return made, nil
	}
	got, err := t.c.MassCreateItems(ctx, account, cp.ID, items)
	for _, it := range got {
		made.Ads = append(made.Ads, network.Ad{ID: it.ID, Title: it.Title, Status: it.Status, AdID: it.CustomID, Active: !it.Paused})
	}
	return made, err
}

func (t *Taboola) Pause(ctx context.Context, account, campaign string) error {
	return t.c.PauseCampaign(ctx, account, campaign)
}

func (t *Taboola) Change(ctx context.Context, account, id string, ch network.Change) (network.Campaign, error) {
	c, err := t.c.ChangeCampaign(ctx, account, id, write.Change{Name: ch.Name, CPC: ch.CPC, DailyCap: ch.DailyCap, SpendingLimit: ch.SpendingLimit})
	if err != nil {
		return network.Campaign{}, err
	}
	return campaign(c), nil
}

func itoa(n int) string {
	const d = "0123456789"
	if n < 10 {
		return d[n : n+1]
	}
	return itoa(n/10) + d[n%10:n%10+1]
}
