// Package run carries out the plans people OK'd, one step at a time, in
// order: an action step calls its change or ask and follows it in the app's
// view until it ends; a choose step shows the person a read's rows with
// Desk's suggestion and waits for their pick; a person step puts a to-do on
// someone's list and waits until it is done.
//
// A call to an app and the step's new state are written in one transaction,
// with the record of the call, so a step never calls twice and never loses
// what a call returned. The apps' functions also take the step as their
// origin ("desk:step:<id>"), so one that tracks origins answers a retry with
// what it already made.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/internal/act"
	"github.com/Raposa-Industries/adhunters/desk/internal/plan"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
)

// Suggester picks the rows Desk suggests in a choose step (agent.Suggest).
type Suggester func(ctx context.Context, convID int64, goal, says string, pick int, rows []map[string]any, ids []string, outside []string) ([]string, map[string]string, error)

// Runner runs plans.
type Runner struct {
	Store   *store.Store
	Catalog *actions.Catalog
	Suggest Suggester // nil: the person chooses without a suggestion
	Log     *slog.Logger
}

// Follow is how long a step waiting on an app or a person rests before the
// runner looks again. A choice or a to-do marked done wakes it at once.
const Follow = 30 * time.Second

// Plan takes a plan one pass further: it starts the next step, or looks at
// the one waiting. It returns how long the plan can rest before the next
// pass (0: go on at once), and lets the claim go.
func (r *Runner) Plan(ctx context.Context, planID int64, token string) error {
	rest, err := r.pass(ctx, planID)
	if err != nil {
		rest = time.Minute
	}
	if rerr := r.Store.ReleasePlan(context.WithoutCancel(ctx), planID, token, rest); rerr != nil {
		err = errors.Join(err, rerr)
	}
	return err
}

func (r *Runner) pass(ctx context.Context, planID int64) (time.Duration, error) {
	if r.Store.Stopped(ctx) {
		return time.Minute, nil
	}
	p, err := r.Store.Plan(ctx, planID)
	if err != nil {
		return 0, err
	}
	if p.State != "running" {
		return time.Hour, nil
	}
	conv, err := r.Store.Conversation(ctx, p.ConversationID)
	if err != nil {
		return 0, err
	}
	if conv.State != "open" {
		return time.Hour, nil
	}
	for _, st := range p.Steps {
		switch st.State {
		case "done", "skipped":
			continue
		case "failed":
			return 0, store.EndPlan(ctx, r.Store.Pool, p.ID, "failed", fmt.Sprintf("passo %d: %s", st.N, st.Error))
		case "waiting":
			return 0, r.start(ctx, conv, p, st)
		case "asked":
			return r.look(ctx, conv, p, st)
		default:
			return 0, fmt.Errorf("run: step %d is %s", st.ID, st.State)
		}
	}
	return time.Hour, store.EndPlan(ctx, r.Store.Pool, p.ID, "done", "")
}

func origin(st store.Step) string { return fmt.Sprintf("desk:step:%d", st.ID) }

// start begins a step.
func (r *Runner) start(ctx context.Context, conv store.Conversation, p store.Plan, st store.Step) error {
	switch st.Kind {
	case "action":
		return r.call(ctx, conv, p, st)
	case "choose":
		return r.offer(ctx, conv, p, st)
	case "person":
		return r.Store.Tx(ctx, func(tx pgx.Tx) error {
			t := store.Todo{Title: st.Says, Holder: st.Holder, MadeBy: conv.Person, Due: st.Due,
				Link: fmt.Sprintf("/desk/c/%d", conv.ID), ConversationID: &conv.ID, StepID: &st.ID}
			id, err := store.AddTodo(ctx, tx, t)
			if err != nil {
				return err
			}
			st.State, st.TodoID = "asked", &id
			if err := store.UpdateStep(ctx, tx, st); err != nil {
				return err
			}
			return r.event(ctx, tx, conv, p, st, fmt.Sprintf("Passo %d: to-do para %s: %s", st.N, st.Holder, st.Says))
		})
	}
	return fmt.Errorf("run: step %d is a %s", st.ID, st.Kind)
}

