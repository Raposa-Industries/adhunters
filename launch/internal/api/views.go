package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

// The Groups, Campaigns and Ads pages read the same lists again and again
// (one tab after another, every account at once), and Taboola allows few
// calls a minute for everyone. So lists are kept for a short while and
// dropped as soon as anything is written through Launch.
const keepFor = 30 * time.Second

// maxAdCampaigns is how many campaigns' ads one Ads page reads.
const maxAdCampaigns = 25

type kept[T any] struct {
	at time.Time
	v  T
}

type lists struct {
	mu    sync.Mutex
	trees map[string]kept[treeLists]
	ads   map[string]kept[[]network.Ad]
}

type treeLists struct {
	groups []network.Group
	camps  []network.Campaign
}

func newLists() *lists {
	return &lists{trees: map[string]kept[treeLists]{}, ads: map[string]kept[[]network.Ad]{}}
}

// forget drops every kept list: something was written.
func (l *lists) forget() {
	l.mu.Lock()
	l.trees = map[string]kept[treeLists]{}
	l.ads = map[string]kept[[]network.Ad]{}
	l.mu.Unlock()
}

// forgetOnWrite drops the kept lists before any request that is not a read.
func (a *API) forgetOnWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			a.shown.forget()
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) treeLists(ctx context.Context, n network.Network, acct string) (treeLists, error) {
	key := n.Name() + "/" + acct
	a.shown.mu.Lock()
	k, ok := a.shown.trees[key]
	a.shown.mu.Unlock()
	if ok && time.Since(k.at) < keepFor {
		return k.v, nil
	}
	groups, err := n.Groups(ctx, acct)
	if err != nil {
		return treeLists{}, err
	}
	camps, err := n.Campaigns(ctx, acct)
	if err != nil {
		return treeLists{}, err
	}
	t := treeLists{groups: groups, camps: camps}
	a.shown.mu.Lock()
	a.shown.trees[key] = kept[treeLists]{at: time.Now(), v: t}
	a.shown.mu.Unlock()
	return t, nil
}

func (a *API) adsOf(ctx context.Context, n network.Network, acct, campaign string) ([]network.Ad, error) {
	key := n.Name() + "/" + acct + "/" + campaign
	a.shown.mu.Lock()
	k, ok := a.shown.ads[key]
	a.shown.mu.Unlock()
	if ok && time.Since(k.at) < keepFor {
		return k.v, nil
	}
	ads, err := n.Ads(ctx, acct, campaign)
	if err != nil {
		return nil, err
	}
	a.shown.mu.Lock()
	a.shown.ads[key] = kept[[]network.Ad]{at: time.Now(), v: ads}
	a.shown.mu.Unlock()
	return ads, nil
}

// ads answers GET {net}/{account}/ads?campaigns=1,2,3: each campaign's ads,
// at most maxAdCampaigns campaigns, read four at a time. A campaign whose
// ads could not be read is in errors, with why.
func (a *API) ads(w http.ResponseWriter, r *http.Request) {
	n, ok := a.net(w, r)
	if !ok {
		return
	}
	acct := r.PathValue("account")
	var ids []string
	for _, id := range strings.Split(r.URL.Query().Get("campaigns"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		say(w, http.StatusBadRequest, "diga de quais campanhas (campaigns=1,2,3)")
		return
	}
	if len(ids) > maxAdCampaigns {
		say(w, http.StatusBadRequest, "no máximo 25 campanhas de cada vez")
		return
	}
	out := map[string][]network.Ad{}
	errs := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			ads, err := a.adsOf(r.Context(), n, acct, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				_, msg := a.classify(err)
				errs[id] = msg
				return
			}
			out[id] = nonNil(ads)
		}(id)
	}
	wg.Wait()
	send(w, http.StatusOK, map[string]any{"ads": out, "errors": errs})
}

// numbers answers GET numbers?window=7d&accounts=a,b with Intel's numbers
// for the window (every account when none is named).
func (a *API) numbers(w http.ResponseWriter, r *http.Request) {
	win := r.URL.Query().Get("window")
	if win == "" {
		win = "7d"
	}
	if !store.Windows[win] {
		say(w, http.StatusBadRequest, "período desconhecido: use today, yesterday, 7d ou 30d")
		return
	}
	var accts []string
	for _, a := range strings.Split(r.URL.Query().Get("accounts"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			accts = append(accts, a)
		}
	}
	res, err := a.l.Store().Results(r.Context(), win, accts)
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, res)
}
