// Package policy watches Taboola's advertiser policy pages in the Realize
// help center (an Intercom help center) and says what changed in them.
//
// A crawl starts at one or more collection pages, follows the collections and
// articles each one lists (never the links inside an article), and keeps each
// page's text as lines. Every answer is saved raw before it is read, so the
// reading can be re-run over any two saved crawls (Compare).
package policy

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Kind is what a help center page is.
type Kind string

const (
	Article    Kind = "articles"
	Collection Kind = "collections"
)

// Page is one help center page as read: its title, its text one block per
// line, and (for a collection) the pages it lists.
type Page struct {
	Key   string   `json:"key"` // "articles/3878202": the id, since the slug changes with the title
	Kind  Kind     `json:"kind"`
	URL   string   `json:"url"`
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	Links []string `json:"links,omitempty"` // absolute URLs of listed pages
}

var helpPath = regexp.MustCompile(`^/help/[a-zA-Z-]+/(articles|collections)/(\d+)`)

// KeyOf is the page key for a help center URL, or "" when u is not an
// article or collection page.
func KeyOf(u *url.URL) (string, Kind) {
	m := helpPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", ""
	}
	return m[1] + "/" + m[2], Kind(m[1])
}

// noise is text that changes without the policy changing: relative dates,
// author blocks, article counts, the feedback widget.
var noise = regexp.MustCompile(`(?i)^(updated\b|written by\b|by\s.{1,80}\bauthors?$|\d+ (articles?|authors?)$|did this answer your question\??$|table of contents$|[😞😐😃\s]+$)`)

// Parse reads one saved page. base is the URL it was fetched from, for
// resolving its links.
func Parse(base string, raw []byte) (Page, error) {
	bu, err := url.Parse(base)
	if err != nil {
		return Page{}, err
	}
	key, kind := KeyOf(bu)
	if key == "" {
		return Page{}, fmt.Errorf("%s is not a help center article or collection", base)
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return Page{}, err
	}
	p := Page{Key: key, Kind: kind, URL: canonical(bu)}
	root := first(doc, atom.Article)
	if root == nil {
		root = first(doc, atom.Main)
	}
	if root == nil {
		root = first(doc, atom.Body)
	}
	if root == nil {
		return Page{}, fmt.Errorf("%s: no body", base)
	}
	if h := first(doc, atom.H1); h != nil {
		p.Title = clean(text(h))
	}
	if p.Title == "" {
		if t := first(doc, atom.Title); t != nil {
			p.Title = strings.TrimSpace(strings.Split(clean(text(t)), " | ")[0])
		}
	}
	var w lineWriter
	w.walk(root)
	for _, l := range w.done() {
		if l == "Related Articles" || l == "## Related Articles" {
			break // the help center picks these itself; they are not policy
		}
		if !noise.MatchString(l) {
			p.Lines = append(p.Lines, l)
		}
	}
	if kind == Collection {
		p.Links = links(doc, root, bu)
	}
	if len(p.Lines) == 0 {
		return Page{}, fmt.Errorf("%s: no text (a bot check or a new page layout?)", base)
	}
	return p, nil
}

// links are the articles and collections a collection page lists, in page
// order, from its main part, or the whole page when that part has none.
func links(doc, root *html.Node, base *url.URL) []string {
	collect := func(n *html.Node) []string {
		var out []string
		seen := map[string]bool{}
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode && n.DataAtom == atom.A {
				if u := resolve(base, attr(n, "href")); u != nil {
					if k, _ := KeyOf(u); k != "" && !seen[k] {
						if self, _ := KeyOf(base); k != self {
							seen[k] = true
							out = append(out, canonical(u))
						}
					}
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(n)
		return out
	}
	if l := collect(root); len(l) > 0 {
		return l
	}
	return collect(doc)
}

func resolve(base *url.URL, href string) *url.URL {
	if href == "" {
		return nil
	}
	u, err := base.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	return u
}

// canonical drops the query and fragment (the help center adds tracking
// fragments to its own links).
func canonical(u *url.URL) string {
	c := *u
	c.RawQuery, c.Fragment, c.Scheme = "", "", "https"
	return c.String()
}

func first(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := first(c, a); f != nil {
			return f
		}
	}
	return nil
}

func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

var spaces = regexp.MustCompile(`[\s\x{00a0}\x{200b}\x{200c}\x{200d}\x{feff}]+`)

func clean(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// skip are elements whose text is never policy.
var skip = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
	atom.Svg: true, atom.Nav: true, atom.Header: true, atom.Footer: true,
	atom.Button: true, atom.Form: true, atom.Iframe: true, atom.Img: true,
}

// block are elements that start a new line.
var block = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Li: true, atom.Br: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Tr: true, atom.Td: true, atom.Th: true, atom.Table: true, atom.Ul: true, atom.Ol: true,
	atom.Blockquote: true, atom.Pre: true, atom.Hr: true, atom.Dt: true, atom.Dd: true,
	atom.Figcaption: true, atom.Article: true, atom.Main: true, atom.Aside: true,
}

type lineWriter struct {
	lines []string
	cur   strings.Builder
}

func (w *lineWriter) br() {
	if l := clean(w.cur.String()); l != "" {
		w.lines = append(w.lines, l)
	}
	w.cur.Reset()
}

func (w *lineWriter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.cur.WriteString(n.Data)
		return
	case html.ElementNode:
		if skip[n.DataAtom] || attr(n, "hidden") != "" || attr(n, "aria-hidden") == "true" {
			return
		}
	}
	isBlock := n.Type == html.ElementNode && block[n.DataAtom]
	if isBlock {
		w.br()
		switch n.DataAtom {
		case atom.Li:
			w.cur.WriteString("• ")
		case atom.H1, atom.H2, atom.H3, atom.H4:
			w.cur.WriteString("## ")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
	if isBlock {
		w.br()
	}
}

// done returns the lines, dropping marker-only lines ("•", "##") left by
// empty blocks.
func (w *lineWriter) done() []string {
	w.br()
	out := w.lines[:0]
	for _, l := range w.lines {
		if l != "•" && l != "##" {
			out = append(out, l)
		}
	}
	return out
}
