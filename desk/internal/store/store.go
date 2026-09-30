// Package store is Desk's own data in the desk schema: conversations and
// their messages, the model's turns, plans and steps, the calls Desk made,
// the to-do list and the settings. desk-web and desk-agent both use it.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is a pool or a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store reads and writes the desk schema.
type Store struct{ Pool *pgxpool.Pool }

// New wraps a pool.
func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

// Tx runs fn in one transaction.
func (s *Store) Tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, fn)
}

// ErrNotAllowed is a person trying what is not theirs to do.
var ErrNotAllowed = errors.New("not yours to do")

// ErrChoice is marking a choice's to-do done by hand: it is done when its
// person picks.
var ErrChoice = errors.New("a choice's to-do is done by picking")

// ErrStale is an OK or a choice for something that changed since the page
// was drawn.
var ErrStale = errors.New("this changed since the page was drawn")

// Desk is the author and holder name Desk itself goes by.
const Desk = "desk"

// ---- settings --------------------------------------------------------------

// Setting returns one setting's value, or def when it is not set.
func (s *Store) Setting(ctx context.Context, key, def string) string {
	var v string
	if err := s.Pool.QueryRow(ctx, `SELECT value FROM desk.setting WHERE key = $1`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// SettingFloat reads a number setting.
func (s *Store) SettingFloat(ctx context.Context, key string, def float64) float64 {
	f, err := strconv.ParseFloat(s.Setting(ctx, key, ""), 64)
	if err != nil {
		return def
	}
	return f
}

// Stopped says whether the stop switch is on.
func (s *Store) Stopped(ctx context.Context) bool {
	return s.Setting(ctx, "stopped", "false") != "false"
}

// SetStopped turns the stop switch on or off.
func (s *Store) SetStopped(ctx context.Context, on bool, by string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE desk.setting SET value = $1, changed_by = $2, changed_at = now() WHERE key = 'stopped'`,
		strconv.FormatBool(on), by)
	return err
}

// SetSettings changes some settings at once.
func (s *Store) SetSettings(ctx context.Context, values map[string]string, by string) error {
	return s.Tx(ctx, func(tx pgx.Tx) error {
		for k, v := range values {
			if _, err := tx.Exec(ctx, `UPDATE desk.setting SET value = $2, changed_by = $3, changed_at = now() WHERE key = $1`, k, v, by); err != nil {
				return err
			}
		}
		return nil
	})
}

// Settings returns every setting, by key.
func (s *Store) Settings(ctx context.Context) ([]SettingRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT key, value, says, changed_by, changed_at FROM desk.setting ORDER BY key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SettingRow])
}

// SettingRow is one setting.
type SettingRow struct {
	Key, Value, Says, ChangedBy string
	ChangedAt                   time.Time
}

// ---- conversations and messages ------------------------------------------------

// Conversation is one conversation with one person.
type Conversation struct {
	ID        int64
	Title     string
	Person    string
	StartedAt time.Time
	LastAt    time.Time
	State     string
	WantsTurn bool
	HeardUpTo int64
}

const convCols = `id, title, person, started_at, last_at, state, wants_turn, heard_up_to`

// NewConversation starts a conversation for person with their first message.
func (s *Store) NewConversation(ctx context.Context, person, text string) (int64, error) {
	var id int64
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO desk.conversation (title, person) VALUES ($1, $2) RETURNING id`,
			Title(text), person).Scan(&id); err != nil {
			return err
		}
		_, err := AddMessage(ctx, tx, Message{ConversationID: id, Author: person, Kind: "text", Body: text, Wakes: true})
		return err
	})
	return id, err
}

// Title is a conversation's title: its first line, cut to 80 characters.
func Title(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if utf8.RuneCountInString(line) > 80 {
		line = string([]rune(line)[:79]) + "…"
	}
	return line
}

// Conversation returns one conversation.
func (s *Store) Conversation(ctx context.Context, id int64) (Conversation, error) {
	rows, _ := s.Pool.Query(ctx, `SELECT `+convCols+` FROM desk.conversation WHERE id = $1`, id)
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Conversation])
}

