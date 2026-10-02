// Package openai is Create's OpenAI client: one picture per call from the
// images endpoints, and one structured text call for a plan.
//
// Ported from auto-creative's provider package (openai.go, retry.go,
// instruction.go), which was proven against the real API. What it learned is
// kept, with the reason next to it.
//
// Every successful paid call reports its cost once through Meter.Spent, and
// every reply the service uses is kept on disk (package keep) before it is
// returned.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/keep"
	"github.com/Raposa-Industries/adhunters/kit/ops"
)

const defaultBaseURL = "https://api.openai.com"

// Provider is the name costs and credit refusals are counted under.
const Provider = "openai"

// Settings are the knobs create-web reads from its environment.
type Settings struct {
	APIKey string
	// BaseURL overrides the API host in tests; empty means the real one.
	BaseURL string

	ImageModel   string
	ImageQuality string // low, medium or high
	TextModel    string
	// TextReasoning is sent as reasoning_effort only when it is not empty.
	TextReasoning string

	// Prices are USD per million tokens.
	ImagePriceIn, ImagePriceOut float64
	TextPriceIn, TextPriceOut   float64
}

// Meter is where money and credit refusals are counted; *ops.Server is one.
type Meter interface {
	Spent(provider string, usd float64)
	OutOfCredit(provider string)
}

// Client calls OpenAI. It is safe for concurrent use.
type Client struct {
	s     Settings
	http  *http.Client
	meter Meter
	keep  *keep.Folder
	log   *slog.Logger

	// retryBase is the first backoff pause; tests shrink it.
	retryBase time.Duration

	// provider is the name costs are counted under, name the one people
	// read; chatPath is where chat completions are posted. compat is a
	// client of another OpenAI-compatible provider (compat.go).
	provider, name, chatPath string
	compat                   bool
}

// New returns a client. meter may be nil (nothing is counted).
func New(s Settings, meter Meter, kept *keep.Folder, log *slog.Logger) *Client {
	if s.BaseURL == "" {
		s.BaseURL = defaultBaseURL
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	// No client timeout: every call carries a context deadline instead, set by
	// the caller, so a retry ladder and a slow picture share one budget.
	// Every call is counted on /metrics under the provider (kit/ops Transport).
	return &Client{s: s, http: &http.Client{Transport: ops.Transport(Provider, nil)}, meter: meter, keep: kept, log: log, retryBase: retryBase,
		provider: Provider, name: "OpenAI", chatPath: "/v1/chat/completions"}
}

// Settings returns the settings in force.
func (c *Client) Settings() Settings { return c.s }

// Available reports whether calls can be made at all.
func (c *Client) Available() bool { return c.s.APIKey != "" }

// Why is the reason generation is off, in pt-BR, or "" when it is on.
func (c *Client) Why() string {
	if c.Available() {
		return ""
	}
	return "geração desligada: OPENAI_API_KEY não está definida no servidor"
}

// Error is a failed call, with one short pt-BR line for the person.
type Error struct {
	Status int
	// Message is condensed: never a payload dump.
	Message string
	// OutOfCredit is OpenAI's insufficient_quota: no wait refills a balance.
	OutOfCredit bool

	transient  bool
	retryAfter time.Duration
}

func (e *Error) Error() string { return "openai: " + e.Message }

// ErrKeep means a reply arrived (and was paid for) but could not be kept, so
// it is not handed back.
var ErrKeep = errors.New("não foi possível guardar a resposta da OpenAI no disco")

// ErrNotConfigured is returned by every call when there is no key.
var ErrNotConfigured = errors.New("OPENAI_API_KEY não está definida")

// apiError is the error object OpenAI puts in a non-2xx reply. Type and Code
// overlap in practice (insufficient_quota arrives in both) but the two have
// not always agreed, so the quota check reads either.
type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// post sends one request and returns the body of a 2xx reply, or an *Error.
func (c *Client) post(ctx context.Context, path, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.s.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+c.s.APIKey)
	res, err := c.http.Do(req)
	if err != nil {
		// Never repeated: a request that reached OpenAI before the connection
		// broke may already have produced (and billed) a picture.
		if ctx.Err() != nil {
			return nil, &Error{Message: "tempo esgotado esperando a OpenAI"}
		}
		return nil, &Error{Message: "sem conexão com a OpenAI: " + truncate(err.Error(), 160)}
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, &Error{Status: res.StatusCode, Message: "resposta da OpenAI cortada: " + truncate(err.Error(), 160)}
	}
	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		return resBody, nil
	}
	var parsed struct {
		Error *apiError `json:"error"`
	}
	// A non-2xx body is usually JSON with an error object; a decode failure
	// falls through to the status-based message.
	_ = json.Unmarshal(resBody, &parsed)
	quota := quotaExhausted(parsed.Error)
	return nil, &Error{
		Status:      res.StatusCode,
		Message:     condense(res.StatusCode, parsed.Error),
		OutOfCredit: quota,
		// A 429 is ambiguous: OpenAI answers it both for "too fast" and for
		// "out of money", and only the first clears by waiting. The 5xx family
		// is the vendor's own bad moment, which a later attempt usually misses.
		transient:  !quota && retryableStatus(res.StatusCode),
		retryAfter: retryAfterHeader(res),
	}
}

