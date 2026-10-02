// Package tests checks that what the alerts, the dashboards, observe-bot and
// daily-check.sh read reaches Grafana Cloud. Everything Alloy reads goes
// through a keep-list in ../alloy, so a metric no keep-list names never
// leaves its box: a panel reading it stays empty and an alert on it never
// fires.
package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const observe = ".."

// notScraped are series Grafana makes itself, from the alert rules.
var notScraped = map[string]bool{"ALERTS": true, "ALERTS_FOR_STATE": true}

func TestEveryMetricReadIsKept(t *testing.T) {
	keep := keepLists(t)
	if len(keep) < 6 {
		t.Fatalf("found %d keep-lists in alloy/*.alloy, want one per exporter (6)", len(keep))
	}
	read := metricsRead(t)
	if len(read) < 100 {
		t.Fatalf("found only %d metrics read; is the parsing broken?", len(read))
	}
	names := make([]string, 0, len(read))
	for n := range read {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if notScraped[n] {
			continue
		}
		kept := false
		for _, re := range keep {
			if re.MatchString(n) {
				kept = true
				break
			}
		}
		if !kept {
			t.Errorf("%s is read by %s but no keep-list in alloy/*.alloy lets it through", n, strings.Join(read[n], ", "))
		}
	}
}

// keepLists returns, for each prometheus.relabel rule that keeps by
// __name__, its regex anchored as Alloy anchors it.
func keepLists(t *testing.T) []*regexp.Regexp {
	files, err := filepath.Glob(filepath.Join(observe, "alloy", "*.alloy"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no alloy files: %v", err)
	}
	rule := regexp.MustCompile(`(?s)rule \{(.*?)\n  \}`)
	join := regexp.MustCompile(`(?s)regex\s*=\s*string\.join\(\[(.*?)\]\s*,\s*"\|"\)`)
	single := regexp.MustCompile(`regex\s*=\s*"((?:[^"\\]|\\.)*)"`)
	quoted := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	comment := regexp.MustCompile(`//[^\n]*`)
	var out []*regexp.Regexp
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range rule.FindAllStringSubmatch(string(b), -1) {
			body := comment.ReplaceAllString(m[1], "")
			if !strings.Contains(body, `source_labels = ["__name__"]`) || !regexp.MustCompile(`action\s*=\s*"keep"`).MatchString(body) {
				continue
			}
			var alts []string
			if j := join.FindStringSubmatch(body); j != nil {
				for _, q := range quoted.FindAllStringSubmatch(j[1], -1) {
					alts = append(alts, unescape(q[1]))
				}
			} else if s := single.FindStringSubmatch(body); s != nil {
				alts = append(alts, unescape(s[1]))
			} else {
				t.Fatalf("%s: a keep rule without a regex this test reads", f)
			}
			out = append(out, regexp.MustCompile(`^(?:`+strings.Join(alts, "|")+`)$`))
		}
	}
	return out
}