// Conversations returns a person's conversations, latest first.
func (s *Store) Conversations(ctx context.Context, person string, limit int) ([]Conversation, error) {
	rows, _ := s.Pool.Query(ctx, `SELECT `+convCols+` FROM desk.conversation WHERE person = $1 ORDER BY last_at DESC LIMIT $2`, person, limit)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Conversation])
}

// Say adds a person's message to their conversation. Only the person the
// conversation is with may write in it.
func (s *Store) Say(ctx context.Context, convID int64, person, text string) error {
	return s.Tx(ctx, func(tx pgx.Tx) error {
		var owner, state string
		if err := tx.QueryRow(ctx, `SELECT person, state FROM desk.conversation WHERE id = $1 FOR UPDATE`, convID).Scan(&owner, &state); err != nil {
			return err
		}
		if owner != person {
			return ErrNotAllowed
		}
		if state == "stopped" {
			// Writing again picks the conversation up where it stopped.
			if _, err := tx.Exec(ctx, `UPDATE desk.conversation SET state = 'open' WHERE id = $1`, convID); err != nil {
				return err
			}
		}
		_, err := AddMessage(ctx, tx, Message{ConversationID: convID, Author: person, Kind: "text", Body: text, Wakes: true})
		return err
	})
}

// Message is one line of a conversation as the page shows it.
type Message struct {
	ID             int64
	ConversationID int64
	At             time.Time
	Author         string
	Kind           string
	Body           string
	Wakes          bool
	PlanID         *int64
	StepID         *int64
}

const msgCols = `id, conversation_id, at, author, kind, body, wakes, plan_id, step_id`

// AddMessage adds a message and, when it wakes Desk, asks for a turn.
func AddMessage(ctx context.Context, db DB, m Message) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO desk.message (conversation_id, author, kind, body, wakes, plan_id, step_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		m.ConversationID, m.Author, m.Kind, m.Body, m.Wakes, m.PlanID, m.StepID).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = db.Exec(ctx, `UPDATE desk.conversation SET last_at = now(), wants_turn = wants_turn OR $2 WHERE id = $1`, m.ConversationID, m.Wakes)
	return id, err
}

// Messages returns a conversation's messages after one id, oldest first.
func (s *Store) Messages(ctx context.Context, convID, after int64) ([]Message, error) {
	rows, _ := s.Pool.Query(ctx, `SELECT `+msgCols+` FROM desk.message WHERE conversation_id = $1 AND id > $2 ORDER BY id`, convID, after)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Message])
}

// Version changes whenever anything a conversation's page shows does: a
// message, whether Desk owes an answer, a plan's or a step's state or result.
func (s *Store) Version(ctx context.Context, convID int64) (string, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `SELECT c.state || c.wants_turn::text || ':' ||
		(SELECT COALESCE(max(id), 0) FROM desk.message WHERE conversation_id = c.id)::text || ':' ||
		COALESCE((SELECT md5(string_agg(p.state || s.id || s.state || COALESCE(s.result::text, ''), ',' ORDER BY s.id))
		          FROM desk.plan p JOIN desk.step s ON s.plan_id = p.id WHERE p.conversation_id = c.id), '')
		FROM desk.conversation c WHERE c.id = $1`, convID).Scan(&v)
	return v, err
}

// StepOf returns the conversation a step is in, and the most its person
// may pick in it (0: any number).
func (s *Store) StepOf(ctx context.Context, stepID int64) (convID int64, pick int, err error) {
	var p int16
	err = s.Pool.QueryRow(ctx, `SELECT p.conversation_id, s.pick FROM desk.step s JOIN desk.plan p ON p.id = s.plan_id WHERE s.id = $1`,
		stepID).Scan(&convID, &p)
	return convID, int(p), err
}

