package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Limits of one plan.
const (
	MaxHeadlines = 30
	MaxImages    = 12
	// MaxExamples is how many style examples one plan is shown: the person's
	// own first, then a fresh random slice of the vertical's team library.
	MaxExamples = 40
)

// PlanRequest is what the page asks a plan for. Counts are already
// defaulted and checked by the caller.
type PlanRequest struct {
	Prompt           string   `json:"prompt"`
	HeadlineExamples []string `json:"headline_examples,omitempty"`
	Language         string   `json:"language"`
	Vertical         string   `json:"vertical,omitempty"`
	Headlines        int      `json:"headlines"`
	Images           int      `json:"images"`
	Ages             string   `json:"ages,omitempty"`
	HasReferences    bool     `json:"has_references"`
	Avoid            []string `json:"avoid,omitempty"`
	// Winners are pictures of ads performing well, read for the analysis.
	// They are not kept with the request (the person has them already); the
	// kept record says how many there were.
	Winners []Reference `json:"-"`
}

// Plan is an analysis, headlines and image briefs from one text call.
type Plan struct {
	Analysis  []Aspect `json:"analysis"`
	Headlines []string `json:"headlines"`
	Briefs    []Brief  `json:"briefs"`
	Cost      float64  `json:"cost_usd"`
}

// Aspect is one line of the analysis of the performing ads: what stays and
// what may change.
type Aspect struct {
	Aspect   string `json:"aspect"`
	Fixed    string `json:"fixed"`
	Variable string `json:"variable"`
}

// Brief is one picture to make, with the angle it belongs to.
type Brief struct {
	Angle string `json:"angle"`
	Brief string `json:"brief"`
}

type chatReply struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// planKept is what a plan call leaves in the keep folder: what was asked,
// what was sent, and OpenAI's reply exactly as it came.
type planKept struct {
	Kind         string          `json:"kind"`
	Time         time.Time       `json:"time"`
	Model        string          `json:"model"`
	Reasoning    string          `json:"reasoning_effort,omitempty"`
	Request      PlanRequest     `json:"request"`
	Winners      int             `json:"winners"`
	LanguageName string          `json:"language_name"`
	System       string          `json:"system"`
	User         string          `json:"user"`
	Usage        *Usage          `json:"usage"`
	CostUSD      float64         `json:"cost_usd"`
	Reply        json.RawMessage `json:"reply"`
}

// TextCost is the price of one text call from its usage.
func (c *Client) TextCost(u *Usage) float64 {
	if u == nil {
		return 0
	}
	return float64(u.PromptTokens)*c.s.TextPriceIn/1e6 + float64(u.CompletionTokens)*c.s.TextPriceOut/1e6
}

// Plan writes headlines and briefs in one structured text call. The reply is
// kept before it is parsed.
func (c *Client) Plan(ctx context.Context, r PlanRequest) (Plan, error) {
	r.HeadlineExamples = WithLibrary(r.HeadlineExamples, r.Vertical, rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
	language := LanguageName(r.Language)
	user := planUser(r, language)
	var content any = user
	if len(r.Winners) > 0 {
		parts := []map[string]any{{"type": "text", "text": user}}
		for _, w := range r.Winners {
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url":    "data:" + w.MIME + ";base64," + base64.StdEncoding.EncodeToString(w.Data),
					"detail": "high",
				},
			})
		}
		content = parts
	}
	payload := map[string]any{
		"model": c.s.TextModel,
		"messages": []map[string]any{
			{"role": "system", "content": planSystem},
			{"role": "user", "content": content},
		},
		// No temperature: the reasoning models refuse anything but the default.
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "create_plan", "strict": true, "schema": planSchema,
			},
		},
	}
	if c.s.TextReasoning != "" {
		payload["reasoning_effort"] = c.s.TextReasoning
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Plan{}, err
	}
	raw, err := c.call(ctx, "/v1/chat/completions", "application/json", body)
	if err != nil {
		return Plan{}, err
	}
	var reply chatReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Plan{}, &Error{Status: http.StatusOK, Message: "resposta da OpenAI ilegível"}
	}
	cost := c.TextCost(reply.Usage)
	c.spent(cost)

	// Kept before parsing, so a reply we could not read is still on disk.
	kept, err := c.keep.JSON("plan", planKept{
		Kind: "plan", Time: time.Now().UTC(), Model: c.s.TextModel, Reasoning: c.s.TextReasoning,
		Request: r, Winners: len(r.Winners), LanguageName: language, System: planSystem, User: user,
		Usage: reply.Usage, CostUSD: cost, Reply: raw,
	})
	if err != nil {
		c.log.Error("plan not kept, not handed back", "err", err, "cost_usd", cost)
		return Plan{}, fmt.Errorf("%w: %v", ErrKeep, err)
	}

	if len(reply.Choices) == 0 {
		return Plan{}, &Error{Status: http.StatusOK, Message: "a OpenAI não devolveu texto"}
	}
	choice := reply.Choices[0]
	if choice.Message.Refusal != "" {
		return Plan{}, &Error{Status: http.StatusOK, Message: "a OpenAI recusou o pedido: " + truncate(choice.Message.Refusal, 160)}
	}
	if choice.FinishReason == "length" {
		return Plan{}, &Error{Status: http.StatusOK, Message: "a resposta da OpenAI foi cortada no meio; peça menos títulos ou imagens"}
	}
	plan, err := ParsePlan(choice.Message.Content, r)
	if err != nil {
		return Plan{}, err
	}
	plan.Cost = cost
	c.log.Info("plan made", "model", c.s.TextModel, "winners", len(r.Winners), "headlines", len(plan.Headlines),
		"briefs", len(plan.Briefs), "cost_usd", cost, "kept", kept)
	return plan, nil
}

