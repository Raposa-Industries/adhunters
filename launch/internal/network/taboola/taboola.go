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
			TargetCPA: c.TargetCPA, ExcludeCities: c.ExcludedCities, AdDelivery: c.TrafficAllocation,
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
	model := g.BudgetModel
	if model == "" {
		model = "NONE" // each campaign keeps its own budget
	}
	made, err := t.c.CreateGroup(ctx, account, write.NewGroup{Name: g.Name, SpendingLimit: g.Budget, Model: model, MarketingObjective: g.Objective})
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
	items, err := newItems(n.Ads)
	if err != nil {
		return network.Made{}, err
	}
	s := n.Settings
	if strings.EqualFold(s.BidStrategy, "MAX_CONVERSIONS") {
		s.CPC = 0 // the network sets each bid
	}
	cp, err := t.c.CreateCampaign(ctx, account, write.NewCampaign{
		Name: n.Name, Brand: s.Brand, CPC: s.CPC, DailyCap: s.DailyCap, SpendingLimit: s.SpendingLimit,
		Countries: s.Countries, Platforms: platforms(n.Device), TrackingCode: s.TrackingCode,
		MarketingObjective: s.Objective, BidStrategy: s.BidStrategy, StartDate: s.StartDate, EndDate: s.EndDate,
		GroupID: n.GroupID, TargetCPA: s.TargetCPA, ExcludeCities: s.ExcludeCities, AdDelivery: s.AdDelivery,
	})
	if err != nil {
		return network.Made{}, err
	}
	made := network.Made{Campaign: campaign(cp)}
	made.Ads, err = t.addItems(ctx, account, cp.ID, n.Ads, items, up, "campanha "+cp.ID+" criada, mas ")
	return made, err
}

// newItems checks the ads before anything is sent.
func newItems(ads []network.NewAd) ([]write.NewItem, error) {
	if len(ads) == 0 {
		return nil, &network.Refused{Message: "nenhum anúncio para criar"}
	}
	items := make([]write.NewItem, len(ads))
	for i, a := range ads {
		items[i] = write.NewItem{URL: a.URL, Title: a.Title, Description: a.Description, CTA: CTAType(a.CTA), CustomID: a.AdID, AI: a.AI, ThumbnailURL: "https://pending.invalid/"}
		if err := items[i].Check(); err != nil {
			return nil, &network.Refused{Message: "anúncio " + itoa(i+1) + ": " + write.Message(err)}
		}
	}
	return items, nil
}

// addItems uploads the pictures (once each) and makes the ads, paused.
func (t *Taboola) addItems(ctx context.Context, account, campaign string, ads []network.NewAd, items []write.NewItem, up *network.Uploads, before string) ([]network.Ad, error) {
	for i, a := range ads {
		url, err := up.Once(a.Image, func(name string, data []byte) (string, error) { return t.c.UploadImage(ctx, name, data) })
		if err != nil {
			return nil, errors.New(before + "a imagem do anúncio " + itoa(i+1) + " não subiu: " + write.Message(err))
		}
		items[i].ThumbnailURL = url
	}
	got, err := t.c.MassCreateItems(ctx, account, campaign, items)
	var out []network.Ad
	for _, it := range got {
		out = append(out, network.Ad{ID: it.ID, Title: it.Title, Status: it.Status, AdID: it.CustomID, Active: !it.Paused})
	}
	return out, err
}

// AddAds makes more ads, paused, in an existing campaign.
func (t *Taboola) AddAds(ctx context.Context, account, campaign string, ads []network.NewAd, up *network.Uploads) (network.Made, error) {
	if err := t.c.CheckCampaign(account, campaign); err != nil {
		return network.Made{}, err
	}
	items, err := newItems(ads)
	if err != nil {
		return network.Made{}, err
	}
	made := network.Made{Campaign: network.Campaign{ID: campaign}}
	made.Ads, err = t.addItems(ctx, account, campaign, ads, items, up, "")
	return made, err
}

// Copy duplicates a campaign with its ads in one call (include_items,
// proven 2026-09-30: the ads arrive paused with new ids and go back to
// review), then reads the copy's ads and pauses any that came back running.
func (t *Taboola) Copy(ctx context.Context, account, from string, to network.CopyTo) (network.Made, error) {
	cp, err := t.c.DuplicateCampaign(ctx, account, from, write.NewCampaign{Name: to.Name, GroupID: to.GroupID, WithAds: true})
	if err != nil {
		return network.Made{}, err
	}
	made := network.Made{Campaign: campaign(cp)}
	ads, err := t.c.Ads(ctx, account, cp.ID)
	if err != nil {
		return made, errors.New("cópia " + cp.ID + " feita, pausada, mas não consegui ler os anúncios dela: " + write.Message(err))
	}
	var running []string
	for _, a := range ads {
		if a.IsActive {
			if err := t.c.PauseAd(ctx, account, cp.ID, a.ID); err != nil {
				running = append(running, a.ID)
				made.Ads = append(made.Ads, ad(a))
				continue
			}
			a.IsActive = false
		}
		made.Ads = append(made.Ads, ad(a))
	}
	if len(running) > 0 {
		return made, errors.New("anúncios da cópia " + cp.ID + " que ficaram ligados: " + strings.Join(running, ", "))
	}
	return made, nil
}

func (t *Taboola) Pause(ctx context.Context, account, campaign string) error {
	return t.c.PauseCampaign(ctx, account, campaign)
}

func (t *Taboola) PauseAd(ctx context.Context, account, campaign, ad string) error {
	return t.c.PauseAd(ctx, account, campaign, ad)
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
