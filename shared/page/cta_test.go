package page

import "testing"

func TestFindCTA(t *testing.T) {
	cases := []struct {
		name, page, html, want string
	}{
		{"most repeated offer link wins over the page itself",
			"https://americanhealthdaily.site/advertorial/?utm_id=1",
			`<a href="/advertorial/?a=1">x</a><a href="/advertorial/?a=2">x</a><a href="/advertorial/#top">x</a>
			 <a href="https://www.buygoods.com/secure/checkout.html?product=1">buy</a>
			 <a href="https://www.buygoods.com/secure/checkout.html?product=1">buy</a>
			 <a href="/privacy">p</a><a href="/privacy">p</a><a href="/privacy">p</a>`,
			"https://www.buygoods.com/secure/checkout.html?product=1"},
		{"a tracker click link counts even when shown once",
			"https://60-minutes.org/news/x/",
			`<a href="https://track.60-minutes.org/click/1">a</a><a href="https://track.60-minutes.org/click/3">b</a>`,
			"https://track.60-minutes.org/click/1"},
		{"no repeated link, no step",
			"https://try.smoothspine.com/lp",
			`<a href="https://checkoutchamp.com/">x</a><a href="mailto:a@b.com">m</a>`,
			""},
	}
	for _, c := range cases {
		if got := FindCTA([]byte(c.html), c.page); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNewsBreakPixel(t *testing.T) {
	p := ExtractPixels(`<script>nbpix('init', 'ID-1963229365139505154'); nbpix("init","ID-1963229365139505154");</script>`)
	nb, ok := p["newsbreak"].(map[string]any)
	if !ok {
		t.Fatal("NewsBreak pixel not found")
	}
	if ids := nb["ids"].([]string); len(ids) != 1 || ids[0] != "1963229365139505154" {
		t.Fatalf("got ids %v", ids)
	}
}

func TestCheckoutSellers(t *testing.T) {
	cases := map[string]CheckoutInfo{
		"https://buygoods.com/secure/checkout.html?aff_id=1854&account_id=12377&product_codename=x": {Platform: "BuyGoods", MerchantID: "12377"},
		"https://www.jvzoo.com/b/117603/446369/99?aid=3624185":                                      {Platform: "JVZoo", MerchantID: "117603"},
	}
	for u, want := range cases {
		if got := DetectCheckout("<html></html>", u); got != want {
			t.Errorf("%s: got %+v, want %+v", u, got, want)
		}
	}
}

func TestWithClickID(t *testing.T) {
	// The dark page of everviewjournal.com (2026-09-26): served at the root,
	// with the click id only in its <base href> and in the script that patches
	// the links.
	dark := `<html><head><base href="https://everviewjournal.com/index.php/040-tb-ps21-mm/?sub1=50521055&rtkcid=6ab838142a901d0b054ada2d&rtkcmpid=6ab325d6f5d686da814c80e2"></head>
		<body><a href="https://rt.everviewjournal.com/preclick">CLICK TO WATCH THE VIDEO</a>
		<script>var clickId = "6ab838142a901d0b054ada2d";</script></body></html>`
	cases := []struct {
		name, link, page, html, want string
	}{
		{"click id from the page",
			"https://rt.everviewjournal.com/preclick", "https://everviewjournal.com/?sub1=50521055", dark,
			"https://rt.everviewjournal.com/preclick?clickid=6ab838142a901d0b054ada2d"},
		{"click id from the page address first",
			"https://trk.example.com/click/2", "https://lander.example.com/a?rtkcid=aaaaaaaaaaaaaaaaaaaaaaaa", dark,
			"https://trk.example.com/click/2?clickid=aaaaaaaaaaaaaaaaaaaaaaaa"},
		{"an unfilled macro is replaced",
			"https://trk.example.com/click?clickid={clickid}", "https://lander.example.com/a?rtkcid=aaaaaaaaaaaaaaaaaaaaaaaa", "",
			"https://trk.example.com/click?clickid=aaaaaaaaaaaaaaaaaaaaaaaa"},
		{"a link that has its click id keeps it",
			"https://trk.example.com/click?clickid=bbbbbbbbbbbbbbbbbbbbbbbb", "https://lander.example.com/a?rtkcid=aaaaaaaaaaaaaaaaaaaaaaaa", "",
			"https://trk.example.com/click?clickid=bbbbbbbbbbbbbbbbbbbbbbbb"},
		{"not a tracker click link",
			"https://everviewjournal.com/040-tb-vsl-wt/", "https://everviewjournal.com/", dark,
			"https://everviewjournal.com/040-tb-vsl-wt/"},
		{"no click id on the page",
			"https://rt.everviewjournal.com/preclick", "https://everviewjournal.com/", `<a href="https://rt.everviewjournal.com/preclick">x</a>`,
			"https://rt.everviewjournal.com/preclick"},
	}
	for _, c := range cases {
		if got := WithClickID(c.link, []byte(c.html), c.page); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