// Stop stops a conversation: Desk takes no more turns in it and its plan
// stops before its next step. The person picks it up again by writing.
func (s *Store) Stop(ctx context.Context, convID int64, by string) error {
	return s.Tx(ctx, func(tx pgx.Tx) error {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT person FROM desk.conversation WHERE id = $1 FOR UPDATE`, convID).Scan(&owner); err != nil {
			return err
		}
		if owner != by {
			return ErrNotAllowed
		}
		if _, err := tx.Exec(ctx, `UPDATE desk.conversation SET state = 'stopped', wants_turn = false WHERE id = $1`, convID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE desk.plan SET state = 'stopped', ended_at = now(), note = 'stopped by ' || $2
			WHERE conversation_id = $1 AND state IN ('proposed', 'approved', 'running')`, convID, by)
		if err != nil {
			return err
		}
		body := "Parado por " + by + "."
		if tag.RowsAffected() > 0 {
			body = "Parado por " + by + ". O plano não segue."
		}
		_, err = AddMessage(ctx, tx, Message{ConversationID: convID, Author: Desk, Kind: "event", Body: body})
		return err
	})
}

// ---- turns -------------------------------------------------------------------

// ClaimTurn takes one conversation that wants a turn, for hold, or returns
// 0 when none does.
func (s *Store) ClaimTurn(ctx context.Context, token string, hold time.Duration) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `UPDATE desk.conversation SET claim_token = $1, claimed_until = now() + $2::interval
		WHERE id = (SELECT id FROM desk.conversation WHERE wants_turn AND state = 'open'
		              AND (claimed_until IS NULL OR claimed_until < now())
		            ORDER BY last_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id`, token, hold.String()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Hold extends a claim on a conversation while its turn runs.
func (s *Store) Hold(ctx context.Context, convID int64, token string, hold time.Duration) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE desk.conversation SET claimed_until = now() + $3::interval WHERE id = $1 AND claim_token = $2`,
		convID, token, hold.String())
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("the claim on conversation %d was lost", convID)
	}
	return err
}

// Heard records that the model has been given every message up to upTo.
func (s *Store) Heard(ctx context.Context, convID, upTo int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE desk.conversation SET heard_up_to = GREATEST(heard_up_to, $2) WHERE id = $1`, convID, upTo)
	return err
}

// FinishTurn lets a turn's claim go. The conversation still wants a turn if
// a waking message came in while it ran.
func (s *Store) FinishTurn(ctx context.Context, convID int64, token string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE desk.conversation c SET claim_token = NULL, claimed_until = NULL,
		wants_turn = EXISTS (SELECT 1 FROM desk.message m WHERE m.conversation_id = c.id AND m.id > c.heard_up_to AND m.wakes)
		WHERE id = $1 AND claim_token = $2`, convID, token)
	return err
}

// Release lets a claim go without finishing the turn: the conversation is
// taken again after rest (a turn that failed is tried again later).
func (s *Store) Release(ctx context.Context, convID int64, token string, rest time.Duration) error {
	_, err := s.Pool.Exec(ctx, `UPDATE desk.conversation SET claim_token = NULL, claimed_until = now() + $3::interval
		WHERE id = $1 AND claim_token = $2`, convID, token, rest.String())
	return err
}

// Turn is one message of the model's side of a conversation: the API's
// own JSON ({"role": …, "content": […]}), and the prefix (instructions and
// tools) it was made with.
type Turn struct {
	ID      int64
	Role    string
	Content json.RawMessage
	Prefix  string
}

// AddTurn adds one message to the model's side of a conversation.
func (s *Store) AddTurn(ctx context.Context, convID int64, t Turn) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO desk.turn (conversation_id, role, content, prefix) VALUES ($1, $2, $3, $4)`,
		convID, t.Role, string(t.Content), t.Prefix)
	return err
}

// Turns returns the model's side of a conversation, in order.
func (s *Store) Turns(ctx context.Context, convID int64) ([]Turn, error) {
	rows, _ := s.Pool.Query(ctx, `SELECT id, role, content::text, prefix FROM desk.turn WHERE conversation_id = $1 ORDER BY id`, convID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Turn, error) {
		var t Turn
		var c string
		err := r.Scan(&t.ID, &t.Role, &c, &t.Prefix)
		t.Content = json.RawMessage(c)
		return t, err
	})
}

// ModelCall is one call to Claude.
type ModelCall struct {
	ConversationID                                               *int64
	Purpose, Model, StopReason                                   string
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64
	USD                                                          float64
	Took                                                         time.Duration
}

