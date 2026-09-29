package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Limits of one plan.
const (
	MaxHeadlines = 30
	MaxImages    = 12
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
	HasReferences    bool     `json:"has_references"`
	Avoid            []string `json:"avoid,omitempty"`
}

// Plan is headlines and image briefs from one text call.
type Plan struct {
	Headlines []string `json:"headlines"`
	Briefs    []string `json:"briefs"`
	Cost      float64  `json:"cost_usd"`
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
	language := LanguageName(r.Language)
	user := planUser(r, language)
	payload := map[string]any{
		"model": c.s.TextModel,
		"messages": []map[string]string{
			{"role": "system", "content": planSystem},
			{"role": "user", "content": user},
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
		Request: r, LanguageName: language, System: planSystem, User: user,
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
	c.log.Info("plan made", "model", c.s.TextModel, "headlines", len(plan.Headlines),
		"briefs", len(plan.Briefs), "cost_usd", cost, "kept", kept)
	return plan, nil
}

// ParsePlan reads the model's JSON and cleans it: each line trimmed and
// stripped of invisible characters, empties and exact duplicates dropped,
// anything already shown dropped, and the counts capped to what was asked.
func ParsePlan(content string, r PlanRequest) (Plan, error) {
	var answer struct {
		Headlines []string `json:"headlines"`
		Briefs    []string `json:"briefs"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &answer); err != nil {
		return Plan{}, &Error{Status: http.StatusOK, Message: "a OpenAI devolveu um plano ilegível"}
	}
	shown := map[string]bool{}
	for _, a := range r.Avoid {
		shown[CleanLine(a)] = true
	}
	return Plan{
		Headlines: tidy(answer.Headlines, r.Headlines, shown),
		Briefs:    tidy(answer.Briefs, r.Images, shown),
	}, nil
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
