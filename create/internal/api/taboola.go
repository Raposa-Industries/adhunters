package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	taboola "github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// Limits of the Taboola requests.
const (
	campaignBodyMax = 64 << 10
	adsBodyMax      = 400 << 20
	adsFormMemory   = 32 << 20
	adImageMax      = 5 << 20
	maxAdImages     = 100
	maxAds          = 500
	maxAdCampaigns  = 10

	taboolaReadBudget   = 30 * time.Second
	taboolaCreateBudget = 2 * time.Minute
	// adsBudget covers every upload and every mass create of one send, and
	// ends inside the server's 11 minute write timeout.
	adsBudget = 10 * time.Minute
)

// WithTaboola gives the API a Taboola client; nil (or one without
// credentials) leaves the Taboola routes answering that it is off.
func (s *Server) WithTaboola(tb *taboola.Client) *Server {
	s.tb = tb
	return s
}

type taboolaStatusReply struct {
	Connected   bool              `json:"connected"`
	Reason      string            `json:"reason,omitempty"`
	Accounts    []taboola.Account `json:"accounts"`
	MaxCPC      float64           `json:"max_cpc"`
	MaxDailyCap float64           `json:"max_daily_cap"`
	// OnlyOwn is a lent account: only campaigns and groups made here, named
	// with NamePrefix, are listed or touched.
	OnlyOwn    bool   `json:"only_own"`
	NamePrefix string `json:"name_prefix,omitempty"`
}

func (s *Server) taboolaStatus(w http.ResponseWriter, r *http.Request) {
	reply := taboolaStatusReply{Connected: s.tb.Available(), Reason: s.tb.Why(), Accounts: []taboola.Account{}}
	reply.MaxCPC, reply.MaxDailyCap = s.tb.Limits()
	reply.OnlyOwn, reply.NamePrefix = s.tb.OnlyOwn()
	if reply.Connected {
		ctx, cancel := context.WithTimeout(r.Context(), taboolaReadBudget)
		defer cancel()
		if accts, err := s.tb.Accounts(ctx); err == nil {
			reply.Accounts = accts
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

func (s *Server) taboolaCampaigns(w http.ResponseWriter, r *http.Request) {
	if !s.tb.Available() {
		writeError(w, http.StatusServiceUnavailable, s.tb.Why())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), taboolaReadBudget)
	defer cancel()
	list, err := s.tb.Campaigns(ctx, strings.TrimSpace(r.URL.Query().Get("account")))
	if err != nil {
		s.taboolaFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type campaignBody struct {
	Account            string   `json:"account"`
	Name               string   `json:"name"`
	Brand              string   `json:"brand"`
	CPC                float64  `json:"cpc"`
	DailyCap           float64  `json:"daily_cap"`
	SpendingLimit      float64  `json:"spending_limit"`
	Countries          []string `json:"countries"`
	Platforms          []string `json:"platforms"`
	TrackingCode       string   `json:"tracking_code"`
	MarketingObjective string   `json:"marketing_objective"`
	BidStrategy        string   `json:"bid_strategy"`
	StartDate          string   `json:"start_date"`
	EndDate            string   `json:"end_date"`
	GroupID            string   `json:"group_id"`
	// CopyFrom makes the campaign a copy of this one (without its ads), with
	// whatever else is set in place of the copy's.
	CopyFrom string `json:"copy_from"`
}

func (s *Server) taboolaCreateCampaign(w http.ResponseWriter, r *http.Request) {
	if !s.tb.Available() {
		writeError(w, http.StatusServiceUnavailable, s.tb.Why())
		return
	}
	var b campaignBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, campaignBodyMax)).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "corpo do pedido não é um JSON de campanha válido")
		return
	}
	if len(b.Countries) > 250 || len(b.Platforms) > 3 {
		writeError(w, http.StatusBadRequest, "países ou plataformas demais")
		return
	}
	n := taboola.NewCampaign{
		Name: b.Name, Brand: b.Brand, CPC: b.CPC, DailyCap: b.DailyCap, SpendingLimit: b.SpendingLimit,
		Countries: b.Countries, Platforms: b.Platforms, TrackingCode: b.TrackingCode,
		MarketingObjective: b.MarketingObjective, BidStrategy: b.BidStrategy,
		StartDate: b.StartDate, EndDate: b.EndDate, GroupID: b.GroupID,
	}
	// Once sent, the create finishes even if the tab closes: a campaign
	// half-known is worse than one made and shown later in the list.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), taboolaCreateBudget)
	defer cancel()
	account := strings.TrimSpace(b.Account)
	var cp taboola.Campaign
	var err error
	if from := strings.TrimSpace(b.CopyFrom); from != "" {
		cp, err = s.tb.DuplicateCampaign(ctx, account, from, n)
	} else {
		cp, err = s.tb.CreateCampaign(ctx, account, n)
	}
	if err != nil {
		s.taboolaFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cp)
}

