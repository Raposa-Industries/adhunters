package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig reads the answer's size
	_ "image/png"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"
)

// ImageSize is the default canvas (Sizes[0]): 16:9, Taboola's preferred
// thumbnail shape, both edges multiples of 16 as the endpoint requires, and
// above the 1200x674 Taboola recommends. A request may ask for another of
// Sizes.
const ImageSize = "1600x896"

// Qualities are the image qualities a call may ask for.
var Qualities = []string{"low", "medium", "high"}

// ValidQuality reports whether q is one of Qualities.
func ValidQuality(q string) bool {
	for _, v := range Qualities {
		if q == v {
			return true
		}
	}
	return false
}

// imageTokens is the image-output token count of ONE picture by quality, used
// only when a reply carries no usage. OpenAI bills GPT Image by token and
// publishes no per-image price for the 2.5 snapshots, so these were MEASURED
// by the old tool (auto-creative, 2026-09-18) on its 1K canvas (1504x1008,
// close to ours) by reading usage.output_tokens off real replies. The count
// depends on canvas and quality, not on the model.
var imageTokens = map[string]int{"low": 155, "medium": 338, "high": 1351}

// Reference is one picture sent with a brief.
type Reference struct {
	Data []byte
	MIME string // image/jpeg or image/png
}

// ImageRequest is one picture to make.
type ImageRequest struct {
	Brief string
	// Quality is low, medium or high; empty means the setting.
	Quality    string
	References []Reference
	// Size is the picture's size; zero means the default (landscape).
	Size Size
}

// Image is one picture made and kept.
type Image struct {
	Data   []byte
	MIME   string
	Width  int
	Height int
	Cost   float64
	Model  string
	// Kept is where the picture was saved.
	Kept string
}

// Usage is what OpenAI counted for one call. The images endpoints name the
// fields input/output_tokens; chat completions prompt/completion_tokens.
type Usage struct {
	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type imageReply struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
	Usage *Usage `json:"usage"`
}

// ImageCost is the price of one picture from its usage; with no usage, the
// measured output-token table at quality stands in (estimated is then true).
func (c *Client) ImageCost(u *Usage, quality string) (usd float64, estimated bool) {
	if u != nil && (u.InputTokens > 0 || u.OutputTokens > 0) {
		return float64(u.InputTokens)*c.s.ImagePriceIn/1e6 + float64(u.OutputTokens)*c.s.ImagePriceOut/1e6, false
	}
	return c.EstimateImageCost(quality), true
}

// EstimateImageCost is what one picture at quality should cost, from the
// measured table; an unknown quality is priced at the dear end.
func (c *Client) EstimateImageCost(quality string) float64 {
	tokens, ok := imageTokens[quality]
	if !ok {
		tokens = imageTokens["high"]
	}
	return float64(tokens) * c.s.ImagePriceOut / 1e6
}

// imageKept is the sidecar kept beside each picture.
type imageKept struct {
	Kind     string    `json:"kind"`
	Time     time.Time `json:"time"`
	Endpoint string    `json:"endpoint"`
	Model    string    `json:"model"`
	Quality  string    `json:"quality"`
	Size     string    `json:"size"`
	// FitTo is the size the kept picture was then cut to, when the model
	// could not make it itself; the picture kept here is the model's own.
	FitTo           string          `json:"fit_to,omitempty"`
	Brief           string          `json:"brief"`
	Prompt          string          `json:"prompt"`
	References      int             `json:"references"`
	ReferenceSHA256 []string        `json:"reference_sha256,omitempty"`
	Usage           *Usage          `json:"usage"`
	CostUSD         float64         `json:"cost_usd"`
	CostEstimated   bool            `json:"cost_estimated"`
	MIME            string          `json:"mime"`
	Width           int             `json:"width"`
	Height          int             `json:"height"`
	SHA256          string          `json:"sha256"`
	Reply           json.RawMessage `json:"reply"`
}

