package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/create/internal/keep"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// meter counts what the client reports.
type meter struct {
	mu     sync.Mutex
	spent  []float64
	credit int
}

func (m *meter) Spent(p string, usd float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p != Provider {
		panic(p)
	}
	m.spent = append(m.spent, usd)
}

func (m *meter) OutOfCredit(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.credit++
}

func settings(url string) Settings {
	return Settings{
		APIKey: "sk-test", BaseURL: url,
		ImageModel: "gpt-image-2.5-flare", ImageQuality: "medium",
		TextModel: "gpt-5-mini", TextReasoning: "low",
		ImagePriceIn: 10, ImagePriceOut: 30, TextPriceIn: 0.25, TextPriceOut: 2,
	}
}

func newClient(t *testing.T, h http.HandlerFunc) (*Client, *meter, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m := &meter{}
	dir := t.TempDir()
	c := New(settings(srv.URL), m, keep.New(dir), quiet)
	c.retryBase = time.Millisecond
	return c, m, dir
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{200, 10, 10, 255})
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageReplyJSON(pic []byte, usage string) string {
	u := ""
	if usage != "" {
		u = `,"usage":` + usage
	}
	return fmt.Sprintf(`{"created":1,"data":[{"b64_json":%q}]%s}`, base64.StdEncoding.EncodeToString(pic), u)
}

const usage = `{"input_tokens":100,"output_tokens":400,"total_tokens":500}`

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func kept(t *testing.T, dir, suffix string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, suffix) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestGenerationsWithoutReferences(t *testing.T) {
	pic := testJPEG(t, 32, 18)
	var got map[string]any
	c, m, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("%s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content type %q", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, imageReplyJSON(pic, usage))
	})
	img, err := c.Image(context.Background(), ImageRequest{Brief: "A woman holding a jar"})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"model": "gpt-image-2.5-flare", "n": 1.0, "size": "1600x896", "quality": "medium",
		"output_format": "jpeg", "output_compression": 90.0,
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	prompt, _ := got["prompt"].(string)
	if !strings.HasPrefix(prompt, singleFrameClause) || !strings.HasSuffix(prompt, "BRIEF:\nA woman holding a jar") {
		t.Errorf("prompt:\n%s", prompt)
	}
	if !bytes.Equal(img.Data, pic) || img.MIME != "image/jpeg" || img.Width != 32 || img.Height != 18 {
		t.Errorf("image %s %dx%d", img.MIME, img.Width, img.Height)
	}
	want := 100*10/1e6 + 400*30/1e6
	if !near(img.Cost, want) || len(m.spent) != 1 || !near(m.spent[0], want) {
		t.Errorf("cost %v spent %v, want %v once", img.Cost, m.spent, want)
	}
	if jpgs, sides := kept(t, dir, ".jpg"), kept(t, dir, ".json"); len(jpgs) != 1 || len(sides) != 1 {
		t.Fatalf("kept %v %v", jpgs, sides)
	}
	side, _ := os.ReadFile(kept(t, dir, ".json")[0])
	var meta map[string]any
	if err := json.Unmarshal(side, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["brief"] != "A woman holding a jar" || meta["model"] != "gpt-image-2.5-flare" ||
		meta["quality"] != "medium" || meta["references"] != 0.0 || meta["cost_usd"] == nil || meta["usage"] == nil {
		t.Errorf("sidecar: %s", side)
	}
	if bytes.Contains(side, []byte(base64.StdEncoding.EncodeToString(pic))) {
		t.Error("sidecar repeats the picture")
	}
	if img.Kept != kept(t, dir, ".jpg")[0] {
		t.Errorf("kept path %q", img.Kept)
	}
}

