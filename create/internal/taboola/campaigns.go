package taboola

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Account is one advertiser account the page may use.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// namesFor is how long the accounts' names are trusted before being read
// again.
const namesFor = time.Hour

// Accounts lists Settings.Accounts (network accounts left out), named as
// Taboola names them when the login may read that list; when the read fails
// each account is named by its id.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	if !c.Available() {
		return nil, ErrNotConfigured
	}
	names := c.accountNames(ctx)
	ids := c.usable()
	out := make([]Account, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if name == "" {
			name = id
		}
		out = append(out, Account{ID: id, Name: name})
	}
	return out, nil
}

func (c *Client) accountNames(ctx context.Context) map[string]string {
	c.mu.Lock()
	if c.names != nil && time.Since(c.namesAt) < namesFor {
		n := c.names
		c.mu.Unlock()
		return n
	}
	c.mu.Unlock()
	out, err := c.sendJSON(ctx, http.MethodGet, allowedAccounts, nil, true)
	if err != nil {
		c.log.Warn("taboola account names not read, ids shown instead", "err", err)
		return nil
	}
	names := map[string]string{}
	for _, r := range results(out) {
		if id := str(r["account_id"]); id != "" {
			names[id] = oneLine(str(r["name"]), 100)
		}
	}
	c.mu.Lock()
	c.names, c.namesAt = names, time.Now()
	c.mu.Unlock()
	return names
}

// Campaign is one campaign as the page shows it. A daily cap or total
// budget of 0 means none; dates are Taboola's "YYYY-MM-DD" ("" for none).
type Campaign struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Status             string  `json:"status"`
	IsActive           bool    `json:"is_active"`
	BrandingText       string  `json:"branding_text"`
	CPC                float64 `json:"cpc"`
	DailyCap           float64 `json:"daily_cap"`
	SpendingLimit      float64 `json:"spending_limit"`
	BidStrategy        string  `json:"bid_strategy"`
	TrackingCode       string  `json:"tracking_code"`
	CampaignGroupID    string  `json:"campaign_group_id"`
	MarketingObjective string  `json:"marketing_objective"`
	StartDate          string  `json:"start_date"`
	EndDate            string  `json:"end_date"`
}

func campaignFrom(o obj) Campaign {
	active, _ := o["is_active"].(bool)
	return Campaign{
		ID:                 str(o["id"]),
		Name:               str(o["name"]),
		Status:             str(o["status"]),
		IsActive:           active,
		BrandingText:       str(o["branding_text"]),
		CPC:                num(o["cpc"]),
		DailyCap:           num(o["daily_cap"]),
		SpendingLimit:      num(o["spending_limit"]),
		BidStrategy:        str(o["bid_strategy"]),
		TrackingCode:       str(o["tracking_code"]),
		CampaignGroupID:    str(o["campaign_group_id"]),
		MarketingObjective: str(o["marketing_objective"]),
		StartDate:          str(o["start_date"]),
		EndDate:            str(o["end_date"]),
	}
}

// Campaigns lists an account's campaigns, leaving out deleted
// (TERMINATED) ones.
func (c *Client) Campaigns(ctx context.Context, account string) ([]Campaign, error) {
	if err := c.CheckAccount(account); err != nil {
		return nil, err
	}
	out, err := c.sendJSON(ctx, http.MethodGet, account+"/campaigns/", nil, true)
	if err != nil {
		return nil, err
	}
	list := []Campaign{}
	for _, r := range results(out) {
		if cp := campaignFrom(r); cp.Status != "TERMINATED" && cp.ID != "" && c.ownsCampaign(account, cp.ID) {
			list = append(list, cp)
		}
	}
	return list, nil
}

