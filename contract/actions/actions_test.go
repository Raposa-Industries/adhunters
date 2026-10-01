package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoad(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Latest()) == 0 {
		t.Fatal("no actions")
	}
}

// Every action names a view or function some app publishes in contract/sql,
// with arguments and columns that exist there. An action cannot name
// something the app does not publish.
func TestEveryActionIsPublished(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range c.All() {
		t.Run(fmt.Sprintf("%s_v%d", a.Name, a.Version), func(t *testing.T) {
			sql := published(t, a.Source())
			switch {
			case a.View != "":
				has := viewColumns(t, sql, a.View)
				for _, col := range a.Columns {
					if !slices.Contains(has, col) {
						t.Errorf("%s has no column %s (it has %v)", a.View, col, has)
					}
				}
			default:
				params := functionParams(t, sql, a.Call)
				checkArgs(t, a, params)
				if a.Kind == Read {
					has := tableColumns(t, sql, a.Call)
					for _, col := range a.Columns {
						if !slices.Contains(has, col) {
							t.Errorf("%s returns no column %s (it returns %v)", a.Call, col, has)
						}
					}
				}
			}
			if f := a.Follow; f != nil {
				has := viewColumns(t, published(t, f.View), f.View)
				for _, col := range []string{f.ID, f.State} {
					if !slices.Contains(has, col) {
						t.Errorf("follow view %s has no column %s", f.View, col)
					}
				}
			}
		})
	}
}

func checkArgs(t *testing.T, a Action, params []param) {
	t.Helper()
	byName := map[string]param{}
	for _, p := range params {
		byName[p.name] = p
	}
	given := map[string]bool{}
	for _, g := range a.Args {
		given[g.name()] = true
		p, ok := byName[g.name()]
		if !ok {
			t.Errorf("%s has no parameter %s", a.Call, g.name())
			continue
		}
		typ := g.Type
		switch {
		case g.From != "":
			typ = "string"
		case len(g.Const) > 0:
			continue
		}
		if !slices.Contains(Types[typ], p.typ) {
			t.Errorf("%s: %s is %s in SQL, %s in the list", a.Call, g.name(), p.typ, typ)
		}
	}
	for _, p := range params {
		if !p.optional && !given[p.name] {
			t.Errorf("%s needs %s, which no arg gives", a.Call, p.name)
		}
	}
}

func (g Arg) name() string { return "p_" + g.Name }

// published returns the contract file that creates name (schema.object).
func published(t *testing.T, name string) string {
	t.Helper()
	schema, object, _ := strings.Cut(name, ".")
	app := strings.TrimSuffix(schema, "_api")
	b, err := os.ReadFile(filepath.Join("..", "sql", app, object+".sql"))
	if err != nil {
		t.Fatalf("%s is not published in contract/sql/%s: %v", name, app, err)
	}
	return string(b)
}

type param struct {
	name, typ string
	optional  bool
}

var defaultRe = regexp.MustCompile(`(?i)\s+(DEFAULT\b|=).*$`)

func functionParams(t *testing.T, sql, name string) []param {
	t.Helper()
	i := strings.Index(sql, "CREATE FUNCTION "+name+"(")
	if i < 0 {
		t.Fatalf("no CREATE FUNCTION %s( in its file", name)
	}
	inner := balanced(sql[i+len("CREATE FUNCTION "+name):])
	var out []param
	for _, item := range splitTop(inner) {
		item = strings.Join(strings.Fields(item), " ")
		if item == "" {
			continue
		}
		p := param{optional: defaultRe.MatchString(item)}
		item = defaultRe.ReplaceAllString(item, "")
		p.name, p.typ, _ = strings.Cut(item, " ")
		p.typ = strings.ToLower(p.typ)
		out = append(out, p)
	}
	return out
}

