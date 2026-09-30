// Package act runs catalog actions (contract/actions) against the apps'
// _api views and functions: reads with their filters, changes and asks with
// their arguments, and follows what a change or ask started.
//
// It builds each statement from the catalog entry only: names come from the
// entry (checked when the catalog loads) and every value the model gives is
// a parameter, never text in the SQL. The input a model sees and the input
// these functions take are described by the same schema (Schema).
package act

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Raposa-Industries/adhunters/contract/actions"
)

// Querier is what following a call needs: a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pool starts transactions: what a read runs in.
type Pool interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Who fills the arguments that come from Desk rather than the model.
type Who struct {
	Person string // the email of the person the work is for
	Origin string // "desk:step:<id>"
}

// Refused is an input the model gave that the action does not take. Its
// text goes back to the model, which can correct itself.
type Refused struct{ Why string }

func (e *Refused) Error() string { return e.Why }

func refuse(format string, a ...any) error { return &Refused{Why: fmt.Sprintf(format, a...)} }

// Schema is the JSON Schema of an action's input, as the model gets it: a
// read's filters and args plus limit, or a change's and ask's own args
// (those Desk fills or the catalog fixes are left out). Closed, and with no
// numeric or length limits, as strict tool schemas need.
func Schema(a actions.Action) map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, g := range a.Args {
		if g.From != "" || len(g.Const) > 0 {
			continue
		}
		props[g.Name] = argSchema(g)
		if !g.Optional {
			required = append(required, g.Name)
		}
	}
	if a.Kind == actions.Read {
		for _, f := range a.Filters {
			s := typeSchema(f.Type, f.Enum, f.Says, nil)
			if f.Op == "in" {
				s = map[string]any{"type": "array", "items": typeSchema(f.Type, f.Enum, "", nil), "description": f.Says}
			}
			props[f.Property()] = s
		}
		props[actions.LimitProperty] = map[string]any{"type": "integer", "description": fmt.Sprintf("the most rows wanted, up to %d", a.Limit)}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func argSchema(g actions.Arg) map[string]any {
	says := g.Says
	if g.Max > 0 {
		unit := "characters"
		if strings.HasSuffix(g.Type, "[]") {
			unit = "items"
		}
		says = fmt.Sprintf("%s (at most %d %s)", says, g.Max, unit)
	}
	return typeSchema(g.Type, g.Enum, says, g.Schema)
}

func typeSchema(typ string, enum []string, says string, schema json.RawMessage) map[string]any {
	var s map[string]any
	switch typ {
	case "object":
		s = map[string]any{}
		_ = json.Unmarshal(schema, &s) // checked when the catalog loaded
	case "time":
		s = map[string]any{"type": "string", "format": "date-time"}
	case "date":
		s = map[string]any{"type": "string", "format": "date"}
	case "string[]":
		s = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	case "integer[]":
		s = map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}
	default:
		s = map[string]any{"type": typ}
	}
	if len(enum) > 0 {
		s["enum"] = enum
	}
	if says != "" {
		s["description"] = says
	}
	return s
}

// Read runs a read with the input in (filters, args and limit, by the names
// Schema gives them) and returns its rows as JSON objects, in the action's
// order. Values in the action's outside columns are cut to maxOutside
// characters each. It runs in a read-only transaction, so a read changes
// nothing, whatever the view or function behind it does.
func Read(ctx context.Context, db Pool, a actions.Action, in map[string]any) ([]map[string]any, error) {
	var rows []map[string]any
	err := pgx.BeginTxFunc(ctx, db, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		rows, err = read(ctx, tx, a, in)
		return err
	})
	return rows, err
}