// NewCampaign is what the page asks for in a new campaign. There is no way
// to ask for a running one: every campaign is created paused
// (is_active false), and only a person turns it on, in Taboola's own
// dashboard.
//
// For a copy (DuplicateCampaign) only Name is needed, and a zero or empty
// field keeps what the copied campaign has.
type NewCampaign struct {
	Name, Brand string
	CPC         float64
	DailyCap    float64
	// SpendingLimit is the campaign's total budget; 0 means none (or, for a
	// copy, the copied one's).
	SpendingLimit float64
	// Countries are ISO 3166 two-letter codes; empty means US.
	Countries []string
	// Platforms are DESK, PHON and TBLT; empty means DESK and PHON.
	Platforms    []string
	TrackingCode string
	// MarketingObjective is DRIVE_WEBSITE_TRAFFIC when empty.
	MarketingObjective string
	// BidStrategy is FIXED (the default) or SMART; both take CPC, under the
	// same ceiling.
	BidStrategy string
	// StartDate and EndDate are "YYYY-MM-DD", or empty for none.
	StartDate, EndDate string
	// GroupID puts the campaign in a campaign group, which may then carry the
	// budget. Taboola lets it be set only when the campaign is made
	// (campaign_group_id is final); proven on the real API on 2026-09-29.
	// A copy (DuplicateCampaign) stays in its original's group, so it takes
	// no GroupID.
	GroupID string
}

// maxBrand is Taboola's limit on branding_text.
const maxBrand = 25

