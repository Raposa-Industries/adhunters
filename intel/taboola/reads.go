package taboola

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"time"
)

// The reads Backstage offers for one account. Each returns the raw answer;
// Results reads the rows out of it.

// CurrentAccount is the account the credentials belong to.
func (c *Client) CurrentAccount(ctx context.Context) (*Response, error) {
	return c.Get(ctx, "users/current/account", nil)
}

// AllowedAccounts lists every account the credentials may read (a network
// account's credentials see all its accounts).
func (c *Client) AllowedAccounts(ctx context.Context) (*Response, error) {
	return c.Get(ctx, "users/current/allowed-accounts", nil)
}

// Campaigns lists an account's campaigns with every setting.
func (c *Client) Campaigns(ctx context.Context, account string) (*Response, error) {
	return c.Get(ctx, url.PathEscape(account)+"/campaigns", nil)
}

// Items lists one campaign's items.
func (c *Client) Items(ctx context.Context, account, campaignID string) (*Response, error) {
	return c.Get(ctx, url.PathEscape(account)+"/campaigns/"+url.PathEscape(campaignID)+"/items/", nil)
}

// Report reads one report ("campaign-summary", "top-campaign-content") split
// by one dimension ("day", "site_breakdown", "item_breakdown", …) for the
// days from..to, both included, in the account's time zone.
func (c *Client) Report(ctx context.Context, account, report, dimension string, from, to time.Time, extra url.Values) (*Response, error) {
	q := url.Values{"start_date": {from.Format(time.DateOnly)}, "end_date": {to.Format(time.DateOnly)}}
	for k, v := range extra {
		q[k] = v
	}
	return c.Get(ctx, url.PathEscape(account)+"/reports/"+report+"/dimensions/"+dimension, q)
}

// Rows are the rows of a list or report answer, each as its JSON object.
type Rows []map[string]any

// Results reads the "results" array most Backstage answers wrap their rows
// in. An answer that is a single object comes back as one row.
func Results(body []byte) (Rows, error) {
	var wrapped struct {
		Results Rows `json:"results"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Results != nil {
		return wrapped.Results, nil
	}
	var one map[string]any
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, err
	}
	return Rows{one}, nil
}

// Fields is every key seen in any row, sorted.
func (r Rows) Fields() []string {
	seen := map[string]bool{}
	for _, row := range r {
		for k := range row {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