func read(ctx context.Context, q Querier, a actions.Action, in map[string]any) ([]map[string]any, error) {
	if a.Kind != actions.Read {
		return nil, fmt.Errorf("act: %s is a %s, not a read", a.Name, a.Kind)
	}
	if err := known(a, in); err != nil {
		return nil, err
	}
	var args []any
	param := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	cols := make([]string, len(a.Columns))
	for i, c := range a.Columns {
		cols[i] = pgx.Identifier{c}.Sanitize()
	}
	from, err := source(a, in, param)
	if err != nil {
		return nil, err
	}
	var where []string
	for _, f := range a.Filters {
		raw, ok := in[f.Property()]
		if !ok || raw == nil {
			continue
		}
		col := pgx.Identifier{f.Column}.Sanitize()
		if f.Op == "in" {
			list, ok := raw.([]any)
			if !ok {
				return nil, refuse("%s is a list", f.Property())
			}
			vals := make([]any, 0, len(list))
			for _, item := range list {
				v, err := value(f.Property(), f.Type, f.Enum, 0, nil, item)
				if err != nil {
					return nil, err
				}
				vals = append(vals, v)
			}
			where = append(where, fmt.Sprintf("%s = ANY(%s)", col, param(vals)))
			continue
		}
		v, err := value(f.Property(), f.Type, f.Enum, 0, nil, raw)
		if err != nil {
			return nil, err
		}
		switch f.Op {
		case "=":
			where = append(where, fmt.Sprintf("%s = %s", col, param(v)))
		case ">=":
			where = append(where, fmt.Sprintf("%s >= %s", col, param(v)))
		case "<=":
			where = append(where, fmt.Sprintf("%s <= %s", col, param(v)))
		case "has":
			s, _ := v.(string)
			where = append(where, fmt.Sprintf("%s ILIKE %s", col, param("%"+likeEscape(s)+"%")))
		}
	}
	limit := a.Limit
	if raw, ok := in[actions.LimitProperty]; ok && raw != nil {
		n, err := integer(actions.LimitProperty, raw)
		if err != nil {
			return nil, err
		}
		if n < 1 {
			return nil, refuse("limit is 1 or more")
		}
		limit = int(min(n, int64(a.Limit)))
	}
	sql := "SELECT " + strings.Join(cols, ", ") + " FROM " + from
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	if a.Order != "" {
		sql += " ORDER BY " + order(a.Order)
	}
	sql += fmt.Sprintf(" LIMIT %d", limit)
	rows, err := q.Query(ctx, "SELECT row_to_json(t)::text FROM ("+sql+") t", args...)
	if err != nil {
		return nil, fmt.Errorf("act: %s: %w", a.Name, err)
	}
	out := []map[string]any{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, err
		}
		row := map[string]any{}
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		if err := dec.Decode(&row); err != nil {
			rows.Close()
			return nil, err
		}
		for _, c := range a.Outside {
			if t, ok := row[c].(string); ok {
				row[c] = Clip(t, maxOutside)
			}
		}
		out = append(out, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("act: %s: %w", a.Name, err)
	}
	return out, nil
}

// maxOutside is the most characters of one outside value a read returns.
const maxOutside = 600

// Clip cuts s to at most n characters, marking the cut, and drops control
// characters other than newlines and tabs.
func Clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0) && r != 0x200b && r != 0xfeff {
			return r
		}
		return -1
	}, s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func source(a actions.Action, in map[string]any, param func(any) string) (string, error) {
	if a.View != "" {
		schema, name, _ := strings.Cut(a.View, ".")
		return pgx.Identifier{schema, name}.Sanitize(), nil
	}
	named, err := callArgs(a, in, Who{}, param)
	if err != nil {
		return "", err
	}
	schema, name, _ := strings.Cut(a.Call, ".")
	return pgx.Identifier{schema, name}.Sanitize() + "(" + named + ")", nil
}

