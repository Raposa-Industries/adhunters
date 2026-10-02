package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/keep"
)

// The headline memory holds every team example of the vertical, then the
// session's and the library's headlines newest first, until the budget.
func TestWithMemory(t *testing.T) {
	lib, _ := LibraryFor("Tinnitus")
	r := withMemory(PlanRequest{Vertical: "Tinnitus", Avoid: []string{"new one", "old one"}, Saved: []string{"saved new", "saved old"}}, MemoryTokens)
	if len(r.Library) != len(lib.Headlines) || len(r.Avoid) != 2 || len(r.Saved) != 2 {
		t.Fatalf("library %d of %d, avoid %v, saved %v", len(r.Library), len(lib.Headlines), r.Avoid, r.Saved)
	}
	// A budget that only just holds the team's examples and one more line:
	// the newest session headline is kept, the rest left out.
	used := 0
	for _, h := range lib.Headlines {
		used += roughTokens(h)
	}
	r = withMemory(PlanRequest{Vertical: "Tinnitus", Avoid: []string{"new one", "old one"}, Saved: []string{"saved new"}}, used+roughTokens("new one"))
	if len(r.Library) != len(lib.Headlines) || len(r.Avoid) != 1 || r.Avoid[0] != "new one" || len(r.Saved) != 0 {
		t.Fatalf("trimmed: library %d, avoid %v, saved %v", len(r.Library), r.Avoid, r.Saved)
	}
	// A vertical without team examples still gets its own.
	r = withMemory(PlanRequest{Vertical: "Vision", Saved: []string{"x"}}, MemoryTokens)
	if len(r.Library) != 0 || len(r.Saved) != 1 {
		t.Fatalf("vision: %+v", r)
	}
}

func TestPlanLongMemory(t *testing.T) {
	var got map[string]any
	c, _, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, chatReplyJSON(`{"analysis":[],"headlines":["Saved line here","A brand new line for tonight"],"briefs":[]}`))
	})
	plan, err := c.Plan(context.Background(), PlanRequest{
		Vertical: "Tinnitus", Language: "en", Headlines: 2, LongMemory: true,
		HeadlineExamples: []string{"Ringing Ears After 60? Try This"}, Avoid: []string{"Session line"}, Saved: []string{"Saved line here"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Headlines) != 1 || plan.Headlines[0] != "A brand new line for tonight" {
		t.Errorf("a saved headline came back: %v", plan.Headlines)
	}
	user := got["messages"].([]any)[1].(map[string]any)["content"].(string)
	lib, _ := LibraryFor("Tinnitus")
	team := grepLines(user, "TEAM EXAMPLES", "ALREADY SHOWN", "THE TEAM'S HEADLINES SAVED", "ADDITIONAL INSTRUCTIONS")
	if n := strings.Count(team, "\n- "); n != len(lib.Headlines) {
		t.Errorf("%d team examples shown, want all %d", n, len(lib.Headlines))
	}
	for _, want := range []string{"HEADLINES TO VARY", "minimal variation", "- Session line", "THE TEAM'S HEADLINES SAVED IN THE LIBRARY", "- Saved line here"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message lacks %q", want)
		}
	}
}

type countMeter struct {
	mu    sync.Mutex
	spent map[string]float64
}

func (m *countMeter) Spent(p string, usd float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spent[p] += usd
}
func (m *countMeter) OutOfCredit(string) {}

// A compat provider is called at its own address with the chat-completions
// request, no schema, no pictures, English; its reply is kept before it is
// read and its cost counted under its id.
func TestCompatHeadlines(t *testing.T) {
	var got map[string]any
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, chatReplyJSON(`{"headlines":["Why Your Ears Ring At Night"]}`))
	}))
	t.Cleanup(srv.Close)
	m := &countMeter{spent: map[string]float64{}}
	dir := t.TempDir()
	c := NewCompat(Compat{ID: "grok", Name: "Grok", APIKey: "xai-test", BaseURL: srv.URL + "/v1", Model: "grok-test", PriceIn: 1, PriceOut: 1},
		m, keep.New(dir), quiet)
	plan, err := c.Plan(context.Background(), PlanRequest{Vertical: "Tinnitus", Language: "pt", Headlines: 1, LongMemory: true,
		Winners: []Reference{{Data: []byte("x"), MIME: "image/jpeg"}}, ForPictures: true, Images: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Headlines) != 1 || len(plan.Briefs) != 0 {
		t.Errorf("plan %+v", plan)
	}
	if path != "/v1/chat/completions" || auth != "Bearer xai-test" || got["model"] != "grok-test" {
		t.Errorf("called %s %q model %v", path, auth, got["model"])
	}
	if rf := got["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format %v", rf)
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Error("reasoning_effort sent to a compat model")
	}
	msgs := got["messages"].([]any)
	user, ok := msgs[1].(map[string]any)["content"].(string)
	if !ok || !strings.Contains(user, "in English and exactly 0 image briefs") {
		t.Errorf("user content %v", msgs[1])
	}
	if sys := msgs[0].(map[string]any)["content"].(string); !strings.Contains(sys, "BLOCKED WORDS") || !strings.Contains(sys, `"headlines"`) {
		t.Error("system message lacks the rules or the shape")
	}
	if m.spent["grok"] != 3000.0/1e6 || m.spent[Provider] != 0 {
		t.Errorf("spent %v", m.spent)
	}
	plans := kept(t, dir, "-plan.json")
	if len(plans) != 1 {
		t.Fatalf("kept %v", plans)
	}
	raw, _ := os.ReadFile(plans[0])
	if !bytes.Contains(raw, []byte(`"provider": "grok"`)) || !bytes.Contains(raw, []byte("chatcmpl-1")) {
		t.Errorf("kept %s", raw)
	}
}

func TestCompatErrorsNameTheProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	t.Cleanup(srv.Close)
	c := NewCompat(Compat{ID: "deepseek", Name: "DeepSeek", APIKey: "k", BaseURL: srv.URL, Model: "m"}, nil, keep.New(t.TempDir()), quiet)
	c.retryBase = time.Millisecond
	_, err := c.Plan(context.Background(), PlanRequest{Vertical: "Tinnitus", Headlines: 1})
	var e *Error
	if !asError(err, &e) || e.Message != "chave da DeepSeek recusada (401)" {
		t.Fatalf("err %v", err)
	}
}

func asError(err error, e **Error) bool {
	x, ok := err.(*Error)
	if ok {
		*e = x
	}
	return ok
}

func TestCompatFromEnv(t *testing.T) {
	t.Setenv("CREATE_GROK_API_KEY", "")
	t.Setenv("CREATE_DEEPSEEK_API_KEY", "ds")
	t.Setenv("CREATE_DEEPSEEK_MODEL", "deepseek-x")
	t.Setenv("CREATE_KIMI_API_KEY", "")
	got, err := CompatFromEnv()
	if err != nil || len(got) != 1 || got[0].ID != "deepseek" || got[0].Model != "deepseek-x" || got[0].BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("%+v %v", got, err)
	}
	t.Setenv("CREATE_DEEPSEEK_PRICE_IN", "lots")
	if _, err := CompatFromEnv(); err == nil {
		t.Error("a bad price was taken")
	}
}