var (
	countryCode = regexp.MustCompile(`^[A-Z]{2}$`)
	platforms   = map[string]bool{"DESK": true, "PHON": true, "TBLT": true}
	objectives  = map[string]bool{"DRIVE_WEBSITE_TRAFFIC": true, "LEADS_GENERATION": true, "ONLINE_PURCHASES": true, "BRAND_AWARENESS": true}
	bids        = map[string]bool{"FIXED": true, "SMART": true}
	digits      = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// body checks n against the ceilings and returns the body Taboola takes.
// full is a new campaign, where everything the guard asks for must be set
// and defaults are filled; otherwise (a copy) only what is set is checked
// and sent. Either way is_active is false.
func (n NewCampaign) body(maxCPC, maxDailyCap float64, full bool) (obj, error) {
	name := strings.TrimSpace(n.Name)
	brand := strings.TrimSpace(n.Brand)
	switch {
	case name == "":
		return nil, refuse("dê um nome à campanha")
	case utf8.RuneCountInString(name) > 200 || strings.ContainsAny(name, "\r\n\t"):
		return nil, refuse("nome da campanha inválido (uma linha, até 200 caracteres)")
	case brand == "" && full:
		return nil, refuse("escreva a marca (branding text) da campanha")
	case utf8.RuneCountInString(brand) > maxBrand || strings.ContainsAny(brand, "\r\n\t"):
		return nil, refuse("a marca pode ter no máximo 25 caracteres")
	case (full || n.CPC != 0) && (!(n.CPC > 0) || n.CPC > maxCPC):
		return nil, refuse("o CPC deve ficar entre 0 e %s", usd(maxCPC))
	case (full || n.DailyCap != 0) && (!(n.DailyCap > 0) || n.DailyCap > maxDailyCap):
		return nil, refuse("o limite diário deve ficar entre 0 e %s", usd(maxDailyCap))
	case n.SpendingLimit < 0 || n.SpendingLimit > 30*maxDailyCap:
		return nil, refuse("o orçamento total deve ficar entre 0 (sem limite) e %s", usd(30*maxDailyCap))
	}
	b := obj{"name": name, "is_active": false}
	if brand != "" {
		b["branding_text"] = brand
	}
	if n.CPC != 0 {
		b["cpc"] = n.CPC
	}
	if n.DailyCap != 0 {
		b["daily_cap"] = n.DailyCap
		b["daily_ad_delivery_model"] = "STRICT"
	}
	switch {
	case n.SpendingLimit > 0:
		b["spending_limit_model"] = "ENTIRE"
		b["spending_limit"] = n.SpendingLimit
	case full:
		// No total budget of its own: none, or the group's when it is in one.
		b["spending_limit_model"] = "NONE"
	}

	obj0 := strings.ToUpper(strings.TrimSpace(n.MarketingObjective))
	if obj0 == "" && full {
		obj0 = "DRIVE_WEBSITE_TRAFFIC"
	}
	if obj0 != "" {
		if !objectives[obj0] {
			return nil, refuse("objetivo %q inválido: use DRIVE_WEBSITE_TRAFFIC, LEADS_GENERATION, ONLINE_PURCHASES ou BRAND_AWARENESS", oneLine(n.MarketingObjective, 40))
		}
		b["marketing_objective"] = obj0
	}
	bid := strings.ToUpper(strings.TrimSpace(n.BidStrategy))
	if bid == "" && full {
		bid = "FIXED"
	}
	if bid != "" {
		if !bids[bid] {
			return nil, refuse("estratégia de lance %q inválida: use FIXED ou SMART", oneLine(n.BidStrategy, 40))
		}
		b["bid_strategy"] = bid
	}

	countries := append([]string(nil), n.Countries...)
	if len(countries) == 0 && full {
		countries = []string{"US"}
	}
	for i, cc := range countries {
		countries[i] = strings.ToUpper(strings.TrimSpace(cc))
		if !countryCode.MatchString(countries[i]) {
			return nil, refuse("país %q inválido: use o código de duas letras (US, BR)", oneLine(cc, 10))
		}
	}
	if len(countries) > 0 {
		b["country_targeting"] = obj{"type": "INCLUDE", "value": countries}
	}
	plats := append([]string(nil), n.Platforms...)
	if len(plats) == 0 && full {
		plats = []string{"DESK", "PHON"}
	}
	for i, p := range plats {
		plats[i] = strings.ToUpper(strings.TrimSpace(p))
		if !platforms[plats[i]] {
			return nil, refuse("plataforma %q inválida: use DESK, PHON ou TBLT", oneLine(p, 10))
		}
	}
	if len(plats) > 0 {
		b["platform_targeting"] = obj{"type": "INCLUDE", "value": plats}
	}

	if tc := strings.TrimSpace(n.TrackingCode); tc != "" {
		if utf8.RuneCountInString(tc) > 2000 || strings.ContainsAny(tc, "\r\n\t ") {
			return nil, refuse("código de rastreamento inválido (sem espaços, até 2000 caracteres)")
		}
		b["tracking_code"] = tc
	}

	start, end := strings.TrimSpace(n.StartDate), strings.TrimSpace(n.EndDate)
	var from, to time.Time
	var err error
	if start != "" {
		if from, err = time.Parse(time.DateOnly, start); err != nil {
			return nil, refuse("data de início %q inválida: use AAAA-MM-DD", oneLine(start, 20))
		}
		b["start_date"] = start
	}
	if end != "" {
		if to, err = time.Parse(time.DateOnly, end); err != nil {
			return nil, refuse("data de fim %q inválida: use AAAA-MM-DD", oneLine(end, 20))
		}
		b["end_date"] = end
	}
	if start != "" && end != "" && to.Before(from) {
		return nil, refuse("a data de fim vem antes da data de início")
	}

	if g := strings.TrimSpace(n.GroupID); g != "" {
		if !digits.MatchString(g) {
			return nil, refuse("id de grupo de campanhas %q inválido: só números", oneLine(g, 30))
		}
		// Sent as Taboola sends ids back: a string of digits.
		b["campaign_group_id"] = g
	}
	return b, nil
}

// CreateCampaign creates a paused campaign in account. It is never repeated
// after a 5xx, which may have created it.
func (c *Client) CreateCampaign(ctx context.Context, account string, n NewCampaign) (Campaign, error) {
	if err := c.CheckAccount(account); err != nil {
		return Campaign{}, err
	}
	b, err := n.body(c.s.MaxCPC, c.s.MaxDailyCap, true)
	if err != nil {
		return Campaign{}, err
	}
	if err := c.checkOwnCampaignBody(account, n); err != nil {
		return Campaign{}, err
	}
	out, err := c.sendJSON(ctx, http.MethodPost, account+"/campaigns/", b, false)
	if err != nil {
		return Campaign{}, err
	}
	cp := campaignFrom(out)
	if err := c.rememberOrSay(account, cp.ID, ""); err != nil {
		return cp, err
	}
	c.log.Info("taboola campaign created", "account", account, "campaign", cp.ID, "active", cp.IsActive,
		"cpc", cp.CPC, "daily_cap", cp.DailyCap, "spending_limit", cp.SpendingLimit, "group", cp.CampaignGroupID)
	return cp, nil
}

// DuplicateCampaign copies campaign from (without its ads) into a new,
// paused campaign named n.Name, with whatever else n sets in place of the
// copy's. Proven on the real API on 2026-09-29: the copy took the new name,
// brand, CPC and daily cap, came back paused, and stayed in the original's
// campaign group, which is why n may not name another one.
func (c *Client) DuplicateCampaign(ctx context.Context, account, from string, n NewCampaign) (Campaign, error) {
	if err := c.CheckAccount(account); err != nil {
		return Campaign{}, err
	}
	if err := CheckCampaignID(from); err != nil {
		return Campaign{}, err
	}
	if strings.TrimSpace(n.GroupID) != "" {
		return Campaign{}, refuse("uma cópia fica no grupo da campanha copiada: não escolha outro grupo")
	}
	b, err := n.body(c.s.MaxCPC, c.s.MaxDailyCap, false)
	if err != nil {
		return Campaign{}, err
	}
	if err := c.checkOwnCampaign(account, from); err != nil {
		return Campaign{}, err
	}
	if err := c.checkOwnCampaignBody(account, n); err != nil {
		return Campaign{}, err
	}
	b["duplicate_settings"] = obj{"include_items": false}
	out, err := c.sendJSON(ctx, http.MethodPost, campaignPath(account, from)+"/duplicate/", b, false)
	if err != nil {
		return Campaign{}, err
	}
	cp := campaignFrom(out)
	if err := c.rememberOrSay(account, cp.ID, ""); err != nil {
		return cp, err
	}
	c.log.Info("taboola campaign copied", "account", account, "from", from, "campaign", cp.ID, "active", cp.IsActive)
	return cp, nil
}

// Group is one campaign group as the page shows it.
type Group struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Status             string  `json:"status"`
	SpendingLimit      float64 `json:"spending_limit"`
	SpendingLimitModel string  `json:"spending_limit_model"`
	MarketingObjective string  `json:"marketing_objective"`
}

