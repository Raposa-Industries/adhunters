package briefs

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
	p := openai.Plan{Analysis: []openai.Aspect{}, Headlines: []string{}, Briefs: []openai.Brief{}, Cost: 0.01}
	if len(r.Winners) > 0 && len(r.Analysis) == 0 {
		p.Analysis = []openai.Aspect{{Aspect: "Sujeito", Fixed: "mulher de 60", Variable: "cabelo"}}
	}
	for i := 0; i < r.Headlines; i++ {
		p.Headlines = append(p.Headlines, "Ringing After 60? Try This Tonight "+string(rune('A'+len(f.plans)))+string(rune('a'+i)))
	}
	for i := 0; i < r.Images; i++ {
		idea := "A woman in her kitchen holding a spoon"
		if i == 1 {
			idea = "please fail this one"
		}
		p.Briefs = append(p.Briefs, openai.Brief{Angle: "Colher", Brief: idea + " " + string(rune('a'+len(f.plans)))})
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
	sets      []library.NewSet
	creatives []library.CreativeMeta
	headlines []library.NewHeadline
	failSet   bool
}

func (f *fakeLib) File(_ context.Context, id string) ([]byte, error) {
	if id == "404" {
		return nil, library.ErrNotFound
	}
	return pic(50, 50), nil
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
	return library.Creative{ID: int64(len(f.creatives))}, nil
}

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
	db := testdb.New(t)
	ctx := context.Background()
	// Tracks' published views, as far as Create reads them.
	for _, q := range []string{
		`CREATE SCHEMA tracks_api`,
		`CREATE TABLE tracks_api.creative_v1 (id INTEGER, image_url TEXT)`,
		`CREATE TABLE tracks_api.ad_v1 (id INTEGER, creative_id INTEGER, headline TEXT)`,
		`INSERT INTO tracks_api.creative_v1 VALUES (3, 'https://images.example/3.png'), (4, NULL)`,
		`INSERT INTO tracks_api.ad_v1 VALUES (7, 3, 'Doctors Stunned by This Ringing Trick'), (8, 4, 'No picture')`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	ai, lib := &fakeAI{}, &fakeLib{}
	var w *Worker
	st := New(db, &files.Dir{Root: t.TempDir()}, func() { w.Kick() })
	w = NewWorker(st, ai, lib, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.Fetch = func(_ context.Context, url string) ([]byte, error) {
		if url != "https://images.example/3.png" {
			return nil, errors.New("unexpected " + url)
		}
		return pic(120, 80), nil
	}
	return st, w, ai, lib
}

func ptr[T any](v T) *T { return &v }

func TestBriefFromPage(t *testing.T) {
	st, w, ai, lib := setup(t)
	ctx := context.Background()

	b, err := st.NewBrief(ctx, Input{VerticalID: ptr("tinnitus"), VerticalName: ptr("Tinnitus"), Images: ptr(2), Headlines: ptr(3),
		Angles: ptr([]string{"Colher", " colher ", "Garrafa"}), OwnHeadlines: ptr([]string{"Ring​ing ears?"})}, "mari")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != "draft" || strings.Join(b.Angles, ",") != "Colher,Garrafa" || b.OwnHeadlines[0] != "Ringing ears?" {
		t.Fatalf("new brief: %+v", b)
	}
	if _, err := st.Read(ctx, b.ID); err == nil {
		t.Error("read with no references")
	}
	if _, err := st.AddUpload(ctx, b.ID, []byte("not a picture")); err == nil {
		t.Error("text taken as a picture")
	}
	if _, err := st.AddUpload(ctx, b.ID, pic(100, 60)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"7", "8", "7"} {
		if _, err := st.AddReference(ctx, b.ID, RefSpyAd, id); err != nil {
			t.Fatal(err)
		}
	}

	if b, err = st.Read(ctx, b.ID); err != nil || b.State != "reading" {
		t.Fatalf("read: %v %+v", err, b)
	}
	drain(t, w)
	d, err := st.Detail(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Brief.State != "draft" || len(d.Brief.Analysis) != 1 {
		t.Fatalf("after reading: %+v", d.Brief)
	}
	if len(d.References) != 3 || d.References[1].State != "kept" || d.References[2].State != "failed" {
		t.Fatalf("references: %+v", d.References)
	}
	if got := ai.plans[0]; len(got.Winners) != 2 || got.Images != 0 || strings.Join(got.HeadlineExamples, "|") != "Ringing ears?|Doctors Stunned by This Ringing Trick" {
		t.Fatalf("reading asked: %+v", got)
	}

	// The person edits the analysis, then makes the first round.
	if _, err := st.Change(ctx, b.ID, Input{Analysis: ptr([]openai.Aspect{{Aspect: "Sujeito", Fixed: "homem de 65", Variable: "roupa"}})}); err != nil {
		t.Fatal(err)
	}
	if b, err = st.Make(ctx, b.ID, nil); err != nil || b.State != "making" || b.Rounds != 1 {
		t.Fatalf("make: %v %+v", err, b)
	}
	if _, err := st.Make(ctx, b.ID, nil); err == nil {
		t.Error("a second round while the first is being planned")
	}
	drain(t, w)
	if got := ai.plans[1]; got.Analysis[0].Fixed != "homem de 65" || got.Images != 2 || got.Headlines != 3 || strings.Join(got.Angles, ",") != "Colher,Garrafa" {
		t.Fatalf("plan asked: %+v", got)
	}
	d, _ = st.Detail(ctx, b.ID)
	if d.Brief.State != "ready" || len(d.Options) != 5 {
		t.Fatalf("after the round: %s, %d options", d.Brief.State, len(d.Options))
	}
	var done, failed, heads []Option
	for _, o := range d.Options {
		switch {
		case o.Kind == "headline":
			heads = append(heads, o)
		case o.State == "done":
			done = append(done, o)
		case o.State == "failed":
			failed = append(failed, o)
		}
	}
	if len(done) != 1 || len(failed) != 1 || len(heads) != 3 || done[0].URL == "" || failed[0].Error != "a OpenAI recusou o pedido" {
		t.Fatalf("options: %+v", d.Options)
	}
	if d.Brief.CostUSD < 0.049 || d.Brief.Analysis[0].Fixed != "homem de 65" {
		t.Errorf("brief after the round: %+v", d.Brief)
	}

	// Again, with a note.
	again, err := st.Again(ctx, done[0].ID, "make her smile")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	last := ai.images[len(ai.images)-1]
	if len(last.References) != 1 || !strings.Contains(last.Brief, "make her smile") {
		t.Fatalf("again asked: %+v", last.Brief)
	}
	if again, _ = st.Option(ctx, again.ID); again.State != "done" || *again.ParentID != done[0].ID {
		t.Fatalf("again: %+v", again)
	}

	// Three more, new angle.
	if _, err := st.Make(ctx, b.ID, &Round{Images: 3, NewAngle: true}); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if got := ai.plans[2]; !got.NewAngle || got.Images != 3 || got.Headlines != 0 || len(got.Avoid) == 0 || strings.Join(got.AvoidAngles, ",") != "Colher" {
		t.Fatalf("more asked: %+v", got)
	}

	// Choose and save.
	if _, err := st.MarkOption(ctx, failed[0].ID, Mark{Chosen: ptr(true)}); err == nil {
		t.Error("chose a failed picture")
	}
	if o, err := st.MarkOption(ctx, heads[0].ID, Mark{Text: ptr("Sharper memory after 60"), Starred: ptr(true)}); err != nil || o.Text != "Sharper memory after 60" || !o.Starred || len(o.Warnings) == 0 {
		t.Fatalf("edit headline: %v %+v", err, o)
	}
	if _, err := st.Save(ctx, b.ID, []int64{done[0].ID, failed[0].ID}, "x", "ai", "mari"); err == nil {
		t.Error("saved a failed picture")
	}
	if _, err := st.Save(ctx, b.ID, []int64{done[0].ID}, "x", "", "mari"); err == nil {
		t.Error("saved without an AI answer")
	}
	lib.failSet = true
	sv, err := st.Save(ctx, b.ID, []int64{done[0].ID, again.ID, heads[0].ID}, "", "ai", "mari")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if sv, _ = st.SaveByID(ctx, sv.ID); sv.State != "failed" {
		t.Fatalf("save with the library down: %+v", sv)
	}
	sv, err = st.Save(ctx, b.ID, []int64{done[0].ID, again.ID, heads[0].ID}, "", "ai", "mari")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if sv, _ = st.SaveByID(ctx, sv.ID); sv.State != "done" || sv.LibrarySetID == nil || *sv.LibrarySetID != 41 {
		t.Fatalf("save: %+v", sv)
	}
	if len(lib.creatives) != 2 || lib.creatives[0].Origin != "create" || lib.creatives[0].AILabel != "ai" || lib.creatives[0].VerticalID != "tinnitus" ||
		len(lib.headlines) != 1 || lib.headlines[0].Text != "Sharper memory after 60" || !strings.HasPrefix(lib.sets[0].Name, "Tinnitus ") {
		t.Fatalf("library got: %+v %+v %+v", lib.sets, lib.creatives, lib.headlines)
	}

	list, err := st.Briefs(ctx, 0, 10)
	if err != nil || len(list) != 1 || list[0].Options == 0 {
		t.Fatalf("list: %v %+v", err, list)
	}
	rc, mt, err := st.OptionFile(ctx, done[0].ID)
	if err != nil || mt != "image/png" {
		t.Fatalf("option file: %v %s", err, mt)
	}
	_ = rc.Close()
}

func TestCreateAPI(t *testing.T) {
	st, w, ai, lib := setup(t)
	ctx := context.Background()
	input := `{"vertical_id":"memory-loss","vertical_name":"Memory Loss","images":1,"headlines":2,
		"references":[{"kind":"library_creative","id":"12"},{"kind":"library_creative","id":"404"}]}`
	var id, again int64
	if err := st.db.QueryRow(ctx, `SELECT create_api.new_brief_v1($1, 'desk', 'desk:turn-1')`, input).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(ctx, `SELECT create_api.new_brief_v1($1, 'desk', 'desk:turn-1')`, input).Scan(&again); err != nil || again != id {
		t.Fatalf("asked twice: %d %d %v", id, again, err)
	}
	if _, err := st.db.Exec(ctx, `SELECT create_api.new_brief_v1('{"images":0,"headlines":0}', 'desk', '')`); err == nil {
		t.Error("a brief asking for nothing")
	}
	drain(t, w)
	var state string
	if err := st.db.QueryRow(ctx, `SELECT state FROM create_api.brief_v1 WHERE id = $1`, id).Scan(&state); err != nil || state != "ready" {
		t.Fatalf("brief_v1: %s %v", state, err)
	}
	if len(ai.plans) != 1 || len(ai.plans[0].Winners) != 1 {
		t.Fatalf("plan: %+v", ai.plans)
	}
	var ids []int64
	rows, _ := st.db.Query(ctx, `SELECT id FROM create_api.option_v1 WHERE brief_id = $1 AND (image_url IS NOT NULL OR kind = 'headline') ORDER BY id`, id)
	for rows.Next() {
		var x int64
		_ = rows.Scan(&x)
		ids = append(ids, x)
	}
	rows.Close()
	if len(ids) != 3 {
		t.Fatalf("options: %v", ids)
	}
	var saveID int64
	if err := st.db.QueryRow(ctx, `SELECT create_api.save_set_v1($1, $2, 'Desk set', 'desk', 'desk:save-1')`, id, ids).Scan(&saveID); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	var setID *int64
	if err := st.db.QueryRow(ctx, `SELECT state, library_set_id FROM create_api.save_v1 WHERE id = $1`, saveID).Scan(&state, &setID); err != nil ||
		state != "done" || setID == nil {
		t.Fatalf("save_v1: %s %v %v", state, setID, err)
	}
	if lib.sets[0].Name != "Desk set" || lib.creatives[0].AILabel != "ai" || len(lib.headlines) != 2 {
		t.Fatalf("library: %+v %+v", lib.sets, lib.creatives)
	}
}

func TestOffAndRestart(t *testing.T) {
	st, w, ai, _ := setup(t)
	ctx := context.Background()
	ai.off = "geração desligada"
	b, _ := st.NewBrief(ctx, Input{Images: ptr(1), Headlines: ptr(0)}, "vini")
	if _, err := st.Make(ctx, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if b, _ = st.Brief(ctx, b.ID); b.State != "failed" || b.Error != "geração desligada" {
		t.Fatalf("with OpenAI off: %+v", b)
	}

	// A picture left running by a stop fails at the next start; a plan runs
	// again.
	ai.off = ""
	if _, err := st.Make(ctx, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	var optID int64
	if err := st.db.QueryRow(ctx, `INSERT INTO create_app.option (brief_id, round, kind, idea, state) VALUES ($1, 2, 'image', 'x', 'making') RETURNING id`, b.ID).Scan(&optID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(ctx, `INSERT INTO create_app.job (brief_id, kind, option_id, state) VALUES ($1, 'image', $2, 'running')`, b.ID, optID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(ctx, `INSERT INTO create_app.job (brief_id, kind, input, state) VALUES ($1, 'plan', '{"round":3,"images":1,"headlines":0}', 'running')`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.Option(ctx, optID); o.State != "failed" {
		t.Fatalf("stuck picture: %+v", o)
	}
	before := len(ai.plans)
	drain(t, w)
	if len(ai.plans) != before+1 {
		t.Error("the stuck plan did not run again")
	}
	if b, _ = st.Brief(ctx, b.ID); b.State != "ready" {
		t.Errorf("state: %s %s", b.State, b.Error)
	}
}
