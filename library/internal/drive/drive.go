// Package drive is the library's Google Drive client: the few Drive v3 calls
// the sync needs, written by hand over net/http, with the OAuth refresh
// token of the Google account that owns the library folder.
//
// The account is a person's own Google account, not a Workspace: a service
// account cannot own files there (it has no storage of its own), so the
// library signs in as that person once (`library drive-login`) and keeps
// the refresh token. The OAuth client is a "Desktop app" client of a Google
// Cloud project in "In production" status, so the token does not expire
// after 7 days as a testing project's does.
package drive

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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/ops"
)

// Scope is full Drive access: the sync must see files people add to the
// folder by hand, which the narrower drive.file scope hides.
const Scope = "https://www.googleapis.com/auth/drive"

// FolderType is Drive's media type for a folder.
const FolderType = "application/vnd.google-apps.folder"

// DocType is Drive's media type for a Google Doc.
const DocType = "application/vnd.google-apps.document"

// Endpoints are Google's addresses; tests point them at a fake.
type Endpoints struct {
	Auth  string // consent page
	Token string // token exchange and refresh
	API   string // Drive API host (files, about, upload)
}

// Google are the real endpoints.
var Google = Endpoints{
	Auth:  "https://accounts.google.com/o/oauth2/v2/auth",
	Token: "https://oauth2.googleapis.com/token",
	API:   "https://www.googleapis.com",
}

// App is the OAuth client the library signs in with.
type App struct {
	ClientID     string
	ClientSecret string
	Endpoints    Endpoints
}

func (a App) ends() Endpoints {
	e := a.Endpoints
	if e.Auth == "" {
		e.Auth = Google.Auth
	}
	if e.Token == "" {
		e.Token = Google.Token
	}
	if e.API == "" {
		e.API = Google.API
	}
	e.API = strings.TrimRight(e.API, "/")
	return e
}

// File is a Drive file or folder, with the fields the sync asks for.
type File struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	MimeType      string            `json:"mimeType"`
	MD5           string            `json:"md5Checksum"`
	Size          string            `json:"size"`
	Parents       []string          `json:"parents"`
	ModifiedTime  time.Time         `json:"modifiedTime"`
	AppProperties map[string]string `json:"appProperties"`
	Trashed       bool              `json:"trashed"`
}

// Bytes is the file's size, 0 for folders and Google's own documents.
func (f File) Bytes() int64 {
	n, _ := strconv.ParseInt(f.Size, 10, 64)
	return n
}

// IsFolder reports whether f is a folder.
func (f File) IsFolder() bool { return f.MimeType == FolderType }

const fileFields = "id,name,mimeType,md5Checksum,size,parents,modifiedTime,appProperties,trashed"

// Error is a refused call, with Drive's own reason when it gave one.
type Error struct {
	Status  int
	Reason  string
	Message string
}

func (e *Error) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("drive: %d %s: %s", e.Status, e.Reason, e.Message)
	}
	return fmt.Sprintf("drive: %d: %s", e.Status, e.Message)
}

// NotFound reports whether Drive said the file is not there.
func (e *Error) NotFound() bool { return e.Status == 404 }

// ErrSignedOut means the refresh token was refused: someone removed the
// library's access in their Google account, or it was never signed in.
var ErrSignedOut = errors.New("drive: the Google sign-in was refused; run library drive-login again")

// Client calls Drive as the signed-in account. It is safe for concurrent use.
type Client struct {
	app     App
	refresh string
	http    *http.Client

	mu      sync.Mutex
	access  string
	expires time.Time

	// pause is the first wait before a retry; tests shrink it.
	pause time.Duration
}

// New returns a client for the account whose refresh token this is.
func New(app App, refreshToken string) *Client {
	return &Client{app: app, refresh: refreshToken, http: &http.Client{Timeout: 5 * time.Minute, Transport: ops.Transport("drive", nil)}, pause: time.Second}
}

// token returns an access token, refreshing it a minute before it ends.
func (c *Client) token(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.access != "" && time.Now().Before(c.expires.Add(-time.Minute)) {
		return c.access, nil
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.refresh},
		"client_id":     {c.app.ClientID},
		"client_secret": {c.app.ClientSecret},
	}
	tok, err := postToken(ctx, c.http, c.app.ends().Token, form)
	if err != nil {
		return "", err
	}
	c.access = tok.AccessToken
	c.expires = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return c.access, nil
}

type tokenReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func postToken(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (tokenReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenReply{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := hc.Do(req)
	if err != nil {
		return tokenReply{}, fmt.Errorf("drive: token: %w", err)
	}
	defer res.Body.Close()
	var tok tokenReply
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	_ = json.Unmarshal(body, &tok)
	if tok.Error == "invalid_grant" {
		return tokenReply{}, ErrSignedOut
	}
	if res.StatusCode != http.StatusOK || tok.AccessToken == "" {
		msg := tok.Description
		if msg == "" {
			msg = tok.Error
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return tokenReply{}, &Error{Status: res.StatusCode, Reason: tok.Error, Message: msg}
	}
	return tok, nil
}

// request is one call; body is rebuilt for each attempt.
type request struct {
	method      string
	url         string
	contentType string
	body        func() ([]byte, error)
}

// do sends a call, retrying Drive's "slow down" and passing failures, and
// signing in again once on a 401. It returns the reply body.
func (c *Client) do(ctx context.Context, r request) ([]byte, error) {
	forced := false
	wait := c.pause
	for attempt := 1; ; attempt++ {
		tok, err := c.token(ctx, false)
		if err != nil {
			return nil, err
		}
		var body io.Reader
		if r.body != nil {
			b, err := r.body()
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, r.method, r.url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if r.contentType != "" {
			req.Header.Set("Content-Type", r.contentType)
		}
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil || attempt >= 4 {
				return nil, fmt.Errorf("drive: %s: %w", r.method, err)
			}
		} else {
			out, rerr := io.ReadAll(io.LimitReader(res.Body, 64<<20))
			res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode <= 299 && rerr == nil {
				return out, nil
			}
			e := apiError(res.StatusCode, out)
			switch {
			case res.StatusCode == http.StatusUnauthorized && !forced:
				forced = true
				if _, err := c.token(ctx, true); err != nil {
					return nil, err
				}
				continue
			case !retryable(e) || attempt >= 5:
				return nil, e
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// retryable: Drive's rate limits (429, or 403 with a rate-limit reason) and
// its own failures.
func retryable(e *Error) bool {
	switch {
	case e.Status == http.StatusTooManyRequests, e.Status >= 500:
		return true
	case e.Status == http.StatusForbidden:
		return e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded"
	}
	return false
}

func apiError(status int, body []byte) *Error {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	e := &Error{Status: status, Message: parsed.Error.Message}
	if len(parsed.Error.Errors) > 0 {
		e.Reason = parsed.Error.Errors[0].Reason
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

// Page is one page of a folder's listing, with the reply as it came.
type Page struct {
	Files []File
	Next  string
	Raw   []byte
}

// List returns one page of what is directly in a folder (not in the bin).
func (c *Client) List(ctx context.Context, folderID, pageToken string) (Page, error) {
	q := url.Values{
		"q":                         {fmt.Sprintf("'%s' in parents and trashed = false", strings.ReplaceAll(folderID, "'", `\'`))},
		"fields":                    {"nextPageToken,files(" + fileFields + ")"},
		"pageSize":                  {"1000"},
		"supportsAllDrives":         {"true"},
		"includeItemsFromAllDrives": {"true"},
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	raw, err := c.do(ctx, request{method: http.MethodGet, url: c.app.ends().API + "/drive/v3/files?" + q.Encode()})
	if err != nil {
		return Page{}, err
	}
	var reply struct {
		NextPageToken string `json:"nextPageToken"`
		Files         []File `json:"files"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Page{}, fmt.Errorf("drive: list: %w", err)
	}
	return Page{Files: reply.Files, Next: reply.NextPageToken, Raw: raw}, nil
}

// Get returns one file's details.
func (c *Client) Get(ctx context.Context, id string) (File, error) {
	raw, err := c.do(ctx, request{method: http.MethodGet,
		url: c.app.ends().API + "/drive/v3/files/" + url.PathEscape(id) + "?supportsAllDrives=true&fields=" + fileFields})
	if err != nil {
		return File{}, err
	}
	var f File
	return f, json.Unmarshal(raw, &f)
}

// Export returns a Google Doc's text as plain text (UTF-8).
func (c *Client) Export(ctx context.Context, id string) ([]byte, error) {
	return c.do(ctx, request{method: http.MethodGet,
		url: c.app.ends().API + "/drive/v3/files/" + url.PathEscape(id) + "/export?mimeType=text%2Fplain"})
}

// Download returns a file's bytes.
func (c *Client) Download(ctx context.Context, id string) ([]byte, error) {
	return c.do(ctx, request{method: http.MethodGet,
		url: c.app.ends().API + "/drive/v3/files/" + url.PathEscape(id) + "?alt=media&supportsAllDrives=true"})
}

// Account returns the signed-in account's email address.
func (c *Client) Account(ctx context.Context) (string, error) {
	raw, err := c.do(ctx, request{method: http.MethodGet, url: c.app.ends().API + "/drive/v3/about?fields=user(emailAddress)"})
	if err != nil {
		return "", err
	}
	var about struct {
		User struct {
			Email string `json:"emailAddress"`
		} `json:"user"`
	}
	return about.User.Email, json.Unmarshal(raw, &about)
}

// CreateFolder makes a folder in parent.
func (c *Client) CreateFolder(ctx context.Context, parent, name string, props map[string]string) (File, error) {
	meta, err := json.Marshal(map[string]any{"name": name, "mimeType": FolderType, "parents": []string{parent}, "appProperties": props})
	if err != nil {
		return File{}, err
	}
	raw, err := c.do(ctx, request{method: http.MethodPost, contentType: "application/json",
		url:  c.app.ends().API + "/drive/v3/files?supportsAllDrives=true&fields=" + fileFields,
		body: func() ([]byte, error) { return meta, nil }})
	if err != nil {
		return File{}, err
	}
	var f File
	return f, json.Unmarshal(raw, &f)
}

// Upload makes a file in parent with these bytes.
func (c *Client) Upload(ctx context.Context, parent, name, mediaType string, data []byte, props map[string]string) (File, error) {
	meta, err := json.Marshal(map[string]any{"name": name, "parents": []string{parent}, "appProperties": props})
	if err != nil {
		return File{}, err
	}
	var boundary string
	build := func() ([]byte, error) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		boundary = w.Boundary()
		h := textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}}
		p, err := w.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if _, err := p.Write(meta); err != nil {
			return nil, err
		}
		p, err = w.CreatePart(textproto.MIMEHeader{"Content-Type": {mediaType}})
		if err != nil {
			return nil, err
		}
		if _, err := p.Write(data); err != nil {
			return nil, err
		}
		// Closing writes the final boundary, so it comes before Bytes.
		if err := w.Close(); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	// The boundary is fixed by the first build, so the header can name it.
	first, err := build()
	if err != nil {
		return File{}, err
	}
	raw, err := c.do(ctx, request{method: http.MethodPost, contentType: "multipart/related; boundary=" + boundary,
		url:  c.app.ends().API + "/upload/drive/v3/files?uploadType=multipart&supportsAllDrives=true&fields=" + fileFields,
		body: func() ([]byte, error) { return first, nil }})
	if err != nil {
		return File{}, err
	}
	var f File
	return f, json.Unmarshal(raw, &f)
}

// Replace puts new bytes in an existing file.
func (c *Client) Replace(ctx context.Context, id, mediaType string, data []byte) (File, error) {
	raw, err := c.do(ctx, request{method: http.MethodPatch, contentType: mediaType,
		url:  c.app.ends().API + "/upload/drive/v3/files/" + url.PathEscape(id) + "?uploadType=media&supportsAllDrives=true&fields=" + fileFields,
		body: func() ([]byte, error) { return data, nil }})
	if err != nil {
		return File{}, err
	}
	var f File
	return f, json.Unmarshal(raw, &f)
}

// Rename gives a file or folder a new name. The library renames only the
// set folders it made, when the app that owns the set renames it.
func (c *Client) Rename(ctx context.Context, id, name string) error {
	meta, err := json.Marshal(map[string]any{"name": name})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, request{method: http.MethodPatch, contentType: "application/json",
		url:  c.app.ends().API + "/drive/v3/files/" + url.PathEscape(id) + "?supportsAllDrives=true&fields=id",
		body: func() ([]byte, error) { return meta, nil }})
	return err
}

// Label sets the library's own properties on a file, so our id rides on it.
// Only the library's OAuth client can read them; people never see them.
func (c *Client) Label(ctx context.Context, id string, props map[string]string) error {
	meta, err := json.Marshal(map[string]any{"appProperties": props})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, request{method: http.MethodPatch, contentType: "application/json",
		url:  c.app.ends().API + "/drive/v3/files/" + url.PathEscape(id) + "?supportsAllDrives=true&fields=id",
		body: func() ([]byte, error) { return meta, nil }})
	return err
}
