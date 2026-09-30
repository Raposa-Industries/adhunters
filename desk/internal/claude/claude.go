// Package claude is Desk's one door to the Claude API: a request in the
// API's own JSON shapes goes in, the model's answer and what it cost come
// out. The agent talks to the Caller interface, so tests run on a scripted
// model and never reach the API.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Tool is one tool the model may call. Schema is its input's JSON Schema
// (an object).
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Request is one call.
type Request struct {
	Model     string
	Effort    string // low, medium, high, xhigh or max; "" is the model's own
	MaxTokens int64
	System    string
	Tools     []Tool
	// Messages are the conversation so far, each the API's JSON message
	// ({"role": "user", "content": […]}), exactly as stored.
	Messages []json.RawMessage
	// Answer makes the model answer in words: it may not call a tool.
	Answer bool
	// Format, when set, is the JSON Schema the answer's text follows.
	Format map[string]any
}

// Response is the model's answer.
type Response struct {
	// Message is the answer as the API's JSON message, to add to the
	// conversation as it is.
	Message    json.RawMessage
	StopReason string // end_turn, tool_use, max_tokens, refusal…
	Text       string // the answer's text blocks, joined
	Uses       []Use
	Usage      Usage
	Took       time.Duration
}

// Use is one tool call in an answer.
type Use struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Usage counts tokens.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int64
}

// Caller makes one call.
type Caller interface {
	Call(ctx context.Context, r Request) (Response, error)
}

// Client calls the Claude API.
type Client struct{ c anthropic.Client }

// New returns a client with the API key.
func New(apiKey string) *Client {
	return &Client{c: anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(3))}
}

// Call streams one answer (long answers would otherwise hit request
// timeouts) and returns it whole. A conversation whose earlier reasoning the
// API no longer accepts (instructions or tools changed since) is tried once
// more without that reasoning.
func (c *Client) Call(ctx context.Context, r Request) (Response, error) {
	resp, err := c.call(ctx, r)
	var apiErr *anthropic.Error
	if err != nil && errors.As(err, &apiErr) && apiErr.StatusCode == 400 && strings.Contains(err.Error(), "thinking") &&
		strings.Contains(err.Error(), "signature") {
		r.Messages = StripThinking(r.Messages)
		resp, err = c.call(ctx, r)
	}
	return resp, err
}

func (c *Client) call(ctx context.Context, r Request) (Response, error) {
	params, err := Params(r)
	if err != nil {
		return Response{}, err
	}
	start := time.Now()
	stream := c.c.Messages.NewStreaming(ctx, params)
	var msg anthropic.Message
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			stream.Close()
			return Response{}, fmt.Errorf("claude: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, fmt.Errorf("claude: %w", err)
	}
	out := Response{
		StopReason: string(msg.StopReason),
		Took:       time.Since(start),
		Usage: Usage{
			Input:      msg.Usage.InputTokens,
			Output:     msg.Usage.OutputTokens,
			CacheRead:  msg.Usage.CacheReadInputTokens,
			CacheWrite: msg.Usage.CacheCreationInputTokens,
		},
	}
	var text []string
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			text = append(text, b.Text)
		case anthropic.ToolUseBlock:
			in := b.Input
			if !json.Valid(in) {
				in = json.RawMessage(`{}`)
			}
			out.Uses = append(out.Uses, Use{ID: b.ID, Name: b.Name, Input: in})
		}
	}
	out.Text = strings.Join(text, "\n\n")
	if out.Message, err = json.Marshal(msg.ToParam()); err != nil {
		return Response{}, fmt.Errorf("claude: keeping the answer: %w", err)
	}
	return out, nil
}

// Params builds the SDK's request. The instructions and tools are cached
// (they are the same on every call), and so is the conversation up to its
// last message, so each call of a turn pays again only for what is new.
//
// Tool inputs are small here (filters, a plan), so they are not streamed as
// they are generated: the API checks each one is whole JSON first.
func Params(r Request) (anthropic.MessageNewParams, error) {
	p := anthropic.MessageNewParams{
		Model:        anthropic.Model(r.Model),
		MaxTokens:    r.MaxTokens,
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
		Thinking:     anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
	}
	if r.System != "" {
		p.System = []anthropic.TextBlockParam{{Text: r.System, CacheControl: anthropic.NewCacheControlEphemeralParam()}}
	}
	for _, t := range r.Tools {
		schema := anthropic.ToolInputSchemaParam{ExtraFields: map[string]any{}}
		for k, v := range t.Schema {
			switch k {
			case "type":
			case "properties":
				schema.Properties = v
			case "required":
				for _, name := range asStrings(v) {
					schema.Required = append(schema.Required, name)
				}
			default:
				schema.ExtraFields[k] = v
			}
		}
		tool := anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema}
		p.Tools = append(p.Tools, anthropic.ToolUnionParam{OfTool: &tool})
	}
	if len(r.Tools) > 0 {
		if r.Answer {
			p.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
		} else {
			p.ToolChoice = anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
		}
	}
	if r.Effort != "" {
		p.OutputConfig.Effort = anthropic.OutputConfigEffort(r.Effort)
	}
	if r.Format != nil {
		p.OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: r.Format}
	}
	for i, raw := range r.Messages {
		var m anthropic.MessageParam
		if err := json.Unmarshal(raw, &m); err != nil {
			return p, fmt.Errorf("claude: message %d: %w", i, err)
		}
		p.Messages = append(p.Messages, m)
	}
	return p, nil
}

func asStrings(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// StripThinking removes the model's reasoning blocks from messages, for a
// conversation whose instructions or tools changed since it reasoned: the
// API accepts reasoning only in the conversation that produced it.
func StripThinking(messages []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(messages))
	for _, raw := range messages {
		var m struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Role != "assistant" {
			out = append(out, raw)
			continue
		}
		kept := make([]json.RawMessage, 0, len(m.Content))
		for _, b := range m.Content {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(b, &head)
			if head.Type != "thinking" && head.Type != "redacted_thinking" {
				kept = append(kept, b)
			}
		}
		if len(kept) == len(m.Content) {
			out = append(out, raw)
			continue
		}
		if len(kept) == 0 {
			kept = append(kept, json.RawMessage(`{"type": "text", "text": "…"}`))
		}
		m.Content = kept
		b, _ := json.Marshal(m)
		out = append(out, b)
	}
	return out
}

// Prices are dollars per million tokens: input, output, cache reads, and
// cache writes (5-minute cache, 1.25 × input).
type Prices struct{ Input, Output, CacheRead, CacheWrite float64 }

// prices by model. A model not listed is priced as the dearest one, so the
// daily cap errs on the safe side.
var prices = map[string]Prices{
	"claude-opus-5-5":   {4, 20, 0.20, 5},
	"claude-opus-5":     {5, 25, 0.50, 6.25},
	"claude-sonnet-5-5": {2, 10, 0.20, 2.5},
	"claude-sonnet-5":   {2, 10, 0.20, 2.5},
	"claude-haiku-4-5":  {1, 5, 0.10, 1.25},
	"claude-fable-5-1":  {10, 50, 0.25, 12.5},
}

// Cost is what a call's usage cost, in dollars.
func Cost(model string, u Usage) float64 {
	p, ok := prices[model]
	if !ok {
		p = prices["claude-fable-5-1"]
	}
	return (float64(u.Input)*p.Input + float64(u.Output)*p.Output +
		float64(u.CacheRead)*p.CacheRead + float64(u.CacheWrite)*p.CacheWrite) / 1e6
}