// AddModelCall records one call to Claude.
func (s *Store) AddModelCall(ctx context.Context, m ModelCall) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO desk.model_call (conversation_id, purpose, model, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, usd, stop_reason, ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		m.ConversationID, m.Purpose, m.Model, m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens,
		m.USD, m.StopReason, m.Took.Milliseconds())
	return err
}

// SpentToday is what Desk spent on Claude since 00:00 UTC.
func (s *Store) SpentToday(ctx context.Context) (float64, error) {
	var usd float64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(sum(usd), 0)::float8 FROM desk.model_call
		WHERE at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`).Scan(&usd)
	return usd, err
}

// ---- plans and steps -------------------------------------------------------

// StepSpec is one step as Desk proposes it and the person OKs it.
type StepSpec struct {
	Kind          string          `json:"kind"` // action, choose or person
	Says          string          `json:"says"`
	Action        string          `json:"action,omitempty"` // action: a change or ask; choose: a read
	ActionVersion int             `json:"action_version,omitempty"`
	Input         json.RawMessage `json:"input,omitempty"`
	Uses          []Use           `json:"uses,omitempty"`
	Pick          int             `json:"pick,omitempty"`   // choose: the most the person picks (0: any)
	Holder        string          `json:"holder,omitempty"` // person: who does it
	Due           string          `json:"due,omitempty"`    // person: by when, 2026-10-01
}

// Use fills one input of a step from an earlier step's result: what an
// action returned, or the ids a person chose.
type Use struct {
	Step int    `json:"step"`
	Into string `json:"into"`
}

// Fingerprint names a plan's goal and steps exactly: an OK is for this and
// nothing else.
func Fingerprint(goal string, steps []StepSpec) string {
	b, _ := json.Marshal(struct {
		Goal  string     `json:"goal"`
		Steps []StepSpec `json:"steps"`
	}{goal, steps})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Propose records a plan in the conversation, replacing one still waiting
// for an OK, and shows it.
func (s *Store) Propose(ctx context.Context, convID int64, goal string, steps []StepSpec) (int64, string, error) {
	fp := Fingerprint(goal, steps)
	var id int64
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE desk.plan SET state = 'replaced', ended_at = now() WHERE conversation_id = $1 AND state = 'proposed'`, convID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO desk.plan (conversation_id, goal, fingerprint) VALUES ($1, $2, $3) RETURNING id`,
			convID, goal, fp).Scan(&id); err != nil {
			return err
		}
		for i, st := range steps {
			in := st.Input
			if len(in) == 0 {
				in = json.RawMessage(`{}`)
			}
			uses, _ := json.Marshal(st.Uses)
			if st.Uses == nil {
				uses = []byte(`[]`)
			}
			var due any
			if st.Due != "" {
				due = st.Due
			}
			if _, err := tx.Exec(ctx, `INSERT INTO desk.step (plan_id, n, kind, says, action, action_version, input, uses, pick, holder, due)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date)`, id, i+1, st.Kind, st.Says, st.Action, st.ActionVersion,
				string(in), string(uses), st.Pick, st.Holder, due); err != nil {
				return err
			}
		}
		_, err := AddMessage(ctx, tx, Message{ConversationID: convID, Author: Desk, Kind: "plan", Body: goal, PlanID: &id})
		return err
	})
	return id, fp, err
}

// Plan is a plan and its steps.
type Plan struct {
	ID             int64
	ConversationID int64
	Goal           string
	Fingerprint    string
	State          string
	ProposedAt     time.Time
	DecidedBy      *string
	DecidedAt      *time.Time
	EndedAt        *time.Time
	Note           string
	Steps          []Step
}

// Step is one step of a plan.
type Step struct {
	ID            int64
	PlanID        int64
	N             int16
	Kind          string
	Says          string
	Action        string
	ActionVersion int32
	Input         json.RawMessage
	Uses          []Use
	Pick          int16
	Holder        string
	Due           *time.Time
	State         string
	Result        json.RawMessage
	Ref           string
	Link          string
	TodoID        *int64
	StartedAt     *time.Time
	EndedAt       *time.Time
	Error         string
}

const planCols = `id, conversation_id, goal, fingerprint, state, proposed_at, decided_by, decided_at, ended_at, note`
const stepCols = `id, plan_id, n, kind, says, action, action_version, input::text, uses::text, pick, holder, due, state, result::text,
	ref, link, todo_id, started_at, ended_at, error`

func scanStep(r pgx.CollectableRow) (Step, error) {
	var st Step
	var in, uses string
	var res *string
	err := r.Scan(&st.ID, &st.PlanID, &st.N, &st.Kind, &st.Says, &st.Action, &st.ActionVersion, &in, &uses, &st.Pick, &st.Holder,
		&st.Due, &st.State, &res, &st.Ref, &st.Link, &st.TodoID, &st.StartedAt, &st.EndedAt, &st.Error)
	if err != nil {
		return st, err
	}
	st.Input = json.RawMessage(in)
	if res != nil {
		st.Result = json.RawMessage(*res)
	}
	return st, json.Unmarshal([]byte(uses), &st.Uses)
}

// GetPlan returns a plan with its steps.
func GetPlan(ctx context.Context, db DB, id int64) (Plan, error) {
	var p Plan
	err := db.QueryRow(ctx, `SELECT `+planCols+` FROM desk.plan WHERE id = $1`, id).Scan(&p.ID, &p.ConversationID, &p.Goal,
		&p.Fingerprint, &p.State, &p.ProposedAt, &p.DecidedBy, &p.DecidedAt, &p.EndedAt, &p.Note)
	if err != nil {
		return p, err
	}
	rows, _ := db.Query(ctx, `SELECT `+stepCols+` FROM desk.step WHERE plan_id = $1 ORDER BY n`, id)
	p.Steps, err = pgx.CollectRows(rows, scanStep)
	return p, err
}

// Plan returns a plan with its steps.
func (s *Store) Plan(ctx context.Context, id int64) (Plan, error) { return GetPlan(ctx, s.Pool, id) }

// Plans returns a conversation's plans with their steps, oldest first.
func (s *Store) Plans(ctx context.Context, convID int64) ([]Plan, error) {
	rows, _ := s.Pool.Query(ctx, `SELECT id FROM desk.plan WHERE conversation_id = $1 ORDER BY id`, convID)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	out := make([]Plan, 0, len(ids))
	for _, id := range ids {
		p, err := s.Plan(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Decide records a person's OK or no for a plan they saw with fingerprint.
// Only the person the conversation is with decides, and only a plan still
// waiting, exactly as it was shown.
func (s *Store) Decide(ctx context.Context, planID int64, fingerprint, person string, ok bool) error {
	return s.Tx(ctx, func(tx pgx.Tx) error {
		var convID int64
		var owner, state, fp string
		err := tx.QueryRow(ctx, `SELECT p.conversation_id, c.person, p.state, p.fingerprint FROM desk.plan p
			JOIN desk.conversation c ON c.id = p.conversation_id WHERE p.id = $1 FOR UPDATE OF p`, planID).Scan(&convID, &owner, &state, &fp)
		if err != nil {
			return err
		}
		if owner != person {
			return ErrNotAllowed
		}
		if state != "proposed" || fp != fingerprint {
			return ErrStale
		}
		next, body, wakes := "approved", "OK de "+person+". Desk começa o plano.", false
		if !ok {
			next, body, wakes = "refused", person+" disse não ao plano.", true
		}
		if _, err := tx.Exec(ctx, `UPDATE desk.plan SET state = $2, decided_by = $3, decided_at = now(),
			ended_at = CASE WHEN $2 = 'refused' THEN now() END WHERE id = $1`, planID, next, person); err != nil {
			return err
		}
		if !ok {
			if _, err := tx.Exec(ctx, `UPDATE desk.step SET state = 'skipped' WHERE plan_id = $1`, planID); err != nil {
				return err
			}
		}
		_, err = AddMessage(ctx, tx, Message{ConversationID: convID, Author: Desk, Kind: "event", Body: body, Wakes: wakes, PlanID: &planID})
		return err
	})
}

// ClaimPlan takes one OK'd or running plan whose conversation is open, for
// hold, or returns 0.
func (s *Store) ClaimPlan(ctx context.Context, token string, hold time.Duration) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `UPDATE desk.plan SET claim_token = $1, claimed_until = now() + $2::interval,
		state = CASE WHEN state = 'approved' THEN 'running' ELSE state END
		WHERE id = (SELECT p.id FROM desk.plan p JOIN desk.conversation c ON c.id = p.conversation_id
		            WHERE p.state IN ('approved', 'running') AND c.state = 'open'
		              AND (p.claimed_until IS NULL OR p.claimed_until < now())
		            ORDER BY p.claimed_until NULLS FIRST, p.id LIMIT 1 FOR UPDATE OF p SKIP LOCKED)
		RETURNING id`, token, hold.String()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// ReleasePlan lets a plan's claim go; it is looked at again after rest.
func (s *Store) ReleasePlan(ctx context.Context, planID int64, token string, rest time.Duration) error {
	_, err := s.Pool.Exec(ctx, `UPDATE desk.plan SET claim_token = NULL, claimed_until = now() + $3::interval
		WHERE id = $1 AND claim_token = $2`, planID, token, rest.String())
	return err
}

// EndPlan closes a plan as done, failed or stopped, and tells the
// conversation (which wakes Desk to report).
func EndPlan(ctx context.Context, db DB, planID int64, state, note string) error {
	var convID int64
	if err := db.QueryRow(ctx, `UPDATE desk.plan SET state = $2, note = $3, ended_at = now(), claim_token = NULL
		WHERE id = $1 RETURNING conversation_id`, planID, state, note).Scan(&convID); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `UPDATE desk.step SET state = 'skipped' WHERE plan_id = $1 AND state IN ('waiting', 'running')`, planID); err != nil {
		return err
	}
	body := map[string]string{"done": "Plano concluído.", "failed": "O plano parou: ", "stopped": "O plano foi parado: "}[state]
	if state != "done" {
		body += note
	}
	_, err := AddMessage(ctx, db, Message{ConversationID: convID, Author: Desk, Kind: "event", Body: body, Wakes: true, PlanID: &planID})
	return err
}

// UpdateStep writes a step's new state and what came of it.
func UpdateStep(ctx context.Context, db DB, st Step) error {
	var res any
	if len(st.Result) > 0 {
		res = string(st.Result)
	}
	_, err := db.Exec(ctx, `UPDATE desk.step SET state = $2, result = $3::jsonb, ref = $4, link = $5, todo_id = $6, error = $7,
		started_at = COALESCE(started_at, CASE WHEN $2 <> 'waiting' THEN now() END),
		ended_at = CASE WHEN $2 IN ('done', 'failed', 'skipped') THEN now() END
		WHERE id = $1`, st.ID, st.State, res, st.Ref, st.Link, st.TodoID, st.Error)
	return err
}

// Choose records a person's pick among a choose step's candidates, which
// must be ones Desk showed them.
func (s *Store) Choose(ctx context.Context, stepID int64, person string, ids []string) error {
	return s.Tx(ctx, func(tx pgx.Tx) error {
		var convID, planID int64
		var owner, state, planState string
		var result *string
		var todo *int64
		err := tx.QueryRow(ctx, `SELECT c.id, p.id, c.person, s.state, p.state, s.result::text, s.todo_id FROM desk.step s
			JOIN desk.plan p ON p.id = s.plan_id JOIN desk.conversation c ON c.id = p.conversation_id
			WHERE s.id = $1 AND s.kind = 'choose' FOR UPDATE OF s`, stepID).Scan(&convID, &planID, &owner, &state, &planState, &result, &todo)
		if err != nil {
			return err
		}
		if owner != person {
			return ErrNotAllowed
		}
		if state != "asked" || planState != "running" || result == nil {
			return ErrStale
		}
		var r ChoiceResult
		if err := json.Unmarshal([]byte(*result), &r); err != nil {
			return err
		}
		shown := map[string]bool{}
		for _, c := range r.Candidates {
			shown[c.ID] = true
		}
		if len(ids) == 0 {
			return fmt.Errorf("choose at least one")
		}
		picked := []string{}
		for _, id := range ids {
			if !shown[id] {
				return ErrStale
			}
			if !slices.Contains(picked, id) {
				picked = append(picked, id)
			}
		}
		if r.Pick > 0 && len(picked) > r.Pick {
			return fmt.Errorf("choose at most %d", r.Pick)
		}
		r.Chosen = picked
		b, _ := json.Marshal(r)
		if _, err := tx.Exec(ctx, `UPDATE desk.step SET state = 'done', result = $2::jsonb, ended_at = now() WHERE id = $1`, stepID, string(b)); err != nil {
			return err
		}
		if todo != nil {
			if _, err := tx.Exec(ctx, `UPDATE desk.todo SET done_at = now(), done_by = $2 WHERE id = $1 AND done_at IS NULL`, *todo, person); err != nil {
				return err
			}
		}
		// Let the plan go on at once.
		if _, err := tx.Exec(ctx, `UPDATE desk.plan SET claimed_until = NULL WHERE id = $1 AND claim_token IS NULL`, planID); err != nil {
			return err
		}
		_, err = AddMessage(ctx, tx, Message{ConversationID: convID, Author: person, Kind: "event",
			Body: fmt.Sprintf("%s escolheu %d de %d.", person, len(picked), len(r.Candidates)), StepID: &stepID})
		return err
	})
}

// ChoiceResult is a choose step's result: what Desk showed, what it
// suggested and why, and what the person chose.
type ChoiceResult struct {
	Candidates []Candidate        `json:"candidates"`
	Suggested  []string           `json:"suggested"`
	Why        map[string]string  `json:"why,omitempty"`
	Chosen     []string           `json:"chosen,omitempty"`
	Rows       []map[string]any   `json:"-"`
	Extra      map[string]any     `json:"extra,omitempty"`
	From       string             `json:"from,omitempty"`
	Filters    map[string]any     `json:"filters,omitempty"`
	Pick       int                `json:"pick,omitempty"`
	Hints      map[string]float64 `json:"-"`
}

// Candidate is one row a person may choose.
type Candidate struct {
	ID    string `json:"id"`
	Image string `json:"image,omitempty"`
	Text  string `json:"text,omitempty"`
}

// ---- calls to the apps --------------------------------------------------

// ActionCall is one call Desk made to an app's action.
type ActionCall struct {
	ConversationID int64
	StepID         *int64
	Action         string
	Version        int
	Kind           string
	Person         string
	Input          json.RawMessage
	OK             bool
	Result         json.RawMessage
	Error          string
	Took           time.Duration
}

// AddActionCall records one call.
func AddActionCall(ctx context.Context, db DB, c ActionCall) error {
	var res any
	if len(c.Result) > 0 {
		res = string(c.Result)
	}
	in := c.Input
	if len(in) == 0 {
		in = json.RawMessage(`{}`)
	}
	_, err := db.Exec(ctx, `INSERT INTO desk.action_call (conversation_id, step_id, action, action_version, kind, person, input, ok, result, error, ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11)`,
		c.ConversationID, c.StepID, c.Action, c.Version, c.Kind, c.Person, string(in), c.OK, res, c.Error, c.Took.Milliseconds())
	return err
}

// CallsToday counts Desk's calls of one action since 00:00 UTC, whatever
// came of them.
func CallsToday(ctx context.Context, db DB, action string) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM desk.action_call WHERE action = $1
		AND at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, action).Scan(&n)
	return n, err
}

// Calls returns a conversation's calls, newest first.
func (s *Store) Calls(ctx context.Context, convID int64) ([]ActionCall, error) {
	rows, _ := s.Pool.Query(ctx, `SELECT conversation_id, step_id, action, action_version, kind, person, input::text, ok,
		COALESCE(result::text, ''), error, ms FROM desk.action_call WHERE conversation_id = $1 ORDER BY id DESC LIMIT 200`, convID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ActionCall, error) {
		var c ActionCall
		var in, res string
		var ms int32
		err := r.Scan(&c.ConversationID, &c.StepID, &c.Action, &c.Version, &c.Kind, &c.Person, &in, &c.OK, &res, &c.Error, &ms)
		c.Input, c.Result, c.Took = json.RawMessage(in), json.RawMessage(res), time.Duration(ms)*time.Millisecond
		return c, err
	})
}

// ---- to-dos ------------------------------------------------------------------

// Todo is one piece of work someone holds.
type Todo struct {
	ID             int64
	Title          string
	Holder         string
	MadeBy         string
	MadeAt         time.Time
	Due            *time.Time
	Link           string
	ConversationID *int64
	StepID         *int64
	DoneAt         *time.Time
	DoneBy         *string
	Choice         bool // a choice's: done when its person picks
}

const todoCols = `id, title, holder, made_by, made_at, due, link, conversation_id, step_id, done_at, done_by,
	EXISTS (SELECT 1 FROM desk.step s WHERE s.todo_id = todo.id AND s.kind = 'choose')`

// AddTodo puts a to-do on someone's list.
func AddTodo(ctx context.Context, db DB, t Todo) (int64, error) {
	t.Title, t.Holder = strings.TrimSpace(t.Title), strings.ToLower(strings.TrimSpace(t.Holder))
	if t.Title == "" || t.Holder == "" {
		return 0, fmt.Errorf("a to-do has a title and someone who holds it")
	}
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO desk.todo (title, holder, made_by, due, link, conversation_id, step_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, t.Title, t.Holder, t.MadeBy, t.Due, t.Link, t.ConversationID, t.StepID).Scan(&id)
	return id, err
}

// AddTodo puts a to-do on someone's list.
func (s *Store) AddTodo(ctx context.Context, t Todo) (int64, error) { return AddTodo(ctx, s.Pool, t) }

// TodoFilter picks to-dos: one holder's (or everyone's), open or all.
type TodoFilter struct {
	Holder string
	Open   bool
	Limit  int
}

// Todos returns to-dos, open ones by due date first.
func (s *Store) Todos(ctx context.Context, f TodoFilter) ([]Todo, error) {
	if f.Limit <= 0 {
		f.Limit = 200
	}
	rows, _ := s.Pool.Query(ctx, `SELECT `+todoCols+` FROM desk.todo
		WHERE ($1 = '' OR holder = $1) AND (NOT $2 OR done_at IS NULL)
		ORDER BY done_at IS NOT NULL, due NULLS LAST, id DESC LIMIT $3`, strings.ToLower(f.Holder), f.Open, f.Limit)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Todo])
}

// GetTodo returns one to-do.
func GetTodo(ctx context.Context, db DB, id int64) (Todo, error) {
	rows, _ := db.Query(ctx, `SELECT `+todoCols+` FROM desk.todo WHERE id = $1`, id)
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Todo])
}

// DoneTodo marks a to-do done. A choice's is done only by picking.
func (s *Store) DoneTodo(ctx context.Context, id int64, by string) error {
	t, err := GetTodo(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	if t.Choice {
		return ErrChoice
	}
	_, err = s.Pool.Exec(ctx, `UPDATE desk.todo SET done_at = now(), done_by = $2 WHERE id = $1 AND done_at IS NULL`, id, by)
	if err == nil {
		// A plan waiting on this to-do looks again at once.
		_, err = s.Pool.Exec(ctx, `UPDATE desk.plan p SET claimed_until = NULL FROM desk.step s
			WHERE s.todo_id = $1 AND s.plan_id = p.id AND p.claim_token IS NULL`, id)
	}
	return err
}

// Note is one comment on a to-do.
type Note struct {
	ID     int64
	TodoID int64
	At     time.Time
	Author string
	Body   string
}

// AddNote comments on a to-do.
func (s *Store) AddNote(ctx context.Context, todoID int64, author, body string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO desk.todo_note (todo_id, author, body) VALUES ($1, $2, $3)`, todoID, author, strings.TrimSpace(body))
	return err
}

// Notes returns the comments on some to-dos, oldest first.
func (s *Store) Notes(ctx context.Context, todoIDs []int64) ([]Note, error) {
	rows, _ := s.Pool.Query(ctx, `SELECT id, todo_id, at, author, body FROM desk.todo_note WHERE todo_id = ANY($1) ORDER BY id`, todoIDs)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Note])
}