// quotaExhausted spots the refusal that means the balance is empty rather
// than the call too fast. Either field is enough.
func quotaExhausted(e *apiError) bool {
	return e != nil && (e.Code == "insufficient_quota" || e.Type == "insufficient_quota")
}

// OutOfCreditMessage is the line shown when the balance is empty.
const OutOfCreditMessage = "créditos da OpenAI esgotados — recarregue em platform.openai.com/billing"

// condense turns an error reply into one short pt-BR line.
func condense(status int, e *apiError) string {
	if quotaExhausted(e) {
		return OutOfCreditMessage
	}
	switch status {
	case 401, 403:
		return fmt.Sprintf("chave da OpenAI recusada (%d)", status)
	case 429:
		return "muitas requisições à OpenAI, aguarde um minuto"
	}
	if e != nil && e.Message != "" {
		// The moderation refusal is the one people hit most; name it.
		if e.Code == "moderation_blocked" {
			return fmt.Sprintf("pedido bloqueado pela moderação da OpenAI (%d)", status)
		}
		msg, _, _ := strings.Cut(e.Message, "\n")
		return fmt.Sprintf("%s (%d)", truncate(msg, 180), status)
	}
	if status >= 500 {
		return fmt.Sprintf("a OpenAI falhou do lado dela (HTTP %d)", status)
	}
	return fmt.Sprintf("HTTP %d", status)
}

// call runs one paid request through the retry ladder and counts a credit
// refusal once for the whole call, not once per attempt.
func (c *Client) call(ctx context.Context, path, contentType string, body []byte) ([]byte, error) {
	if !c.Available() {
		return nil, ErrNotConfigured
	}
	out, err := retry(ctx, c.retryBase, func() ([]byte, error) { return c.post(ctx, path, contentType, body) })
	var e *Error
	if errors.As(err, &e) && e.OutOfCredit && c.meter != nil {
		c.meter.OutOfCredit(c.provider)
	}
	return out, err
}

func (c *Client) spent(usd float64) {
	if c.meter != nil {
		c.meter.Spent(c.provider, usd)
	}
}

// truncate keeps an error line short: it is shown to the person as it is.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return s
}

// SettingsFromEnv reads the settings from the environment: OPENAI_API_KEY,
// OPENAI_BASE_URL, CREATE_IMAGE_MODEL, CREATE_IMAGE_QUALITY,
// CREATE_TEXT_MODEL, CREATE_TEXT_REASONING (set but empty: no
// reasoning_effort is sent) and the four CREATE_*_PRICE_* in USD per
// million tokens.
func SettingsFromEnv() (Settings, error) {
	env := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	s := Settings{
		APIKey:        os.Getenv("OPENAI_API_KEY"),
		BaseURL:       os.Getenv("OPENAI_BASE_URL"),
		ImageModel:    env("CREATE_IMAGE_MODEL", "gpt-image-2.5-flare"),
		ImageQuality:  env("CREATE_IMAGE_QUALITY", "medium"),
		TextModel:     env("CREATE_TEXT_MODEL", "gpt-5-mini"),
		TextReasoning: "low",
	}
	if !ValidQuality(s.ImageQuality) {
		return s, fmt.Errorf("CREATE_IMAGE_QUALITY must be low, medium or high, not %q", s.ImageQuality)
	}
	if v, ok := os.LookupEnv("CREATE_TEXT_REASONING"); ok {
		s.TextReasoning = v
	}
	prices := []struct {
		key string
		def float64
		to  *float64
	}{
		{"CREATE_IMAGE_PRICE_IN", 10, &s.ImagePriceIn},
		{"CREATE_IMAGE_PRICE_OUT", 30, &s.ImagePriceOut},
		{"CREATE_TEXT_PRICE_IN", 0.25, &s.TextPriceIn},
		{"CREATE_TEXT_PRICE_OUT", 2, &s.TextPriceOut},
	}
	for _, p := range prices {
		*p.to = p.def
		if v := os.Getenv(p.key); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 {
				return s, fmt.Errorf("%s must be a price in USD per million tokens, not %q", p.key, v)
			}
			*p.to = f
		}
	}
	return s, nil
}
