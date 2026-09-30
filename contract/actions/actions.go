// Package actions is the action catalog: what each app lets a teammate do,
// written down so Desk can do the same with the same rights (decided
// 29 Sep 2026: "the agent can use the other apps to accomplish anything a
// team member could").
//
// Each app lists its actions in <app>.json beside this file. An action is
// one published view or function of the app's <app>_api schema, the same one
// the app's own button uses, so Desk and a person go through the same door.
// Its kind says what it may do:
//
//   - read: looks and changes nothing. Desk reads freely.
//   - change: changes the app's own data or starts its work (a brief, an
//     investigation). Desk runs it only as a step of a plan a person OK'd.
//   - ask: asks the app for something a person then confirms on the app's
//     own screen (Launch's Send paused). Desk runs it only as a step of an
//     OK'd plan, and the app does nothing until someone confirms there.
//
// What only a person may do (a confirm, turning spending on) is never an
// action: the list has no kind for it, so Desk cannot be given it by
// mistake. A view or function changes shape only as a new version, so an
// action names the exact version it calls.
package actions

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed *.json
var files embed.FS

// Kind is what an action may do.
type Kind string

// The kinds. A person's confirm is deliberately not one.
const (
	Read   Kind = "read"
	Change Kind = "change"
	Ask    Kind = "ask"
)

// Types an argument, filter or input field can have, and the Postgres types
// each one may be passed to.
var Types = map[string][]string{
	"string":    {"text", "character varying", "varchar", "uuid"},
	"integer":   {"integer", "bigint", "smallint", "int", "int4", "int8", "int2"},
	"number":    {"numeric", "real", "double precision", "float8"},
	"boolean":   {"boolean", "bool"},
	"time":      {"timestamptz", "timestamp with time zone"},
	"date":      {"date"},
	"object":    {"jsonb", "json"},
	"string[]":  {"text[]"},
	"integer[]": {"integer[]", "bigint[]", "int[]"},
}

// Where an argument's value comes from when the model does not give it.
const (
	FromPerson = "person" // the person the work is for (their sign-in email)
	FromOrigin = "origin" // who is asking, as the app records it: "desk:step:<id>"
)

// Arg is one argument of a change or ask: a parameter of the function
// (named without its p_ prefix), given by the model, fixed, or filled by
// Desk.
type Arg struct {
	Name     string          `json:"name"`
	Type     string          `json:"type,omitempty"`
	Says     string          `json:"says,omitempty"`
	Enum     []string        `json:"enum,omitempty"`
	Const    json.RawMessage `json:"const,omitempty"`
	From     string          `json:"from,omitempty"`
	Optional bool            `json:"optional,omitempty"`
	// Schema is the JSON Schema of an object argument's value.
	Schema json.RawMessage `json:"schema,omitempty"`
	// Max is the most characters of a string, or items of a list.
	Max int `json:"max,omitempty"`
}

// Filter is one condition a read may be asked with.
type Filter struct {
	Column string   `json:"column"`
	Op     string   `json:"op"`
	Type   string   `json:"type"`
	Enum   []string `json:"enum,omitempty"`
	Says   string   `json:"says,omitempty"`
}

// Filter operators. "has" is a case-insensitive "contains" on text.
var Ops = map[string]bool{"=": true, "in": true, ">=": true, "<=": true, "has": true}

// Property is the name a filter goes by in a read's input: the column for
// = and in, column_from for >=, column_to for <=, column_has for has.
func (f Filter) Property() string {
	switch f.Op {
	case ">=":
		return f.Column + "_from"
	case "<=":
		return f.Column + "_to"
	case "has":
		return f.Column + "_has"
	}
	return f.Column
}

// LimitProperty is the input a read's caller may lower its row limit with.
const LimitProperty = "limit"

// Follow says where the thing a change or ask started can be followed, so
// Desk knows when it ended and how.
type Follow struct {
	View   string   `json:"view"`
	ID     string   `json:"id"`
	State  string   `json:"state"`
	Done   []string `json:"done"`
	Failed []string `json:"failed"`
}

// Show says which columns of a read show one row to a person when Desk asks
// them to choose among rows (an image, a line of text).
type Show struct {
	ID    string `json:"id"`
	Image string `json:"image,omitempty"`
	Text  string `json:"text,omitempty"`
}