// WithLibrary tops the person's examples up to MaxExamples with a random
// slice of the vertical's team headlines, when the team has any.
func WithLibrary(own []string, vertical string, rnd *rand.Rand) []string {
	lib, ok := LibraryFor(vertical)
	if !ok || len(own) >= MaxExamples {
		return own
	}
	out := append([]string{}, own...)
	have := map[string]bool{}
	for _, o := range own {
		have[o] = true
	}
	for _, h := range sample(lib.Headlines, MaxExamples, rnd) {
		if len(out) >= MaxExamples {
			break
		}
		if !have[h] {
			out = append(out, h)
		}
	}
	return out
}

// ParsePlan reads the model's JSON and cleans it: each line trimmed and
// stripped of invisible characters, empties and exact duplicates dropped,
// anything already shown dropped, and the counts capped to what was asked.
// Briefs keep the model's order grouped by angle, first angle first.
func ParsePlan(content string, r PlanRequest) (Plan, error) {
	var answer struct {
		Analysis  []Aspect `json:"analysis"`
		Headlines []string `json:"headlines"`
		Briefs    []Brief  `json:"briefs"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &answer); err != nil {
		return Plan{}, &Error{Status: http.StatusOK, Message: "a OpenAI devolveu um plano ilegível"}
	}
	shown := map[string]bool{}
	for _, a := range r.Avoid {
		shown[CleanLine(a)] = true
	}
	plan := Plan{Analysis: []Aspect{}, Headlines: tidy(answer.Headlines, r.Headlines, shown), Briefs: []Brief{}}
	if len(r.Winners) > 0 {
		for _, a := range answer.Analysis {
			a = Aspect{Aspect: CleanLine(a.Aspect), Fixed: CleanLine(a.Fixed), Variable: CleanLine(a.Variable)}
			if a.Aspect != "" {
				plan.Analysis = append(plan.Analysis, a)
			}
		}
	}
	seen := map[string]bool{}
	var order []string
	byAngle := map[string][]Brief{}
	for _, b := range answer.Briefs {
		if len(seen) >= r.Images {
			break
		}
		b = Brief{Angle: CleanLine(b.Angle), Brief: CleanLine(b.Brief)}
		if b.Brief == "" || seen[b.Brief] || shown[b.Brief] {
			continue
		}
		seen[b.Brief] = true
		if b.Angle == "" {
			b.Angle = "Outro"
		}
		if _, ok := byAngle[b.Angle]; !ok {
			order = append(order, b.Angle)
		}
		byAngle[b.Angle] = append(byAngle[b.Angle], b)
	}
	for _, a := range order {
		plan.Briefs = append(plan.Briefs, byAngle[a]...)
	}
	return plan, nil
}

func tidy(lines []string, limit int, shown map[string]bool) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range lines {
		if len(out) >= limit {
			break
		}
		l = CleanLine(l)
		if l == "" || seen[l] || shown[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// CleanLine removes the characters a headline must never carry because
// Taboola rejects them and nobody can see them to fix them: zero-width
// spaces and joiners (U+200B to U+200D, U+2060, U+FEFF), the soft hyphen
// (U+00AD) and the bidi controls (U+202A to U+202E, U+2066 to U+2069). Runs
// of whitespace, including line breaks, become one space, and the ends are
// trimmed.
func CleanLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if invisible(r) {
			continue
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func invisible(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200D, r == 0x2060, r == 0xFEFF, r == 0x00AD,
		r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// languages maps the codes the page may send to the names a model reads
// best. Anything else (a name such as "Japanese") is passed on as written.
var languages = map[string]string{
	"en": "English", "en-us": "American English", "en-gb": "British English",
	"pt": "Portuguese", "pt-br": "Brazilian Portuguese", "pt-pt": "European Portuguese",
	"es": "Spanish", "es-419": "Latin American Spanish", "es-mx": "Mexican Spanish", "es-es": "Spanish (Spain)",
	"fr": "French", "de": "German", "it": "Italian", "nl": "Dutch", "sv": "Swedish",
	"da": "Danish", "no": "Norwegian", "nb": "Norwegian", "fi": "Finnish", "pl": "Polish",
	"ja": "Japanese", "ko": "Korean", "zh": "Chinese",
}

// LanguageName turns a code or a name into the name the prompt uses; empty
// means English.
func LanguageName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "English"
	}
	if name, ok := languages[strings.ToLower(strings.ReplaceAll(s, "_", "-"))]; ok {
		return name
	}
	return s
}