func groupFrom(o obj) Group {
	return Group{
		ID:                 str(o["id"]),
		Name:               str(o["name"]),
		Status:             str(o["status"]),
		SpendingLimit:      num(o["spending_limit"]),
		SpendingLimitModel: str(o["spending_limit_model"]),
		MarketingObjective: str(o["marketing_objective"]),
	}
}

// Groups lists an account's campaign groups, leaving out deleted
// (TERMINATED) ones. Not yet tried on the real API beyond reading one group.
func (c *Client) Groups(ctx context.Context, account string) ([]Group, error) {
	if err := c.CheckAccount(account); err != nil {
		return nil, err
	}
	out, err := c.sendJSON(ctx, http.MethodGet, account+"/campaigns_group/", nil, true)
	if err != nil {
		return nil, err
	}
	list := []Group{}
	for _, r := range results(out) {
		if g := groupFrom(r); g.Status != "TERMINATED" && g.ID != "" && c.ownsGroup(account, g.ID) {
			list = append(list, g)
		}
	}
	return list, nil
}

// NewGroup is a campaign group that carries a budget for the campaigns put
// in it.
type NewGroup struct {
	Name string
	// SpendingLimit is the group's budget, above 0 and at most 30 daily caps.
	SpendingLimit float64
	// Model is MONTHLY or ENTIRE.
	Model string
	// MarketingObjective must match its campaigns'; DRIVE_WEBSITE_TRAFFIC
	// when empty.
	MarketingObjective string
}