func TestEditsWithReferencesSendsTypedParts(t *testing.T) {
	pic := testJPEG(t, 16, 9)
	ref1, ref2 := testJPEG(t, 8, 8), testPNG(t)
	c, _, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Errorf("path %s", r.URL.Path)
		}
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "multipart/form-data" {
			t.Fatalf("content type %q", r.Header.Get("Content-Type"))
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string]string{}
		var parts []string
		var datas [][]byte
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(p)
			if p.FormName() == "image[]" {
				parts = append(parts, p.Header.Get("Content-Type"))
				datas = append(datas, b)
				continue
			}
			fields[p.FormName()] = string(b)
		}
		if len(parts) != 2 || parts[0] != "image/jpeg" || parts[1] != "image/png" {
			t.Errorf("image parts %v", parts)
		}
		if len(datas) == 2 && (!bytes.Equal(datas[0], ref1) || !bytes.Equal(datas[1], ref2)) {
			t.Error("parts out of order")
		}
		for k, want := range map[string]string{
			"model": "gpt-image-2.5-flare", "n": "1", "size": "1600x896", "quality": "high",
			"output_format": "jpeg", "output_compression": "90",
		} {
			if fields[k] != want {
				t.Errorf("%s = %q", k, fields[k])
			}
		}
		if !strings.HasPrefix(fields["prompt"], "2 IMAGES ATTACHED") || !strings.HasSuffix(fields["prompt"], "BRIEF:\nput the jar on a kitchen table") {
			t.Errorf("prompt:\n%s", fields["prompt"])
		}
		io.WriteString(w, imageReplyJSON(pic, usage))
	})
	_, err := c.Image(context.Background(), ImageRequest{
		Brief: "put the jar on a kitchen table", Quality: "high",
		References: []Reference{{Data: ref1, MIME: "image/jpeg"}, {Data: ref2, MIME: "image/png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetries429ThenSucceeds(t *testing.T) {
	pic := testJPEG(t, 16, 9)
	var calls atomic.Int32
	c, m, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)
			return
		}
		io.WriteString(w, imageReplyJSON(pic, usage))
	})
	if _, err := c.Image(context.Background(), ImageRequest{Brief: "x"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(m.spent) != 1 || m.credit != 0 {
		t.Errorf("calls %d spent %v credit %d", calls.Load(), m.spent, m.credit)
	}
}

func TestGivesUpAfterThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	c, m, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	})
	_, err := c.Image(context.Background(), ImageRequest{Brief: "x"})
	var e *Error
	if !errors.As(err, &e) || e.Status != 503 || calls.Load() != 3 || len(m.spent) != 0 {
		t.Fatalf("err %v calls %d spent %v", err, calls.Load(), m.spent)
	}
}

func TestInsufficientQuotaIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	c, m, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`)
	})
	_, err := c.Image(context.Background(), ImageRequest{Brief: "x"})
	if !IsOutOfCredit(err) || !strings.Contains(err.Error(), OutOfCreditMessage) {
		t.Fatalf("err %v", err)
	}
	if calls.Load() != 1 || m.credit != 1 || len(m.spent) != 0 {
		t.Errorf("calls %d credit %d spent %v", calls.Load(), m.credit, m.spent)
	}
	if len(kept(t, dir, "")) != 0 {
		t.Error("kept something for a refusal")
	}
}

func TestCondensedErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"error":{"message":"Incorrect API key"}}`, "chave da OpenAI recusada (401)"},
		{400, `{"error":{"message":"Your request was rejected","code":"moderation_blocked"}}`, "pedido bloqueado pela moderação da OpenAI (400)"},
		{400, `{"error":{"message":"Invalid size.\nmore"}}`, "Invalid size. (400)"},
		{400, `not json`, "HTTP 400"},
	} {
		c, _, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		_, err := c.Image(context.Background(), ImageRequest{Brief: "x"})
		var e *Error
		if !errors.As(err, &e) || e.Message != tc.want {
			t.Errorf("%d %s: got %v, want %q", tc.status, tc.body, err, tc.want)
		}
	}
}

