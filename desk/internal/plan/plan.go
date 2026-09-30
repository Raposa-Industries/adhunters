// Package plan is what a plan may hold: the propose_plan tool's schema,
// built from the action catalog, the checks a proposed plan must pass before
// a person sees it, and how a step's input is filled from earlier steps when
// it runs.
//
// A plan is steps in order. An action step calls one change or ask; a
// choose step shows a person some rows of a read, with Desk's suggestion,
// and they pick; a person step puts a to-do on someone's list. A step can
// take a value from an earlier one ("uses"): what an action returned (a
// brief's id), or the ids a person chose.
package plan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/internal/act"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
)

// MaxSteps is the most steps one plan holds.
const MaxSteps = 12

// Schema is propose_plan's input schema: a goal and steps, each step one of
// the catalog's changes and asks, a choice among a read's rows, or a
// person's to-do.
func Schema(c *actions.Catalog) map[string]any {
	uses := map[string]any{
		"type":        "array",
		"description": "inputs this step takes from earlier steps' results; leave those inputs out of input",
		"items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"step", "into"},
			"properties": map[string]any{
				"step": map[string]any{"type": "integer", "description": "an earlier step's number, counting from 1"},
				"into": map[string]any{"type": "string", "description": "the input it fills: a name in input, or arg.field for a field of an object input"},
			},
		},
	}
	says := map[string]any{"type": "string", "description": "what this step does, in one short line in the person's language"}
	var kinds []any
	for _, a := range c.Latest() {
		switch {
		case a.Kind == actions.Change || a.Kind == actions.Ask:
			kinds = append(kinds, map[string]any{
				"type": "object", "additionalProperties": false, "description": describe(a),
				"required": []string{"kind", "action", "says", "input"},
				"properties": map[string]any{
					"kind": map[string]any{"const": "action"}, "action": map[string]any{"const": a.Name},
					"says": says, "input": loose(act.Schema(a)), "uses": uses,
				},
			})
		case a.Kind == actions.Read && a.Show != nil:
			kinds = append(kinds, map[string]any{
				"type": "object", "additionalProperties": false,
				"description": "The person chooses among " + a.Name + " rows (" + a.Says + "); Desk suggests some.",
				"required":    []string{"kind", "action", "says", "input"},
				"properties": map[string]any{
					"kind": map[string]any{"const": "choose"}, "action": map[string]any{"const": a.Name},
					"says": says, "input": loose(act.Schema(a)), "uses": uses,
					"pick": map[string]any{"type": "integer", "description": "the most rows the person picks; 0 for any number"},
				},
			})
		}
	}
	kinds = append(kinds, map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "Work only a person can do (start a pair in Taboola, review a landing page): it goes on their to-do list and the plan waits until they mark it done.",
		"required":    []string{"kind", "says"},
		"properties": map[string]any{
			"kind":   map[string]any{"const": "person"},
			"says":   says,
			"holder": map[string]any{"type": "string", "description": "the email of who does it; leave it out for the person you talk with"},
			"due":    map[string]any{"type": "string", "format": "date", "description": "by when, if there is a date"},
		},
	})
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"goal", "steps"},
		"properties": map[string]any{
			"goal":  map[string]any{"type": "string", "description": "what the plan is for, in one line in the person's language"},
			"steps": map[string]any{"type": "array", "items": map[string]any{"anyOf": kinds}, "description": fmt.Sprintf("1 to %d steps, run in order", MaxSteps)},
		},
	}
}

func describe(a actions.Action) string {
	d := a.Says
	if a.Kind == actions.Ask {
		d += " A person confirms it on " + a.App() + "'s own screen; nothing happens before they do."
	}
	if a.Costs != "" {
		d += " Costs: " + a.Costs
	}
	return d + fmt.Sprintf(" Returns %s. At most %d a day.", a.Returns, a.PerDay)
}

// loose is an input schema with nothing required: a value an earlier step
// gives is left out. Parse checks what is needed.
func loose(s map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		out[k] = v
	}
	out["required"] = []string{}
	return out
}

type proposed struct {
	Goal  string         `json:"goal"`
	Steps []proposedStep `json:"steps"`
}

