package judge

import (
	"fmt"
	"strings"
)

// The Telegram messages: one line per campaign or ad, a coloured dot first,
// its name (a link to its page), then what happened in a word or two. No
// account, no ids, no "Intel" heading: the owner asked (2 Oct) for "a quick
// glance tells everything".

// tgEsc escapes text for Telegram's HTML: only <, > and & (Telegram does not
// read &#34;, which html.EscapeString writes for a quote).
func tgEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// tgLink is text linked to href, or bold text without one.
func tgLink(text, href string) string {
	if href == "" {
		return "<b>" + tgEsc(text) + "</b>"
	}
	return `<a href="` + strings.ReplaceAll(tgEsc(href), `"`, "&quot;") + `">` + tgEsc(text) + "</a>"
}

// statusDot is the colour of a delivery status: green delivers, yellow
// waits, orange stopped on budget, red was stopped by Taboola, white was
// stopped by someone or ended, black is gone.
func statusDot(s string) string {
	switch s {
	case "RUNNING":
		return "🟢"
	case "PENDING_APPROVAL", "PENDING_START_DATE":
		return "🟡"
	case "DEPLETED", "DEPLETED_MONTHLY":
		return "🟠"
	case "REJECTED", "TERMINATED", "FROZEN":
		return "🔴"
	case "DELETED", "GROUP_DELETED":
		return "⚫"
	default:
		return "⚪"
	}
}

// statusLine is one campaign's new delivery status.
func statusLine(name, status, href string) string {
	return statusDot(status) + " " + tgLink(name, href) + " · " + tgEsc(StatusWord(status))
}

// reasonWords is a Taboola review code for people:
// TITLE_INITIAL_LETTER_NOT_CAPITALIZED is "Title initial letter not capitalized".
func reasonWords(code string) string {
	w := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "_", " "))
	if w == "" {
		return ""
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// alertLine is one alert: the dot of its weight, the campaign's (or the
// ad's) name, and what is wrong in a few words.
func alertLine(kind, name, href string, n map[string]any) string {
	num := func(k string) string {
		switch v := n[k].(type) {
		case float64:
			if v == float64(int64(v)) {
				return fmt.Sprint(int64(v))
			}
			return fmt.Sprintf("%.2f", v)
		case nil:
			return "?"
		default:
			return fmt.Sprint(v)
		}
	}
	var dot, what string
	switch kind {
	case "runaway":
		dot, what = "🔴", "$"+num("spent")+" spent today, no sale"
	case "tracking_gap":
		dot, what = "🟠", "RedTrack got "+num("tracker_clicks")+" of "+num("taboola_clicks")+" clicks"
	case "page_gap":
		dot, what = "🟠", num("lp_views")+" page views from "+num("tracker_clicks")+" clicks"
	case "postback_gap":
		dot, what = "🟡", "Taboola saw "+num("network_sales")+" of "+num("tracker_sales")+" sales yesterday"
	case "item_rejected":
		dot, what = "🔴", "Rejected"
		if r, _ := n["reject_reason"].(string); r != "" {
			what += ": " + reasonWords(r)
		}
	default:
		dot, what = "⚪", kindWords[kind]
	}
	return dot + " " + tgLink(name, href) + " · " + tgEsc(what)
}
