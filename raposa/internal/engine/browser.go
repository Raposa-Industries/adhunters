package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"adhunters/collector/internal/model"
)

// The browser runner drives headless Chromium on the other side of one HTTP
// call. It answers in the step shape below, and the plain HTTP engine in
// fetch.go builds the same one, so a single capture path stores both.

// visitPlan is one visit as the ladder decided it: what to load, from where,
// as whom.
type visitPlan struct {
	URL           string
	Referer       string
	Line          *model.ProxyLine
	Device        string // desktop or phone
	Timezone      string
	LoadAssets    bool
	HumanDwell    bool
	DwellMs       int
	MaxSteps      int // how far past the first page to follow the funnel
	MaxAssetBytes int
	// Keep the files the page loaded. Off for a quick investigation, which
	// keeps the landing page's HTML only.
	CaptureAssets bool
}

// VisitRequest is the body POSTed to the browser runner.
type VisitRequest struct {
	URL           string       `json:"url"`
	Referer       string       `json:"referer"`
	Proxy         *ProxyConfig `json:"proxy"`
	Device        string       `json:"device"`
	Timezone      string       `json:"timezone"`
	LoadAssets    bool         `json:"loadAssets"`
	HumanDwell    bool         `json:"humanDwell"`
	DwellMs       int          `json:"dwellMs"`
	MaxSteps      int          `json:"maxSteps"`
	CaptureAssets bool         `json:"captureAssets"`
	MaxAssetBytes int          `json:"maxAssetBytes"`
}

// ProxyConfig is the line the runner sends the visit through.
type ProxyConfig struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// VisitResult is one visit, whatever loaded it.
type VisitResult struct {
	OK         bool        `json:"ok"`
	Bytes      int         `json:"bytes"` // total transferred, for the metered budget
	DurationMs int         `json:"durationMs"`
	ExitIP     string      `json:"exitIp"`
	Error      string      `json:"error"`
	Steps      []VisitStep `json:"steps"`
}

// VisitStep is one page of one visit, in order.
type VisitStep struct {
	StepNo      int          `json:"stepNo"`
	ReachedBy   string       `json:"reachedBy"` // landing, redirect, cta
	ClickedText string       `json:"clickedText"`
	ClickedURL  string       `json:"clickedUrl"`
	URL         string       `json:"url"` // where this step ended after redirects
	Status      int          `json:"status"`
	Title       string       `json:"title"`
	HTML        string       `json:"html"`
	Text        string       `json:"text"`
	Hops        []VisitHop   `json:"hops"`
	Assets      []VisitAsset `json:"assets"`
}

// VisitHop is one redirect on the way to a step.
type VisitHop struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

// VisitAsset is one file a step loaded.
type VisitAsset struct {
	URL           string `json:"url"`
	Role          string `json:"role"` // image, video, stylesheet, script, font, other
	MediaType     string `json:"mediaType"`
	Base64        string `json:"base64"`
	SkippedReason string `json:"skippedReason"`
	// Body is the decoded bytes. The runner sends base64; the fetch engine
	// fills this one directly.
	Body []byte `json:"-"`
}

// BrowserClient talks to the browser runner.
type BrowserClient struct {
	addr string
	http *http.Client
	// One token per browser visit running at once, and one for the keeper.
	// Ten investigations and the keeper each opening a Chromium got the runner
	// killed for memory four times in an hour on 2026-09-27, and every visit
	// in flight failed. The keeper has two slots of its own: sharing the
	// visits' slots, it waited behind the quick investigations and kept
	// nothing.
	slots     chan struct{}
	keepSlots chan struct{}
}

// browserSlots is how many browser visits run at once, besides two keeps.
const browserSlots = 3

// Acquire waits for a free browser visit slot. The wait does not count
// toward a visit's own time budget, so the caller takes the slot before
// starting it.
func (b *BrowserClient) Acquire(ctx context.Context) (func(), error) {
	return take(ctx, b.slots)
}

// AcquireKeep waits for one of the keeper's slots.
func (b *BrowserClient) AcquireKeep(ctx context.Context) (func(), error) {
	return take(ctx, b.keepSlots)
}

func take(ctx context.Context, slots chan struct{}) (func(), error) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

// NewBrowserClient points at the runner in config (raposa.browser_addr). A
// browser visit loads a page, its scripts and sometimes its video, so the
// timeout is generous.
func NewBrowserClient(addr string) *BrowserClient {
	if addr == "" {
		addr = "http://127.0.0.1:8086"
	}
	return &BrowserClient{
		addr:      strings.TrimSuffix(addr, "/"),
		http:      &http.Client{Timeout: 120 * time.Second},
		slots:     make(chan struct{}, browserSlots),
		keepSlots: make(chan struct{}, 2),
	}
}

// Visit asks the runner to load one page in a real browser and follow the
// funnel. An unreachable runner comes back as an error, and the ladder records
// the visit as outcome 'error' and moves on.
func (b *BrowserClient) Visit(ctx context.Context, p visitPlan) (*VisitResult, error) {
	body, err := json.Marshal(VisitRequest{
		URL:           p.URL,
		Referer:       p.Referer,
		Proxy:         proxyConfig(p.Line),
		Device:        p.Device,
		Timezone:      p.Timezone,
		LoadAssets:    p.LoadAssets,
		HumanDwell:    p.HumanDwell,
		DwellMs:       p.DwellMs,
		MaxSteps:      p.MaxSteps,
		CaptureAssets: p.CaptureAssets,
		MaxAssetBytes: p.MaxAssetBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("encode browser request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.addr+"/visit", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create browser request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("browser runner at %s is unreachable: %w", b.addr, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read browser answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("browser runner answered %d: %s", resp.StatusCode, firstLine(raw))
	}

	var res VisitResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode browser answer: %w", err)
	}
	// Decode before the OK check, so a failed visit that still captured a
	// step hands back usable bytes rather than base64 strings.
	decodeAssets(&res)
	if !res.OK {
		return &res, fmt.Errorf("browser visit failed: %s", res.Error)
	}
	return &res, nil
}

// proxyConfig is the line in the shape the runner takes. A visit with no line
// goes out direct.
func proxyConfig(line *model.ProxyLine) *ProxyConfig {
	if line == nil || line.Host == "" {
		return nil
	}
	return &ProxyConfig{
		Server:   fmt.Sprintf("http://%s:%d", line.Host, line.Port),
		Username: line.Username,
		Password: line.Password,
	}
}

// decodeAssets turns the runner's base64 into bytes the capture can hash.
func decodeAssets(res *VisitResult) {
	for i := range res.Steps {
		for j := range res.Steps[i].Assets {
			a := &res.Steps[i].Assets[j]
			if a.Base64 == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(a.Base64)
			if err != nil {
				a.SkippedReason = "the runner sent bytes that are not base64"
				continue
			}
			a.Body = raw
			a.Base64 = ""
		}
	}
}

func firstLine(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