// Call runs a change or ask inside tx with the input in and the arguments
// Desk fills, and returns what the function returned, as JSON.
func Call(ctx context.Context, tx pgx.Tx, a actions.Action, in map[string]any, who Who) (json.RawMessage, error) {
	if a.Kind != actions.Change && a.Kind != actions.Ask {
		return nil, fmt.Errorf("act: %s is a %s; only changes and asks are called", a.Name, a.Kind)
	}
	if err := known(a, in); err != nil {
		return nil, err
	}
	var args []any
	named, err := callArgs(a, in, who, func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	})
	if err != nil {
		return nil, err
	}
	schema, name, _ := strings.Cut(a.Call, ".")
	var out []byte
	// A savepoint, so an app refusing the call (its own RAISE) leaves the
	// caller's transaction usable to record the refusal.
	err = pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		return sp.QueryRow(ctx, "SELECT to_jsonb("+pgx.Identifier{schema, name}.Sanitize()+"("+named+"))::text", args...).Scan(&out)
	})
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "P0001" || strings.HasPrefix(pe.Code, "22") || strings.HasPrefix(pe.Code, "23")) {
			// The app's own refusal (RAISE) or a value it does not take.
			return nil, &Refused{Why: a.Name + ": " + pe.Message}
		}
		return nil, fmt.Errorf("act: %s: %w", a.Name, err)
	}
	return json.RawMessage(out), nil
}

// callArgs builds "p_a => $1, p_b => $2" from the action's args.
func callArgs(a actions.Action, in map[string]any, who Who, param func(any) string) (string, error) {
	var parts []string
	for _, g := range a.Args {
		var v any
		switch {
		case g.From == actions.FromPerson:
			if who.Person == "" {
				return "", fmt.Errorf("act: %s needs the person it is for", a.Name)
			}
			v = who.Person
		case g.From == actions.FromOrigin:
			if who.Origin == "" {
				return "", fmt.Errorf("act: %s needs its origin", a.Name)
			}
			v = who.Origin
		case len(g.Const) > 0:
			var c any
			if err := json.Unmarshal(g.Const, &c); err != nil {
				return "", err
			}
			if g.Const[0] == '{' || g.Const[0] == '[' {
				c = g.Const // a JSON value for a json or jsonb parameter
			}
			v = c
		default:
			raw, ok := in[g.Name]
			if !ok || raw == nil {
				if g.Optional {
					continue
				}
				return "", refuse("%s is needed: %s", g.Name, g.Says)
			}
			var err error
			if v, err = value(g.Name, g.Type, g.Enum, g.Max, g.Schema, raw); err != nil {
				return "", err
			}
		}
		parts = append(parts, pgx.Identifier{"p_" + g.Name}.Sanitize()+" => "+param(v))
	}
	return strings.Join(parts, ", "), nil
}

// known refuses an input name the action does not take.
func known(a actions.Action, in map[string]any) error {
	props, _ := Schema(a)["properties"].(map[string]any)
	for k := range in {
		if _, ok := props[k]; !ok {
			return refuse("%s takes no %q", a.Name, k)
		}
	}
	return nil
}

// value turns one input value, as decoded from JSON, into what the
// parameter's type takes. An object is checked against its schema.
func value(name, typ string, enum []string, maxLen int, schema json.RawMessage, raw any) (any, error) {
	switch typ {
	case "string":
		s, ok := raw.(string)
		if !ok {
			return nil, refuse("%s is text", name)
		}
		if len(enum) > 0 && !contains(enum, s) {
			return nil, refuse("%s is one of %s", name, strings.Join(enum, ", "))
		}
		if maxLen > 0 && utf8.RuneCountInString(s) > maxLen {
			return nil, refuse("%s is at most %d characters", name, maxLen)
		}
		return s, nil
	case "integer":
		return integer(name, raw)
	case "number":
		switch n := raw.(type) {
		case json.Number:
			return n.Float64()
		case float64:
			return n, nil
		case int64:
			return float64(n), nil
		}
		return nil, refuse("%s is a number", name)
	case "boolean":
		b, ok := raw.(bool)
		if !ok {
			return nil, refuse("%s is true or false", name)
		}
		return b, nil
	case "time":
		s, _ := raw.(string)
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, refuse("%s is a time like 2026-09-30T18:00:00Z", name)
		}
		return t.UTC(), nil
	case "date":
		s, _ := raw.(string)
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, refuse("%s is a date like 2026-09-30", name)
		}
		return t, nil
	case "object":
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, refuse("%s is an object", name)
		}
		var s map[string]any
		if err := json.Unmarshal(schema, &s); err != nil {
			return nil, fmt.Errorf("act: %s has no schema", name)
		}
		if err := Valid(s, m, name); err != nil {
			return nil, err
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case "string[]", "integer[]":
		list, ok := raw.([]any)
		if !ok {
			return nil, refuse("%s is a list", name)
		}
		if maxLen > 0 && len(list) > maxLen {
			return nil, refuse("%s has at most %d items", name, maxLen)
		}
		if typ == "string[]" {
			out := make([]string, 0, len(list))
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, refuse("%s is a list of texts", name)
				}
				out = append(out, s)
			}
			return out, nil
		}
		out := make([]int64, 0, len(list))
		for _, item := range list {
			n, err := integer(name, item)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	}
	return nil, fmt.Errorf("act: unknown type %q", typ)
}