// Action is one thing an app lets a teammate do.
type Action struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Kind    Kind   `json:"kind"`
	Says    string `json:"says"`

	// Call is the <app>_api function (a change or ask, or a read of a
	// function that returns a table); View is the <app>_api view a read
	// reads. One of the two.
	Call string `json:"call,omitempty"`
	View string `json:"view,omitempty"`
	Args []Arg  `json:"args,omitempty"`

	// Reads: the columns returned, the ones holding text someone outside
	// the team wrote (competitors' headlines, landing pages), the filters
	// it can be asked with, a fixed order and the most rows.
	Columns []string `json:"columns,omitempty"`
	Outside []string `json:"outside,omitempty"`
	Filters []Filter `json:"filters,omitempty"`
	Order   string   `json:"order,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Show    *Show    `json:"show,omitempty"`

	// Changes and asks: what the call returns, where it can be followed,
	// the app's page for it ({returned} is what the call returned, {name}
	// the input of that name), the action that undoes it, the most calls a
	// day from Desk, and its cost in words.
	Returns string  `json:"returns,omitempty"`
	Follow  *Follow `json:"follow,omitempty"`
	Link    string  `json:"link,omitempty"`
	Undo    string  `json:"undo,omitempty"`
	PerDay  int     `json:"per_day,omitempty"`
	Costs   string  `json:"costs,omitempty"`
}

// App is the name before the dot: "raposa" for "raposa.request_investigation".
func (a Action) App() string { return strings.SplitN(a.Name, ".", 2)[0] }

// Source is the view or function the action reads or calls.
func (a Action) Source() string {
	if a.View != "" {
		return a.View
	}
	return a.Call
}

// Catalog is every app's actions, the newest version of each by name.
type Catalog struct {
	all    []Action
	byName map[string]Action
}

// Load reads and checks every app's file.
func Load() (*Catalog, error) { return LoadFS(files) }

// LoadFS reads and checks the <app>.json files at the root of fsys.
func LoadFS(fsys fs.FS) (*Catalog, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	c := &Catalog{byName: map[string]Action{}}
	seen := map[string]bool{}
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		var f struct {
			App     string   `json:"app"`
			Actions []Action `json:"actions"`
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return nil, fmt.Errorf("actions: %s: %w", n, err)
		}
		if f.App+".json" != n {
			return nil, fmt.Errorf("actions: %s says app %q; the file is named after its app", n, f.App)
		}
		for _, a := range f.Actions {
			if a.App() != f.App {
				return nil, fmt.Errorf("actions: %s: %s is not one of %s's actions", n, a.Name, f.App)
			}
			key := fmt.Sprintf("%s v%d", a.Name, a.Version)
			if seen[key] {
				return nil, fmt.Errorf("actions: %s is listed twice", key)
			}
			seen[key] = true
			if err := a.check(); err != nil {
				return nil, fmt.Errorf("actions: %s: %s: %w", n, key, err)
			}
			c.all = append(c.all, a)
			if cur, ok := c.byName[a.Name]; !ok || a.Version > cur.Version {
				c.byName[a.Name] = a
			}
		}
	}
	for _, a := range c.all {
		if a.Undo != "" {
			if _, ok := c.byName[a.Undo]; !ok {
				return nil, fmt.Errorf("actions: %s is undone by %s, which is not listed", a.Name, a.Undo)
			}
		}
	}
	return c, nil
}

// All returns every listed version, by name then version.
func (c *Catalog) All() []Action {
	out := append([]Action(nil), c.all...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// Latest returns the newest version of every action, by name.
func (c *Catalog) Latest() []Action {
	out := make([]Action, 0, len(c.byName))
	for _, a := range c.byName {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns the newest version of an action.
func (c *Catalog) Get(name string) (Action, bool) {
	a, ok := c.byName[name]
	return a, ok
}

// Version returns one exact version of an action.
func (c *Catalog) Version(name string, v int) (Action, bool) {
	for _, a := range c.all {
		if a.Name == name && a.Version == v {
			return a, true
		}
	}
	return Action{}, false
}

var (
	nameRe   = regexp.MustCompile(`^[a-z]+\.[a-z][a-z0-9_]*$`)
	sourceRe = regexp.MustCompile(`^([a-z]+)_api\.[a-z][a-z0-9_]*_v[0-9]+$`)
	identRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	orderRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*( (ASC|DESC))?( NULLS (FIRST|LAST))?$`)
	holeRe   = regexp.MustCompile(`\{([a-z_]+)\}`)
)

