package sessions

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Raposa-Industries/adhunters/create/internal/library"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/create/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

func pic(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

type fakeAI struct {
	mu     sync.Mutex
	plans  []openai.PlanRequest
	images []openai.ImageRequest
	n      int
	off    string
}

func (f *fakeAI) Why() string { return f.off }

func (f *fakeAI) Plan(_ context.Context, r openai.PlanRequest) (openai.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans = append(f.plans, r)
	p := openai.Plan{Cost: 0.01}
	for i := 0; i < r.Headlines; i++ {
		p.Headlines = append(p.Headlines, "Ringing After 60? Try This Tonight "+string(rune('A'+len(f.plans)))+string(rune('a'+i)))
	}
	for i := 0; i < r.Images; i++ {
		idea := "A woman in her kitchen holding a spoon"
		if i == 1 {
			idea = "please fail this one"
		}
		p.Briefs = append(p.Briefs, openai.Brief{Angle: "Colher", Brief: idea})
	}
	return p, nil
}

func (f *fakeAI) Image(_ context.Context, r openai.ImageRequest) (openai.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, r)
	if strings.Contains(r.Brief, "fail") {
		return openai.Image{}, &openai.Error{Status: 400, Message: "a OpenAI recusou o pedido"}
	}
	f.n++
	return openai.Image{Data: pic(160+f.n, 90), MIME: "image/png", Width: 160 + f.n, Height: 90, Cost: 0.03}, nil
}

type fakeLib struct {
	saved     []string
	sets      []library.NewSet
	creatives []library.CreativeMeta
	headlines []library.NewHeadline
	failSet   bool
}

func (f *fakeLib) AddSet(_ context.Context, s library.NewSet) (library.Set, error) {
	if f.failSet {
		f.failSet = false
		return library.Set{}, &library.Error{Status: 500, Message: "down"}
	}
	f.sets = append(f.sets, s)
	return library.Set{ID: int64(40 + len(f.sets)), Name: s.Name}, nil
}

func (f *fakeLib) AddCreative(_ context.Context, m library.CreativeMeta, _ string, _ []byte) (library.Creative, error) {
	f.creatives = append(f.creatives, m)
	return library.Creative{ID: int64(100 + len(f.creatives))}, nil
}

func (f *fakeLib) Headlines(context.Context, string, int) ([]string, error) { return f.saved, nil }

func (f *fakeLib) AddHeadlines(_ context.Context, hs []library.NewHeadline) error {
	f.headlines = append(f.headlines, hs...)
	return nil
}

