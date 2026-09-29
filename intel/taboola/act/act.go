// Package act is the Taboola write client: the only code here that can
// create, change, pause or delete anything on Taboola. It is for intel-act
// and its tests; nothing else may hold write keys (see intel/README.md).
//
// Every write goes through a Guard, checked before the request leaves:
//
//   - one advertiser account, named up front;
//   - only campaigns and items this client created (kept in a state file, so
//     a later run can still clean them up), and never anything else;
//   - new campaigns are created paused, named with the guard's prefix, bid
//     FIXED under a CPC ceiling, with a total (ENTIRE) budget;
//   - turning campaigns on is refused once their budgets together would pass
//     the guard's money ceiling.
//
// Each request and answer is handed to Record before anything reads it, so
// callers save it raw (decision 0003).
package act

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	tokenPath = "/backstage/oauth/token"
	apiPrefix = "/backstage/api/1.0/"
	uploadAPI = "operations/upload-image"
)

// ErrRefused means the guard stopped a request before it was sent.
var ErrRefused = errors.New("taboola act: refused by guard")

// Guard is what the client may do.
type Guard struct {
	Account    string  // the only advertiser account writes may touch
	NamePrefix string  // every campaign we create is named with it
	MaxCPC     float64 // highest bid allowed
	MaxMoney   float64 // most money all campaigns ever turned on may spend together
	StateFile  string  // ids we created and what we turned on
}

// State is what this client created and turned on. It is saved after every
// change, so a crash never loses track of an object to clean up.
type State struct {
	Campaigns map[string]float64 `json:"campaigns"` // id -> total budget
	Items     map[string]string  `json:"items"`     // item id -> campaign id
	Activated map[string]float64 `json:"activated"` // campaign id -> total budget when turned on
	Deleted   map[string]bool    `json:"deleted"`   // campaign or item ids
}

// Exchange is one request and its answer, for saving raw. The token is never
// in it.
type Exchange struct {
	Time        time.Time
	Method      string
	Path        string
	RequestBody []byte
	Status      int
	Body        []byte
}

// Client writes to one Taboola account within its guard.
type Client struct {
	base, id, secret string
	g                Guard
	http             *http.Client
	Record           func(Exchange)

	mu      sync.Mutex
	st      State
	token   string
	expires time.Time
}