func (a Action) check() error {
	if !nameRe.MatchString(a.Name) {
		return fmt.Errorf("name must look like app.what")
	}
	if a.Version < 1 {
		return fmt.Errorf("version starts at 1")
	}
	if strings.TrimSpace(a.Says) == "" {
		return fmt.Errorf("says what it does in one line")
	}
	if (a.Call == "") == (a.View == "") {
		return fmt.Errorf("names one call or one view")
	}
	m := sourceRe.FindStringSubmatch(a.Source())
	if m == nil || m[1] != a.App() {
		return fmt.Errorf("%s is not a versioned view or function of %s_api", a.Source(), a.App())
	}
	switch a.Kind {
	case Read:
		return a.checkRead()
	case Change, Ask:
		return a.checkCall()
	case "":
		return fmt.Errorf("kind is read, change or ask")
	default:
		return fmt.Errorf("kind %q: only read, change and ask exist; what only a person may do is never listed", a.Kind)
	}
}

func (a Action) checkRead() error {
	if len(a.Columns) == 0 {
		return fmt.Errorf("a read lists its columns")
	}
	if a.Limit < 1 || a.Limit > 500 {
		return fmt.Errorf("a read returns 1 to 500 rows at most")
	}
	if a.Follow != nil || a.Undo != "" || a.Link != "" || a.PerDay != 0 || a.Returns != "" {
		return fmt.Errorf("a read has no follow, undo, link, per_day or returns")
	}
	cols := map[string]bool{}
	for _, c := range a.Columns {
		if !identRe.MatchString(c) || cols[c] {
			return fmt.Errorf("column %q is not a plain name, or is listed twice", c)
		}
		cols[c] = true
	}
	for _, c := range a.Outside {
		if !cols[c] {
			return fmt.Errorf("outside column %q is not one of the columns", c)
		}
	}
	props := map[string]bool{LimitProperty: true}
	for _, g := range a.Args {
		props[g.Name] = true
	}
	for _, f := range a.Filters {
		if !cols[f.Column] {
			return fmt.Errorf("filter on %q, which is not one of the columns", f.Column)
		}
		if props[f.Property()] {
			return fmt.Errorf("two inputs would be called %s", f.Property())
		}
		props[f.Property()] = true
		if !Ops[f.Op] {
			return fmt.Errorf("filter op %q; use =, in, >=, <= or has", f.Op)
		}
		if _, ok := Types[f.Type]; !ok || strings.HasSuffix(f.Type, "[]") || f.Type == "object" {
			return fmt.Errorf("filter on %s has type %q", f.Column, f.Type)
		}
		if f.Op == "has" && f.Type != "string" {
			return fmt.Errorf("has works on text only (%s)", f.Column)
		}
		if len(f.Enum) > 0 && f.Type != "string" {
			return fmt.Errorf("enum works on text only (%s)", f.Column)
		}
	}
	if a.Order != "" {
		for _, part := range strings.Split(a.Order, ",") {
			part = strings.TrimSpace(part)
			if !orderRe.MatchString(part) || !cols[strings.Fields(part)[0]] {
				return fmt.Errorf("order %q names a column that is not returned", part)
			}
		}
	}
	if a.Show != nil {
		for _, c := range []string{a.Show.ID, a.Show.Image, a.Show.Text} {
			if c != "" && !cols[c] {
				return fmt.Errorf("show names %q, which is not one of the columns", c)
			}
		}
		if a.Show.ID == "" || (a.Show.Image == "" && a.Show.Text == "") {
			return fmt.Errorf("show names the id and an image or a text column")
		}
	}
	if a.View != "" && len(a.Args) > 0 {
		return fmt.Errorf("a view takes no args")
	}
	return a.checkArgs()
}

