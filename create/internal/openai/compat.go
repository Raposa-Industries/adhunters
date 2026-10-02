package openai

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Raposa-Industries/adhunters/kit/keep"
)

// Other text models may write headlines (decision 0024): xAI's Grok,
// DeepSeek and Moonshot's Kimi all speak OpenAI's chat-completions dialect,
// so one Client serves them, in compat mode. Pictures stay OpenAI's.

// Compat is one OpenAI-compatible text provider for headlines.
type Compat struct {
	// ID is what a turn records and the page sends: grok, deepseek, kimi.
	ID string
	// Name is what the page shows and errors say: "Grok".
	Name    string
	APIKey  string
	BaseURL string // with its version, e.g. https://api.x.ai/v1
	Model   string
	// Prices are USD per million tokens; 0 counts nothing.
	PriceIn, PriceOut float64
}

// compatDefaults are the providers Create knows, with their public API
// addresses and a model to start from; each is off until its key is set.
var compatDefaults = []Compat{
	{ID: "grok", Name: "Grok", BaseURL: "https://api.x.ai/v1", Model: "grok-4"},
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
	{ID: "kimi", Name: "Kimi", BaseURL: "https://api.moonshot.ai/v1", Model: "kimi-k2-0905-preview"},
}

// CompatFromEnv reads the providers whose key is set: CREATE_<ID>_API_KEY,
// and optionally CREATE_<ID>_BASE_URL, CREATE_<ID>_MODEL,
// CREATE_<ID>_PRICE_IN and CREATE_<ID>_PRICE_OUT (USD per million tokens).
func CompatFromEnv() ([]Compat, error) {
	var out []Compat
	for _, d := range compatDefaults {
		p := "CREATE_" + strings.ToUpper(d.ID) + "_"
		d.APIKey = os.Getenv(p + "API_KEY")
		if d.APIKey == "" {
			continue
		}
		if v := os.Getenv(p + "BASE_URL"); v != "" {
			d.BaseURL = v
		}
		if v := os.Getenv(p + "MODEL"); v != "" {
			d.Model = v
		}
		for _, x := range []struct {
			key string
			to  *float64
		}{{p + "PRICE_IN", &d.PriceIn}, {p + "PRICE_OUT", &d.PriceOut}} {
			if v := os.Getenv(x.key); v != "" {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil || f < 0 {
					return nil, fmt.Errorf("%s must be a price in USD per million tokens, not %q", x.key, v)
				}
				*x.to = f
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// NewCompat returns a client for headlines from cp. Its replies are kept in
// the same folder as OpenAI's, before they are read, and its cost is counted
// under cp.ID.
func NewCompat(cp Compat, meter Meter, kept *keep.Folder, log *slog.Logger) *Client {
	c := New(Settings{APIKey: cp.APIKey, BaseURL: cp.BaseURL, TextModel: cp.Model, TextPriceIn: cp.PriceIn, TextPriceOut: cp.PriceOut},
		meter, kept, log)
	c.provider, c.name, c.chatPath, c.compat = cp.ID, cp.Name, "/chat/completions", true
	return c
}

// Name is who the client calls, as the page shows it.
func (c *Client) Name() string { return c.name }

// compatSystem is said after the house rules to a compat model, which gets
// no JSON schema: the shape of the answer is spelled out instead.
const compatSystem = "\n\nAnswer with one JSON object and nothing else, exactly in this shape: " +
	`{"analysis": [], "headlines": ["...", "..."], "briefs": []}` +
	". Headlines are always in English."

// theOpenAI is "a OpenAI" as a phrase (not the end of "da OpenAI").
var theOpenAI = regexp.MustCompile(`(^|\s)a OpenAI`)

// named puts the provider's name in place of OpenAI's in an error a compat
// client returns.
func (c *Client) named(err error) error {
	if !c.compat {
		return err
	}
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	out := *e
	if out.OutOfCredit {
		out.Message = "créditos de " + c.name + " esgotados"
	} else {
		out.Message = theOpenAI.ReplaceAllString(out.Message, "${1}"+c.name)
		out.Message = strings.ReplaceAll(out.Message, "OpenAI", c.name)
	}
	return &out
}