func (s *Server) taboolaGroups(w http.ResponseWriter, r *http.Request) {
	if !s.tb.Available() {
		writeError(w, http.StatusServiceUnavailable, s.tb.Why())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), taboolaReadBudget)
	defer cancel()
	list, err := s.tb.Groups(ctx, strings.TrimSpace(r.URL.Query().Get("account")))
	if err != nil {
		s.taboolaFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// workbookReply is the account's part of Realize's bulk template, which the
// page writes into its built-in base template: the network account id
// (METADATA's accountName), the accounts and the campaign group names.
type workbookReply struct {
	Network  string            `json:"network"`
	Accounts []taboola.Account `json:"accounts"`
	Groups   []string          `json:"groups"`
}

func (s *Server) taboolaWorkbook(w http.ResponseWriter, r *http.Request) {
	if !s.tb.Available() {
		writeError(w, http.StatusServiceUnavailable, s.tb.Why())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), taboolaReadBudget)
	defer cancel()
	network, accounts, err := s.tb.Directory(ctx)
	if err != nil {
		s.taboolaFail(w, err)
		return
	}
	reply := workbookReply{Network: network, Accounts: accounts, Groups: []string{}}
	if reply.Accounts == nil {
		reply.Accounts = []taboola.Account{}
	}
	usable, err := s.tb.Accounts(ctx)
	if err != nil {
		s.taboolaFail(w, err)
		return
	}
	for _, a := range usable {
		groups, err := s.tb.Groups(ctx, a.ID)
		if err != nil {
			s.taboolaFail(w, err)
			return
		}
		for _, g := range groups {
			reply.Groups = append(reply.Groups, g.Name)
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

type groupBody struct {
	Account            string  `json:"account"`
	Name               string  `json:"name"`
	SpendingLimit      float64 `json:"spending_limit"`
	SpendingLimitModel string  `json:"spending_limit_model"`
	MarketingObjective string  `json:"marketing_objective"`
}

func (s *Server) taboolaCreateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.tb.Available() {
		writeError(w, http.StatusServiceUnavailable, s.tb.Why())
		return
	}
	var b groupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, campaignBodyMax)).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "corpo do pedido não é um JSON de grupo válido")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), taboolaCreateBudget)
	defer cancel()
	g, err := s.tb.CreateGroup(ctx, strings.TrimSpace(b.Account), taboola.NewGroup{
		Name: b.Name, SpendingLimit: b.SpendingLimit, Model: b.SpendingLimitModel, MarketingObjective: b.MarketingObjective,
	})
	if err != nil {
		s.taboolaFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

type adIn struct {
	Image       *int   `json:"image"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CTA         string `json:"cta"`
	URL         string `json:"url"`
	CustomID    string `json:"custom_id"`
	AI          bool   `json:"ai"`
}

type adsReply struct {
	ImagesUploaded int              `json:"images_uploaded"`
	Results        []campaignResult `json:"results"`
}

type campaignResult struct {
	CampaignID string         `json:"campaign_id"`
	Created    []taboola.Item `json:"created"`
	Error      string         `json:"error,omitempty"`
}

// adImage is one uploaded file an ad points at. Its bytes stay in the
// request's temporary file until they are sent, so a send of many images
// holds one at a time in memory.
type adImage struct {
	file *multipart.FileHeader
	url  string
}

func (s *Server) taboolaAds(w http.ResponseWriter, r *http.Request) {
	if !s.tb.Available() {
		writeError(w, http.StatusServiceUnavailable, s.tb.Why())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, adsBodyMax)
	if err := r.ParseMultipartForm(adsFormMemory); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "pedido grande demais (máximo 400 MB)")
			return
		}
		writeError(w, http.StatusBadRequest, "envie o pedido como multipart/form-data")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	field := func(name string) string {
		if v := r.MultipartForm.Value[name]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}

	account := field("account")
	if err := s.tb.CheckAccount(account); err != nil {
		s.taboolaFail(w, err)
		return
	}
	campaigns, msg := campaignIDs(field("campaigns"))
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	for _, id := range campaigns {
		if err := s.tb.CheckCampaign(account, id); err != nil {
			s.taboolaFail(w, err)
			return
		}
	}
	var ads []adIn
	if err := json.Unmarshal([]byte(field("ads")), &ads); err != nil {
		writeError(w, http.StatusBadRequest, "ads deve ser uma lista JSON de anúncios")
		return
	}
	if len(ads) == 0 || len(ads) > maxAds {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("envie de 1 a %d anúncios", maxAds))
		return
	}
	files := r.MultipartForm.File["image"]
	if len(files) == 0 || len(files) > maxAdImages {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("envie de 1 a %d imagens", maxAdImages))
		return
	}
	for i, a := range ads {
		if a.Image == nil || *a.Image < 0 || *a.Image >= len(files) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("anúncio %d: imagem inexistente", i+1))
			return
		}
		if err := toItem(a, "").Check(); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("anúncio %d: %s", i+1, err.Error()))
			return
		}
	}
	// Only the images an ad uses are read and sent.
	images := make([]*adImage, len(files))
	for _, a := range ads {
		i := *a.Image
		if images[i] != nil {
			continue
		}
		if _, msg := readAdImage(files[i]); msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("imagem %d: %s", i+1, msg))
			return
		}
		images[i] = &adImage{file: files[i]}
	}

	// From here on things are made on Taboola, so the send finishes even if
	// the tab closes: what was made is logged and kept either way.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), adsBudget)
	defer cancel()
	reply := adsReply{Results: []campaignResult{}}
	// One at a time: Taboola allows about 84 requests a minute.
	for i, img := range images {
		if img == nil {
			continue
		}
		data, msg := readAdImage(img.file)
		if msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("imagem %d: %s", i+1, msg))
			return
		}
		u, err := s.tb.UploadImage(ctx, img.file.Filename, data)
		if err != nil {
			s.log.Warn("taboola image upload failed", "image", i+1, "uploaded", reply.ImagesUploaded, "err", err)
			status, msg := taboolaError(err)
			writeError(w, status, fmt.Sprintf("imagem %d: %s", i+1, msg))
			return
		}
		img.url = u
		reply.ImagesUploaded++
	}
	items := make([]taboola.NewItem, len(ads))
	for i, a := range ads {
		items[i] = toItem(a, images[*a.Image].url)
	}
	var made, failed int
	for _, id := range campaigns {
		res := campaignResult{CampaignID: id, Created: []taboola.Item{}}
		created, err := s.tb.MassCreateItems(ctx, account, id, items)
		if created != nil {
			res.Created = created
		}
		if err != nil {
			_, res.Error = taboolaError(err)
			failed++
		}
		made += len(res.Created)
		reply.Results = append(reply.Results, res)
	}
	s.log.Info("taboola ads sent", "account", account, "campaigns", len(campaigns), "ads", len(ads),
		"images", reply.ImagesUploaded, "items_made", made, "campaigns_failed", failed)
	writeJSON(w, http.StatusOK, reply)
}

// campaignIDs reads the campaigns field: a JSON list of 1 to 10 Taboola
// campaign ids, as strings or numbers. Repeats are dropped.
func campaignIDs(raw string) ([]string, string) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var list []any
	if err := dec.Decode(&list); err != nil {
		return nil, "campaigns deve ser uma lista JSON de ids de campanha"
	}
	var out []string
	seen := map[string]bool{}
	for _, v := range list {
		var id string
		switch x := v.(type) {
		case string:
			id = strings.TrimSpace(x)
		case json.Number:
			id = x.String()
		default:
			return nil, "campaigns deve ser uma lista de ids de campanha"
		}
		if err := taboola.CheckCampaignID(id); err != nil {
			return nil, err.Error()
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 || len(out) > maxAdCampaigns {
		return nil, fmt.Sprintf("escolha de 1 a %d campanhas", maxAdCampaigns)
	}
	return out, ""
}

// readAdImage reads one uploaded image and checks its size and type.
func readAdImage(fh *multipart.FileHeader) ([]byte, string) {
	if fh.Size > adImageMax {
		return nil, "maior que 5 MB"
	}
	f, err := fh.Open()
	if err != nil {
		return nil, "ilegível"
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(f, adImageMax+1)); err != nil {
		return nil, "ilegível"
	}
	if buf.Len() > adImageMax {
		return nil, "maior que 5 MB"
	}
	// The type is read from the bytes, never from the browser's label.
	switch http.DetectContentType(buf.Bytes()) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return nil, "não é JPEG, PNG, GIF nem WebP"
	}
	return buf.Bytes(), ""
}

func toItem(a adIn, thumb string) taboola.NewItem {
	return taboola.NewItem{
		URL: a.URL, Title: a.Title, Description: a.Description, ThumbnailURL: thumb,
		CTA: strings.ToUpper(strings.TrimSpace(a.CTA)), CustomID: a.CustomID, AI: a.AI,
	}
}

// taboolaError maps a Taboola client error to a status and one pt-BR line:
// 400 refused by the guard, 503 off, 500 an answer that could not be kept,
// 504 out of time, 502 anything Taboola said.
func taboolaError(err error) (int, string) {
	var refused *taboola.Refused
	status := http.StatusBadGateway
	switch {
	case errors.As(err, &refused):
		status = http.StatusBadRequest
	case errors.Is(err, taboola.ErrNotConfigured):
		status = http.StatusServiceUnavailable
	case errors.Is(err, taboola.ErrKeep):
		status = http.StatusInternalServerError
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	}
	return status, taboola.Message(err)
}

func (s *Server) taboolaFail(w http.ResponseWriter, err error) {
	status, msg := taboolaError(err)
	switch {
	case status == http.StatusServiceUnavailable:
		msg = s.tb.Why()
	case status >= 500:
		s.log.Warn("taboola call failed", "status", status, "err", err)
	}
	writeError(w, status, msg)
}