func (a Action) checkCall() error {
	if a.Call == "" {
		return fmt.Errorf("a %s calls a function", a.Kind)
	}
	if len(a.Columns) > 0 || len(a.Filters) > 0 || a.Order != "" || a.Limit != 0 || a.Show != nil || len(a.Outside) > 0 {
		return fmt.Errorf("columns, filters, order, limit, show and outside are for reads")
	}
	if strings.TrimSpace(a.Returns) == "" {
		return fmt.Errorf("says what the call returns")
	}
	if a.PerDay < 1 {
		return fmt.Errorf("per_day caps how often Desk may call it")
	}
	if a.Kind == Ask && (a.Follow == nil || a.Link == "") {
		return fmt.Errorf("an ask has a follow view and a link to where a person confirms")
	}
	if a.Follow != nil {
		f := a.Follow
		m := sourceRe.FindStringSubmatch(f.View)
		if m == nil || m[1] != a.App() || !identRe.MatchString(f.ID) || !identRe.MatchString(f.State) || len(f.Done) == 0 {
			return fmt.Errorf("follow names a %s_api view, its id and state columns, and the done states", a.App())
		}
	}
	if a.Link != "" && !strings.HasPrefix(a.Link, "/"+a.App()+"/") {
		return fmt.Errorf("link %q is not one of %s's pages", a.Link, a.App())
	}
	for _, m := range holeRe.FindAllStringSubmatch(a.Link, -1) {
		if m[1] == "returned" {
			continue
		}
		found := false
		for _, g := range a.Args {
			found = found || g.Name == m[1]
		}
		if !found {
			return fmt.Errorf("link fills {%s}, which is neither {returned} nor an arg", m[1])
		}
	}
	if a.Undo != "" && strings.SplitN(a.Undo, ".", 2)[0] != a.App() {
		return fmt.Errorf("undo %s is another app's action", a.Undo)
	}
	return a.checkArgs()
}

func (a Action) checkArgs() error {
	seen := map[string]bool{}
	person := false
	for _, g := range a.Args {
		if !identRe.MatchString(g.Name) || seen[g.Name] {
			return fmt.Errorf("arg %q is not a plain name, or is listed twice", g.Name)
		}
		seen[g.Name] = true
		set := 0
		if g.Type != "" {
			set++
		}
		if len(g.Const) > 0 {
			set++
		}
		if g.From != "" {
			set++
		}
		if set != 1 {
			return fmt.Errorf("arg %s has exactly one of type, const or from", g.Name)
		}
		switch {
		case g.From != "":
			if g.From != FromPerson && g.From != FromOrigin {
				return fmt.Errorf("arg %s: from is person or origin", g.Name)
			}
			person = person || g.From == FromPerson
		case len(g.Const) > 0:
			if !json.Valid(g.Const) {
				return fmt.Errorf("arg %s: const is not JSON", g.Name)
			}
		default:
			if _, ok := Types[g.Type]; !ok {
				return fmt.Errorf("arg %s has type %q", g.Name, g.Type)
			}
			if strings.TrimSpace(g.Says) == "" {
				return fmt.Errorf("arg %s says what it is", g.Name)
			}
			if len(g.Enum) > 0 && g.Type != "string" {
				return fmt.Errorf("arg %s: enum works on text only", g.Name)
			}
			if (g.Type == "object") != (len(g.Schema) > 0) {
				return fmt.Errorf("arg %s: an object, and only an object, has a schema", g.Name)
			}
			if len(g.Schema) > 0 {
				var s map[string]any
				if err := json.Unmarshal(g.Schema, &s); err != nil {
					return fmt.Errorf("arg %s: schema: %w", g.Name, err)
				}
				if err := CheckSchema(s); err != nil {
					return fmt.Errorf("arg %s: schema: %w", g.Name, err)
				}
			}
		}
	}
	if a.Kind == Ask && !person {
		return fmt.Errorf("an ask records who it is for, to show on the confirm screen: one arg is from person")
	}
	return nil
}

// CheckSchema refuses what strict tool schemas do not take: every object
// closes its properties (additionalProperties false), and no numeric or
// length limits (Desk checks those itself, from max).
func CheckSchema(s map[string]any) error {
	for _, k := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength", "maxLength", "pattern", "minItems", "maxItems", "uniqueItems"} {
		if _, ok := s[k]; ok {
			return fmt.Errorf("%s is not allowed in a strict schema", k)
		}
	}
	t, _ := s["type"].(string)
	switch t {
	case "object":
		if ap, ok := s["additionalProperties"].(bool); !ok || ap {
			return fmt.Errorf("an object sets additionalProperties to false")
		}
		props, _ := s["properties"].(map[string]any)
		for name, p := range props {
			ps, ok := p.(map[string]any)
			if !ok {
				return fmt.Errorf("property %s is not a schema", name)
			}
			if err := CheckSchema(ps); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case "array":
		items, ok := s["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("an array says what its items are")
		}
		return CheckSchema(items)
	case "string", "integer", "number", "boolean":
	default:
		if _, ok := s["enum"]; !ok {
			return fmt.Errorf("type %q", t)
		}
	}
	return nil
}
