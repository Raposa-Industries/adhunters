package run_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/desk/internal/plan"
	"github.com/Raposa-Industries/adhunters/desk/internal/run"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
	"github.com/Raposa-Industries/adhunters/desk/internal/testdb"
)

const token = "00000000-0000-0000-0000-00000000000a"

type env struct {
	store  *store.Store
	runner *run.Runner
	conv   int64
	plan   int64
	fp     string
}

// setup proposes a plan in a new conversation of ana's.
func setup(t *testing.T, body string) env {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	testdb.Demo(t, pool)
	s := store.New(pool)
	c := testdb.Catalog(t)
	r := &run.Runner{Store: s, Catalog: c, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Suggest: func(_ context.Context, _ int64, _, _ string, pick int, _ []map[string]any, ids []string, _ []string) ([]string, map[string]string, error) {
			return ids[:1], map[string]string{ids[0]: "a primeira"}, nil
		}}
	conv, err := s.NewConversation(ctx, "ana@example.com", "faz um pair de tinnitus")
	if err != nil {
		t.Fatal(err)
	}
	goal, steps, err := plan.Parse(c, json.RawMessage(body), "ana@example.com")
	if err != nil {
		t.Fatal(err)
	}
	id, fp, err := s.Propose(ctx, conv, goal, steps)
	if err != nil {
		t.Fatal(err)
	}
	return env{store: s, runner: r, conv: conv, plan: id, fp: fp}
}

