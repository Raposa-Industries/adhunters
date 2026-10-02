package taboola

import (
	"context"
	"sync"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
)

// Login is one Taboola login and the advertiser accounts chosen for it.
type Login struct {
	T        *Taboola
	Accounts []string
}

// Logins is every Taboola login Launch uses, as one network: the server's
// own (TABOOLA_* in its environment) first, then those people added on the
// Contas page. An account belongs to the first login that has it, and every
// call about it goes to that login's client, with that client's guards.
type Logins struct {
	mu    sync.RWMutex
	list  []Login
	owner map[string]*Taboola
}

// NewLogins starts with the server's own login.
func NewLogins(server Login) *Logins {
	l := &Logins{}
	l.set(server, nil)
	return l
}

// Set replaces the added logins; the server's own stays first.
func (l *Logins) Set(added []Login) {
	l.mu.Lock()
	server := l.list[0]
	l.mu.Unlock()
	l.set(server, added)
}

func (l *Logins) set(server Login, added []Login) {
	list := append([]Login{server}, added...)
	owner := map[string]*Taboola{}
	for _, lg := range list {
		for _, a := range lg.Accounts {
			if _, taken := owner[a]; !taken {
				owner[a] = lg.T
			}
		}
	}
	l.mu.Lock()
	l.list, l.owner = list, owner
	l.mu.Unlock()
}

// pick is the login an account belongs to. An account no login has goes to
// the server's own, whose guard refuses it.
func (l *Logins) pick(account string) *Taboola {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if t, ok := l.owner[account]; ok {
		return t
	}
	return l.list[0].T
}

func (l *Logins) logins() []Login {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.list
}

func (l *Logins) Name() string { return "taboola" }

// Available is true when any login is.
func (l *Logins) Available() (bool, string) {
	list := l.logins()
	for _, lg := range list {
		if ok, _ := lg.T.Available(); ok {
			return true, ""
		}
	}
	return list[0].T.Available()
}

// Accounts lists every login's chosen accounts, each once. A login that
// fails is left out while another answers.
func (l *Logins) Accounts(ctx context.Context) ([]network.Account, error) {
	list := l.logins()
	if ok, _ := l.Available(); !ok {
		return list[0].T.Accounts(ctx)
	}
	var out []network.Account
	seen := map[string]bool{}
	var first error
	for _, lg := range list {
		if ok, _ := lg.T.Available(); !ok {
			continue
		}
		accts, err := lg.T.Accounts(ctx)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for _, a := range accts {
			if !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
	}
	if len(out) == 0 && first != nil {
		return nil, first
	}
	return out, nil
}

func (l *Logins) Groups(ctx context.Context, account string) ([]network.Group, error) {
	return l.pick(account).Groups(ctx, account)
}

func (l *Logins) Campaigns(ctx context.Context, account string) ([]network.Campaign, error) {
	return l.pick(account).Campaigns(ctx, account)
}

func (l *Logins) Campaign(ctx context.Context, account, campaign string) (network.Campaign, error) {
	return l.pick(account).Campaign(ctx, account, campaign)
}

func (l *Logins) Ads(ctx context.Context, account, campaign string) ([]network.Ad, error) {
	return l.pick(account).Ads(ctx, account, campaign)
}

func (l *Logins) CreateGroup(ctx context.Context, account string, g network.NewGroup) (network.Group, error) {
	return l.pick(account).CreateGroup(ctx, account, g)
}

func (l *Logins) CreateCampaign(ctx context.Context, account string, c network.NewCampaign, up *network.Uploads) (network.Made, error) {
	return l.pick(account).CreateCampaign(ctx, account, c, up)
}

func (l *Logins) Copy(ctx context.Context, account, campaign string, to network.CopyTo) (network.Made, error) {
	return l.pick(account).Copy(ctx, account, campaign, to)
}

func (l *Logins) AddAds(ctx context.Context, account, campaign string, ads []network.NewAd, up *network.Uploads) (network.Made, error) {
	return l.pick(account).AddAds(ctx, account, campaign, ads, up)
}

func (l *Logins) Pause(ctx context.Context, account, campaign string) error {
	return l.pick(account).Pause(ctx, account, campaign)
}

func (l *Logins) PauseAd(ctx context.Context, account, campaign, ad string) error {
	return l.pick(account).PauseAd(ctx, account, campaign, ad)
}

func (l *Logins) Change(ctx context.Context, account, campaign string, ch network.Change) (network.Campaign, error) {
	return l.pick(account).Change(ctx, account, campaign, ch)
}

var _ network.Network = (*Logins)(nil)
