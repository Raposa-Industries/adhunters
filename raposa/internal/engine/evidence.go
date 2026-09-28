package engine

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/raposa/internal/page"
)

// isEvidence says whether a visit's landing page belongs to the operator. A
// dark page does: only the operator serves it. A white page does only on the
// domain the ad's link points to, because a cloaker may send a reviewer to
// somebody else's site (a search engine, a store), and that site would then
// tie every operator that does the same into one.
func (r *run) isEvidence(outcome, landingURL string) bool {
	switch outcome {
	case "dark":
		return true
	case "white":
		own := registrableDomain(bareHost(r.ad().LandingHost))
		return own != "" && domainOf(landingURL) == own
	}
	return false
}

// evidenceStep is one page past the landing page, as evidence keeps it.
type evidenceStep struct {
	URL              string `json:"url"`
	Title            string `json:"title"`
	PageKind         string `json:"page_kind"`
	ClickedText      string `json:"clicked_text,omitempty"`
	CheckoutPlatform string `json:"checkout_platform,omitempty"`
	MerchantID       string `json:"merchant_id,omitempty"`
}

// storeEvidence keeps the landing page one visit reached, and the funnel
// behind it, once per investigation, so a deep sample does not count as a
// hundred visits. Runs in the step's transaction, after the pages have ids.
func (r *run) storeEvidence(ctx context.Context, tx pgx.Tx, out *visitOutcome, device string) error {
	if len(out.Pages) == 0 {
		return nil
	}
	land := out.Pages[0]
	ad := r.ad()
	campaign := ad.CampaignByDevice[device]
	if campaign == "" {
		campaign = ad.CampaignID
	}
	var account *int32
	if a, ok := ad.AccountByCampaign[campaign]; ok {
		account = &a
	}
	steps := []evidenceStep{}
	var sellerPlatform, sellerAccount string
	for i, p := range out.Pages[1:] {
		st := evidenceStep{URL: p.URL, Title: p.Title, PageKind: p.PageKind,
			CheckoutPlatform: p.CheckoutPlatform, MerchantID: p.CheckoutMerchantID}
		if i+1 < len(out.Steps) {
			st.ClickedText = out.Steps[i+1].ClickedText
		}
		steps = append(steps, st)
		if sellerAccount == "" && p.CheckoutMerchantID != "" {
			sellerPlatform, sellerAccount = p.CheckoutPlatform, p.CheckoutMerchantID
		}
	}
	hops := out.Steps[0].Hops
	if hops == nil {
		hops = []VisitHop{}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO raposa.evidence
			(investigation_id, creative_id, ad_id, account_id, campaign_external_id, device, outcome,
			 landing_page_id, final_url, domain, redirect_hops, page_kind, title, pixels,
			 checkout_platform, checkout_merchant_id, seller_platform, seller_account, funnel_steps)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14::jsonb, $15, $16, $17, $18, $19::jsonb)
		ON CONFLICT (investigation_id, landing_page_id) DO NOTHING`,
		r.inv.ID, r.inv.CreativeID, r.inv.AdID, account, campaign, device, out.Verdict.Outcome,
		out.pageIDs[0], clean(land.URL), page.ExtractCanonicalDomain(land.URL), jsonOr(hops, "[]"),
		orDefault(land.PageKind, "unknown"), clean(land.Title), jsonOr(land.Pixels, "{}"),
		clean(land.CheckoutPlatform), clean(land.CheckoutMerchantID), clean(sellerPlatform), clean(sellerAccount),
		jsonOr(steps, "[]"))
	if err != nil {
		return fmt.Errorf("store evidence for %s: %w", land.URL, err)
	}
	return nil
}
