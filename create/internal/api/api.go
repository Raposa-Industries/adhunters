// Package api is create-web's JSON API: whether generation is on, a plan
// (headlines and image briefs), and one picture at a time. Errors are
// {"error": "<one short pt-BR line>"}.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Raposa-Industries/adhunters/create/internal/openai"
)

// Limits of one request.
const (
	planBodyMax     = 256 << 10
	imageBodyMax    = 60 << 20
	referenceMax    = 10 << 20
	maxReferences   = 6
	maxPromptRunes  = 8000
	maxBriefRunes   = 4000
	maxListItems    = 200
	maxLineRunes    = 500
	maxShortRunes   = 60
	concurrentCalls = 4

	// imageBudget covers the wait for a slot, the retry ladder and a slow
	// picture, and ends inside the server's 6 minute write timeout.
	imageBudget = 5*time.Minute + 30*time.Second
	planBudget  = 3 * time.Minute
)

// Server holds the API's dependencies.
type Server struct {
	ai  *openai.Client
	log *slog.Logger
	// slots allows at most concurrentCalls image calls to OpenAI at once,
	// across every browser, so one page asking for twelve pictures does not
	// meet OpenAI's per-minute limit on its own.
	slots chan struct{}
}

// New returns the API over client.
func New(client *openai.Client, log *slog.Logger) *Server {
	return &Server{ai: client, log: log, slots: make(chan struct{}, concurrentCalls)}
}

// Handler serves the API under /api/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/plan", s.plan)
	mux.HandleFunc("POST /api/image", s.image)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status", "/api/plan", "/api/image":
			writeError(w, http.StatusMethodNotAllowed, "método não permitido")
		default:
			writeError(w, http.StatusNotFound, "rota não encontrada")
		}
	})
	return mux
}