func drain(t *testing.T, w *Worker) {
	t.Helper()
	for i := 0; i < 100; i++ {
		ran, err := w.RunOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("the queue never emptied")
}

func setup(t *testing.T) (*Store, *Worker, *fakeAI, *fakeLib) {
	t.Helper()
	db := testdb.New(t)
	ai, lib := &fakeAI{}, &fakeLib{}
	var w *Worker
	st := New(db, &files.Dir{Root: t.TempDir()}, func() {})
	w = NewWorker(st, ai, lib, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return st, w, ai, lib
}

func itemsOf(t *testing.T, st *Store, id int64) Detail {
	t.Helper()
	d, err := st.Detail(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A whole iteration: a prompt makes pictures and headlines; one picture and
// one headline are picked and varied; the chosen ones are saved into the
// session's folder, twice into the same set.
func TestIterate(t *testing.T) {
	st, w, ai, lib := setup(t)
	ctx := context.Background()

	s, err := st.NewSession(ctx, "Colher de manhã", "tinnitus", "Tinnitus", "", "mari@example.com")
	if err != nil {
		t.Fatal(err)
	}
	again, err := st.NewSession(ctx, "colher de manhã ", "tinnitus", "Tinnitus", "", "vini@example.com")
	if err != nil || again.ID != s.ID {
		t.Fatalf("the same name in the same vertical should open the same session: %v %+v", err, again)
	}
	other, err := st.NewSession(ctx, "Colher de manhã", "memory-loss", "Memory Loss", "", "")
	if err != nil || other.ID == s.ID {
		t.Fatalf("another vertical is another session: %v", err)
	}

	// Turn 1: no pictures picked, three pictures (briefs from the text call,
	// the second one fails) and two headlines.
	t1, err := st.Send(ctx, s.ID, Send{Prompt: "older man, ringing ears, kitchen", Images: 3, Headlines: 2}, "mari@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if t1.State != "making" {
		t.Fatalf("a new turn is making, not %s", t1.State)
	}
	drain(t, w)
	if len(ai.plans) != 1 || ai.plans[0].Images != 3 || ai.plans[0].Headlines != 2 || ai.plans[0].Language != "en" || ai.plans[0].Vertical != "Tinnitus" {
		t.Fatalf("one text call with both counts, in English, for the vertical: %+v", ai.plans)
	}
	d := itemsOf(t, st, s.ID)
	var pics, heads, failed []Item
	for _, it := range d.Items {
		switch {
		case it.State == "failed":
			failed = append(failed, it)
		case it.Kind == "image":
			pics = append(pics, it)
		default:
			heads = append(heads, it)
		}
	}
	if len(pics) != 2 || len(heads) != 2 || len(failed) != 1 || failed[0].Error != "a OpenAI recusou o pedido" {
		t.Fatalf("2 pictures, 2 headlines and 1 failure, got %d %d %+v", len(pics), len(heads), failed)
	}
	if d.Turns[0].State != "done" || pics[0].ImageURL == "" || d.Session.CostUSD < 0.07 {
		t.Fatalf("the turn is done with what came out: %+v %+v", d.Turns[0], d.Session)
	}

	// Turn 2: pick one picture and one headline, ask for a change.
	if _, err := st.Send(ctx, s.ID, Send{Prompt: "make him smile", Picked: []int64{pics[0].ID, heads[0].ID}, Images: 2, Headlines: 3}, ""); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	p := ai.plans[1]
	if p.Images != 0 || p.Headlines != 3 || len(p.HeadlineExamples) != 1 || p.HeadlineExamples[0] != heads[0].Text ||
		len(p.Winners) != 1 || !p.ForPictures || len(p.Avoid) != 2 {
		t.Fatalf("headlines vary the picked one, see the picked picture, avoid the session's: %+v", p)
	}
	last := ai.images[len(ai.images)-1]
	if len(last.References) != 1 || !strings.Contains(last.Brief, "make him smile") || !strings.Contains(last.Brief, "version 2 of 2") ||
		!strings.Contains(last.Brief, heads[0].Text) {
		t.Fatalf("a variation is the picked picture changed as asked: %q (%d refs)", last.Brief, len(last.References))
	}
	d = itemsOf(t, st, s.ID)
	var varied []Item
	for _, it := range d.Items {
		if it.TurnID != nil && *it.TurnID == d.Turns[1].ID {
			varied = append(varied, it)
		}
	}
	if len(varied) != 5 || len(varied[0].FromIDs) != 2 {
		t.Fatalf("5 new items that remember where they came from: %+v", varied)
	}

	// Turn 3: only a picked picture, no words: a close variation.
	if _, err := st.Send(ctx, s.ID, Send{Picked: []int64{varied[len(varied)-1].ID}, Images: 1}, ""); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if last := ai.images[len(ai.images)-1]; !strings.Contains(last.Brief, "new version of this picture") {
		t.Fatalf("no words is a close variation: %q", last.Brief)
	}

	// Save one picture and one headline, then one more picture: one set.
	saved, err := st.Save(ctx, s.ID, []int64{pics[0].ID, heads[1].ID}, "", "mari@example.com")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if _, err := st.Save(ctx, s.ID, []int64{pics[1].ID}, "not_ai", ""); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if len(lib.sets) != 1 || lib.sets[0].Name != "Colher de manhã" || lib.sets[0].VerticalID != "tinnitus" {
		t.Fatalf("one set, named after the session, in its vertical: %+v", lib.sets)
	}
	if len(lib.creatives) != 2 || lib.creatives[0].SetID != 41 || lib.creatives[0].AILabel != "ai" || lib.creatives[1].AILabel != "not_ai" {
		t.Fatalf("both pictures in the set, with the person's label: %+v", lib.creatives)
	}
	if len(lib.headlines) != 1 || lib.headlines[0].Text != heads[1].Text {
		t.Fatalf("the headline went too: %+v", lib.headlines)
	}
	v, err := st.SaveByID(ctx, saved.ID)
	if err != nil || v.State != "done" || v.LibrarySetID == nil || *v.LibrarySetID != 41 {
		t.Fatalf("the save is done with its set: %v %+v", err, v)
	}
	it, err := st.Item(ctx, heads[1].ID)
	if err != nil || !it.Saved {
		t.Fatalf("a saved item says so: %v %+v", err, it)
	}
	if _, err := st.EditHeadline(ctx, heads[1].ID, "Another"); err == nil {
		t.Fatal("a saved headline is not edited")
	}
	if it, err := st.EditHeadline(ctx, heads[0].ID, "  Ringing   After 60  "); err != nil || it.Text != "Ringing After 60" {
		t.Fatalf("a headline is edited and cleaned: %v %+v", err, it)
	}
}

func TestRefusals(t *testing.T) {
	st, w, ai, _ := setup(t)
	ctx := context.Background()
	s, err := st.NewSession(ctx, "Sessão", "tinnitus", "Tinnitus", "", "")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := st.NewSession(ctx, "Outra", "tinnitus", "Tinnitus", "", "")
	up, err := st.AddPicture(ctx, other.ID, "upload", "", pic(40, 40))
	if err != nil {
		t.Fatal(err)
	}
	var bad BadInput
	for name, send := range map[string]Send{
		"nothing":            {Images: 1},
		"no counts":          {Prompt: "x"},
		"too many":           {Prompt: "x", Images: MaxImages + 1},
		"another's item":     {Prompt: "x", Picked: []int64{up.ID}, Images: 1},
		"picture from words": {Picked: []int64{}, Headlines: 0, Images: 2},
	} {
		if _, err := st.Send(ctx, s.ID, send, ""); !errors.As(err, &bad) {
			t.Errorf("%s: want a refusal in words, got %v", name, err)
		}
	}
	if _, err := st.Send(ctx, 999, Send{Prompt: "x", Images: 1}, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("no session: %v", err)
	}
	for _, name := range []string{"", "a/b"} {
		if _, err := st.NewSession(ctx, name, "tinnitus", "Tinnitus", "", ""); !errors.As(err, &bad) {
			t.Errorf("name %q: %v", name, err)
		}
	}
	if _, err := st.AddPicture(ctx, s.ID, "upload", "", []byte("not a picture")); !errors.As(err, &bad) {
		t.Errorf("a file that is not a picture: %v", err)
	}
	if _, err := st.Save(ctx, s.ID, []int64{up.ID}, "ai", ""); !errors.As(err, &bad) {
		t.Errorf("saving another session's item: %v", err)
	}

	// Making off: the turn fails and says why.
	ai.off = "falta OPENAI_API_KEY"
	if _, err := st.Send(ctx, s.ID, Send{Prompt: "x", Images: 1}, ""); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	d := itemsOf(t, st, s.ID)
	if d.Turns[0].State != "failed" || d.Turns[0].Error != "falta OPENAI_API_KEY" {
		t.Fatalf("a turn that could not run says why: %+v", d.Turns[0])
	}
}

// A stop in the middle: a picture that was being made fails (it may have
// been paid for), a turn waits to run again. (The fake refuses the second
// of three pictures.)
func TestRestart(t *testing.T) {
	st, w, _, _ := setup(t)
	ctx := context.Background()
	s, _ := st.NewSession(ctx, "S", "tinnitus", "Tinnitus", "", "")
	if _, err := st.Send(ctx, s.ID, Send{Prompt: "x", Images: 3}, ""); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); !ran || err != nil {
		t.Fatalf("the turn: %v %v", ran, err)
	}
	if _, err := st.db.Exec(ctx, `UPDATE create_app.work SET state = 'running' WHERE kind = 'image' AND id = (SELECT min(id) FROM create_app.work WHERE kind = 'image')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, s.ID, Send{Prompt: "y", Headlines: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(ctx, `UPDATE create_app.work SET state = 'running' WHERE kind = 'turn' AND state = 'waiting'`); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	d := itemsOf(t, st, s.ID)
	var stopped, done int
	for _, it := range d.Items {
		switch {
		case it.Error == interrupted:
			stopped++
		case it.State == "done":
			done++
		}
	}
	if stopped != 1 || done != 2 || d.Turns[0].State != "done" || d.Turns[1].State != "done" {
		t.Fatalf("one picture failed, one picture and one headline done: %d %d %+v", stopped, done, d.Turns)
	}
}

// Desk's door: the create_api functions make the same rows the page does.
func TestCreateAPI(t *testing.T) {
	st, w, _, lib := setup(t)
	ctx := context.Background()
	var sid, again int64
	if err := st.db.QueryRow(ctx, `SELECT create_api.new_session_v1('Desk', 'vision', 'Vision', 'leo@example.com', 'desk:step:1')`).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(ctx, `SELECT create_api.new_session_v1('Other', 'vision', 'Vision', '', 'desk:step:1')`).Scan(&again); err != nil || again != sid {
		t.Fatalf("an origin used before returns its session: %v %d", err, again)
	}
	var tid, tid2 int64
	if err := st.db.QueryRow(ctx, `SELECT create_api.send_turn_v1($1, 'glasses on a table', '{}', 1, 1, 'leo@example.com', 'desk:step:2')`, sid).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(ctx, `SELECT create_api.send_turn_v1($1, 'x', '{}', 1, 0, '', 'desk:step:2')`, sid).Scan(&tid2); err != nil || tid2 != tid {
		t.Fatalf("an origin used before returns its turn: %v", err)
	}
	drain(t, w)
	var state string
	var items int
	if err := st.db.QueryRow(ctx, `SELECT state FROM create_api.turn_v1 WHERE id = $1`, tid).Scan(&state); err != nil || state != "done" {
		t.Fatalf("the turn is done: %v %s", err, state)
	}
	if err := st.db.QueryRow(ctx, `SELECT count(*) FROM create_api.item_v1 WHERE session_id = $1 AND state = 'done'
		AND (kind = 'headline' OR image_url LIKE '/create/files/items/%')`, sid).Scan(&items); err != nil || items != 2 {
		t.Fatalf("two items in the view: %v %d", err, items)
	}
	var ids []int64
	if err := st.db.QueryRow(ctx, `SELECT array_agg(id) FROM create_api.item_v1 WHERE session_id = $1`, sid).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	var saveID int64
	if err := st.db.QueryRow(ctx, `SELECT create_api.save_items_v1($1, $2, 'ai', 'leo@example.com', 'desk:step:3')`, sid, ids).Scan(&saveID); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	var setID *int64
	if err := st.db.QueryRow(ctx, `SELECT state, library_set_id FROM create_api.session_save_v1 WHERE id = $1`, saveID).Scan(&state, &setID); err != nil ||
		state != "done" || setID == nil || len(lib.sets) != 1 || lib.sets[0].MadeBy != "leo@example.com" {
		t.Fatalf("the save is done in the session's set: %v %s %v %+v", err, state, setID, lib.sets)
	}
}