func integer(name string, raw any) (int64, error) {
	switch n := raw.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, nil
		}
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
			return int64(n), nil
		}
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	}
	return 0, refuse("%s is a whole number", name)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// order quotes each column of a checked ORDER BY ("sightings_7d DESC").
func order(o string) string {
	parts := strings.Split(o, ",")
	for i, p := range parts {
		f := strings.Fields(strings.TrimSpace(p))
		f[0] = pgx.Identifier{f[0]}.Sanitize()
		parts[i] = strings.Join(f, " ")
	}
	return strings.Join(parts, ", ")
}

// Follow reads the row a change or ask started, in the view its catalog
// entry names, by the id the call returned. ok is false while the row is
// not there yet.
func Follow(ctx context.Context, q Querier, a actions.Action, returned json.RawMessage) (state string, row map[string]any, ok bool, err error) {
	f := a.Follow
	if f == nil {
		return "", nil, false, fmt.Errorf("act: %s has nothing to follow", a.Name)
	}
	var id any
	dec := json.NewDecoder(strings.NewReader(string(returned)))
	dec.UseNumber()
	if err := dec.Decode(&id); err != nil {
		return "", nil, false, fmt.Errorf("act: %s returned %s, not an id", a.Name, returned)
	}
	if n, isNum := id.(json.Number); isNum {
		if id, err = n.Int64(); err != nil {
			return "", nil, false, fmt.Errorf("act: %s returned %s, not an id", a.Name, returned)
		}
	}
	schema, name, _ := strings.Cut(f.View, ".")
	var s string
	err = q.QueryRow(ctx, "SELECT row_to_json(t)::text FROM (SELECT * FROM "+pgx.Identifier{schema, name}.Sanitize()+
		" WHERE "+pgx.Identifier{f.ID}.Sanitize()+" = $1) t", id).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("act: follow %s: %w", a.Name, err)
	}
	row = map[string]any{}
	dec = json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&row); err != nil {
		return "", nil, false, err
	}
	state, _ = row[f.State].(string)
	return state, row, true, nil
}

// Ended says whether a followed state is one of the done or failed ones,
// and which.
func Ended(a actions.Action, state string) (ended, failed bool) {
	if a.Follow == nil {
		return true, false
	}
	if contains(a.Follow.Failed, state) {
		return true, true
	}
	return contains(a.Follow.Done, state), false
}

// Link fills an action's link from its input and what it returned.
func Link(a actions.Action, in map[string]any, returned json.RawMessage) string {
	if a.Link == "" {
		return ""
	}
	out := a.Link
	var r any
	if json.Unmarshal(returned, &r) == nil {
		out = strings.ReplaceAll(out, "{returned}", url.PathEscape(scalar(r)))
	}
	for k, v := range in {
		out = strings.ReplaceAll(out, "{"+k+"}", url.PathEscape(scalar(v)))
	}
	return out
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case json.Number:
		return x.String()
	case bool:
		return fmt.Sprint(x)
	}
	return ""
}