type statusReply struct {
	Generation   bool    `json:"generation"`
	Reason       string  `json:"reason,omitempty"`
	ImageModel   string  `json:"image_model"`
	ImageQuality string  `json:"image_quality"`
	TextModel    string  `json:"text_model"`
	ImageCostUSD float64 `json:"image_cost_usd"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	set := s.ai.Settings()
	writeJSON(w, http.StatusOK, statusReply{
		Generation:   s.ai.Available(),
		Reason:       s.ai.Why(),
		ImageModel:   set.ImageModel,
		ImageQuality: set.ImageQuality,
		TextModel:    set.TextModel,
		ImageCostUSD: s.ai.EstimateImageCost(set.ImageQuality),
	})
}

type planBody struct {
	Prompt           string   `json:"prompt"`
	HeadlineExamples []string `json:"headline_examples"`
	Language         string   `json:"language"`
	Vertical         string   `json:"vertical"`
	Headlines        *int     `json:"headlines"`
	Images           *int     `json:"images"`
	HasReferences    bool     `json:"has_references"`
	Avoid            []string `json:"avoid"`
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	if !s.ai.Available() {
		writeError(w, http.StatusServiceUnavailable, s.ai.Why())
		return
	}
	var body planBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, planBodyMax))
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "pedido grande demais (máximo 256 KB)")
			return
		}
		writeError(w, http.StatusBadRequest, "corpo do pedido não é um JSON válido")
		return
	}
	req, msg := checkPlan(body)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), planBudget)
	defer cancel()
	plan, err := s.ai.Plan(ctx, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// checkPlan defaults and checks a plan body, returning a pt-BR reason when
// it is not acceptable.
func checkPlan(b planBody) (openai.PlanRequest, string) {
	req := openai.PlanRequest{
		Prompt:        strings.TrimSpace(b.Prompt),
		Language:      strings.TrimSpace(b.Language),
		Vertical:      openai.CleanLine(b.Vertical),
		Headlines:     10,
		Images:        6,
		HasReferences: b.HasReferences,
	}
	if req.Prompt == "" {
		return req, "escreva o prompt"
	}
	if utf8.RuneCountInString(req.Prompt) > maxPromptRunes {
		return req, "prompt longo demais (máximo 8000 caracteres)"
	}
	if req.Language == "" {
		req.Language = "en"
	}
	if utf8.RuneCountInString(req.Language) > maxShortRunes || strings.ContainsAny(req.Language, "\r\n") {
		return req, "idioma inválido"
	}
	if utf8.RuneCountInString(req.Vertical) > maxShortRunes {
		return req, "vertical inválida"
	}
	if b.Headlines != nil {
		req.Headlines = *b.Headlines
	}
	if b.Images != nil {
		req.Images = *b.Images
	}
	if req.Headlines < 0 || req.Headlines > openai.MaxHeadlines {
		return req, "headlines deve ficar entre 0 e 30"
	}
	if req.Images < 0 || req.Images > openai.MaxImages {
		return req, "images deve ficar entre 0 e 12"
	}
	if req.Headlines == 0 && req.Images == 0 {
		return req, "peça pelo menos um título ou uma imagem"
	}
	var ok bool
	if req.HeadlineExamples, ok = lines(b.HeadlineExamples); !ok {
		return req, "exemplos de títulos demais ou longos demais"
	}
	if req.Avoid, ok = lines(b.Avoid); !ok {
		return req, "lista avoid grande demais"
	}
	return req, ""
}

// lines cleans a list of short lines, dropping empties.
func lines(in []string) ([]string, bool) {
	if len(in) > maxListItems {
		return nil, false
	}
	var out []string
	for _, l := range in {
		l = openai.CleanLine(l)
		if l == "" {
			continue
		}
		if utf8.RuneCountInString(l) > maxLineRunes {
			return nil, false
		}
		out = append(out, l)
	}
	return out, true
}

type imageReply struct {
	Image   string  `json:"image"`
	MIME    string  `json:"mime"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	CostUSD float64 `json:"cost_usd"`
	Model   string  `json:"model"`
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	if !s.ai.Available() {
		writeError(w, http.StatusServiceUnavailable, s.ai.Why())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, imageBodyMax)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "pedido grande demais (máximo 60 MB)")
			return
		}
		writeError(w, http.StatusBadRequest, "envie o pedido como multipart/form-data")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	req := openai.ImageRequest{
		Brief:   strings.TrimSpace(r.FormValue("brief")),
		Quality: strings.TrimSpace(r.FormValue("quality")),
	}
	if req.Brief == "" {
		writeError(w, http.StatusBadRequest, "escreva o brief da imagem")
		return
	}
	if utf8.RuneCountInString(req.Brief) > maxBriefRunes {
		writeError(w, http.StatusBadRequest, "brief longo demais (máximo 4000 caracteres)")
		return
	}
	if req.Quality != "" && !openai.ValidQuality(req.Quality) {
		writeError(w, http.StatusBadRequest, "qualidade deve ser low, medium ou high")
		return
	}
	files := r.MultipartForm.File["reference"]
	if len(files) > maxReferences {
		writeError(w, http.StatusBadRequest, "no máximo 6 imagens de referência")
		return
	}
	for i, fh := range files {
		if fh.Size > referenceMax {
			writeError(w, http.StatusBadRequest, "cada imagem de referência pode ter no máximo 10 MB")
			return
		}
		f, err := fh.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "não consegui ler uma imagem de referência")
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, referenceMax+1))
		_ = f.Close()
		if err != nil || len(data) > referenceMax {
			writeError(w, http.StatusBadRequest, "não consegui ler uma imagem de referência")
			return
		}
		// The type is read from the bytes, never from the browser's label.
		switch mime := http.DetectContentType(data); mime {
		case "image/jpeg", "image/png":
			req.References = append(req.References, openai.Reference{Data: data, MIME: mime})
		case "image/webp":
			writeError(w, http.StatusBadRequest, "referência "+strconv.Itoa(i+1)+" é WebP: converta para JPEG ou PNG")
			return
		default:
			writeError(w, http.StatusBadRequest, "referência "+strconv.Itoa(i+1)+" não é JPEG nem PNG")
			return
		}
	}

	// Waiting for a slot ends when the browser leaves: nobody is there to
	// take the picture. Once the call starts it is detached from the browser,
	// so a picture being paid for is still made and kept if the tab closes.
	select {
	case s.slots <- struct{}{}:
	case <-r.Context().Done():
		writeError(w, http.StatusServiceUnavailable, "pedido cancelado enquanto esperava a vez")
		return
	}
	defer func() { <-s.slots }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), imageBudget)
	defer cancel()
	img, err := s.ai.Image(ctx, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imageReply{
		Image:   base64.StdEncoding.EncodeToString(img.Data),
		MIME:    img.MIME,
		Width:   img.Width,
		Height:  img.Height,
		CostUSD: img.Cost,
		Model:   img.Model,
	})
}

// fail maps a client error to its status: 402 when the credit ran out, 503
// with no key, 500 when a paid reply could not be kept, 502 for the rest.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var e *openai.Error
	switch {
	case openai.IsOutOfCredit(err):
		writeError(w, http.StatusPaymentRequired, openai.OutOfCreditMessage)
	case errors.Is(err, openai.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, s.ai.Why())
	case errors.Is(err, openai.ErrKeep):
		writeError(w, http.StatusInternalServerError, openai.ErrKeep.Error())
	case errors.As(err, &e):
		s.log.Warn("openai call failed", "status", e.Status, "err", e.Message)
		writeError(w, http.StatusBadGateway, e.Message)
	default:
		s.log.Error("openai call failed", "err", err)
		writeError(w, http.StatusBadGateway, "falha ao falar com a OpenAI")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