func TestCostFallsBackToMeasuredTable(t *testing.T) {
	pic := testJPEG(t, 16, 9)
	c, m, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, imageReplyJSON(pic, ""))
	})
	img, err := c.Image(context.Background(), ImageRequest{Brief: "x", Quality: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if want := 155 * 30 / 1e6; !near(img.Cost, want) || !near(m.spent[0], want) {
		t.Errorf("cost %v, want %v", img.Cost, want)
	}
	side, _ := os.ReadFile(kept(t, dir, ".json")[0])
	if !bytes.Contains(side, []byte(`"cost_estimated": true`)) {
		t.Errorf("sidecar does not say estimated: %s", side)
	}
	for q, tokens := range map[string]int{"low": 155, "medium": 338, "high": 1351, "weird": 1351} {
		if got := c.EstimateImageCost(q); !near(got, float64(tokens)*30/1e6) {
			t.Errorf("%s: %v", q, got)
		}
	}
}

func TestKeepFailureFailsTheCall(t *testing.T) {
	pic := testJPEG(t, 16, 9)
	c, m, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, imageReplyJSON(pic, usage))
	})
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c.keep = keep.New(blocked) // a file where the folder should be
	_, err := c.Image(context.Background(), ImageRequest{Brief: "x"})
	if !errors.Is(err, ErrKeep) {
		t.Fatalf("err %v", err)
	}
	if len(m.spent) != 1 {
		t.Error("a paid reply that could not be kept was not counted")
	}
}

func TestNoKeyMakesNoCall(t *testing.T) {
	c := New(Settings{}, nil, keep.New(t.TempDir()), quiet)
	if c.Available() || c.Why() == "" {
		t.Fatal("available without a key")
	}
	if _, err := c.Image(context.Background(), ImageRequest{Brief: "x"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err %v", err)
	}
}

func chatReplyJSON(content string) string {
	b, _ := json.Marshal(content)
	return `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":` + string(b) +
		`,"refusal":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":2000,"completion_tokens":1000,"total_tokens":3000}}`
}

func TestPlanRequestAndCleaning(t *testing.T) {
	answer := `{"headlines":[
		"  Why tingling feet\u200b keep you up at night ",
		"Why tingling feet keep you up at night",
		"",
		"\u202eHidden\u202c trick\u00ad for\u2060 sore joints\ufeff",
		"Already shown headline",
		"A fourth one",
		"A fifth one"
	],"analysis":[{"aspect":"Sujeito","fixed":"Idoso real","variable":"Etnia"}],
	"briefs":[
		{"angle":"Colher","brief":"A man, 60, on a porch\nholding the jar."},
		{"angle":"Variação próxima","brief":"A woman in a kitchen."},
		{"angle":"Colher","brief":"A man, 60, on a porch holding the jar."},
		{"angle":"Colher","brief":"   "},
		{"angle":"","brief":"A man at a table."},
		{"angle":"Colher","brief":"A spoon by a window."}
	]}`
	var got map[string]any
	c, m, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, chatReplyJSON(answer))
	})
	plan, err := c.Plan(context.Background(), PlanRequest{
		Prompt: "suplemento para neuropatia", Language: "pt-BR", Vertical: "Neuropathy",
		Headlines: 3, Images: 6, HasReferences: true,
		HeadlineExamples: []string{"Example headline"}, Avoid: []string{"Already shown headline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantH := []string{"Why tingling feet keep you up at night", "Hidden trick for sore joints", "A fourth one"}
	// Grouped by angle in the order the angles first came; a brief with no
	// angle gets one; the analysis is dropped because no ads were attached.
	wantB := []Brief{{"Colher", "A man, 60, on a porch holding the jar."}, {"Colher", "A spoon by a window."},
		{"Variação próxima", "A woman in a kitchen."}, {"Outro", "A man at a table."}}
	if fmt.Sprint(plan.Headlines) != fmt.Sprint(wantH) || fmt.Sprint(plan.Briefs) != fmt.Sprint(wantB) {
		t.Errorf("headlines %q\nbriefs %q", plan.Headlines, plan.Briefs)
	}
	if len(plan.Analysis) != 0 {
		t.Errorf("analysis without ads: %v", plan.Analysis)
	}
	wantCost := 2000*0.25/1e6 + 1000*2/1e6
	if !near(plan.Cost, wantCost) || len(m.spent) != 1 || !near(m.spent[0], wantCost) {
		t.Errorf("cost %v spent %v", plan.Cost, m.spent)
	}

	if got["model"] != "gpt-5-mini" || got["reasoning_effort"] != "low" {
		t.Errorf("model %v reasoning %v", got["model"], got["reasoning_effort"])
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature sent")
	}
	rf, _ := got["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || js["name"] == "" || js["schema"] == nil {
		t.Errorf("response_format %v", rf)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages %v", msgs)
	}
	user := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{"exactly 3 headlines in Brazilian Portuguese", "exactly 6 image briefs", "VERTICAL: Neuropathy",
		"PRODUCT PICTURES ARE ATTACHED", "No reference pictures", "THE PERSON'S REFERENCE HEADLINES", "Example headline", "Already shown headline", "suplemento para neuropatia"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message lacks %q:\n%s", want, user)
		}
	}
	plans := kept(t, dir, "-plan.json")
	if len(plans) != 1 {
		t.Fatalf("kept %v", plans)
	}
	raw, _ := os.ReadFile(plans[0])
	if !bytes.Contains(raw, []byte("chatcmpl-1")) || !bytes.Contains(raw, []byte("suplemento para neuropatia")) {
		t.Errorf("plan not kept raw: %s", raw)
	}
}

func TestPlanReadsPerformingAds(t *testing.T) {
	var got map[string]any
	c, _, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, chatReplyJSON(`{"analysis":[{"aspect":"Sujeito","fixed":"Mulher de 70 anos","variable":"Cabelo"}],"headlines":[],"briefs":[{"angle":"Canudo","brief":"A woman sips through a straw."}]}`))
	})
	jpeg := []byte("\xff\xd8\xff\xe0 fake")
	plan, err := c.Plan(context.Background(), PlanRequest{
		Prompt: "colágeno", Vertical: "Memory Loss", Ages: "70-85", Images: 1,
		Winners: []Reference{{Data: jpeg, MIME: "image/jpeg"}, {Data: jpeg, MIME: "image/jpeg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Analysis) != 1 || plan.Analysis[0].Fixed != "Mulher de 70 anos" || plan.Briefs[0].Angle != "Canudo" {
		t.Errorf("plan %+v", plan)
	}
	parts, ok := got["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("user content %v", got["messages"])
	}
	text := parts[0].(map[string]any)["text"].(string)
	for _, want := range []string{"REFERENCE PICTURES ATTACHED: the 2 picture(s)", "AGE RANGE OF THE PEOPLE IN EVERY PICTURE: 70-85", "TEAM EXAMPLES"} {
		if !strings.Contains(text, want) {
			t.Errorf("user text lacks %q:\n%s", want, text)
		}
	}
	// With no examples of its own, a vertical with a team library is shown
	// MaxExamples of them.
	if n := strings.Count(grepLines(text, "TEAM EXAMPLES", "ALREADY SHOWN", "ADDITIONAL INSTRUCTIONS"), "\n- "); n != MaxExamples {
		t.Errorf("%d examples", n)
	}
	img := parts[1].(map[string]any)["image_url"].(map[string]any)
	if !strings.HasPrefix(img["url"].(string), "data:image/jpeg;base64,") {
		t.Errorf("image part %v", img)
	}
	raw, _ := os.ReadFile(kept(t, dir, "-plan.json")[0])
	if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(jpeg))) || !bytes.Contains(raw, []byte(`"winners": 2`)) {
		t.Errorf("kept record should count the ads, not hold them: %s", raw)
	}
}