// CreateGroup creates a paused campaign group. Proven on the real API on
// 2026-09-29, without bid_strategy (read-only on a group).
func (c *Client) CreateGroup(ctx context.Context, account string, n NewGroup) (Group, error) {
	if err := c.CheckAccount(account); err != nil {
		return Group{}, err
	}
	name := strings.TrimSpace(n.Name)
	model := strings.ToUpper(strings.TrimSpace(n.Model))
	objective := strings.ToUpper(strings.TrimSpace(n.MarketingObjective))
	if objective == "" {
		objective = "DRIVE_WEBSITE_TRAFFIC"
	}
	switch {
	case name == "":
		return Group{}, refuse("dê um nome ao grupo de campanhas")
	case utf8.RuneCountInString(name) > 200 || strings.ContainsAny(name, "\r\n\t"):
		return Group{}, refuse("nome do grupo inválido (uma linha, até 200 caracteres)")
	case !(n.SpendingLimit > 0) || n.SpendingLimit > 30*c.s.MaxDailyCap:
		return Group{}, refuse("o orçamento do grupo deve ficar entre 0 e %s", usd(30*c.s.MaxDailyCap))
	case model != "MONTHLY" && model != "ENTIRE":
		return Group{}, refuse("o orçamento do grupo deve ser MONTHLY ou ENTIRE")
	case !objectives[objective]:
		return Group{}, refuse("objetivo %q inválido", oneLine(n.MarketingObjective, 40))
	}
	if err := c.checkOwnName(name, "do grupo"); err != nil {
		return Group{}, err
	}
	out, err := c.sendJSON(ctx, http.MethodPost, account+"/campaigns_group/", obj{
		"name":                 name,
		"marketing_objective":  objective,
		"spending_limit_model": model,
		"spending_limit":       n.SpendingLimit,
		// No bid_strategy: Taboola answers "Trying to modify a read-only
		// field" for it on a group (seen 2026-09-29).
		"is_active": false,
	}, false)
	if err != nil {
		return Group{}, err
	}
	g := groupFrom(out)
	if err := c.rememberOrSay(account, "", g.ID); err != nil {
		return g, err
	}
	c.log.Info("taboola campaign group created", "account", account, "group", g.ID, "spending_limit", g.SpendingLimit)
	return g, nil
}

// checkOwnCampaignBody applies OnlyOwn to a new campaign or copy: its name
// carries the prefix, and a group it joins is one this client created (a
// campaign in someone else's group would spend from their budget).
func (c *Client) checkOwnCampaignBody(account string, n NewCampaign) error {
	if !c.s.OnlyOwn {
		return nil
	}
	if err := c.checkOwnName(n.Name, "da campanha"); err != nil {
		return err
	}
	if g := strings.TrimSpace(n.GroupID); g != "" && !c.ownsGroup(account, g) {
		return refuse("conta de testes: o grupo %s não foi criado aqui e não pode ser usado", oneLine(g, 30))
	}
	return nil
}

// CheckCampaign refuses a campaign id that is not one, or, with OnlyOwn,
// one this client did not create. It sends nothing.
func (c *Client) CheckCampaign(account, id string) error {
	if err := c.CheckAccount(account); err != nil {
		return err
	}
	if err := CheckCampaignID(id); err != nil {
		return err
	}
	return c.checkOwnCampaign(account, id)
}

// CheckCampaignID refuses anything but a Taboola campaign id (digits).
func CheckCampaignID(id string) error {
	if !digits.MatchString(id) {
		return refuse("id de campanha %q inválido: só números", oneLine(id, 30))
	}
	return nil
}

func campaignPath(account, id string) string {
	return account + "/campaigns/" + url.PathEscape(id)
}

func usd(v float64) string { return "US$ " + strconv.FormatFloat(v, 'f', 2, 64) }