func tableColumns(t *testing.T, sql, name string) []string {
	t.Helper()
	i := strings.Index(sql, "CREATE FUNCTION "+name+"(")
	j := strings.Index(sql[max(i, 0):], "RETURNS TABLE")
	if i < 0 || j < 0 {
		t.Fatalf("%s does not return a table", name)
	}
	var out []string
	for _, item := range splitTop(balanced(sql[i+j+len("RETURNS TABLE"):])) {
		if f := strings.Fields(item); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

var (
	aliasRe = regexp.MustCompile(`(?is)\bAS\s+([a-z_][a-z0-9_]*)\s*$`)
	lastRe  = regexp.MustCompile(`(?i)([a-z_][a-z0-9_]*)\s*$`)
)

func viewColumns(t *testing.T, sql, name string) []string {
	t.Helper()
	i := strings.Index(sql, "CREATE VIEW "+name+" AS")
	if i < 0 {
		t.Fatalf("no CREATE VIEW %s AS in its file", name)
	}
	rest := sql[i:]
	sel := strings.Index(strings.ToUpper(rest), "SELECT")
	if sel < 0 {
		t.Fatalf("%s has no SELECT", name)
	}
	rest = rest[sel+len("SELECT"):]
	// The select list ends at the first FROM outside parentheses.
	depth, end := 0, len(rest)
	up := strings.ToUpper(rest)
	for k := 0; k < len(rest); k++ {
		switch rest[k] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(up[k:], "FROM") && (k == 0 || !isWord(rest[k-1])) && (k+4 >= len(rest) || !isWord(rest[k+4])) {
			end = k
			break
		}
	}
	var out []string
	for _, item := range splitTop(rest[:end]) {
		item = strings.TrimSpace(item)
		if m := aliasRe.FindStringSubmatch(item); m != nil {
			out = append(out, m[1])
		} else if m := lastRe.FindStringSubmatch(item); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

func isWord(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// balanced returns what is inside the first parenthesis of s and its match.
func balanced(s string) string {
	start := strings.IndexByte(s, '(')
	if start < 0 {
		return ""
	}
	depth := 0
	for k := start; k < len(s); k++ {
		switch s[k] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[start+1 : k]
			}
		case '\'':
			// Skip a quoted default such as ''.
			if e := strings.IndexByte(s[k+1:], '\''); e >= 0 {
				k += e + 1
			}
		}
	}
	return s[start+1:]
}

// splitTop splits on commas outside parentheses.
func splitTop(s string) []string {
	var out []string
	depth, from := 0, 0
	for k := 0; k < len(s); k++ {
		switch s[k] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[from:k])
				from = k + 1
			}
		}
	}
	return append(out, s[from:])
}