// grepLines returns the text from the line starting with from up to the
// first line starting with any of until.
func grepLines(s, from string, until ...string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i:]
	end := len(s)
	for _, u := range until {
		if j := strings.Index(s, u); j > 0 && j < end {
			end = j
		}
	}
	return s[:end]
}

func TestTeamRules(t *testing.T) {
	if len(BlockedWords) < 50 {
		t.Errorf("%d blocked words", len(BlockedWords))
	}
	var both []string
	for _, b := range BlockedWords {
		if b.Description {
			both = append(both, b.Text)
		}
	}
	if fmt.Sprint(both) != "[neurologists wine]" {
		t.Errorf("title+description: %v", both)
	}
	if !strings.Contains(planSystem, `"blood sugar"`) {
		t.Error("blocked words not in the system message")
	}
	for _, v := range ExampleVerticals() {
		lib, ok := LibraryFor(v)
		if !ok || len(lib.Headlines) < 30 {
			t.Errorf("%s: %d headlines", v, len(lib.Headlines))
		}
		for _, h := range lib.Headlines {
			if strings.HasPrefix(h, "___") || strings.HasSuffix(h, ":") && len(h) < 25 || strings.ContainsRune(h, 0xFEFF) {
				t.Errorf("%s: heading kept as a headline: %q", v, h)
			}
			for _, r := range h {
				if r >= 0x1D400 && r <= 0x1D7FF {
					t.Errorf("%s: bold letters kept: %q", v, h)
					break
				}
			}
		}
	}
	if lib, _ := LibraryFor("blood pressure"); len(lib.Descriptions) == 0 {
		t.Error("descriptions not split off")
	}
	if _, ok := LibraryFor("Prostate Health"); ok {
		t.Error("library for a vertical with none")
	}
}

