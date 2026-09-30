package write

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Ad is one item already in a campaign, as Launch shows and copies it.
type Ad struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	CTA          string `json:"cta"`
	CustomID     string `json:"custom_id"`
	AI           bool   `json:"ai"`
	// Status is Taboola's (RUNNING, PAUSED, PENDING_APPROVAL, REJECTED…);
	// Approval its review state (APPROVED, PENDING, REJECTED).
	Status   string `json:"status"`
	Approval string `json:"approval"`
	IsActive bool   `json:"is_active"`
}

func adFrom(o obj) Ad {
	active, _ := o["is_active"].(bool)
	cd, _ := o["custom_data"].(obj)
	cta, _ := o["cta"].(obj)
	ai, _ := o["ai_disclosure"].(obj)
	return Ad{
		ID:           str(o["id"]),
		Title:        str(o["title"]),
		Description:  str(o["description"]),
		URL:          str(o["url"]),
		ThumbnailURL: str(o["thumbnail_url"]),
		CTA:          str(cta["cta_type"]),
		CustomID:     str(cd["custom_id"]),
		AI:           str(ai["status"]) == "AI_GENERATED",
		Status:       str(o["status"]),
		Approval:     str(o["approval_state"]),
		IsActive:     active,
	}
}

// NewItem is the ad again, to make it in another campaign (a copy, a move).
func (a Ad) NewItem() NewItem {
	return NewItem{URL: a.URL, Title: a.Title, Description: a.Description, ThumbnailURL: a.ThumbnailURL, CTA: a.CTA, CustomID: a.CustomID, AI: a.AI}
}

// Ads lists a campaign's items, leaving out deleted (TERMINATED) ones.
func (c *Client) Ads(ctx context.Context, account, campaign string) ([]Ad, error) {
	if err := c.CheckCampaign(account, campaign); err != nil {
		return nil, err
	}
	out, err := c.sendJSON(ctx, http.MethodGet, campaignPath(account, campaign)+"/items/", nil, true)
	if err != nil {
		return nil, err
	}
	list := []Ad{}
	for _, r := range results(out) {
		if a := adFrom(r); a.ID != "" && a.Status != "TERMINATED" {
			list = append(list, a)
		}
	}
	return list, nil
}

// PauseCampaign sets is_active false on a campaign and checks Taboola's
// answer. There is no way to start one here: only a person does, in
// Taboola's own dashboard. Repeating a pause is harmless, so a 5xx is retried.
func (c *Client) PauseCampaign(ctx context.Context, account, campaign string) error {
	if err := c.CheckCampaign(account, campaign); err != nil {
		return err
	}
	out, err := c.sendJSON(ctx, http.MethodPost, campaignPath(account, campaign)+"/", obj{"is_active": false}, true)
	if err != nil {
		return err
	}
	if out["is_active"] != false {
		return &Error{Status: http.StatusOK, Message: "a Taboola não confirmou a pausa da campanha " + campaign}
	}
	c.log.Info("taboola campaign paused", "account", account, "campaign", campaign)
	return nil
}

// PauseAd pauses one item of a campaign.
func (c *Client) PauseAd(ctx context.Context, account, campaign, item string) error {
	if err := c.CheckCampaign(account, campaign); err != nil {
		return err
	}
	if !digits.MatchString(item) {
		return refuse("id de anúncio %q inválido: só números", oneLine(item, 30))
	}
	return c.pauseItem(ctx, account, campaign, item)
}

// Change is what may change on an existing campaign; a zero field stays as
// it is. Nothing here can turn a campaign on.
type Change struct {
	Name          string  `json:"name,omitempty"`
	CPC           float64 `json:"cpc,omitempty"`
	DailyCap      float64 `json:"daily_cap,omitempty"`
	SpendingLimit float64 `json:"spending_limit,omitempty"`
}

// Empty reports whether the change changes nothing.
func (ch Change) Empty() bool { return ch == Change{} }

// ChangeCampaign sends a change, checked against the same ceilings as a new
// campaign. A daily cap over the campaign's own total is refused before it
// is sent when both are in the change; Taboola refuses the rest. Never
// repeated after a 5xx.
func (c *Client) ChangeCampaign(ctx context.Context, account, campaign string, ch Change) (Campaign, error) {
	if err := c.CheckCampaign(account, campaign); err != nil {
		return Campaign{}, err
	}
	if ch.Empty() {
		return Campaign{}, refuse("nada para mudar")
	}
	b := obj{}
	name := strings.TrimSpace(ch.Name)
	switch {
	case name != "" && (utf8.RuneCountInString(name) > 200 || strings.ContainsAny(name, "\r\n\t")):
		return Campaign{}, refuse("nome da campanha inválido (uma linha, até 200 caracteres)")
	case ch.CPC < 0 || ch.CPC > c.s.MaxCPC:
		return Campaign{}, refuse("o CPC deve ficar entre 0 e %s", usd(c.s.MaxCPC))
	case ch.DailyCap < 0 || ch.DailyCap > c.s.MaxDailyCap:
		return Campaign{}, refuse("o limite diário deve ficar entre 0 e %s", usd(c.s.MaxDailyCap))
	case ch.SpendingLimit < 0 || ch.SpendingLimit > 30*c.s.MaxDailyCap:
		return Campaign{}, refuse("o orçamento total deve ficar entre 0 e %s", usd(30*c.s.MaxDailyCap))
	case ch.SpendingLimit > 0 && ch.DailyCap > ch.SpendingLimit:
		return Campaign{}, refuse("o limite por dia (%s) passa do limite total (%s)", usd(ch.DailyCap), usd(ch.SpendingLimit))
	}
	if name != "" {
		if err := c.checkOwnName(name, "da campanha"); err != nil {
			return Campaign{}, err
		}
		b["name"] = name
	}
	if ch.CPC > 0 {
		b["cpc"] = ch.CPC
	}
	if ch.DailyCap > 0 {
		b["daily_cap"] = ch.DailyCap
	}
	if ch.SpendingLimit > 0 {
		b["spending_limit_model"] = "ENTIRE"
		b["spending_limit"] = ch.SpendingLimit
	}
	out, err := c.sendJSON(ctx, http.MethodPost, campaignPath(account, campaign)+"/", b, false)
	if err != nil {
		return Campaign{}, err
	}
	c.log.Info("taboola campaign changed", "account", account, "campaign", campaign, "change", b)
	return campaignFrom(out), nil
}

// Campaign reads one campaign.
func (c *Client) Campaign(ctx context.Context, account, campaign string) (Campaign, error) {
	if err := c.CheckCampaign(account, campaign); err != nil {
		return Campaign{}, err
	}
	out, err := c.sendJSON(ctx, http.MethodGet, campaignPath(account, campaign)+"/", nil, true)
	if err != nil {
		return Campaign{}, err
	}
	return campaignFrom(out), nil
}
