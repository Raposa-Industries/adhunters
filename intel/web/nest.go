package web

import "sort"

// accountRows is one account in the overview's campaign table: the account
// on top, with its numbers summed, and its campaigns under it.
type accountRows struct {
	Account   string
	Campaigns []Campaign
	R         *Result // nil when none of its campaigns has numbers
}

// byAccount nests campaigns under their accounts, the account that spent
// most first. Within an account the campaigns keep their order. An
// account's sales source is the tracker's when any campaign's is; its
// revenue, profit and ROI then count only those campaigns (the network's
// count has no revenue), and its cost per sale is all its spend over all
// its sales.
func byAccount(cs []Campaign) []accountRows {
	var out []accountRows
	at := map[string]int{}
	for _, c := range cs {
		i, ok := at[c.Account]
		if !ok {
			i = len(out)
			at[c.Account] = i
			out = append(out, accountRows{Account: c.Account})
		}
		out[i].Campaigns = append(out[i].Campaigns, c)
	}
	for i := range out {
		out[i].R = sum(out[i].Campaigns)
	}
	sort.SliceStable(out, func(a, b int) bool {
		sa, sb := spent(out[a].R), spent(out[b].R)
		if sa != sb {
			return sa > sb
		}
		return out[a].Account < out[b].Account
	})
	return out
}

func spent(r *Result) float64 {
	if r == nil {
		return -1
	}
	return r.Spent
}

func sum(cs []Campaign) *Result {
	var s *Result
	var trkSpent float64
	for _, c := range cs {
		r := c.R
		if r == nil {
			continue
		}
		if s == nil {
			s = &Result{Window: r.Window, SalesSource: "network"}
		}
		s.Impressions += r.Impressions
		s.Clicks += r.Clicks
		s.Spent += r.Spent
		s.NetSales += r.NetSales
		s.TrkClicks += r.TrkClicks
		s.LPViews += r.LPViews
		s.LPClicks += r.LPClicks
		s.Sales += r.Sales
		if r.SalesSource != "network" {
			s.SalesSource = "tracker"
			s.Revenue += r.Revenue
			s.Profit += r.Profit
			trkSpent += r.Spent
		}
	}
	if s == nil {
		return nil
	}
	if trkSpent > 0 {
		roi := s.Profit / trkSpent
		s.ROI = &roi
	}
	if s.Sales > 0 {
		cps := s.Spent / float64(s.Sales)
		s.CPS = &cps
	}
	return s
}