// New loads the guard's state file (if any) and returns a client.
func New(base, clientID, clientSecret string, g Guard) (*Client, error) {
	if g.Account == "" || g.NamePrefix == "" || g.MaxCPC <= 0 || g.MaxMoney <= 0 || g.StateFile == "" {
		return nil, errors.New("taboola act: guard needs account, name prefix, max CPC, max money and state file")
	}
	if strings.HasSuffix(g.Account, "-network") {
		return nil, fmt.Errorf("%w: %s is a network account", ErrRefused, g.Account)
	}
	c := &Client{base: strings.TrimRight(base, "/"), id: clientID, secret: clientSecret, g: g,
		http: &http.Client{Timeout: 2 * time.Minute}, Record: func(Exchange) {}}
	c.st = State{Campaigns: map[string]float64{}, Items: map[string]string{}, Activated: map[string]float64{}, Deleted: map[string]bool{}}
	if b, err := os.ReadFile(g.StateFile); err == nil {
		if err := json.Unmarshal(b, &c.st); err != nil {
			return nil, fmt.Errorf("taboola act: state file: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return c, nil
}

// State returns a copy of what the client created.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := json.Marshal(c.st)
	var s State
	json.Unmarshal(b, &s)
	return s
}

// Obj is a JSON object as Taboola sends or takes it.
type Obj = map[string]any

// CreateCampaign creates a paused campaign. The body must carry the guard's
// name prefix, is_active false, bid_strategy FIXED with cpc within the
// ceiling, and spending_limit_model ENTIRE.
func (c *Client) CreateCampaign(ctx context.Context, body Obj) (Obj, error) {
	if err := c.checkCampaign(body, true); err != nil {
		return nil, err
	}
	out, err := c.send(ctx, http.MethodPost, c.acct("campaigns/"), body)
	if err != nil {
		return out, err
	}
	return out, c.remember(func(s *State) { s.Campaigns[str(out["id"])] = num(out["spending_limit"]) }, out)
}

// UpdateCampaign changes one of our campaigns. Turning it on (is_active
// true) is refused if our campaigns turned on so far, plus this one, could
// spend more than the money ceiling.
func (c *Client) UpdateCampaign(ctx context.Context, id string, body Obj) (Obj, error) {
	if err := c.owned(id, ""); err != nil {
		return nil, err
	}
	if err := c.checkCampaign(body, false); err != nil {
		return nil, err
	}
	budget := c.budget(id, body)
	on, _ := body["is_active"].(bool)
	if on {
		if err := c.checkMoney(id, budget); err != nil {
			return nil, err
		}
	}
	out, err := c.send(ctx, http.MethodPost, c.acct("campaigns/"+url.PathEscape(id)), body)
	if err != nil {
		return out, err
	}
	// Trust Taboola's answer for the budget, but never let a missing field
	// lower what the ceiling counts.
	if b := num(out["spending_limit"]); b > budget {
		budget = b
	}
	return out, c.remember(func(s *State) {
		s.Campaigns[id] = budget
		if on {
			s.Activated[id] = budget
		}
	}, out)
}

// PatchCampaign adds to or removes from one of our campaign's collections
// (blocked sites, bid modifiers).
func (c *Client) PatchCampaign(ctx context.Context, id string, body Obj) (Obj, error) {
	if err := c.owned(id, ""); err != nil {
		return nil, err
	}
	return c.send(ctx, http.MethodPatch, c.acct("campaigns/"+url.PathEscape(id)), body)
}

// DuplicateCampaign copies one of our campaigns, paused, under a name with
// the guard's prefix.
func (c *Client) DuplicateCampaign(ctx context.Context, id string, body Obj) (Obj, error) {
	if err := c.owned(id, ""); err != nil {
		return nil, err
	}
	if body == nil {
		body = Obj{}
	}
	body["is_active"] = false
	if _, ok := body["name"]; !ok {
		return nil, fmt.Errorf("%w: a copy needs its own name starting with %q", ErrRefused, c.g.NamePrefix)
	}
	if err := c.checkCampaign(body, false); err != nil {
		return nil, err
	}
	out, err := c.send(ctx, http.MethodPost, c.acct("campaigns/"+url.PathEscape(id)+"/duplicate/"), body)
	if err != nil {
		return out, err
	}
	return out, c.remember(func(s *State) { s.Campaigns[str(out["id"])] = num(out["spending_limit"]) }, out)
}

// DeleteCampaign deletes (terminates) one of our campaigns.
func (c *Client) DeleteCampaign(ctx context.Context, id string) (Obj, error) {
	if err := c.owned(id, ""); err != nil {
		return nil, err
	}
	out, err := c.send(ctx, http.MethodDelete, c.acct("campaigns/"+url.PathEscape(id)), nil)
	if err != nil {
		return out, err
	}
	return out, c.remember(func(s *State) { s.Deleted[id] = true }, out)
}

// CreateItem creates one item in one of our campaigns from its URL; Taboola
// crawls the page for its title and image.
func (c *Client) CreateItem(ctx context.Context, campaignID string, body Obj) (Obj, error) {
	if err := c.owned(campaignID, ""); err != nil {
		return nil, err
	}
	out, err := c.send(ctx, http.MethodPost, c.acct("campaigns/"+url.PathEscape(campaignID)+"/items/"), body)
	if err != nil {
		return out, err
	}
	return out, c.remember(func(s *State) { s.Items[str(out["id"])] = campaignID }, out)
}

// MassCreateItems creates several items in one of our campaigns in one call,
// without a crawl.
func (c *Client) MassCreateItems(ctx context.Context, campaignID string, items []Obj) (Obj, error) {
	if err := c.owned(campaignID, ""); err != nil {
		return nil, err
	}
	out, err := c.send(ctx, http.MethodPost, c.acct("campaigns/"+url.PathEscape(campaignID)+"/items/mass"), Obj{"collection": items})
	if err != nil {
		return out, err
	}
	return out, c.remember(func(s *State) {
		rows, _ := out["results"].([]any)
		for _, r := range rows {
			if o, ok := r.(Obj); ok {
				s.Items[str(o["id"])] = campaignID
			}
		}
	}, out)
}

// UpdateItem changes one of our items.
func (c *Client) UpdateItem(ctx context.Context, campaignID, itemID string, body Obj) (Obj, error) {
	if err := c.owned(campaignID, itemID); err != nil {
		return nil, err
	}
	return c.send(ctx, http.MethodPost, c.acct("campaigns/"+url.PathEscape(campaignID)+"/items/"+url.PathEscape(itemID)+"/"), body)
}

// DeleteItem deletes one of our items.
func (c *Client) DeleteItem(ctx context.Context, campaignID, itemID string) (Obj, error) {
	if err := c.owned(campaignID, itemID); err != nil {
		return nil, err
	}
	out, err := c.send(ctx, http.MethodDelete, c.acct("campaigns/"+url.PathEscape(campaignID)+"/items/"+url.PathEscape(itemID)), nil)
	if err != nil {
		return out, err
	}
	return out, c.remember(func(s *State) { s.Deleted[itemID] = true }, out)
}

// UploadImage puts an image on Taboola's CDN and returns its URL. It touches
// no campaign.
func (c *Client) UploadImage(ctx context.Context, name string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	// Taboola refuses a part sent as application/octet-stream ("unsupported
	// type of image"), so name the image's own type.
	h.Set("Content-Type", http.DetectContentType(data))
	part, err := w.CreatePart(h)
	if err != nil {
		return "", err
	}
	part.Write(data)
	w.Close()
	out, err := c.do(ctx, http.MethodPost, uploadAPI, buf.Bytes(), w.FormDataContentType(), []byte("(image "+name+")"))
	if err != nil {
		return "", err
	}
	u := str(out["value"])
	if u == "" {
		return "", errors.New("taboola act: upload answered without a URL")
	}
	return u, nil
}

// Get reads anything under the guard's account, or the dictionary.
func (c *Client) Get(ctx context.Context, path string) (Obj, error) {
	p := strings.TrimLeft(path, "/")
	if !strings.HasPrefix(p, c.g.Account+"/") && !strings.HasPrefix(p, "resources/") {
		return nil, fmt.Errorf("%w: GET outside %s: %s", ErrRefused, c.g.Account, path)
	}
	return c.do(ctx, http.MethodGet, p, nil, "", nil)
}

func (c *Client) acct(p string) string { return c.g.Account + "/" + p }

// checkCampaign applies the guard to a campaign body. create is true for a
// new campaign, which must set everything the guard asks for.
func (c *Client) checkCampaign(b Obj, create bool) error {
	refuse := func(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrRefused}, a...)...) }
	if n, ok := b["name"]; ok || create {
		if s, _ := n.(string); !strings.HasPrefix(s, c.g.NamePrefix) {
			return refuse("campaign name must start with %q", c.g.NamePrefix)
		}
	}
	if v, ok := b["is_active"]; create && (!ok || v != false) {
		return refuse("a new campaign must be created with is_active false")
	}
	if v, ok := b["bid_strategy"]; ok || create {
		if v != "FIXED" {
			return refuse("bid_strategy must be FIXED")
		}
	}
	if v, ok := b["cpc"]; ok || create {
		if f := num(v); f <= 0 || f > c.g.MaxCPC {
			return refuse("cpc %v outside (0, %v]", v, c.g.MaxCPC)
		}
	}
	if v, ok := b["spending_limit_model"]; ok || create {
		if v != "ENTIRE" {
			return refuse("spending_limit_model must be ENTIRE")
		}
	}
	if v, ok := b["spending_limit"]; ok || create {
		if f := num(v); f <= 0 || f > c.g.MaxMoney {
			return refuse("spending_limit %v outside (0, %v]", v, c.g.MaxMoney)
		}
	}
	return nil
}