// fail ends a step and its plan with why, in one go.
func (r *Runner) fail(ctx context.Context, db store.DB, p store.Plan, st store.Step, why string) error {
	st.State, st.Error = "failed", why
	if err := store.UpdateStep(ctx, db, st); err != nil {
		return err
	}
	return store.EndPlan(ctx, db, p.ID, "failed", fmt.Sprintf("passo %d: %s", st.N, why))
}

func (r *Runner) event(ctx context.Context, db store.DB, conv store.Conversation, p store.Plan, st store.Step, body string) error {
	_, err := store.AddMessage(ctx, db, store.Message{ConversationID: conv.ID, Author: store.Desk, Kind: "event", Body: body,
		PlanID: &p.ID, StepID: &st.ID})
	return err
}

// call runs an action step's change or ask.
func (r *Runner) call(ctx context.Context, conv store.Conversation, p store.Plan, st store.Step) error {
	a, in, err := plan.Input(r.Catalog, st, p.Steps)
	if err != nil {
		return r.fail(ctx, r.Store.Pool, p, st, err.Error())
	}
	return r.Store.Tx(ctx, func(tx pgx.Tx) error {
		// The daily cap counts inside the transaction that calls, so two
		// runners cannot both take the last call of the day.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('desk.action:' || $1))`, a.Name); err != nil {
			return err
		}
		n, err := store.CallsToday(ctx, tx, a.Name)
		if err != nil {
			return err
		}
		if n >= a.PerDay {
			return r.fail(ctx, tx, p, st, fmt.Sprintf("%s já foi chamado %d vezes hoje, o máximo do Desk", a.Name, a.PerDay))
		}
		input, _ := json.Marshal(in)
		start := time.Now()
		returned, err := act.Call(ctx, tx, a, in, act.Who{Person: conv.Person, Origin: origin(st)})
		call := store.ActionCall{ConversationID: conv.ID, StepID: &st.ID, Action: a.Name, Version: a.Version, Kind: string(a.Kind),
			Person: conv.Person, Input: input, OK: err == nil, Result: returned, Took: time.Since(start)}
		var refused *act.Refused
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &refused):
			call.Error = refused.Why
			if err := store.AddActionCall(ctx, tx, call); err != nil {
				return err
			}
			return r.fail(ctx, tx, p, st, refused.Why)
		case errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "42"):
			// The app's function is not there, or Desk may not call it: a
			// retry would fail the same way.
			call.Error = pgErr.Message
			if err := store.AddActionCall(ctx, tx, call); err != nil {
				return err
			}
			return r.fail(ctx, tx, p, st, a.Name+" não está disponível para o Desk: "+pgErr.Message)
		case err != nil:
			return err // nothing is kept; the step is tried again
		}
		if err := store.AddActionCall(ctx, tx, call); err != nil {
			return err
		}
		st.Result, _ = json.Marshal(plan.ActionResult{Returned: returned})
		st.Ref, st.Link = strings.Trim(string(returned), `"`), act.Link(a, in, returned)
		st.State = "done"
		body := fmt.Sprintf("Passo %d feito: %s", st.N, st.Says)
		if a.Follow != nil {
			st.State = "asked"
			body = fmt.Sprintf("Passo %d pedido: %s", st.N, st.Says)
			if a.Kind == actions.Ask {
				body += ". Alguém confirma no " + a.App() + "."
			}
		}
		if err := store.UpdateStep(ctx, tx, st); err != nil {
			return err
		}
		return r.event(ctx, tx, conv, p, st, body)
	})
}

