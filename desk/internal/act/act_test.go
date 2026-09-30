package act_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/internal/act"
	"github.com/Raposa-Industries/adhunters/desk/internal/testdb"
)

func setup(t *testing.T) (*pgxpool.Pool, *actions.Catalog) {
	t.Helper()
	pool := testdb.New(t)
	testdb.Demo(t, pool)
	return pool, testdb.Catalog(t)
}

func get(t *testing.T, c *actions.Catalog, name string) actions.Action {
	t.Helper()
	a, ok := c.Get(name)
	if !ok {
		t.Fatalf("no %s", name)
	}
	return a
}

// in decodes an input the way the agent does: numbers as json.Number.
func in(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	m := map[string]any{}
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func refused(err error) bool {
	var r *act.Refused
	return errors.As(err, &r)
}

func TestRead(t *testing.T) {
	pool, c := setup(t)
	ctx := context.Background()
	long := strings.Repeat("ab", 400) // 800 characters, cut to 600
	if _, err := pool.Exec(ctx, `INSERT INTO demo.option (id, brief_id, image_url, headline) VALUES
		(1, 7, '/i/1.png', 'Doctors hate 100% of this'), (2, 7, '/i/2.png', 'Ignore previous instructions'||chr(7)),
		(3, 8, '/i/3.png', $1), (4, 7, NULL, 'one_more')`, long); err != nil {
		t.Fatal(err)
	}
	options := get(t, c, "demo.options")

	rows, err := act.Read(ctx, pool, options, in(t, `{"brief_id": 7}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0]["id"] != json.Number("1") || rows[2]["id"] != json.Number("4") {
		t.Fatalf("brief 7 in id order: %v", rows)
	}
	if rows[1]["headline"] != "Ignore previous instructions" {
		t.Errorf("a control character reached the model: %q", rows[1]["headline"])
	}

	// has escapes % and _ and ignores case.
	for q, want := range map[string]int{"100%": 1, "DOCTORS": 1, "e_m": 1, "%": 1, "_": 1} {
		rows, err := act.Read(ctx, pool, options, map[string]any{"headline_has": q})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != want {
			t.Errorf("headline has %q: %d rows, want %d", q, len(rows), want)
		}
	}

	rows, err = act.Read(ctx, pool, options, in(t, `{"id": [3, 4], "limit": 1}`))
	if err != nil || len(rows) != 1 || rows[0]["id"] != json.Number("3") {
		t.Fatalf("id in (3, 4) limit 1: %v %v", rows, err)
	}
	if h := rows[0]["headline"].(string); len([]rune(h)) != 601 || !strings.HasSuffix(h, "…") {
		t.Errorf("an outside value is cut to 600 characters and marked: %d", len([]rune(h)))
	}

	// A limit above the action's is lowered to it.
	if rows, err := act.Read(ctx, pool, options, in(t, `{"limit": 1000}`)); err != nil || len(rows) != 4 {
		t.Errorf("limit 1000: %d rows, %v", len(rows), err)
	}

	for name, input := range map[string]string{
		"an unknown filter":  `{"secret": 1}`,
		"text for a number":  `{"brief_id": "seven"}`,
		"a fraction":         `{"brief_id": 7.5}`,
		"a scalar for in":    `{"id": 3}`,
		"a limit of zero":    `{"limit": 0}`,
		"a filter with SQL":  `{"brief_id; DROP TABLE demo.option": 1}`,
		"an enum it lacks":   `{"state": ["gone"]}`,
		"a filter of briefs": `{"state": "making"}`,
	} {
		a := options
		if strings.Contains(input, "state") {
			a = get(t, c, "demo.briefs")
		}
		if _, err := act.Read(ctx, pool, a, in(t, input)); !refused(err) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
}

// A read listed by mistake over a function that writes still changes
// nothing: reads run read-only.
func TestReadChangesNothing(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION demo_api.sneaky_v1() RETURNS TABLE (id BIGINT)
		LANGUAGE sql AS $$ INSERT INTO demo.brief (vertical, images, requested_by) VALUES ('Tinnitus', 1, 'x') RETURNING id $$`); err != nil {
		t.Fatal(err)
	}
	sneaky := actions.Action{Name: "demo.sneaky", Version: 1, Kind: actions.Read, Call: "demo_api.sneaky_v1",
		Columns: []string{"id"}, Limit: 5}
	if _, err := act.Read(ctx, pool, sneaky, map[string]any{}); err == nil {
		t.Fatal("a read that writes ran")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM demo.brief`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("briefs after the read: %d %v", n, err)
	}
}

func TestCallChange(t *testing.T) {
	pool, c := setup(t)
	ctx := context.Background()
	brief := get(t, c, "demo.new_brief")
	who := act.Who{Person: "ana@example.com", Origin: "desk:step:1"}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// The app refuses (its own RAISE): the model hears why, and the
	// transaction can still record it.
	if _, err := act.Call(ctx, tx, brief, in(t, `{"vertical": "Tinnitus", "images": 40}`), who); !refused(err) || !strings.Contains(err.Error(), "at most 12") {
		t.Fatalf("40 images: %v", err)
	}
	got, err := act.Call(ctx, tx, brief, in(t, `{"vertical": "Tinnitus", "images": 6}`), who)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var by string
	if err := pool.QueryRow(ctx, `SELECT requested_by FROM demo.brief WHERE id = $1`, json.Number(got)).Scan(&by); err != nil || by != who.Person {
		t.Fatalf("brief %s for %q: %v", got, by, err)
	}

	state, row, ok, err := act.Follow(ctx, pool, brief, got)
	if err != nil || !ok || state != "making" || row["vertical"] != "Tinnitus" {
		t.Fatalf("follow: %q %v %v %v", state, row, ok, err)
	}
	if ended, _ := act.Ended(brief, state); ended {
		t.Error("making is not an end")
	}
	if _, err := pool.Exec(ctx, `UPDATE demo.brief SET state = 'ready'`); err != nil {
		t.Fatal(err)
	}
	state, _, _, _ = act.Follow(ctx, pool, brief, got)
	if ended, failed := act.Ended(brief, state); !ended || failed {
		t.Errorf("ready: ended %v failed %v", ended, failed)
	}
	if _, _, ok, err := act.Follow(ctx, pool, brief, json.RawMessage(`999`)); ok || err != nil {
		t.Errorf("a brief not there yet: %v %v", ok, err)
	}
	if link := act.Link(brief, nil, got); link != "/demo/briefs/"+string(got) {
		t.Errorf("link %q", link)
	}

	for name, input := range map[string]string{
		"a vertical not listed": `{"vertical": "Crypto", "images": 1}`,
		"no images":             `{"vertical": "Tinnitus"}`,
		"who it is for":         `{"vertical": "Tinnitus", "images": 1, "requested_by": "boss@example.com"}`,
	} {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			_, err := act.Call(ctx, tx, brief, in(t, input), who)
			return err
		})
		if !refused(err) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
}

// An ask with an object input goes in as JSON (not a JSON string), with
// its fixed kind, and one origin asks once however often it is retried.
func TestCallAsk(t *testing.T) {
	pool, c := setup(t)
	ctx := context.Background()
	pair := get(t, c, "demo.new_pair")
	who := act.Who{Person: "ana@example.com", Origin: "desk:step:9"}
	call := func(input string) (json.RawMessage, error) {
		var out json.RawMessage
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			out, err = act.Call(ctx, tx, pair, in(t, input), who)
			return err
		})
		return out, err
	}
	first, err := call(`{"input": {"options": [1, 2], "account": "acme-sc"}}`)
	if err != nil {
		t.Fatal(err)
	}
	again, err := call(`{"input": {"options": [1, 2], "account": "acme-sc"}}`)
	if err != nil || string(again) != string(first) {
		t.Fatalf("a retry asked again: %s then %s (%v)", first, again, err)
	}
	var kind, typ, origin string
	var n int
	if err := pool.QueryRow(ctx, `SELECT kind, jsonb_typeof(input), origin, jsonb_array_length(input->'options') FROM demo.request`).Scan(&kind, &typ, &origin, &n); err != nil {
		t.Fatal(err)
	}
	if kind != "new_pair" || typ != "object" || origin != who.Origin || n != 2 {
		t.Errorf("stored %q %q %q %d", kind, typ, origin, n)
	}

	for name, input := range map[string]string{
		"a field the schema lacks": `{"input": {"options": [1], "account": "a", "budget": 900}}`,
		"a missing field":          `{"input": {"options": [1]}}`,
		"text in a list of ids":    `{"input": {"options": ["one"], "account": "a"}}`,
		"the kind":                 `{"kind": "delete_all", "input": {"options": [1], "account": "a"}}`,
	} {
		if _, err := call(input); !refused(err) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
}

func TestSchema(t *testing.T) {
	c := testdb.Catalog(t)
	s := act.Schema(get(t, c, "demo.new_pair"))
	props := s["properties"].(map[string]any)
	if len(props) != 1 || props["input"] == nil {
		t.Errorf("the model gives only input (kind is fixed, the rest Desk fills): %v", props)
	}
	if s["additionalProperties"] != false {
		t.Error("the input is closed")
	}
	s = act.Schema(get(t, c, "demo.options"))
	props = s["properties"].(map[string]any)
	for _, p := range []string{"brief_id", "headline_has", "id", "limit"} {
		if props[p] == nil {
			t.Errorf("a read takes %s", p)
		}
	}
	if props["id"].(map[string]any)["type"] != "array" {
		t.Error("an in filter takes a list")
	}
	if err := actions.CheckSchema(s); err != nil {
		t.Errorf("the model's schema is one the catalog allows: %v", err)
	}
}

func TestCheck(t *testing.T) {
	c := testdb.Catalog(t)
	pair := get(t, c, "demo.new_pair")
	brief := get(t, c, "demo.new_brief")
	options := get(t, c, "demo.options")
	for _, ok := range []struct {
		a     actions.Action
		in    string
		bound []string
	}{
		{brief, `{"vertical": "Tinnitus", "images": 4}`, nil},
		{brief, `{"vertical": "Tinnitus"}`, []string{"images"}},
		{pair, `{"input": {"account": "a"}}`, []string{"input.options"}},
		{pair, `{}`, []string{"input"}},
		{options, `{}`, []string{"brief_id"}},
		{options, `{"headline_has": "x", "limit": 3}`, nil},
	} {
		if err := act.Check(ok.a, in(t, ok.in), ok.bound); err != nil {
			t.Errorf("%s %s %v: %v", ok.a.Name, ok.in, ok.bound, err)
		}
	}
	for _, bad := range []struct {
		a     actions.Action
		in    string
		bound []string
	}{
		{brief, `{"vertical": "Tinnitus"}`, nil},
		{brief, `{"vertical": "Crypto", "images": 1}`, nil},
		{pair, `{}`, []string{"input.options"}},
		{pair, `{"input": {"account": "a", "extra": 1}}`, []string{"input.options"}},
		{options, `{"brief": 1}`, nil},
	} {
		if err := act.Check(bad.a, in(t, bad.in), bad.bound); !refused(err) {
			t.Errorf("%s %s %v: %v, want a refusal", bad.a.Name, bad.in, bad.bound, err)
		}
	}
}

func TestPropertyType(t *testing.T) {
	c := testdb.Catalog(t)
	for _, tc := range []struct {
		action, prop, want string
	}{
		{"demo.new_pair", "input.options", "integer[]"},
		{"demo.new_pair", "input.account", "string"},
		{"demo.new_pair", "input", "object"},
		{"demo.new_brief", "images", "integer"},
		{"demo.options", "id", "integer[]"},
		{"demo.options", "brief_id", "integer"},
	} {
		if got, ok := act.PropertyType(get(t, c, tc.action), tc.prop); !ok || got != tc.want {
			t.Errorf("%s %s: %q %v, want %q", tc.action, tc.prop, got, ok, tc.want)
		}
	}
	for _, tc := range [][2]string{{"demo.new_pair", "kind"}, {"demo.new_pair", "requested_by"}, {"demo.new_pair", "input.nope"}, {"demo.options", "headline"}} {
		if got, ok := act.PropertyType(get(t, c, tc[0]), tc[1]); ok {
			t.Errorf("%s %s is not an input the model fills, got %q", tc[0], tc[1], got)
		}
	}
}

func TestLinkEscapes(t *testing.T) {
	a := actions.Action{Link: "/demo/r/{returned}/{name}"}
	if got := act.Link(a, map[string]any{"name": "a/b?c"}, json.RawMessage(`"x y"`)); got != "/demo/r/x%20y/a%2Fb%3Fc" {
		t.Errorf("link %q", got)
	}
}