func TestParseLibrary(t *testing.T) {
	lib := parseLibrary("\uFEFFHEAD ORIGINAIS:\nOne\n\n________________\nHeadlines (sem drink) 14/09/26\n𝐁𝐨𝐥𝐝 𝐓𝐰𝐨 𝟒\nOne\nDESCRIÇÕES\nA description\n")
	if fmt.Sprint(lib.Headlines) != "[One Bold Two 4]" || fmt.Sprint(lib.Descriptions) != "[A description]" {
		t.Errorf("%q %q", lib.Headlines, lib.Descriptions)
	}
}

func TestLibrarySample(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	if got := LibrarySample("Tinnitus", false, rnd); len(got) != MaxExamples {
		t.Errorf("alone: %d", len(got))
	}
	// The person's own headlines weigh the most, so the team's step aside.
	if got := LibrarySample("Tinnitus", true, rnd); len(got) != MaxExamplesAside {
		t.Errorf("beside the person's: %d", len(got))
	}
	if got := LibrarySample("Vision", false, rnd); len(got) != 0 {
		t.Errorf("no library: %q", got)
	}
}

func TestPlanOmitsEmptyReasoning(t *testing.T) {
	var got map[string]any
	c, _, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, chatReplyJSON(`{"headlines":["a"],"briefs":[]}`))
	})
	c.s.TextReasoning = ""
	if _, err := c.Plan(context.Background(), PlanRequest{Prompt: "p", Headlines: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Error("reasoning_effort sent while empty")
	}
}

func TestPlanUnreadableIsStillKept(t *testing.T) {
	c, _, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReplyJSON(`not json`))
	})
	_, err := c.Plan(context.Background(), PlanRequest{Prompt: "p", Headlines: 1})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err %v", err)
	}
	if len(kept(t, dir, "-plan.json")) != 1 {
		t.Error("unreadable reply not kept")
	}
}

func TestCleanLine(t *testing.T) {
	for in, want := range map[string]string{
		"  plain  ":                        "plain",
		"a\u200bb\u200cc\u200dd":           "abcd",
		"x\u2060y\ufeffz\u00adw":           "xyzw",
		"\u202aa\u202b\u202c\u202d\u202eb": "ab",
		"\u2066a\u2067\u2068\u2069b":       "ab",
		"two\n\tlines   here":              "two lines here",
		"\u200b \u200b":                    "",
		"Olá, você já viu isto?":           "Olá, você já viu isto?",
	} {
		if got := CleanLine(in); got != want {
			t.Errorf("CleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLanguageName(t *testing.T) {
	for in, want := range map[string]string{
		"": "English", "en": "English", "pt-BR": "Brazilian Portuguese", "pt_br": "Brazilian Portuguese",
		"es": "Spanish", "Japanese": "Japanese", "Português": "Português",
	} {
		if got := LanguageName(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestRetryAfterHeader(t *testing.T) {
	res := &http.Response{Header: http.Header{}}
	res.Header.Set("Retry-After", "7")
	if d := retryAfterHeader(res); d != 7*time.Second {
		t.Errorf("seconds: %v", d)
	}
	res.Header.Set("Retry-After-Ms", "1500")
	if d := retryAfterHeader(res); d != 1500*time.Millisecond {
		t.Errorf("ms: %v", d)
	}
	e := &Error{transient: true, retryAfter: 10 * time.Minute}
	if d := retryDelay(e, time.Second, 1); d != retryMax {
		t.Errorf("clamp: %v", d)
	}
	if d := retryDelay(&Error{transient: true}, time.Second, 2); d != 2*time.Second {
		t.Errorf("backoff: %v", d)
	}
}