type proposedStep struct {
	Kind   string         `json:"kind"`
	Action string         `json:"action"`
	Says   string         `json:"says"`
	Input  map[string]any `json:"input"`
	Uses   []store.Use    `json:"uses"`
	Pick   *json.Number   `json:"pick"`
	Holder string         `json:"holder"`
	Due    string         `json:"due"`
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Parse reads propose_plan's input and checks it against the catalog. A
// person step with no holder is for person. A refusal (*act.Refused) says
// what to fix, for the model.
func Parse(c *actions.Catalog, input json.RawMessage, person string) (string, []store.StepSpec, error) {
	var p proposed
	dec := json.NewDecoder(strings.NewReader(string(input)))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return "", nil, &act.Refused{Why: "the plan is not in propose_plan's shape: " + err.Error()}
	}
	p.Goal = strings.TrimSpace(p.Goal)
	if p.Goal == "" || utf8.RuneCountInString(p.Goal) > 300 {
		return "", nil, refuse("a plan has a goal of one line")
	}
	if len(p.Steps) == 0 || len(p.Steps) > MaxSteps {
		return "", nil, refuse("a plan has 1 to %d steps", MaxSteps)
	}
	var out []store.StepSpec
	for i, ps := range p.Steps {
		n := i + 1
		spec, err := parseStep(c, ps, n, out, person)
		if err != nil {
			return "", nil, refuse("step %d: %s", n, err.Error())
		}
		out = append(out, spec)
	}
	return p.Goal, out, nil
}

func parseStep(c *actions.Catalog, ps proposedStep, n int, earlier []store.StepSpec, person string) (store.StepSpec, error) {
	spec := store.StepSpec{Kind: ps.Kind, Says: strings.TrimSpace(ps.Says), Uses: ps.Uses}
	if spec.Says == "" {
		return spec, fmt.Errorf("says what it does")
	}
	if ps.Input == nil {
		ps.Input = map[string]any{}
	}
	switch ps.Kind {
	case "action", "choose":
		a, ok := c.Get(ps.Action)
		if !ok {
			return spec, fmt.Errorf("there is no action %q", ps.Action)
		}
		switch {
		case ps.Kind == "action" && a.Kind != actions.Change && a.Kind != actions.Ask:
			return spec, fmt.Errorf("%s is a %s; an action step runs a change or an ask (a read needs no plan)", a.Name, a.Kind)
		case ps.Kind == "choose" && (a.Kind != actions.Read || a.Show == nil):
			return spec, fmt.Errorf("%s has no rows a person can choose among", a.Name)
		}
		spec.Action, spec.ActionVersion = a.Name, a.Version
		var into []string
		for _, u := range ps.Uses {
			if err := checkUse(a, u, n, earlier, ps.Input); err != nil {
				return spec, err
			}
			into = append(into, u.Into)
		}
		if err := act.Check(a, ps.Input, into); err != nil {
			return spec, err
		}
		if ps.Kind == "choose" && ps.Pick != nil {
			pick, err := ps.Pick.Int64()
			if err != nil || pick < 0 || pick > int64(a.Limit) {
				return spec, fmt.Errorf("pick is 0 (any number) to %d", a.Limit)
			}
			spec.Pick = int(pick)
		}
		b, err := json.Marshal(ps.Input)
		if err != nil {
			return spec, err
		}
		spec.Input = b
	case "person":
		if len(ps.Uses) > 0 || len(ps.Input) > 0 || ps.Action != "" {
			return spec, fmt.Errorf("a person step has only says, holder and due")
		}
		spec.Holder = strings.ToLower(strings.TrimSpace(ps.Holder))
		if spec.Holder == "" {
			spec.Holder = person
		}
		if !emailRe.MatchString(spec.Holder) {
			return spec, fmt.Errorf("holder %q is not someone's email", ps.Holder)
		}
		if ps.Due != "" {
			if _, err := time.Parse("2006-01-02", ps.Due); err != nil {
				return spec, fmt.Errorf("due is a date like 2026-10-01")
			}
			spec.Due = ps.Due
		}
	default:
		return spec, fmt.Errorf("kind is action, choose or person")
	}
	return spec, nil
}

