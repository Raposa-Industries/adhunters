// Package fake is an ad network kept in memory, for Launch's tests and for
// seeing the pages without a network (launch-web's TestDemo). It keeps the
// real rules that matter to Launch: everything is made paused, a
// campaign's group never changes, and a copy gets a new id.
package fake

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
)

// Net is one fake network login.
type Net struct {
	mu        sync.Mutex
	name      string
	off       string
	accounts  []network.Account
	groups    map[string][]network.Group
	campaigns map[string][]network.Campaign
	ads       map[string][]network.Ad // by campaign
	next      int
	// Fail makes a call fail by its name ("CreateCampaign", "Copy",
	// "Pause", …), optionally only for one campaign name or id.
	Fail map[string]error
	// FailAdsAfter makes CreateCampaign stop after this many ads (0: never).
	FailAdsAfter int
	// Calls lists every write, in order: "CreateGroup 12", "Pause 34".
	Calls []string
	// Uploaded counts image uploads.
	Uploaded int
}

// New returns a fake network named name with the given accounts.
func New(name string, accounts ...network.Account) *Net {
	return &Net{name: name, accounts: accounts, groups: map[string][]network.Group{}, campaigns: map[string][]network.Campaign{}, ads: map[string][]network.Ad{}, next: 1000, Fail: map[string]error{}}
}

// Off makes the network unavailable, with why.
func (n *Net) Off(why string) { n.mu.Lock(); n.off = why; n.mu.Unlock() }

func (n *Net) id() string { n.next++; return strconv.Itoa(n.next) }

func (n *Net) fail(call, key string) error {
	if err, ok := n.Fail[call+" "+key]; ok {
		return err
	}
	return n.Fail[call]
}

func (n *Net) Name() string { return n.name }

func (n *Net) Available() (bool, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.off == "", n.off
}

func (n *Net) Accounts(context.Context) ([]network.Account, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.fail("Accounts", ""); err != nil {
		return nil, err
	}
	return slices.Clone(n.accounts), nil
}

func (n *Net) known(account string) error {
	for _, a := range n.accounts {
		if a.ID == account {
			return nil
		}
	}
	return &network.Refused{Message: "conta " + account + " não permitida"}
}

// AddGroup and AddCampaign seed the network as if made elsewhere.
func (n *Net) AddGroup(account string, g network.Group) network.Group {
	n.mu.Lock()
	defer n.mu.Unlock()
	if g.ID == "" {
		g.ID = n.id()
	}
	n.groups[account] = append(n.groups[account], g)
	return g
}

func (n *Net) AddCampaign(account string, c network.Campaign, ads ...network.Ad) network.Campaign {
	n.mu.Lock()
	defer n.mu.Unlock()
	if c.ID == "" {
		c.ID = n.id()
	}
	for i := range ads {
		if ads[i].ID == "" {
			ads[i].ID = n.id()
		}
	}
	n.campaigns[account] = append(n.campaigns[account], c)
	n.ads[c.ID] = ads
	return c
}

// Start turns a campaign on, as a person does in the network's dashboard.
func (n *Net) Start(account, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, c := range n.campaigns[account] {
		if c.ID == id {
			n.campaigns[account][i].Active, n.campaigns[account][i].Status = true, "RUNNING"
		}
	}
}

func (n *Net) Groups(_ context.Context, account string) ([]network.Group, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.known(account); err != nil {
		return nil, err
	}
	return slices.Clone(n.groups[account]), nil
}

func (n *Net) Campaigns(_ context.Context, account string) ([]network.Campaign, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.known(account); err != nil {
		return nil, err
	}
	return slices.Clone(n.campaigns[account]), nil
}