func unescape(s string) string { return strings.ReplaceAll(s, `\\`, `\`) }

// metricsRead returns each metric name the rules, the dashboards,
// daily-check.sh and observe-bot read, with where they read it.
func metricsRead(t *testing.T) map[string][]string {
	read := map[string][]string{}
	add := func(expr, where string) {
		for _, n := range metricNames(expr) {
			if l := read[n]; len(l) == 0 || l[len(l)-1] != where {
				read[n] = append(l, where)
			}
		}
	}

	// Rules: each expr, on one line or as a block.
	files, _ := filepath.Glob(filepath.Join(observe, "rules", "*.yaml"))
	exprLine := regexp.MustCompile(`(?m)^(\s*)expr:\s*(.*)$`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			m := exprLine.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			expr := m[2]
			if strings.HasPrefix(expr, "|") || strings.HasPrefix(expr, ">") {
				expr = ""
				for _, next := range lines[i+1:] {
					if strings.TrimSpace(next) != "" && len(next)-len(strings.TrimLeft(next, " ")) <= len(m[1]) {
						break
					}
					expr += next + "\n"
				}
			}
			add(expr, "rules/"+filepath.Base(f))
		}
	}

	// Dashboards: every Prometheus query, and the variables' queries.
	files, _ = filepath.Glob(filepath.Join(observe, "dashboards", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Panels     []panel `json:"panels"`
			Templating struct {
				List []struct {
					Type  string `json:"type"`
					Query any    `json:"query"`
				} `json:"list"`
			} `json:"templating"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		where := "dashboards/" + filepath.Base(f)
		var walk func([]panel)
		walk = func(ps []panel) {
			for _, p := range ps {
				for _, q := range p.Targets {
					if q.Datasource.Type != "loki" {
						add(q.Expr, where)
					}
				}
				walk(p.Panels)
			}
		}
		walk(d.Panels)
		for _, v := range d.Templating.List {
			if s, ok := v.Query.(string); ok && v.Type == "query" {
				add(s, where)
			}
			if m, ok := v.Query.(map[string]any); ok && v.Type == "query" {
				if s, ok := m["query"].(string); ok {
					add(s, where)
				}
			}
		}
	}

	// daily-check.sh: each q TITLE QUERY line.
	b, err := os.ReadFile(filepath.Join(observe, "daily-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^q "[^"]*" (?:'([^']*)'|"((?:[^"\\]|\\.)*)")\s*$`).FindAllStringSubmatch(string(b), -1) {
		add(m[1]+m[2], "daily-check.sh")
	}

	// observe-bot's queries: the Go files that ask Prometheus.
	for _, f := range []string{"internal/digest/digest.go", "cmd/observe-bot/credits.go"} {
		b, err := os.ReadFile(filepath.Join(observe, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile("`([^`]*)`").FindAllStringSubmatch(string(b), -1) {
			if strings.ContainsAny(m[1], "({") {
				add(regexp.MustCompile(`%[a-z]`).ReplaceAllString(m[1], ""), f)
			}
		}
	}
	return read
}

type panel struct {
	Targets []struct {
		Expr       string `json:"expr"`
		Datasource struct {
			Type string `json:"type"`
		} `json:"datasource"`
	} `json:"targets"`
	Panels []panel `json:"panels"`
}

var (
	strLit   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)
	matchers = regexp.MustCompile(`\{[^}]*\}`)
	grouping = regexp.MustCompile(`\b(?:by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)`)
	ranges   = regexp.MustCompile(`\[[^\]]*\]`)
	numbers  = regexp.MustCompile(`\b\d[\w.]*`)
	ident    = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)
	keywords = map[string]bool{"bool": true, "and": true, "or": true, "unless": true, "offset": true, "inf": true, "nan": true}
)

// metricNames returns the metric names in a PromQL expression: the
// identifiers left once strings, label matchers, groupings, ranges, numbers,
// functions and keywords are taken out. label_values(metric, label) names
// only its first argument.
func metricNames(expr string) []string {
	e := strings.TrimSpace(expr)
	if rest, ok := strings.CutPrefix(e, "label_values("); ok {
		if i := strings.LastIndex(rest, ","); i >= 0 {
			e = rest[:i]
		} else {
			return nil
		}
	}
	e = strLit.ReplaceAllString(e, `""`)
	e = matchers.ReplaceAllString(e, " ")
	e = grouping.ReplaceAllString(e, " ")
	e = ranges.ReplaceAllString(e, " ")
	e = numbers.ReplaceAllString(e, " ")
	var out []string
	for _, loc := range ident.FindAllStringIndex(e, -1) {
		w := e[loc[0]:loc[1]]
		if keywords[w] || strings.HasPrefix(strings.TrimLeft(e[loc[1]:], " \t\n"), "(") {
			continue
		}
		out = append(out, w)
	}
	return out
}