// offer starts a choose step: the rows, Desk's suggestion, and a to-do for
// the person to pick.
func (r *Runner) offer(ctx context.Context, conv store.Conversation, p store.Plan, st store.Step) error {
	a, in, err := plan.Input(r.Catalog, st, p.Steps)
	if err != nil {
		return r.fail(ctx, r.Store.Pool, p, st, err.Error())
	}
	rows, err := act.Read(ctx, r.Store.Pool, a, in)
	var refused *act.Refused
	if errors.As(err, &refused) {
		return r.fail(ctx, r.Store.Pool, p, st, refused.Why)
	}
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return r.fail(ctx, r.Store.Pool, p, st, "não há nada para escolher")
	}
	res := store.ChoiceResult{From: a.Name, Filters: in, Pick: int(st.Pick)}
	ids := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		c := store.Candidate{ID: text(row[a.Show.ID]), Image: text(row[a.Show.Image]), Text: text(row[a.Show.Text])}
		if c.ID == "" || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		ids = append(ids, c.ID)
		res.Candidates = append(res.Candidates, c)
	}
	if r.Suggest != nil {
		res.Suggested, res.Why, err = r.Suggest(ctx, conv.ID, p.Goal, st.Says, int(st.Pick), rows, ids, a.Outside)
		if err != nil {
			// The person chooses without a suggestion rather than wait.
			r.Log.Warn("no suggestion", "step", st.ID, "err", err)
			res.Suggested, res.Why = nil, nil
		}
	}
	return r.Store.Tx(ctx, func(tx pgx.Tx) error {
		t := store.Todo{Title: "Escolher: " + st.Says, Holder: conv.Person, MadeBy: store.Desk,
			Link: fmt.Sprintf("/desk/c/%d#step-%d", conv.ID, st.ID), ConversationID: &conv.ID, StepID: &st.ID}
		id, err := store.AddTodo(ctx, tx, t)
		if err != nil {
			return err
		}
		st.Result, _ = json.Marshal(res)
		st.State, st.TodoID = "asked", &id
		if err := store.UpdateStep(ctx, tx, st); err != nil {
			return err
		}
		_, err = store.AddMessage(ctx, tx, store.Message{ConversationID: conv.ID, Author: store.Desk, Kind: "choice",
			Body: st.Says, PlanID: &p.ID, StepID: &st.ID})
		return err
	})
}

func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// look checks a step that waits: an app's work, a person's pick, a to-do.
func (r *Runner) look(ctx context.Context, conv store.Conversation, p store.Plan, st store.Step) (time.Duration, error) {
	switch st.Kind {
	case "choose":
		return Follow, nil // Choose ends it, and wakes the plan
	case "person":
		if st.TodoID == nil {
			return 0, fmt.Errorf("run: step %d has no to-do", st.ID)
		}
		t, err := store.GetTodo(ctx, r.Store.Pool, *st.TodoID)
		if err != nil {
			return 0, err
		}
		if t.DoneAt == nil {
			return Follow, nil
		}
		return 0, r.Store.Tx(ctx, func(tx pgx.Tx) error {
			st.State = "done"
			if err := store.UpdateStep(ctx, tx, st); err != nil {
				return err
			}
			return r.event(ctx, tx, conv, p, st, fmt.Sprintf("Passo %d feito por %s: %s", st.N, deref(t.DoneBy), st.Says))
		})
	}
	a, ok := r.Catalog.Version(st.Action, int(st.ActionVersion))
	if !ok {
		return 0, r.fail(ctx, r.Store.Pool, p, st, fmt.Sprintf("%s v%d saiu do catálogo", st.Action, st.ActionVersion))
	}
	var res plan.ActionResult
	if err := json.Unmarshal(st.Result, &res); err != nil {
		return 0, err
	}
	state, row, found, err := act.Follow(ctx, r.Store.Pool, a, res.Returned)
	if err != nil || !found {
		return Follow, err
	}
	ended, failed := act.Ended(a, state)
	if !ended {
		if state != res.State {
			res.State, res.Row = state, row
			st.Result, _ = json.Marshal(res)
			return Follow, store.UpdateStep(ctx, r.Store.Pool, st)
		}
		return Follow, nil
	}
	res.State, res.Row = state, row
	st.Result, _ = json.Marshal(res)
	if failed {
		return 0, r.Store.Tx(ctx, func(tx pgx.Tx) error {
			return r.fail(ctx, tx, p, st, fmt.Sprintf("%s diz %s", a.App(), state))
		})
	}
	return 0, r.Store.Tx(ctx, func(tx pgx.Tx) error {
		st.State = "done"
		if err := store.UpdateStep(ctx, tx, st); err != nil {
			return err
		}
		return r.event(ctx, tx, conv, p, st, fmt.Sprintf("Passo %d concluído (%s): %s", st.N, state, st.Says))
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