func (n *Net) find(account, id string) (int, error) {
	for i, c := range n.campaigns[account] {
		if c.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("fake: campaign %s not found in %s", id, account)
}

func (n *Net) Campaign(_ context.Context, account, id string) (network.Campaign, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	i, err := n.find(account, id)
	if err != nil {
		return network.Campaign{}, err
	}
	return n.campaigns[account][i], nil
}

func (n *Net) Ads(_ context.Context, account, campaign string) ([]network.Ad, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, err := n.find(account, campaign); err != nil {
		return nil, err
	}
	return slices.Clone(n.ads[campaign]), nil
}

func (n *Net) CreateGroup(_ context.Context, account string, g network.NewGroup) (network.Group, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.known(account); err != nil {
		return network.Group{}, err
	}
	if err := n.fail("CreateGroup", g.Name); err != nil {
		return network.Group{}, err
	}
	made := network.Group{ID: n.id(), Name: g.Name, Status: "PAUSED", Budget: g.Budget, BudgetModel: g.BudgetModel}
	n.groups[account] = append(n.groups[account], made)
	n.Calls = append(n.Calls, "CreateGroup "+made.ID)
	return made, nil
}

func (n *Net) CreateCampaign(_ context.Context, account string, c network.NewCampaign, up *network.Uploads) (network.Made, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.known(account); err != nil {
		return network.Made{}, err
	}
	if err := n.fail("CreateCampaign", c.Name); err != nil {
		return network.Made{}, err
	}
	made := network.Campaign{ID: n.id(), Name: c.Name, GroupID: c.GroupID, Status: "PAUSED", Device: c.Device, Settings: c.Settings, Objective: c.Settings.Objective}
	n.campaigns[account] = append(n.campaigns[account], made)
	n.Calls = append(n.Calls, "CreateCampaign "+made.ID)
	out := network.Made{Campaign: made}
	for i, a := range c.Ads {
		if n.FailAdsAfter > 0 && i >= n.FailAdsAfter {
			return out, &network.Refused{Message: fmt.Sprintf("o Taboola recusou o anúncio %d", i+1)}
		}
		url, err := up.Once(a.Image, func(string, []byte) (string, error) {
			n.Uploaded++
			return "https://images.fake/" + a.Image[:12] + ".jpg", nil
		})
		if err != nil {
			return out, err
		}
		ad := network.Ad{ID: n.id(), Title: a.Title, Description: a.Description, URL: a.URL, ImageURL: url, CTA: a.CTA, AdID: a.AdID, AI: a.AI, Status: "PAUSED", Approval: "PENDING"}
		n.ads[made.ID] = append(n.ads[made.ID], ad)
		out.Ads = append(out.Ads, ad)
	}
	return out, nil
}

func (n *Net) Copy(_ context.Context, account, id string, to network.CopyTo) (network.Made, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	i, err := n.find(account, id)
	if err != nil {
		return network.Made{}, err
	}
	if err := n.fail("Copy", id); err != nil {
		return network.Made{}, err
	}
	c := n.campaigns[account][i]
	c.ID, c.Status, c.Active = n.id(), "PAUSED", false
	if to.Name != "" {
		c.Name = to.Name
	}
	if to.GroupID != "" {
		c.GroupID = to.GroupID
	}
	n.campaigns[account] = append(n.campaigns[account], c)
	n.Calls = append(n.Calls, "Copy "+id+" "+c.ID)
	out := network.Made{Campaign: c}
	for _, a := range n.ads[id] {
		a.ID, a.Status, a.Active = n.id(), "PAUSED", false
		n.ads[c.ID] = append(n.ads[c.ID], a)
		out.Ads = append(out.Ads, a)
	}
	return out, nil
}

func (n *Net) Pause(_ context.Context, account, id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	i, err := n.find(account, id)
	if err != nil {
		return err
	}
	if err := n.fail("Pause", id); err != nil {
		return err
	}
	n.campaigns[account][i].Active, n.campaigns[account][i].Status = false, "PAUSED"
	n.Calls = append(n.Calls, "Pause "+id)
	return nil
}

func (n *Net) PauseAd(_ context.Context, account, campaign, ad string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, err := n.find(account, campaign); err != nil {
		return err
	}
	if err := n.fail("PauseAd", ad); err != nil {
		return err
	}
	for i, a := range n.ads[campaign] {
		if a.ID == ad {
			n.ads[campaign][i].Active, n.ads[campaign][i].Status = false, "PAUSED"
			n.Calls = append(n.Calls, "PauseAd "+campaign+" "+ad)
			return nil
		}
	}
	return &network.Refused{Message: "anúncio " + ad + " não existe na campanha " + campaign}
}

func (n *Net) Change(_ context.Context, account, id string, ch network.Change) (network.Campaign, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	i, err := n.find(account, id)
	if err != nil {
		return network.Campaign{}, err
	}
	if err := n.fail("Change", id); err != nil {
		return network.Campaign{}, err
	}
	c := &n.campaigns[account][i]
	if ch.Name != "" {
		c.Name = ch.Name
	}
	if ch.CPC > 0 {
		c.Settings.CPC = ch.CPC
	}
	if ch.DailyCap > 0 {
		c.Settings.DailyCap = ch.DailyCap
	}
	if ch.SpendingLimit > 0 {
		c.Settings.SpendingLimit = ch.SpendingLimit
	}
	n.Calls = append(n.Calls, "Change "+id)
	return *c, nil
}