func checkUse(a actions.Action, u store.Use, n int, earlier []store.StepSpec, input map[string]any) error {
	if u.Step < 1 || u.Step >= n {
		return fmt.Errorf("uses step %d, which is not an earlier step", u.Step)
	}
	src := earlier[u.Step-1]
	if src.Kind == "person" {
		return fmt.Errorf("uses step %d, a person's to-do, which gives nothing", u.Step)
	}
	typ, ok := act.PropertyType(a, u.Into)
	if !ok || typ == "object" || typ == "boolean" || typ == "number" {
		return fmt.Errorf("%s takes no input %q an earlier step can fill", a.Name, u.Into)
	}
	if src.Kind == "choose" && !strings.HasSuffix(typ, "[]") && src.Pick != 1 {
		return fmt.Errorf("step %d may pick several, and %s takes one: make it pick 1", u.Step, u.Into)
	}
	top, field, nested := strings.Cut(u.Into, ".")
	given := input[top]
	if nested {
		obj, _ := given.(map[string]any)
		given = obj[field]
	}
	if given != nil {
		return fmt.Errorf("%s comes from step %d; leave it out of input", u.Into, u.Step)
	}
	return nil
}

func refuse(format string, a ...any) error {
	return &act.Refused{Why: fmt.Sprintf(format, a...)}
}

// ActionResult is an action step's result: what the call returned, and the
// row it started as last followed.
type ActionResult struct {
	Returned json.RawMessage `json:"returned"`
	State    string          `json:"state,omitempty"`
	Row      map[string]any  `json:"row,omitempty"`
}

// Input is a step's input as it runs: the input the person OK'd, with what
// it uses from earlier steps filled in. It returns the exact action version
// the person OK'd.
func Input(c *actions.Catalog, st store.Step, steps []store.Step) (actions.Action, map[string]any, error) {
	a, ok := c.Version(st.Action, int(st.ActionVersion))
	if !ok {
		return a, nil, fmt.Errorf("%s v%d is no longer in the catalog", st.Action, st.ActionVersion)
	}
	in := map[string]any{}
	dec := json.NewDecoder(strings.NewReader(string(st.Input)))
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		return a, nil, err
	}
	for _, u := range st.Uses {
		var src *store.Step
		for i := range steps {
			if int(steps[i].N) == u.Step {
				src = &steps[i]
			}
		}
		if src == nil || src.State != "done" {
			return a, nil, fmt.Errorf("step %d has not given its result", u.Step)
		}
		vals, err := output(*src)
		if err != nil {
			return a, nil, fmt.Errorf("step %d: %w", u.Step, err)
		}
		typ, _ := act.PropertyType(a, u.Into)
		v, err := convert(vals, typ)
		if err != nil {
			return a, nil, fmt.Errorf("%s from step %d: %w", u.Into, u.Step, err)
		}
		if top, field, nested := strings.Cut(u.Into, "."); nested {
			obj, _ := in[top].(map[string]any)
			if obj == nil {
				obj = map[string]any{}
			}
			obj[field] = v
			in[top] = obj
		} else {
			in[u.Into] = v
		}
	}
	return a, in, nil
}

// output is what a finished step gives later ones: what an action
// returned, or the ids a person chose.
func output(st store.Step) ([]string, error) {
	switch st.Kind {
	case "action":
		var r ActionResult
		if err := json.Unmarshal(st.Result, &r); err != nil {
			return nil, err
		}
		var v any
		dec := json.NewDecoder(strings.NewReader(string(r.Returned)))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		switch x := v.(type) {
		case json.Number:
			return []string{x.String()}, nil
		case string:
			return []string{x}, nil
		}
		return nil, fmt.Errorf("it returned %s, not an id", r.Returned)
	case "choose":
		var r store.ChoiceResult
		if err := json.Unmarshal(st.Result, &r); err != nil {
			return nil, err
		}
		if len(r.Chosen) == 0 {
			return nil, fmt.Errorf("nothing was chosen")
		}
		return r.Chosen, nil
	}
	return nil, fmt.Errorf("a %s step gives nothing", st.Kind)
}

func convert(vals []string, typ string) (any, error) {
	one := func(s string) any {
		if strings.HasPrefix(typ, "integer") {
			return json.Number(s)
		}
		return s
	}
	if strings.HasSuffix(typ, "[]") {
		out := make([]any, len(vals))
		for i, s := range vals {
			out[i] = one(s)
		}
		return out, nil
	}
	if len(vals) != 1 {
		return nil, fmt.Errorf("%d values for one", len(vals))
	}
	return one(vals[0]), nil
}
