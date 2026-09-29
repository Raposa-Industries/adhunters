package policy

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// Change is one policy change between two snapshots.
type Change struct {
	What    string // "new", "removed" or "changed"
	Page    Page   // the new page; the old one when removed
	Added   []string
	Removed []string
}

// Compare says what changed from old to cur: articles and collections that
// appeared or went away, and articles whose text changed. A collection whose
// own text changed is not a change by itself: what it lists shows up as new
// or removed pages.
func Compare(old, cur *Snapshot) []Change {
	var out []Change
	for _, k := range keys(cur.Pages) {
		p := cur.Pages[k]
		o, ok := old.Pages[k]
		switch {
		case !ok:
			out = append(out, Change{What: "new", Page: p, Added: p.Lines})
		case p.Kind == Article:
			add, rem := diff(o.Lines, p.Lines)
			if len(add)+len(rem) > 0 {
				out = append(out, Change{What: "changed", Page: p, Added: add, Removed: rem})
			}
		}
	}
	for _, k := range keys(old.Pages) {
		if _, ok := cur.Pages[k]; !ok {
			out = append(out, Change{What: "removed", Page: old.Pages[k], Removed: old.Pages[k].Lines})
		}
	}
	return out
}

func keys(m map[string]Page) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diff returns the lines only in b (added) and only in a (removed), in
// order, by the longest common subsequence of lines.
func diff(a, b []string) (added, removed []string) {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			removed = append(removed, a[i])
			i++
		default:
			added = append(added, b[j])
			j++
		}
	}
	removed = append(removed, a[i:]...)
	added = append(added, b[j:]...)
	return added, removed
}

// maxMessage keeps a message under Telegram's 4096 characters.
const maxMessage = 3500

// maxLine shortens one long paragraph in a message.
const maxLine = 400

// Message is the Telegram message (HTML) for one change.
func Message(c Change) string {
	kind := "article"
	if c.Page.Kind == Collection {
		kind = "section"
	}
	head := map[string]string{
		"new":     "📜 <b>New Taboola policy " + kind + "</b>",
		"removed": "📜 <b>Taboola policy " + kind + " removed</b>",
		"changed": "📜 <b>Taboola policy changed</b>",
	}[c.What]
	var b strings.Builder
	fmt.Fprintf(&b, "%s: <a href=\"%s\">%s</a>\n", head, html.EscapeString(c.Page.URL), html.EscapeString(c.Page.Title))
	if c.What == "new" && c.Page.Kind == Collection {
		return b.String()
	}
	if c.What == "removed" {
		b.WriteString("It is no longer listed in the help center.")
		return b.String()
	}
	var lines []string
	for _, l := range c.Removed {
		if c.What == "changed" {
			lines = append(lines, "➖ "+short(l))
		}
	}
	for _, l := range c.Added {
		if c.What == "changed" {
			lines = append(lines, "➕ "+short(l))
		} else {
			lines = append(lines, short(l))
		}
	}
	for i, l := range lines {
		e := html.EscapeString(l) + "\n"
		if b.Len()+len(e) > maxMessage {
			fmt.Fprintf(&b, "… and %d more lines: see the page.", len(lines)-i)
			break
		}
		b.WriteString(e)
	}
	return strings.TrimRight(b.String(), "\n")
}

func short(s string) string {
	r := []rune(s)
	if len(r) <= maxLine {
		return s
	}
	return string(r[:maxLine]) + "…"
}
