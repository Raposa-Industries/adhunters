package sessions

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Raposa-Industries/adhunters/create/internal/openai"
)

func turnItems(t *testing.T, st *Store, sessionID, turnID int64) (pics, heads []Item) {
	t.Helper()
	for _, it := range itemsOf(t, st, sessionID).Items {
		if it.TurnID == nil || *it.TurnID != turnID {
			continue
		}
		if it.Kind == "image" {
			pics = append(pics, it)
		} else {
			heads = append(heads, it)
		}
	}
	return pics, heads
}

// Parar: pictures not started are not made, the turn stops waiting for the
// one being made, and that one still shows up when it arrives.
func TestInterrupt(t *testing.T) {
	st, w, ai, _ := setup(t)
	ctx := context.Background()
	s, _ := st.NewSession(ctx, "S", "tinnitus", "Tinnitus", "", "")
	tr, err := st.Send(ctx, s.ID, Send{Prompt: "a man, a spoon", Images: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); !ran || err != nil {
		t.Fatalf("the turn: %v %v", ran, err)
	}
	// One picture is being made when the person presses Parar.
	var running int64
	if err := st.db.QueryRow(ctx, `UPDATE create_app.work SET state = 'running', started_at = now()
		WHERE id = (SELECT min(id) FROM create_app.work WHERE kind = 'image') RETURNING id`).Scan(&running); err != nil {
		t.Fatal(err)
	}
	got, err := st.Interrupt(ctx, tr.ID)
	if err != nil || got.InterruptedAt == nil || got.State != "failed" {
		t.Fatalf("interrupted: %v %+v", err, got)
	}
	var bad BadInput
	if _, err := st.Interrupt(ctx, tr.ID); !errors.As(err, &bad) {
		t.Fatalf("twice: %v", err)
	}
	pics, _ := turnItems(t, st, s.ID, tr.ID)
	var cancelled int
	for _, p := range pics {
		if p.State == "failed" && p.Error == Interrupted {
			cancelled++
		}
	}
	if cancelled != 2 {
		t.Fatalf("the two not started are not made: %+v", pics)
	}
	// The running one arrives after all: it is kept and the turn is done.
	if _, err := st.db.Exec(ctx, `UPDATE create_app.work SET state = 'waiting' WHERE id = $1`, running); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if len(ai.images) != 1 {
		t.Fatalf("only the running picture was made: %d", len(ai.images))
	}
	got, _ = st.Turn(ctx, tr.ID)
	pics, _ = turnItems(t, st, s.ID, tr.ID)
	done := 0
	for _, p := range pics {
		if p.State == "done" {
			done++
		}
	}
	if got.State != "done" || got.InterruptedAt == nil || done != 1 {
		t.Fatalf("after it arrived: %+v, %d done", got, done)
	}
}

// Parar while the text call runs: its headlines are kept, no picture is
// queued.
func TestInterruptDuringText(t *testing.T) {
	st, w, ai, _ := setup(t)
	ctx := context.Background()
	s, _ := st.NewSession(ctx, "S", "tinnitus", "Tinnitus", "", "")
	tr, _ := st.Send(ctx, s.ID, Send{Prompt: "x", Images: 2, Headlines: 2}, "")
	if _, err := st.db.Exec(ctx, `UPDATE create_app.work SET state = 'running' WHERE turn_id = $1`, tr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Interrupt(ctx, tr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(ctx, `UPDATE create_app.work SET state = 'waiting' WHERE turn_id = $1`, tr.ID); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	pics, heads := turnItems(t, st, s.ID, tr.ID)
	got, _ := st.Turn(ctx, tr.ID)
	if len(ai.plans) != 1 || len(pics) != 0 || len(heads) != 2 || got.State != "done" {
		t.Fatalf("headlines kept, no pictures: %d pictures, %d headlines, %+v", len(pics), len(heads), got)
	}
}

// Tentar de novo makes one failed picture again, alone.
func TestRetry(t *testing.T) {
	st, w, ai, _ := setup(t)
	ctx := context.Background()
	s, _ := st.NewSession(ctx, "S", "tinnitus", "Tinnitus", "", "")
	tr, _ := st.Send(ctx, s.ID, Send{Prompt: "x", Images: 3}, "")
	drain(t, w)
	pics, _ := turnItems(t, st, s.ID, tr.ID)
	var failed, ok Item
	for _, p := range pics {
		if p.State == "failed" {
			failed = p
		} else {
			ok = p
		}
	}
	if failed.ID == 0 {
		t.Fatal("the fake should fail one")
	}
	var bad BadInput
	if _, err := st.Retry(ctx, ok.ID); !errors.As(err, &bad) {
		t.Fatalf("a done picture: %v", err)
	}
	// The rate limit has passed: this time it works.
	if _, err := st.db.Exec(ctx, `UPDATE create_app.item SET brief = 'a woman, a spoon' WHERE id = $1`, failed.ID); err != nil {
		t.Fatal(err)
	}
	before := len(ai.images)
	it, err := st.Retry(ctx, failed.ID)
	if err != nil || it.State != "waiting" {
		t.Fatalf("retry: %v %+v", err, it)
	}
	if got, _ := st.Turn(ctx, tr.ID); got.State != "making" {
		t.Fatalf("the turn makes again: %s", got.State)
	}
	drain(t, w)
	it, _ = st.Item(ctx, failed.ID)
	got, _ := st.Turn(ctx, tr.ID)
	if it.State != "done" || len(ai.images) != before+1 || got.State != "done" {
		t.Fatalf("retried: %+v, %d calls, %+v", it, len(ai.images)-before, got)
	}
}

// The session's platform goes with its saves; a turn's size goes with its
// pictures.
func TestPlatformAndSize(t *testing.T) {
	st, w, ai, lib := setup(t)
	ctx := context.Background()
	s, err := st.NewSession(ctx, "NB", "tinnitus", "Tinnitus", "newsbreak", "")
	if err != nil || s.Platform != "newsbreak" {
		t.Fatalf("new: %v %+v", err, s)
	}
	again, _ := st.NewSession(ctx, "NB", "tinnitus", "Tinnitus", "taboola", "")
	if again.ID != s.ID || again.Platform != "newsbreak" {
		t.Fatalf("opened again keeps its platform: %+v", again)
	}
	if d, _ := st.NewSession(ctx, "Default", "tinnitus", "Tinnitus", "", ""); d.Platform != "taboola" {
		t.Fatalf("default platform %q", d.Platform)
	}
	var bad BadInput
	if _, err := st.NewSession(ctx, "X", "tinnitus", "Tinnitus", "outbrain", ""); !errors.As(err, &bad) {
		t.Fatalf("unknown platform: %v", err)
	}
	if _, err := st.Send(ctx, s.ID, Send{Prompt: "x", Images: 1, Size: "square"}, ""); !errors.As(err, &bad) {
		t.Fatalf("unknown size: %v", err)
	}
	tr, err := st.Send(ctx, s.ID, Send{Prompt: "x", Images: 1, Size: "newsbreak"}, "")
	if err != nil || tr.Size != "newsbreak" {
		t.Fatalf("send: %v %+v", err, tr)
	}
	drain(t, w)
	if r := ai.images[0]; r.Size.ID != "newsbreak" || r.Size.Width != 1504 || r.Size.Height != 786 {
		t.Fatalf("size asked %+v", r.Size)
	}
	pics, _ := turnItems(t, st, s.ID, tr.ID)
	if _, err := st.Save(ctx, s.ID, []int64{pics[0].ID}, "ai", ""); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if len(lib.sets) != 1 || lib.sets[0].Platform != "newsbreak" || len(lib.creatives) != 1 || lib.creatives[0].Platform != "newsbreak" {
		t.Fatalf("saved with its platform: %+v %+v", lib.sets, lib.creatives)
	}
}

type fakeHeadliner struct {
	mu   sync.Mutex
	reqs []openai.PlanRequest
	fail bool
}

func (f *fakeHeadliner) Plan(_ context.Context, r openai.PlanRequest) (openai.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	if f.fail {
		return openai.Plan{}, &openai.Error{Status: 200, Message: "Grok devolveu um plano ilegível"}
	}
	p := openai.Plan{Cost: 0.001}
	for i := 0; i < r.Headlines; i++ {
		p.Headlines = append(p.Headlines, "Grok Line "+string(rune('a'+len(f.reqs)))+string(rune('a'+i)))
	}
	return p, nil
}

// Headlines from another model (decision 0024) learn from the same memory;
// pictures and their briefs stay OpenAI's, and a failing headline model
// never fails the pictures.
func TestOtherHeadlineModel(t *testing.T) {
	st, w, ai, lib := setup(t)
	ctx := context.Background()
	grok := &fakeHeadliner{}
	w.Headliners = map[string]Headliner{"grok": grok}
	lib.saved = []string{"Saved In The Library"}
	s, _ := st.NewSession(ctx, "S", "tinnitus", "Tinnitus", "", "")
	tr, err := st.Send(ctx, s.ID, Send{Prompt: "x", Images: 2, Headlines: 2, Model: "grok"}, "")
	if err != nil || tr.HeadlineModel != "grok" {
		t.Fatalf("send: %v %+v", err, tr)
	}
	drain(t, w)
	if len(grok.reqs) != 1 || grok.reqs[0].Headlines != 2 || grok.reqs[0].Images != 0 || !grok.reqs[0].LongMemory ||
		len(grok.reqs[0].Saved) != 1 {
		t.Fatalf("grok asked %+v", grok.reqs)
	}
	if len(ai.plans) != 1 || ai.plans[0].Headlines != 0 || ai.plans[0].Images != 2 {
		t.Fatalf("openai asked %+v", ai.plans)
	}
	pics, heads := turnItems(t, st, s.ID, tr.ID)
	if len(heads) != 2 || !strings.HasPrefix(heads[0].Text, "Grok Line") || len(pics) != 2 {
		t.Fatalf("items %+v %+v", heads, pics)
	}

	// Grok fails: the pictures are made, the turn says what happened.
	grok.fail = true
	tr, _ = st.Send(ctx, s.ID, Send{Prompt: "y", Images: 1, Headlines: 2, Model: "grok"}, "")
	drain(t, w)
	got, _ := st.Turn(ctx, tr.ID)
	pics, _ = turnItems(t, st, s.ID, tr.ID)
	if got.State != "done" || !strings.Contains(got.Error, "headlines: Grok") || len(pics) != 1 {
		t.Fatalf("after grok failed: %+v %+v", got, pics)
	}

	// A model that is not on fails the turn with words for the person.
	tr, _ = st.Send(ctx, s.ID, Send{Prompt: "z", Headlines: 1, Model: "kimi"}, "")
	drain(t, w)
	if got, _ := st.Turn(ctx, tr.ID); got.State != "failed" || !strings.Contains(got.Error, "kimi") {
		t.Fatalf("model off: %+v", got)
	}

	// OpenAI's headlines get the memory too.
	tr, _ = st.Send(ctx, s.ID, Send{Prompt: "w", Headlines: 1}, "")
	drain(t, w)
	last := ai.plans[len(ai.plans)-1]
	if !last.LongMemory || len(last.Saved) != 1 || len(last.Avoid) != 2 {
		t.Fatalf("openai memory: %+v", last)
	}
}
