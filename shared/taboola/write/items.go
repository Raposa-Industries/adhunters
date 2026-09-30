package write

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	api "github.com/Raposa-Industries/adhunters/shared/taboola"
)

// UploadImage puts an image on Taboola's CDN and returns its URL. It touches
// no campaign.
func (c *Client) UploadImage(ctx context.Context, name string, data []byte) (string, error) {
	if !c.Available() {
		return "", ErrNotConfigured
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		name = "image"
	}
	form, ctype, err := api.ImageForm(name, data)
	if err != nil {
		return "", err
	}
	b, err := c.do(ctx, call{
		method: http.MethodPost, path: uploadPath, body: form, ctype: ctype,
		kept:     fmt.Sprintf("(image %s, %d bytes)", name, len(data)),
		retry5xx: true,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(b, &out) != nil || !strings.HasPrefix(out.Value, "http") {
		return "", &Error{Status: http.StatusOK, Message: "a Taboola recebeu a imagem mas não devolveu o endereço dela"}
	}
	return out.Value, nil
}

// NewItem is one ad to make in a campaign.
type NewItem struct {
	URL          string
	Title        string
	Description  string
	ThumbnailURL string
	// CTA is Taboola's cta_type (LEARN_MORE, SHOP_NOW…); "" or NONE sends
	// none.
	CTA      string
	CustomID string
	// AI marks the ad as AI-made (ai_disclosure AI_GENERATED).
	AI bool
}

// Item is one ad Taboola made. Paused is true once Taboola has said the
// item is not active (is_active false).
type Item struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	CustomID string `json:"custom_id"`
	Paused   bool   `json:"paused"`
}

// Limits of an ad.
const (
	MaxTitle       = 100 // Taboola's template says under 100
	MaxDescription = 1000
	MaxCustomID    = 30 // Taboola's Custom ID
	// MassChunk is the most items sent in one mass create.
	MassChunk = 50
)

var (
	link    = regexp.MustCompile(`^https?://[^\s]+$`)
	ctaType = regexp.MustCompile(`^[A-Z][A-Z_]{1,39}$`)
)

// Check refuses an ad Taboola would take badly. The thumbnail is not
// checked: it is filled once the image is uploaded.
func (it NewItem) Check() error {
	title := strings.TrimSpace(it.Title)
	switch {
	case title == "":
		return refuse("título vazio")
	case utf8.RuneCountInString(title) > MaxTitle:
		return refuse("título com mais de %d caracteres", MaxTitle)
	case strings.ContainsAny(title, "\r\n"):
		return refuse("título com quebra de linha")
	case utf8.RuneCountInString(it.Description) > MaxDescription:
		return refuse("descrição com mais de %d caracteres", MaxDescription)
	}
	u := strings.TrimSpace(it.URL)
	switch {
	case u == "":
		return refuse("falta o link")
	case !link.MatchString(u):
		return refuse("o link deve começar com http:// ou https:// e não ter espaços")
	case strings.ContainsAny(u, "{}"):
		// Taboola escapes macros in an item's link; they belong in the
		// campaign's tracking code.
		return refuse("o link não pode ter {macros}: coloque-as no código de rastreamento da campanha")
	}
	if cta := strings.TrimSpace(it.CTA); cta != "" && cta != "NONE" && !ctaType.MatchString(cta) {
		return refuse("CTA %q inválido", oneLine(cta, 40))
	}
	if id := strings.TrimSpace(it.CustomID); utf8.RuneCountInString(id) > MaxCustomID || strings.ContainsAny(id, "\r\n") {
		return refuse("Custom ID com mais de %d caracteres", MaxCustomID)
	}
	return nil
}

func (it NewItem) body() obj {
	b := obj{
		"url":           strings.TrimSpace(it.URL),
		"title":         strings.TrimSpace(it.Title),
		"thumbnail_url": it.ThumbnailURL,
		// New items start active by default; an ad made here only runs once
		// a person turns it on in Taboola's dashboard.
		"is_active": false,
	}
	if d := strings.TrimSpace(it.Description); d != "" {
		b["description"] = d
	}
	if cta := strings.TrimSpace(it.CTA); cta != "" && cta != "NONE" {
		b["cta"] = obj{"cta_type": cta}
	}
	if id := strings.TrimSpace(it.CustomID); id != "" {
		b["custom_data"] = obj{"custom_id": id}
	}
	if it.AI {
		b["ai_disclosure"] = obj{"status": "AI_GENERATED"}
	}
	return b
}

// MassCreateItems makes paused items in one campaign, at most MassChunk per
// call. Every item is checked before the first call. Each is sent with
// is_active false, and any Taboola answers as not paused is paused at once
// (is_active false on an item waiting for approval pauses it once approved,
// proven 2026-09-29). When a call fails, the items made before it are
// returned with the error; an item that could not be paused is named in the
// error. A create is never repeated after a 5xx, which may have made its
// items.
func (c *Client) MassCreateItems(ctx context.Context, account, campaign string, items []NewItem) ([]Item, error) {
	if err := c.CheckAccount(account); err != nil {
		return nil, err
	}
	if err := CheckCampaignID(campaign); err != nil {
		return nil, err
	}
	if err := c.checkOwnCampaign(account, campaign); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, refuse("nenhum anúncio para criar")
	}
	for i, it := range items {
		if err := it.Check(); err != nil {
			return nil, refuse("anúncio %d: %s", i+1, err.Error())
		}
		if !link.MatchString(it.ThumbnailURL) {
			return nil, refuse("anúncio %d: imagem sem endereço", i+1)
		}
	}
	chunks := (len(items) + MassChunk - 1) / MassChunk
	made := []Item{}
	var unpaused []string
	var failed error
	for n := 0; n < chunks; n++ {
		part := items[n*MassChunk : min((n+1)*MassChunk, len(items))]
		coll := make([]obj, len(part))
		for i, it := range part {
			coll[i] = it.body()
		}
		out, err := c.sendJSON(ctx, http.MethodPost, campaignPath(account, campaign)+"/items/mass", obj{"collection": coll}, false)
		if err != nil {
			if e, ok := err.(*Error); ok && chunks > 1 {
				err = &Error{Status: e.Status, Message: "lote " + strconv.Itoa(n+1) + " de " + strconv.Itoa(chunks) + ": " + e.Message}
			}
			failed = err
			break
		}
		for _, r := range results(out) {
			cd, _ := r["custom_data"].(obj)
			it := Item{ID: str(r["id"]), Title: str(r["title"]), Status: str(r["status"]), CustomID: str(cd["custom_id"]), Paused: r["is_active"] == false}
			if !it.Paused {
				if err := c.pauseItem(ctx, account, campaign, it.ID); err != nil {
					unpaused = append(unpaused, "anúncio "+orNone(it.ID)+" criado mas não pausado: "+Message(err))
				} else {
					it.Paused = true
				}
			}
			made = append(made, it)
		}
	}
	c.log.Info("taboola items created", "account", account, "campaign", campaign, "sent", len(items), "made", len(made),
		"not_paused", len(unpaused), "failed", failed != nil)
	if len(unpaused) == 0 {
		return made, failed
	}
	c.log.Error("taboola items left active", "account", account, "campaign", campaign, "count", len(unpaused))
	msg := strings.Join(unpaused[:min(3, len(unpaused))], "; ")
	if len(unpaused) > 3 {
		msg += fmt.Sprintf("; e mais %d", len(unpaused)-3)
	}
	status := 0
	if failed != nil {
		msg = Message(failed) + "; " + msg
		if e, ok := failed.(*Error); ok {
			status = e.Status
		}
	}
	return made, &Error{Status: status, Message: msg}
}

// pauseItem sets is_active false on one item and checks Taboola's answer.
// Repeating it is harmless, so a 5xx is retried.
func (c *Client) pauseItem(ctx context.Context, account, campaign, id string) error {
	if id == "" {
		return &Error{Message: "a Taboola não devolveu o id do anúncio"}
	}
	out, err := c.sendJSON(ctx, http.MethodPost, campaignPath(account, campaign)+"/items/"+url.PathEscape(id)+"/", obj{"is_active": false}, true)
	if err != nil {
		return err
	}
	if out["is_active"] != false {
		return &Error{Status: http.StatusOK, Message: "a Taboola não confirmou a pausa"}
	}
	return nil
}

func orNone(id string) string {
	if id == "" {
		return "(sem id)"
	}
	return id
}
