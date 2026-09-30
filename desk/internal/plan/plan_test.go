package plan_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/internal/act"
	"github.com/Raposa-Industries/adhunters/desk/internal/plan"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
	"github.com/Raposa-Industries/adhunters/desk/internal/testdb"
)

// The team's main path: make a brief, the person picks among its options,
// Launch is asked for a pair from the picked ones, a person starts it.
const good = `{"goal": "Pair de Tinnitus a partir de um brief novo", "steps": [
	{"kind": "action", "action": "demo.new_brief", "says": "Pedir um brief de Tinnitus", "input": {"vertical": "Tinnitus", "images": 6}},
	{"kind": "choose", "action": "demo.options", "says": "Escolher as opções", "input": {}, "uses": [{"step": 1, "into": "brief_id"}], "pick": 4},
	{"kind": "action", "action": "demo.new_pair", "says": "Pedir o pair ao Launch", "input": {"input": {"account": "acme-sc"}},
	 "uses": [{"step": 2, "into": "input.options"}]},
	{"kind": "person", "says": "Ligar o pair no Taboola", "holder": "Mari@Example.com", "due": "2026-10-01"}
]}`

func TestParse(t *testing.T) {
	c := testdb.Catalog(t)
	goal, steps, err := plan.Parse(c, json.RawMessage(good), "ana@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if goal == "" || len(steps) != 4 {
		t.Fatalf("%q %d steps", goal, len(steps))
	}
	if steps[1].Pick != 4 || steps[1].ActionVersion != 1 || steps[3].Holder != "mari@example.com" || steps[3].Due != "2026-10-01" {
		t.Errorf("%+v", steps)
	}
	// The fingerprint is the same for the same plan, and changes with any step.
	_, again, _ := plan.Parse(c, json.RawMessage(good), "ana@example.com")
	if store.Fingerprint(goal, steps) != store.Fingerprint(goal, again) {
		t.Error("one plan, two fingerprints")
	}
	other := strings.Replace(good, `"images": 6`, `"images": 7`, 1)
	_, changed, _ := plan.Parse(c, json.RawMessage(other), "ana@example.com")
	if store.Fingerprint(goal, steps) == store.Fingerprint(goal, changed) {
		t.Error("a changed step kept its fingerprint")
	}

	// A person step for no one named is for the person talking.
	_, steps, err = plan.Parse(c, json.RawMessage(`{"goal": "g", "steps": [{"kind": "person", "says": "revisar"}]}`), "ana@example.com")
	if err != nil || steps[0].Holder != "ana@example.com" {
		t.Errorf("%+v %v", steps, err)
	}
}

