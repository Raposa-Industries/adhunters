package claude

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// The request carries the conversation exactly as stored (reasoning and
// tool calls included), the tools as closed schemas, adaptive thinking, the
// effort, and cache marks on the instructions and on the conversation.
func TestParams(t *testing.T) {
	history := []json.RawMessage{
		json.RawMessage(`{"role": "user", "content": [{"type": "text", "text": "oi"}]}`),
		json.RawMessage(`{"role": "assistant", "content": [{"type": "thinking", "thinking": "", "signature": "c2lnbmVk"},
			{"type": "tool_use", "id": "toolu_1", "name": "spy_operators", "input": {"limit": 3}}]}`),
		json.RawMessage(`{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "[]", "is_error": false}]}`),
	}
	p, err := Params(Request{
		Model: "claude-opus-5-5", Effort: "medium", MaxTokens: 16000, System: "You are Desk.",
		Tools: []Tool{{Name: "spy_operators", Description: "Operators.", Schema: map[string]any{
			"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}},
			"required": []string{}, "additionalProperties": false,
		}}},
		Messages: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if th := got["thinking"].(map[string]any); th["type"] != "adaptive" {
		t.Errorf("thinking %v", th)
	}
	if oc := got["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("output_config %v", oc)
	}
	if tc := got["tool_choice"].(map[string]any); tc["type"] != "auto" {
		t.Errorf("tool_choice %v", tc)
	}
	if got["cache_control"] == nil || got["system"].([]any)[0].(map[string]any)["cache_control"] == nil {
		t.Error("the instructions and the conversation are cached")
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	if s := tool["input_schema"].(map[string]any); s["additionalProperties"] != false || s["type"] != "object" {
		t.Errorf("input_schema %v", s)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("%d messages", len(msgs))
	}
	think := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if think["type"] != "thinking" || think["signature"] != "c2lnbmVk" {
		t.Errorf("the reasoning goes back as it came: %v", think)
	}
	use := msgs[1].(map[string]any)["content"].([]any)[1].(map[string]any)
	if use["id"] != "toolu_1" || use["input"].(map[string]any)["limit"] != float64(3) {
		t.Errorf("tool_use %v", use)
	}

	p, _ = Params(Request{Model: "claude-opus-5-5", MaxTokens: 1000, Tools: []Tool{{Name: "x", Schema: map[string]any{"type": "object"}}}, Answer: true})
	b, _ = json.Marshal(p)
	if !strings.Contains(string(b), `"tool_choice":{"type":"none"}`) {
		t.Errorf("an answer in words: %s", b)
	}
}

func TestStripThinking(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role": "user", "content": [{"type": "text", "text": "oi"}]}`),
		json.RawMessage(`{"role": "assistant", "content": [{"type": "thinking", "thinking": "", "signature": "x"}, {"type": "text", "text": "Olá"}]}`),
		json.RawMessage(`{"role": "assistant", "content": [{"type": "redacted_thinking", "data": "x"}]}`),
	}
	out := StripThinking(in)
	if string(out[0]) != string(in[0]) {
		t.Error("the person's messages stay as they are")
	}
	if strings.Contains(string(out[1]), "thinking") || !strings.Contains(string(out[1]), "Olá") {
		t.Errorf("%s", out[1])
	}
	if !strings.Contains(string(out[2]), `"text"`) {
		t.Errorf("an answer never goes back empty: %s", out[2])
	}
}

func TestCost(t *testing.T) {
	// A million of each at Opus 5.5 prices.
	got := Cost("claude-opus-5-5", Usage{Input: 1e6, Output: 1e6, CacheRead: 1e6, CacheWrite: 1e6})
	if math.Abs(got-29.2) > 1e-9 {
		t.Errorf("cost %v", got)
	}
	if Cost("claude-new-9", Usage{Input: 1e6}) != 10 {
		t.Error("an unknown model is priced as the dearest")
	}
}