// pass runs the plan once if it can be taken now, and returns it.
func (e env) pass(t *testing.T) store.Plan {
	t.Helper()
	ctx := context.Background()
	// Nothing rests in a test: every pass may take the plan.
	if _, err := e.store.Pool.Exec(ctx, `UPDATE desk.plan SET claimed_until = NULL WHERE claim_token IS NULL`); err != nil {
		t.Fatal(err)
	}
	id, err := e.store.ClaimPlan(ctx, token, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if id != 0 {
		if err := e.runner.Plan(ctx, id, token); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.store.Plan(ctx, e.plan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const pairPlan = `{"goal": "Pair de Tinnitus a partir de um brief novo", "steps": [
	{"kind": "action", "action": "demo.new_brief", "says": "Pedir um brief de Tinnitus", "input": {"vertical": "Tinnitus", "images": 6}},
	{"kind": "choose", "action": "demo.options", "says": "Escolher as opções", "input": {}, "uses": [{"step": 1, "into": "brief_id"}], "pick": 2},
	{"kind": "action", "action": "demo.new_pair", "says": "Pedir o pair ao Launch", "input": {"input": {"account": "acme-sc"}},
	 "uses": [{"step": 2, "into": "input.options"}]},
	{"kind": "person", "says": "Ligar o pair no Taboola", "holder": "mari@example.com"}
]}`

// The whole path: nothing before the OK; the brief is asked for and
// followed until ready; ana picks among its options with Desk's suggestion;
// Launch is asked for a pair of exactly the picked ones; Mari's to-do ends it.
func TestPairPlan(t *testing.T) {
	e := setup(t, pairPlan)
	ctx := context.Background()
	db := e.store.Pool

	if p := e.pass(t); p.State != "proposed" || p.Steps[0].State != "waiting" {
		t.Fatalf("a plan ran before its OK: %s", p.State)
	}
	if err := e.store.Decide(ctx, e.plan, e.fp, "mari@example.com", true); err != store.ErrNotAllowed {
		t.Fatalf("someone else OK'd ana's plan: %v", err)
	}
	if err := e.store.Decide(ctx, e.plan, "not-what-she-saw", "ana@example.com", true); err != store.ErrStale {
		t.Fatalf("an OK for another version of the plan: %v", err)
	}
	if err := e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", true); err != nil {
		t.Fatal(err)
	}

	p := e.pass(t)
	if p.State != "running" || p.Steps[0].State != "asked" || p.Steps[0].Link == "" {
		t.Fatalf("step 1: %+v", p.Steps[0])
	}
	briefID := p.Steps[0].Ref
	var by string
	if err := db.QueryRow(ctx, `SELECT requested_by FROM demo.brief WHERE id = $1`, briefID).Scan(&by); err != nil || by != "ana@example.com" {
		t.Fatalf("the brief is ana's: %q %v", by, err)
	}
	if p = e.pass(t); p.Steps[0].State != "asked" {
		t.Fatal("step 1 ended before the brief was ready")
	}
	if _, err := db.Exec(ctx, `UPDATE demo.brief SET state = 'ready'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO demo.option VALUES (11, $1, '/i/11.png', 'One'), (12, $1, '/i/12.png', 'Two'),
		(13, $1, '/i/13.png', 'Three'), (99, 12345, '/i/99.png', 'Another brief')`, briefID); err != nil {
		t.Fatal(err)
	}
	if p = e.pass(t); p.Steps[0].State != "done" {
		t.Fatalf("step 1 after ready: %s", p.Steps[0].State)
	}

	p = e.pass(t)
	st := p.Steps[1]
	if st.State != "asked" || st.TodoID == nil {
		t.Fatalf("step 2: %+v", st)
	}
	var choice store.ChoiceResult
	if err := json.Unmarshal(st.Result, &choice); err != nil {
		t.Fatal(err)
	}
	if len(choice.Candidates) != 3 || choice.Candidates[0].Image != "/i/11.png" || len(choice.Suggested) != 1 || choice.Suggested[0] != "11" {
		t.Fatalf("the brief's options, with the suggestion: %+v", choice)
	}
	if err := e.store.Choose(ctx, st.ID, "ana@example.com", []string{"99"}); err != store.ErrStale {
		t.Fatalf("a pick that was never shown: %v", err)
	}
	if err := e.store.Choose(ctx, st.ID, "mari@example.com", []string{"12"}); err != store.ErrNotAllowed {
		t.Fatalf("someone else picked for ana: %v", err)
	}
	if err := e.store.Choose(ctx, st.ID, "ana@example.com", []string{"11", "12", "13"}); err == nil {
		t.Fatal("three picked where the step takes two")
	}
	if err := e.store.Choose(ctx, st.ID, "ana@example.com", []string{"12", "13", "12"}); err != nil {
		t.Fatal(err)
	}
	if todo, _ := store.GetTodo(ctx, db, *st.TodoID); todo.DoneAt == nil {
		t.Error("the pick's to-do is still open")
	}

	p = e.pass(t)
	st = p.Steps[2]
	if st.State != "asked" || !strings.HasPrefix(st.Link, "/demo/requests/") {
		t.Fatalf("step 3: %+v", st)
	}
	var options, origin, account string
	if err := db.QueryRow(ctx, `SELECT input->>'options', origin, input->>'account' FROM demo.request`).Scan(&options, &origin, &account); err != nil {
		t.Fatal(err)
	}
	if options != "[12, 13]" || account != "acme-sc" || origin != "desk:step:"+itoa(st.ID) {
		t.Errorf("the pair asked for %s %s from %s", options, account, origin)
	}
	if _, err := db.Exec(ctx, `UPDATE demo.request SET state = 'sent'`); err != nil {
		t.Fatal(err)
	}
	p = e.pass(t)
	if p.Steps[2].State != "done" {
		t.Fatalf("step 3 after sent: %s", p.Steps[2].State)
	}

	p = e.pass(t)
	st = p.Steps[3]
	if st.State != "asked" || st.TodoID == nil {
		t.Fatalf("step 4: %+v", st)
	}
	todo, _ := store.GetTodo(ctx, db, *st.TodoID)
	if todo.Holder != "mari@example.com" || todo.Title != "Ligar o pair no Taboola" {
		t.Errorf("mari's to-do: %+v", todo)
	}
	if err := e.store.DoneTodo(ctx, todo.ID, "mari@example.com"); err != nil {
		t.Fatal(err)
	}
	p = e.pass(t)
	if p.Steps[3].State != "done" {
		t.Fatalf("step 4 after done: %s", p.Steps[3].State)
	}
	if p = e.pass(t); p.State != "done" {
		t.Fatalf("the plan: %s", p.State)
	}

	// Every call is on record, and the end wakes Desk to tell ana.
	calls, _ := e.store.Calls(ctx, e.conv)
	if len(calls) != 2 {
		t.Errorf("%d calls on record", len(calls))
	}
	c, _ := e.store.Conversation(ctx, e.conv)
	if !c.WantsTurn {
		t.Error("the plan ended and Desk was not woken to report")
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// An app that refuses fails the step and ends the plan, and says why.
func TestRefusedStep(t *testing.T) {
	e := setup(t, `{"goal": "Brief grande", "steps": [
		{"kind": "action", "action": "demo.new_brief", "says": "Pedir 40 imagens", "input": {"vertical": "Tinnitus", "images": 40}},
		{"kind": "person", "says": "Revisar"}]}`)
	ctx := context.Background()
	if err := e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", true); err != nil {
		t.Fatal(err)
	}
	p := e.pass(t)
	if p.State != "failed" || p.Steps[0].State != "failed" || !strings.Contains(p.Steps[0].Error, "at most 12") || p.Steps[1].State != "skipped" {
		t.Fatalf("%s %+v", p.State, p.Steps)
	}
	calls, _ := e.store.Calls(ctx, e.conv)
	if len(calls) != 1 || calls[0].OK {
		t.Errorf("the refused call is on record: %+v", calls)
	}
}

// Desk calls an action at most per_day times a day, whatever the plans say.
func TestPerDay(t *testing.T) {
	e := setup(t, `{"goal": "Mais um brief", "steps": [
		{"kind": "action", "action": "demo.new_brief", "says": "Pedir", "input": {"vertical": "Tinnitus", "images": 2}}]}`)
	ctx := context.Background()
	for i := 0; i < 3; i++ { // demo.new_brief allows 3 a day
		if err := store.AddActionCall(ctx, e.store.Pool, store.ActionCall{ConversationID: e.conv, Action: "demo.new_brief", Version: 1,
			Kind: "change", Person: "ana@example.com", OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", true)
	if p := e.pass(t); p.State != "failed" || !strings.Contains(p.Steps[0].Error, "máximo") {
		t.Fatalf("%s %+v", p.State, p.Steps[0])
	}
	var n int
	e.store.Pool.QueryRow(ctx, `SELECT count(*) FROM demo.brief`).Scan(&n)
	if n != 0 {
		t.Error("the fourth call of the day ran")
	}
}

// Stopping the conversation stops its plan before the next step.
func TestStop(t *testing.T) {
	e := setup(t, pairPlan)
	ctx := context.Background()
	e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", true)
	if err := e.store.Stop(ctx, e.conv, "ana@example.com"); err != nil {
		t.Fatal(err)
	}
	p := e.pass(t)
	if p.State != "stopped" || p.Steps[0].State != "waiting" {
		t.Fatalf("%s %s", p.State, p.Steps[0].State)
	}
	var n int
	e.store.Pool.QueryRow(ctx, `SELECT count(*) FROM demo.brief`).Scan(&n)
	if n != 0 {
		t.Error("a stopped plan ran a step")
	}
}

// With the stop switch on, no step runs.
func TestStopSwitch(t *testing.T) {
	e := setup(t, pairPlan)
	ctx := context.Background()
	e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", true)
	if err := e.store.SetStopped(ctx, true, "ana@example.com"); err != nil {
		t.Fatal(err)
	}
	if p := e.pass(t); p.Steps[0].State != "waiting" {
		t.Fatalf("a step ran with Desk stopped: %s", p.Steps[0].State)
	}
	e.store.SetStopped(ctx, false, "ana@example.com")
	if p := e.pass(t); p.Steps[0].State != "asked" {
		t.Fatalf("the plan did not go on: %s", p.Steps[0].State)
	}
}

// A refused plan never runs, and wakes Desk to hear why.
func TestRefusedPlan(t *testing.T) {
	e := setup(t, pairPlan)
	ctx := context.Background()
	if err := e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", false); err != nil {
		t.Fatal(err)
	}
	p := e.pass(t)
	if p.State != "refused" || p.Steps[0].State != "skipped" {
		t.Fatalf("%s %s", p.State, p.Steps[0].State)
	}
	if err := e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", true); err != store.ErrStale {
		t.Errorf("a refused plan was OK'd after: %v", err)
	}
}

// A newer plan replaces one still waiting; the old one cannot be OK'd.
func TestReplaced(t *testing.T) {
	e := setup(t, pairPlan)
	ctx := context.Background()
	c := testdb.Catalog(t)
	goal, steps, _ := plan.Parse(c, json.RawMessage(`{"goal": "Outro", "steps": [{"kind": "person", "says": "x"}]}`), "ana@example.com")
	if _, _, err := e.store.Propose(ctx, e.conv, goal, steps); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Decide(ctx, e.plan, e.fp, "ana@example.com", true); err != store.ErrStale {
		t.Errorf("a replaced plan was OK'd: %v", err)
	}
}
