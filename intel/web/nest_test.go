package web

import "testing"

func TestByAccount(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cs := []Campaign{
		{ID: 1, Account: "b-sc", R: &Result{Spent: 10, Clicks: 5, Sales: 1, Revenue: 30, Profit: 20, SalesSource: "tracker", CPS: f(10)}},
		{ID: 2, Account: "a-sc", R: &Result{Spent: 100, Clicks: 50, Sales: 4, Revenue: 150, Profit: 50, SalesSource: "tracker"}},
		{ID: 3, Account: "b-sc"},
		{ID: 4, Account: "a-sc", R: &Result{Spent: 60, Clicks: 20, Sales: 2, Profit: -60, SalesSource: "network"}},
		{ID: 5, Account: "c-sc"},
	}
	out := byAccount(cs)
	if len(out) != 3 || out[0].Account != "a-sc" || out[1].Account != "b-sc" || out[2].Account != "c-sc" {
		t.Fatalf("order: %+v", out)
	}
	if ids := []int64{out[1].Campaigns[0].ID, out[1].Campaigns[1].ID}; ids[0] != 1 || ids[1] != 3 {
		t.Errorf("b-sc campaigns keep their order: %v", ids)
	}
	a := out[0].R
	// Spend, clicks and sales count every campaign; revenue, profit and ROI
	// only the tracker's (the network's count has no revenue).
	if a.Spent != 160 || a.Clicks != 70 || a.Sales != 6 || a.Revenue != 150 || a.Profit != 50 || a.SalesSource != "tracker" {
		t.Errorf("a-sc sum: %+v", a)
	}
	if a.ROI == nil || *a.ROI != 0.5 || a.CPS == nil || *a.CPS != 160.0/6 {
		t.Errorf("a-sc ROI %v, CPS %v", a.ROI, a.CPS)
	}
	if out[2].R != nil {
		t.Errorf("an account without numbers has none: %+v", out[2].R)
	}
	if only := sum([]Campaign{cs[3]}); only.SalesSource != "network" || only.ROI != nil {
		t.Errorf("network only: %+v", only)
	}
}
