package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
)

// The browser runner (raposa-browser) drives headless Chromium on the other
// side of one HTTP call. It answers in the step shape below, and the plain
// HTTP engine in fetch.go builds the same one, so a single capture path
// stores both.

// visitPlan is one visit as the ladder decided it: what to load, from where,
// as whom.
type visitPlan struct {
	URL        string
	Referer    string
	Line       *lines.Line
	Device     string // desktop or phone
	Timezone   string
	LoadAssets bool
	HumanDwell bool
	DwellMs    int
	MaxSteps   int // how far past the first page to follow the funnel
}

// VisitRequest is the body POSTed to the runner's /visit.
type VisitRequest struct {
	URL        string       `json:"url"`
	Referer    string       `json:"referer"`
	Proxy      *ProxyConfig `json:"proxy"`
	Device     string       `json:"device"`
	Timezone   string       `json:"timezone"`
	LoadAssets bool         `json:"loadAssets"`
	HumanDwell bool         `json:"humanDwell"`
	DwellMs    int          `json:"dwellMs"`
	MaxSteps   int          `json:"maxSteps"`
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
	StepNo      int        `json:"stepNo"`
	ReachedBy   string     `json:"reachedBy"` // landing, redirect, cta
	ClickedText string     `json:"clickedText"`
	ClickedURL  string     `json:"clickedUrl"`
	URL         string     `json:"url"` // where this step ended after redirects
	Status      int        `json:"status"`
	Title       string     `json:"title"`
	HTML        string     `json:"html"`
	Text        string     `json:"text"`
	Hops        []VisitHop `json:"hops"`
}

// VisitHop is one redirect on the way to a step.
type VisitHop struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

// errRunnerBusy says the runner could not take the visit now: it is down,
// restarting, or has no free slot. Nothing about the page was learnt, so the
// visit is tried again later and not recorded.
var errRunnerBusy = errors.New("the browser runner is busy or down")

// BrowserClient talks to the runner.
type BrowserClient struct {
	addr string
	http *http.Client
	// One token per browser visit running at once, and for the keeper. Ten
	// investigations and the keeper each opening a Chromium got the old
	// runner killed for memory four times in an hour on 2026-09-27, and
	// every visit in flight failed. The runner holds the same limits.
	slots     chan struct{}
	keepSlots chan struct{}
}

// Slots the runner is sized for (raposa/browser/runner.js holds the same).
const (
	browserSlots = 2
	keepSlots    = 1
)

// NewBrowserClient points at the runner.
func NewBrowserClient(addr string) *BrowserClient {
	if addr == "" {
		addr = "http://127.0.0.1:8086"
	}
	return &BrowserClient{
		addr:      strings.TrimSuffix(addr, "/"),
		http:      &http.Client{Timeout: 170 * time.Second},
		slots:     make(chan struct{}, browserSlots),
		keepSlots: make(chan struct{}, keepSlots),
	}
}

// TryAcquire takes a free visit slot, or reports false at once. A worker
// holds its investigation for one visit only, so it does not queue behind
// the browser: it puts the investigation back and comes again.
func (b *BrowserClient) TryAcquire() (func(), bool) {
	select {
	case b.slots <- struct{}{}:
		return func() { <-b.slots }, true
	default:
		return nil, false
	}
}

// AcquireKeep waits for the keeper's slot.
func (b *BrowserClient) AcquireKeep(ctx context.Context) (func(), error) {
	select {
	case b.keepSlots <- struct{}{}:
		return func() { <-b.keepSlots }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

// Visit asks the runner to load one page in a real browser and follow the
// funnel. errRunnerBusy comes back when the runner could not take it.
func (b *BrowserClient) Visit(ctx context.Context, p visitPlan) (*VisitResult, error) {
	body, err := json.Marshal(VisitRequest{
		URL:        p.URL,
		Referer:    p.Referer,
		Proxy:      proxyConfig(p.Line),
		Device:     p.Device,
		Timezone:   p.Timezone,
		LoadAssets: p.LoadAssets,
		HumanDwell: p.HumanDwell,
		DwellMs:    p.DwellMs,
		MaxSteps:   p.MaxSteps,
	})
	if err != nil {
		return nil, fmt.Errorf("encode browser request: %w", err)
	}
	raw, err := b.post(ctx, "/visit", body, b.http)
	if err != nil {
		return nil, err
	}
	var res VisitResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode browser answer: %w", err)
	}
	if !res.OK {
		return &res, fmt.Errorf("browser visit failed: %s", res.Error)
	}
	return &res, nil
}

// post sends one request to the runner and reads its answer. A refused
// connection or a 503 is errRunnerBusy.
func (b *BrowserClient) post(ctx context.Context, path string, body []byte, client *http.Client) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.addr+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create browser request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%w: %v", errRunnerBusy, err)
		}
		return nil, fmt.Errorf("browser runner at %s: %w", b.addr, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read browser answer: %w", err)
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("%w: %s", errRunnerBusy, firstLine(raw))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("browser runner answered %d: %s", resp.StatusCode, firstLine(raw))
	}
	return raw, nil
}

// proxyConfig is the line in the shape the runner takes. A visit with no line
// goes out direct.
func proxyConfig(line *lines.Line) *ProxyConfig {
	if line == nil || line.Host == "" {
		return nil
	}
	return &ProxyConfig{
		Server:   "http://" + line.Host + ":" + strconv.Itoa(line.Port),
		Username: line.Username,
		Password: line.Password,
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