// The list refuses what would let Desk do more than a teammate, or call
// something no one can follow.
func TestRefusals(t *testing.T) {
	good := `{"name": "demo.ask_thing", "version": 1, "kind": "ask", "says": "x", "call": "demo_api.ask_thing_v1",
		"args": [{"name": "who", "from": "person"}], "returns": "id", "per_day": 5, "link": "/demo/r/{returned}",
		"follow": {"view": "demo_api.request_v1", "id": "id", "state": "state", "done": ["done"], "failed": ["refused"]}}`
	cases := map[string]string{
		"a person's confirm":     strings.Replace(good, `"kind": "ask"`, `"kind": "person"`, 1),
		"another app's view":     strings.Replace(good, `demo_api.ask_thing_v1`, `launch_api.ask_thing_v1`, 1),
		"no version on the view": strings.Replace(good, `demo_api.ask_thing_v1`, `demo_api.ask_thing`, 1),
		"an ask nobody follows":  strings.Replace(good, `"follow": {"view": "demo_api.request_v1", "id": "id", "state": "state", "done": ["done"], "failed": ["refused"]}`, `"per_day": 5`, 1),
		"an ask for nobody":      strings.Replace(good, `{"name": "who", "from": "person"}`, `{"name": "who", "type": "string", "says": "w"}`, 1),
		"no daily cap":           strings.Replace(good, `"per_day": 5`, `"per_day": 0`, 1),
		"a link elsewhere":       strings.Replace(good, `/demo/r/{returned}`, `/launch/r/{returned}`, 1),
		"a hole with no value":   strings.Replace(good, `/demo/r/{returned}`, `/demo/r/{nope}`, 1),
		"an unknown field":       strings.Replace(good, `"version": 1`, `"version": 1, "approve": true`, 1),
		"a loose schema": strings.Replace(good, `{"name": "who", "from": "person"}`,
			`{"name": "who", "from": "person"}, {"name": "input", "type": "object", "says": "i", "schema": {"type": "object", "properties": {}}}`, 1),
		"a length limit in the schema": strings.Replace(good, `{"name": "who", "from": "person"}`,
			`{"name": "who", "from": "person"}, {"name": "input", "type": "object", "says": "i", "schema": {"type": "object", "additionalProperties": false, "properties": {"t": {"type": "string", "maxLength": 3}}}}`, 1),
	}
	load := func(body string) error {
		_, err := LoadFS(fstest.MapFS{"demo.json": {Data: []byte(`{"app": "demo", "actions": [` + body + `]}`)}})
		return err
	}
	if err := load(good); err != nil {
		t.Fatalf("the good one: %v", err)
	}
	for name, body := range cases {
		if load(body) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	read := `{"name": "demo.things", "version": 1, "kind": "read", "says": "x", "view": "demo_api.thing_v1",
		"columns": ["id", "title"], "limit": 10, "order": "id DESC", "filters": [{"column": "title", "op": "has", "type": "string"}]}`
	if err := load(read); err != nil {
		t.Fatalf("the good read: %v", err)
	}
	for name, body := range map[string]string{
		"a read that follows":         strings.Replace(read, `"limit": 10`, `"limit": 10, "follow": {"view": "demo_api.thing_v1", "id": "id", "state": "s", "done": ["x"]}`, 1),
		"an order on a hidden column": strings.Replace(read, `id DESC`, `secret DESC`, 1),
		"an order with SQL in it":     strings.Replace(read, `id DESC`, `id; DROP TABLE x`, 1),
		"a filter on a hidden column": strings.Replace(read, `"column": "title"`, `"column": "secret"`, 1),
		"no row limit":                strings.Replace(read, `"limit": 10`, `"limit": 0`, 1),
	} {
		if load(body) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Nothing in the list turns a campaign or ad on: only a person does, in
// Taboola (decided 1 Oct 2026: "never run a campaign without me giving the
// go"). A change or ask that speaks of turning on, in its name, function,
// args, fixed values, choices or input fields, is refused. A read may still
// show what runs.
func TestNothingTurnsOn(t *testing.T) {
	good := `{"name": "demo.ask_thing", "version": 1, "kind": "ask", "says": "x", "call": "demo_api.ask_thing_v1",
		"args": [{"name": "kind", "const": "pause"}, {"name": "who", "from": "person"},
			{"name": "input", "type": "object", "says": "i", "schema": {"type": "object", "additionalProperties": false,
				"properties": {"originals": {"type": "string", "enum": ["when_started", "now"]}}}}],
		"returns": "id", "per_day": 5, "link": "/demo/r/{returned}",
		"follow": {"view": "demo_api.request_v1", "id": "id", "state": "state", "done": ["done"], "failed": ["refused"]}}`
	load := func(body string) error {
		_, err := LoadFS(fstest.MapFS{"demo.json": {Data: []byte(`{"app": "demo", "actions": [` + body + `]}`)}})
		return err
	}
	if err := load(good); err != nil {
		t.Fatalf("the good one (a person starting the copies is not turning on): %v", err)
	}
	for name, body := range map[string]string{
		"resume in the name":      strings.Replace(good, `"demo.ask_thing"`, `"demo.resume_campaigns"`, 1),
		"turn on in the name":     strings.Replace(good, `"demo.ask_thing"`, `"demo.turn_on_ads"`, 1),
		"an unpause function":     strings.Replace(good, `demo_api.ask_thing_v1`, `demo_api.unpause_v1`, 1),
		"start as the fixed kind": strings.Replace(good, `"const": "pause"`, `"const": "start"`, 1),
		"active in a fixed value": strings.Replace(good, `"const": "pause"`, `"const": {"is_active": true}`, 1),
		"an is_active field":      strings.Replace(good, `"originals": {`, `"is_active": {"type": "boolean"}, "originals": {`, 1),
		"running as a choice":     strings.Replace(good, `"now"]`, `"now", "running"]`, 1),
		"an arg to enable": strings.Replace(good, `{"name": "who", "from": "person"}`,
			`{"name": "who", "from": "person"}, {"name": "enable", "type": "boolean", "says": "e"}`, 1),
	} {
		if err := load(body); err == nil || !strings.Contains(err.Error(), "only a person turns") {
			t.Errorf("%s: %v", name, err)
		}
	}
	read := `{"name": "demo.things", "version": 1, "kind": "read", "says": "x", "view": "demo_api.thing_v1",
		"columns": ["id", "is_active"], "limit": 10, "filters": [{"column": "is_active", "op": "=", "type": "boolean"}]}`
	if err := load(read); err != nil {
		t.Errorf("a read of what runs: %v", err)
	}
}
