package policy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const collectionHTML = `<!doctype html><html><head><title>Policy &amp; Content Review | Realize - Advertiser Help Center</title></head>
<body><header><a href="/help/en/collections/999-other">Other</a></header>
<main>
 <h1>Policy &amp; Content Review</h1>
 <p>Everything about our policies</p>
 <div>12 articles</div>
 <a href="/help/en/collections/12166456-content-products-policies#h_1">Content &amp; Products Policies</a>
 <a href="/help/en/articles/3878202-prohibited-content?x=1">Prohibited Content</a>
</main></body></html>`

const subHTML = `<html><body><main><h1>Content &amp; Products Policies</h1>
 <a href="https://realize.com/help/en/articles/3878202-prohibited-content">Prohibited Content</a>
 <a href="/help/en/articles/7048957-faq">FAQ</a>
 <a href="/help/en/collections/11915686-policy-content-review">Back</a>
</main></body></html>`

func articleHTML(extra string) string {
	return `<html><body><nav>Home</nav><article>
 <h1>Prohibited Content</h1>
 <div>Written by Jane</div><div>Updated over a week ago</div>
 <p>Ads must not promote   weapons.</p>
 <ul><li><p>No tobacco</p></li><li>No&nbsp;gambling</li></ul>` + extra + `
 <script>var x = 1</script>
 <section><h2>Related Articles</h2><a href="/help/en/articles/1-unrelated">Related</a></section>
</article><footer>Did this answer your question?</footer></body></html>`
}

func TestParseArticle(t *testing.T) {
	p, err := Parse("https://realize.com/help/en/articles/3878202-prohibited-content?x=1#top", []byte(articleHTML("")))
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "articles/3878202" || p.Kind != Article || p.Title != "Prohibited Content" {
		t.Fatalf("got %+v", p)
	}
	want := []string{"## Prohibited Content", "Ads must not promote weapons.", "No tobacco", "• No gambling"}
	if !reflect.DeepEqual(p.Lines, want) {
		t.Errorf("lines = %q, want %q", p.Lines, want)
	}
	if p.Links != nil {
		t.Errorf("an article's links are not followed: %v", p.Links)
	}
	if p.URL != "https://realize.com/help/en/articles/3878202-prohibited-content" {
		t.Errorf("url = %s", p.URL)
	}
}

func TestParseCollectionLinks(t *testing.T) {
	p, err := Parse("https://realize.com/help/en/collections/11915686-policy-content-review", []byte(collectionHTML))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://realize.com/help/en/collections/12166456-content-products-policies",
		"https://realize.com/help/en/articles/3878202-prohibited-content",
	}
	if !reflect.DeepEqual(p.Links, want) {
		t.Errorf("links = %v, want %v", p.Links, want)
	}
	if p.Title != "Policy & Content Review" {
		t.Errorf("title = %q", p.Title)
	}
	for _, l := range p.Lines {
		if strings.Contains(l, "articles") {
			t.Errorf("article count kept: %q", l)
		}
	}
}

func TestParseRefusesAnEmptyPage(t *testing.T) {
	if _, err := Parse("https://realize.com/help/en/articles/1-x", []byte(`<html><body><script>challenge()</script></body></html>`)); err == nil {
		t.Error("a page with no text must fail, not read as an emptied policy")
	}
}

func TestDiff(t *testing.T) {
	add, rem := diff([]string{"a", "b", "c", "d"}, []string{"a", "c", "x", "d", "e"})
	if !reflect.DeepEqual(add, []string{"x", "e"}) || !reflect.DeepEqual(rem, []string{"b"}) {
		t.Errorf("added %v removed %v", add, rem)
	}
}

func TestCompare(t *testing.T) {
	old := &Snapshot{Pages: map[string]Page{
		"collections/1": {Key: "collections/1", Kind: Collection, Lines: []string{"x"}},
		"articles/1":    {Key: "articles/1", Kind: Article, Title: "A", Lines: []string{"a", "b"}},
		"articles/2":    {Key: "articles/2", Kind: Article, Title: "Gone", Lines: []string{"z"}},
	}}
	cur := &Snapshot{Pages: map[string]Page{
		"collections/1": {Key: "collections/1", Kind: Collection, Lines: []string{"y"}},
		"articles/1":    {Key: "articles/1", Kind: Article, Title: "A", Lines: []string{"a", "c"}},
		"articles/3":    {Key: "articles/3", Kind: Article, Title: "New", Lines: []string{"n"}},
	}}
	got := Compare(old, cur)
	var whats []string
	for _, c := range got {
		whats = append(whats, c.What+" "+c.Page.Key)
	}
	want := []string{"changed articles/1", "new articles/3", "removed articles/2"}
	if !reflect.DeepEqual(whats, want) {
		t.Fatalf("got %v, want %v", whats, want)
	}
	msg := Message(got[0])
	if !strings.Contains(msg, "➖ b") || !strings.Contains(msg, "➕ c") {
		t.Errorf("message: %s", msg)
	}
}

func TestMessageStaysShort(t *testing.T) {
	c := Change{What: "changed", Page: Page{Title: "<T>", URL: "https://x/a?b&c"}}
	for i := 0; i < 200; i++ {
		c.Added = append(c.Added, strings.Repeat("word ", 40))
	}
	msg := Message(c)
	if len(msg) > 4096 || !strings.Contains(msg, "more lines") || !strings.Contains(msg, "&lt;T&gt;") {
		t.Errorf("len %d: %s", len(msg), msg[:200])
	}
}

func TestCrawlAndReparse(t *testing.T) {
	article := articleHTML("")
	mux := http.NewServeMux()
	mux.HandleFunc("/help/en/collections/11915686-policy-content-review", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(collectionHTML)) })
	mux.HandleFunc("/help/en/collections/12166456-content-products-policies", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(subHTML)) })
	mux.HandleFunc("/help/en/articles/3878202-prohibited-content", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(article)) })
	mux.HandleFunc("/help/en/articles/7048957-faq", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><article><h1>FAQ</h1><p>Q and A</p></article></body></html>`))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	c := &Crawler{HTTP: srv.Client(), Roots: []string{srv.URL + "/help/en/collections/11915686-policy-content-review"}, Dir: t.TempDir()}
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	snap, dir, err := c.Crawl(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pages) != 4 || snap.Articles() != 2 {
		t.Fatalf("pages %d articles %d", len(snap.Pages), snap.Articles())
	}
	if _, ok := snap.Pages["collections/999"]; ok {
		t.Error("followed a link from outside the collection's main part")
	}
	again, err := Reparse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Pages, snap.Pages) || !again.Taken.Equal(now) {
		t.Error("a re-read of the saved crawl differs from the crawl")
	}
	dirs, _ := Crawls(c.Dir)
	if len(dirs) != 1 || dirs[0] != dir {
		t.Errorf("crawls = %v", dirs)
	}

	// A listed page that cannot be read fails the crawl.
	mux.HandleFunc("/help/en/articles/404", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	c.Roots = []string{srv.URL + "/help/en/articles/404"}
	if _, _, err := c.Crawl(context.Background(), now.Add(time.Hour)); err == nil {
		t.Error("a missing page must fail the crawl")
	}
}