func (c *Client) budget(id string, body Obj) float64 {
	if v, ok := body["spending_limit"]; ok {
		return num(v)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.Campaigns[id]
}

func (c *Client) checkMoney(id string, budget float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := budget
	for other, b := range c.st.Activated {
		if other != id {
			total += b
		}
	}
	if budget <= 0 || total > c.g.MaxMoney {
		return fmt.Errorf("%w: turning on campaign %s would allow $%.2f in total, over $%.2f", ErrRefused, id, total, c.g.MaxMoney)
	}
	return nil
}

func (c *Client) owned(campaignID, itemID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.st.Campaigns[campaignID]; !ok {
		return fmt.Errorf("%w: campaign %s was not created by this client", ErrRefused, campaignID)
	}
	if c.st.Deleted[campaignID] {
		return fmt.Errorf("%w: campaign %s is deleted", ErrRefused, campaignID)
	}
	if itemID != "" && c.st.Items[itemID] != campaignID {
		return fmt.Errorf("%w: item %s was not created by this client in campaign %s", ErrRefused, itemID, campaignID)
	}
	return nil
}

// remember applies a change to the state and saves it at once.
func (c *Client) remember(change func(*State), out Obj) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	change(&c.st)
	delete(c.st.Campaigns, "")
	delete(c.st.Items, "")
	b, _ := json.MarshalIndent(c.st, "", "  ")
	tmp := c.g.StateFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, c.g.StateFile)
}

func (c *Client) send(ctx context.Context, method, path string, body Obj) (Obj, error) {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	return c.do(ctx, method, path, raw, "application/json", raw)
}

// do sends one request. It is the only place requests leave, and it
// re-checks the path: under the guard's account, or the image upload.
func (c *Client) do(ctx context.Context, method, path string, body []byte, ctype string, logged []byte) (Obj, error) {
	if !strings.HasPrefix(path, c.g.Account+"/") && path != uploadAPI && !(method == http.MethodGet && strings.HasPrefix(path, "resources/")) {
		return nil, fmt.Errorf("%w: %s %s is outside account %s", ErrRefused, method, path, c.g.Account)
	}
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+apiPrefix+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	c.Record(Exchange{Time: time.Now().UTC(), Method: method, Path: path, RequestBody: logged, Status: res.StatusCode, Body: resBody})
	var out Obj
	json.Unmarshal(resBody, &out)
	if res.StatusCode/100 != 2 {
		return out, fmt.Errorf("taboola act: %s %s: HTTP %d: %s", method, path, res.StatusCode, snippet(resBody))
	}
	return out, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > time.Minute {
		return c.token, nil
	}
	form := url.Values{"client_id": {c.id}, "client_secret": {c.secret}, "grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return "", fmt.Errorf("taboola act: token request: HTTP %d: %s", res.StatusCode, snippet(b))
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 3600
	}
	c.token, c.expires = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	return c.token, nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