// Image makes one picture. With references it goes to /v1/images/edits, the
// one OpenAI image endpoint that accepts pictures; without, to
// /v1/images/generations. The picture is kept before it is returned.
func (c *Client) Image(ctx context.Context, r ImageRequest) (Image, error) {
	quality := r.Quality
	if quality == "" {
		quality = c.s.ImageQuality
	}
	size := r.Size
	if size.Width == 0 || size.Height == 0 {
		size = Sizes[0]
	}
	native := size.Native()
	prompt := imagePrompt(r.Brief, len(r.References), size)

	var path, contentType string
	var body []byte
	var err error
	if len(r.References) > 0 {
		path = "/v1/images/edits"
		body, contentType, err = c.editBody(prompt, quality, native.String(), r.References)
	} else {
		path = "/v1/images/generations"
		contentType = "application/json"
		body, err = json.Marshal(map[string]any{
			"model": c.s.ImageModel, "prompt": prompt, "n": 1, "size": native.String(),
			"quality": quality, "output_format": "jpeg", "output_compression": 90,
		})
	}
	if err != nil {
		return Image{}, err
	}

	raw, err := c.call(ctx, path, contentType, body)
	if err != nil {
		return Image{}, err
	}
	var reply imageReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Image{}, &Error{Status: http.StatusOK, Message: "resposta da OpenAI ilegível"}
	}
	// A 2xx reply is billed whether or not the picture in it is usable, so
	// it is counted before anything else can fail.
	cost, estimated := c.ImageCost(reply.Usage, quality)
	c.spent(cost)

	if len(reply.Data) == 0 || reply.Data[0].B64JSON == "" {
		return Image{}, &Error{Status: http.StatusOK, Message: "a OpenAI não devolveu imagem"}
	}
	data, err := base64.StdEncoding.DecodeString(reply.Data[0].B64JSON)
	if err != nil {
		return Image{}, &Error{Status: http.StatusOK, Message: "imagem da OpenAI ilegível"}
	}
	img := Image{Data: data, MIME: http.DetectContentType(data), Cost: cost, Model: c.s.ImageModel}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		img.Width, img.Height = cfg.Width, cfg.Height
	}
	ext := ".jpg"
	switch img.MIME {
	case "image/jpeg":
	case "image/png":
		ext = ".png"
	default:
		ext = ".bin"
	}

	refSums := make([]string, len(r.References))
	for i, ref := range r.References {
		refSums[i] = sha256hex(ref.Data)
	}
	fitTo := ""
	if native != size {
		fitTo = size.String()
	}
	kept, err := c.keep.Image(data, ext, imageKept{
		Kind: "image", Time: time.Now().UTC(), Endpoint: path, Model: c.s.ImageModel,
		Quality: quality, Size: native.String(), FitTo: fitTo, Brief: r.Brief, Prompt: prompt,
		References: len(r.References), ReferenceSHA256: refSums,
		Usage: reply.Usage, CostUSD: cost, CostEstimated: estimated,
		MIME: img.MIME, Width: img.Width, Height: img.Height, SHA256: sha256hex(data),
		Reply: withoutPicture(raw),
	})
	if err != nil {
		c.log.Error("image not kept, not handed back", "err", err, "cost_usd", cost)
		return Image{}, fmt.Errorf("%w: %v", ErrKeep, err)
	}
	img.Kept = kept
	if img.MIME != "image/jpeg" && img.MIME != "image/png" {
		return Image{}, &Error{Status: http.StatusOK, Message: "a OpenAI devolveu um arquivo que não é imagem"}
	}
	// A size the model cannot make is cut from the model's picture, which
	// is kept above as it came.
	if fitTo != "" {
		fitted, err := fit(data, size.Width, size.Height)
		if err != nil {
			return Image{}, &Error{Status: http.StatusOK, Message: "não deu para cortar a imagem em " + fitTo + ": " + truncate(err.Error(), 120)}
		}
		img.Data, img.MIME, img.Width, img.Height = fitted, "image/jpeg", size.Width, size.Height
	}
	c.log.Info("image made", "model", img.Model, "quality", quality, "references", len(r.References),
		"cost_usd", cost, "cost_estimated", estimated, "kept", kept)
	return img, nil
}

// editBody is /v1/images/edits' multipart form. The pictures go in as
// repeated image[] parts in the order they were chosen, so a brief can say
// "the first one".
func (c *Client) editBody(prompt, quality, size string, refs []Reference) ([]byte, string, error) {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	fields := [][2]string{
		{"model", c.s.ImageModel}, {"prompt", prompt}, {"n", "1"}, {"size", size},
		{"quality", quality}, {"output_format", "jpeg"}, {"output_compression", "90"},
	}
	for _, f := range fields {
		if err := form.WriteField(f[0], f[1]); err != nil {
			return nil, "", err
		}
	}
	// CreateFormFile would label the part application/octet-stream, which
	// this endpoint rejects outright ("unsupported mimetype"). The header is
	// written by hand so each attachment declares its real type. Getting this
	// wrong made every call carrying a picture fail in the old tool, and only
	// the real API surfaced it.
	for i, ref := range refs {
		mime, ext := "image/jpeg", "jpg"
		if ref.MIME == "image/png" {
			mime, ext = "image/png", "png"
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image[]"; filename="image-%d.%s"`, i+1, ext))
		h.Set("Content-Type", mime)
		part, err := form.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(ref.Data); err != nil {
			return nil, "", err
		}
	}
	if err := form.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), form.FormDataContentType(), nil
}

// withoutPicture is the raw reply with each b64_json replaced by a note: the
// picture itself is kept beside it, decoded, and twice would only double the
// folder.
func withoutPicture(raw []byte) json.RawMessage {
	var reply map[string]any
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil
	}
	if data, ok := reply["data"].([]any); ok {
		for _, d := range data {
			if m, ok := d.(map[string]any); ok {
				if _, ok := m["b64_json"]; ok {
					m["b64_json"] = "(kept beside this file)"
				}
			}
		}
	}
	out, err := json.Marshal(reply)
	if err != nil {
		return nil
	}
	return out
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// IsOutOfCredit reports whether err is OpenAI's insufficient_quota.
func IsOutOfCredit(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.OutOfCredit
}