func TestParseRefuses(t *testing.T) {
	c := testdb.Catalog(t)
	cases := map[string]string{
		"a read as an action":               strings.Replace(good, `"kind": "action", "action": "demo.new_brief"`, `"kind": "action", "action": "demo.options"`, 1),
		"a choice among what shows nothing": strings.Replace(good, `"action": "demo.options"`, `"action": "demo.briefs"`, 1),
		"an action not listed":              strings.Replace(good, `demo.new_brief`, `demo.delete_everything`, 1),
		"a use of a later step":             strings.Replace(good, `{"step": 1, "into": "brief_id"}`, `{"step": 3, "into": "brief_id"}`, 1),
		"a use of a to-do":                  strings.Replace(good, `"uses": [{"step": 2, "into": "input.options"}]}`, `"uses": [{"step": 2, "into": "input.options"}]}, {"kind": "action", "action": "demo.new_brief", "says": "x", "input": {"vertical": "Tinnitus"}, "uses": [{"step": 4, "into": "images"}]}`, 1),
		"several picks into one value":      strings.Replace(good, `{"step": 2, "into": "input.options"}`, `{"step": 2, "into": "input.account"}`, 1),
		"a used value also given":           strings.Replace(good, `"input": {}, "uses": [{"step": 1`, `"input": {"brief_id": 3}, "uses": [{"step": 1`, 1),
		"a use into nothing":                strings.Replace(good, `"into": "brief_id"`, `"into": "brief"`, 1),
		"a missing input":                   strings.Replace(good, `, "images": 6`, ``, 1),
		"an input the action lacks":         strings.Replace(good, `"images": 6`, `"images": 6, "budget": 100`, 1),
		"a holder who is no one":            strings.Replace(good, `Mari@Example.com`, `mari`, 1),
		"a due date that is not one":        strings.Replace(good, `2026-10-01`, `amanhã`, 1),
		"a field plans lack":                strings.Replace(good, `"goal":`, `"approved": true, "goal":`, 1),
		"a step with no words":              strings.Replace(good, `"Escolher as opções"`, `" "`, 1),
		"a pick above the read's limit":     strings.Replace(good, `"pick": 4`, `"pick": 400`, 1),
		"no goal":                           strings.Replace(good, `Pair de Tinnitus a partir de um brief novo`, ``, 1),
		"no steps":                          `{"goal": "g", "steps": []}`,
		"too many steps":                    `{"goal": "g", "steps": [` + strings.TrimSuffix(strings.Repeat(`{"kind": "person", "says": "x"},`, plan.MaxSteps+1), ",") + `]}`,
	}
	for name, body := range cases {
		_, _, err := plan.Parse(c, json.RawMessage(body), "ana@example.com")
		var r *act.Refused
		if !errors.As(err, &r) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
}

// When a step runs, what it uses is filled from the earlier steps' results.
func TestInput(t *testing.T) {
	c := testdb.Catalog(t)
	_, specs, err := plan.Parse(c, json.RawMessage(good), "ana@example.com")
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]store.Step, len(specs))
	for i, sp := range specs {
		steps[i] = store.Step{N: int16(i + 1), Kind: sp.Kind, Action: sp.Action, ActionVersion: int32(sp.ActionVersion), Input: sp.Input, Uses: sp.Uses, State: "waiting"}
	}
	if _, _, err := plan.Input(c, steps[1], steps); err == nil {
		t.Error("a step ran before the one it uses had a result")
	}
	steps[0].State, steps[0].Result = "done", json.RawMessage(`{"returned": 42, "state": "ready"}`)
	a, in, err := plan.Input(c, steps[1], steps)
	if err != nil || a.Name != "demo.options" || in["brief_id"] != json.Number("42") {
		t.Fatalf("%v %v", in, err)
	}
	steps[1].State, steps[1].Result = "done", json.RawMessage(`{"candidates": [{"id": "3"}, {"id": "5"}, {"id": "8"}], "chosen": ["3", "8"]}`)
	a, in, err = plan.Input(c, steps[2], steps)
	if err != nil {
		t.Fatal(err)
	}
	obj := in["input"].(map[string]any)
	if obj["account"] != "acme-sc" || len(obj["options"].([]any)) != 2 || obj["options"].([]any)[1] != json.Number("8") {
		t.Errorf("%v", in)
	}
	if err := act.Check(a, in, nil); err != nil {
		t.Errorf("the filled input is one the action takes: %v", err)
	}

	// The exact version the person OK'd runs, or nothing does.
	steps[2].ActionVersion = 9
	if _, _, err := plan.Input(c, steps[2], steps); err == nil {
		t.Error("a version the catalog lacks ran")
	}
}

func TestSchema(t *testing.T) {
	c := testdb.Catalog(t)
	s := plan.Schema(c)
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"const":"demo.new_pair"`, `"const":"demo.new_brief"`, `"const":"demo.options"`, `"const":"person"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the schema lacks %s", want)
		}
	}
	if strings.Contains(string(b), `"const":"demo.briefs"`) {
		t.Error("a read that shows nothing a person can choose is not a choice")
	}
	var reads []actions.Action
	for _, a := range c.Latest() {
		if a.Kind == actions.Read {
			reads = append(reads, a)
		}
	}
	if len(reads) != 2 {
		t.Fatalf("%d reads", len(reads))
	}
}
