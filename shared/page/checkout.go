package page

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	rxBuyGoodsMerchant = regexp.MustCompile(`buygoods\.com/[^"'\s<>]*?account_id=([0-9a-zA-Z_-]+)`)
	rxJVZooVendor      = regexp.MustCompile(`jvzoo\.com/b/([0-9]+)/`)
	rxClickbankHop     = regexp.MustCompile(`([0-9a-zA-Z_-]+)\.pay\.clickbank\.net`)
	rxClickbankHop2    = regexp.MustCompile(`hop\.clickbank\.net/\?affiliate=[^&]+&vendor=([0-9a-zA-Z_-]+)`)
	rxDigistore        = regexp.MustCompile(`digistore24\.com/redir/([0-9]+)/([0-9a-zA-Z_-]+)`)
	rxShopifyShop      = regexp.MustCompile(`myshopify\.com`)
)

type CheckoutInfo struct {
	Platform   string `json:"platform"`
	MerchantID string `json:"merchant_id,omitempty"`
}

func DetectCheckout(htmlContent string, finalURL string) CheckoutInfo {
	lowerHTML := strings.ToLower(htmlContent)
	lowerURL := strings.ToLower(finalURL)

	// BuyGoods
	if strings.Contains(lowerURL, "buygoods.com") || strings.Contains(lowerHTML, "buygoods.com") {
		merchantID := ""
		if m := rxBuyGoodsMerchant.FindStringSubmatch(finalURL); len(m) > 1 {
			merchantID = m[1]
		} else if m := rxBuyGoodsMerchant.FindStringSubmatch(htmlContent); len(m) > 1 {
			merchantID = m[1]
		}
		return CheckoutInfo{Platform: "BuyGoods", MerchantID: merchantID}
	}

	// ClickBank
	if strings.Contains(lowerURL, "clickbank.net") || strings.Contains(lowerHTML, "clickbank.net") {
		merchantID := ""
		if m := rxClickbankHop.FindStringSubmatch(htmlContent); len(m) > 1 {
			merchantID = m[1]
		} else if m := rxClickbankHop2.FindStringSubmatch(htmlContent); len(m) > 1 {
			merchantID = m[1]
		}
		return CheckoutInfo{Platform: "ClickBank", MerchantID: merchantID}
	}

	// JVZoo: jvzoo.com/b/<vendor>/<product>/<price point>
	if strings.Contains(lowerURL, "jvzoo.com") || strings.Contains(lowerHTML, "jvzoo.com/b/") {
		merchantID := ""
		if m := rxJVZooVendor.FindStringSubmatch(finalURL); len(m) > 1 {
			merchantID = m[1]
		} else if m := rxJVZooVendor.FindStringSubmatch(htmlContent); len(m) > 1 {
			merchantID = m[1]
		}
		return CheckoutInfo{Platform: "JVZoo", MerchantID: merchantID}
	}

	// Digistore24
	if strings.Contains(lowerURL, "digistore24") || strings.Contains(lowerHTML, "digistore24") {
		merchantID := ""
		if m := rxDigistore.FindStringSubmatch(htmlContent); len(m) > 2 {
			merchantID = m[2]
		}
		return CheckoutInfo{Platform: "Digistore24", MerchantID: merchantID}
	}

	// Shopify
	if rxShopifyShop.MatchString(lowerURL) || rxShopifyShop.MatchString(lowerHTML) || strings.Contains(lowerHTML, "shopify.checkout") {
		return CheckoutInfo{Platform: "Shopify"}
	}

	// Stripe
	if strings.Contains(lowerURL, "checkout.stripe.com") || strings.Contains(lowerHTML, "js.stripe.com") {
		return CheckoutInfo{Platform: "Stripe"}
	}

	// CheckoutChamp / Konnektive CRM
	if strings.Contains(lowerHTML, "checkoutchamp") || strings.Contains(lowerHTML, "konnektive") {
		return CheckoutInfo{Platform: "CheckoutChamp"}
	}

	// Cart66 / UltraCart / WooCommerce
	if strings.Contains(lowerHTML, "woocommerce") || strings.Contains(lowerHTML, "wc-api") {
		return CheckoutInfo{Platform: "WooCommerce"}
	}

	return CheckoutInfo{}
}

func DetectPageType(htmlContent string, finalURL string, checkout CheckoutInfo) string {
	lowerHTML := strings.ToLower(htmlContent)
	lowerURL := strings.ToLower(finalURL)

	if checkout.Platform != "" || strings.Contains(lowerURL, "checkout") || strings.Contains(lowerURL, "order") || strings.Contains(lowerURL, "cart") {
		return "CHECKOUT"
	}

	// VSL (Video Sales Letter)
	if strings.Contains(lowerHTML, "vidalytics") || strings.Contains(lowerHTML, "wistia") || strings.Contains(lowerHTML, "vsl") ||
		strings.Contains(lowerHTML, "player.vimeo.com") || strings.Contains(lowerHTML, "converteai") || strings.Contains(lowerHTML, "vturb") {
		return "VSL"
	}

	// Advertorial / Presell
	if strings.Contains(lowerHTML, "advertorial") || strings.Contains(lowerHTML, "sponsored content") || strings.Contains(lowerURL, "article") || strings.Contains(lowerURL, "report") {
		return "ADVERTORIAL"
	}

	// Quiz / Survey Funnel
	if strings.Contains(lowerHTML, "quiz") || strings.Contains(lowerHTML, "survey") || strings.Contains(lowerURL, "step1") || strings.Contains(lowerURL, "question") {
		return "QUIZ_SURVEY"
	}

	// Lead Magnet / Opt-in
	if strings.Contains(lowerHTML, "type=\"email\"") && strings.Contains(lowerHTML, "subscribe") {
		return "LEAD_MAGNET"
	}

	return "DIRECT_LANDER"
}

func ExtractCanonicalDomain(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	return host
}
