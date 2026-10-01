package e2e

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDrive is Google Drive as the library uses it (library/internal/drive):
// the token refresh, folder listings, folder creation, multipart uploads,
// downloads, renames and labels, kept in memory.
type fakeDrive struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	next  int
	files map[string]*driveFile
}

type driveFile struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	MimeType      string            `json:"mimeType"`
	MD5           string            `json:"md5Checksum,omitempty"`
	Size          string            `json:"size,omitempty"`
	Parents       []string          `json:"parents"`
	ModifiedTime  time.Time         `json:"modifiedTime"`
	AppProperties map[string]string `json:"appProperties,omitempty"`
	Trashed       bool              `json:"trashed"`
	data          []byte
}

const (
	driveRoot   = "stepnutra-root"
	driveFolder = "application/vnd.google-apps.folder"
)

func newFakeDrive(t *testing.T) *fakeDrive {
	f := &fakeDrive{t: t, files: map[string]*driveFile{
		driveRoot: {ID: driveRoot, Name: "StepNutra", MimeType: driveFolder, ModifiedTime: time.Now().UTC()},
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDrive) URL() string { return f.srv.URL }

var inParents = regexp.MustCompile(`^'([^']+)' in parents`)

func (f *fakeDrive) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "fake-refresh" {
			f.t.Errorf("drive: token asked with %v", r.PostForm)
		}
		answer(w, map[string]any{"access_token": "fake-drive-token", "expires_in": 3600, "token_type": "Bearer"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake-drive-token" {
		f.t.Errorf("drive: %s %s without the token", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	id, one := strings.CutPrefix(r.URL.Path, "/drive/v3/files/")
	upID, upOne := strings.CutPrefix(r.URL.Path, "/upload/drive/v3/files/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/about":
		answer(w, map[string]any{"user": map[string]any{"emailAddress": "adhuntertech@gmail.test"}})
	case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files":
		m := inParents.FindStringSubmatch(r.URL.Query().Get("q"))
		if m == nil {
			f.t.Errorf("drive: listing asked with q=%q", r.URL.Query().Get("q"))
		}
		list := []*driveFile{}
		for _, df := range f.files {
			if m != nil && !df.Trashed && contains(df.Parents, m[1]) {
				list = append(list, df)
			}
		}
		answer(w, map[string]any{"files": list})
	case r.Method == http.MethodPost && r.URL.Path == "/drive/v3/files":
		df := f.add(raw)
		answer(w, df)
	case r.Method == http.MethodPost && r.URL.Path == "/upload/drive/v3/files":
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || r.URL.Query().Get("uploadType") != "multipart" {
			f.t.Errorf("drive: upload as %q: %v", r.Header.Get("Content-Type"), err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(strings.NewReader(string(raw)), params["boundary"])
		meta, err1 := mr.NextPart()
		metaRaw, _ := io.ReadAll(meta)
		body, err2 := mr.NextPart()
		if err1 != nil || err2 != nil {
			f.t.Errorf("drive: upload parts: %v %v", err1, err2)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(body)
		df := f.add(metaRaw)
		df.MimeType = body.Header.Get("Content-Type")
		f.setData(df, data)
		answer(w, df)
	case r.Method == http.MethodPatch && upOne:
		df := f.files[upID]
		if df == nil {
			driveNotFound(w)
			return
		}
		f.setData(df, raw)
		answer(w, df)
	case one && r.Method == http.MethodGet:
		df := f.files[id]
		if df == nil {
			driveNotFound(w)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			_, _ = w.Write(df.data)
			return
		}
		answer(w, df)
	case one && r.Method == http.MethodPatch:
		df := f.files[id]
		if df == nil {
			driveNotFound(w)
			return
		}
		var meta struct {
			Name          string            `json:"name"`
			AppProperties map[string]string `json:"appProperties"`
		}
		_ = json.Unmarshal(raw, &meta)
		if meta.Name != "" {
			df.Name = meta.Name
		}
		for k, v := range meta.AppProperties {
			if df.AppProperties == nil {
				df.AppProperties = map[string]string{}
			}
			df.AppProperties[k] = v
		}
		answer(w, df)
	default:
		f.t.Errorf("drive: no fake answer for %s %s", r.Method, r.URL.Path)
		driveNotFound(w)
	}
}

// add makes a file from Drive's JSON metadata. Call with mu held.
func (f *fakeDrive) add(meta []byte) *driveFile {
	var m struct {
		Name          string            `json:"name"`
		MimeType      string            `json:"mimeType"`
		Parents       []string          `json:"parents"`
		AppProperties map[string]string `json:"appProperties"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		f.t.Errorf("drive: metadata %s: %v", meta, err)
	}
	for _, p := range m.Parents {
		if f.files[p] == nil {
			f.t.Errorf("drive: %q made in a folder that does not exist: %s", m.Name, p)
		}
	}
	f.next++
	df := &driveFile{ID: "drive-" + strconv.Itoa(f.next), Name: m.Name, MimeType: m.MimeType, Parents: m.Parents,
		AppProperties: m.AppProperties, ModifiedTime: time.Now().UTC()}
	f.files[df.ID] = df
	return df
}

func (f *fakeDrive) setData(df *driveFile, data []byte) {
	sum := md5.Sum(data)
	df.data, df.MD5, df.Size, df.ModifiedTime = data, hex.EncodeToString(sum[:]), strconv.Itoa(len(data)), time.Now().UTC()
}

// stored is every file (not folder) with its folder path from the library
// folder down, by the SHA-256 of its bytes.
func (f *fakeDrive) stored() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, df := range f.files {
		if df.MimeType == driveFolder || df.Trashed {
			continue
		}
		var path []string
		for p := df; p != nil; {
			path = append([]string{p.Name}, path...)
			if len(p.Parents) == 0 {
				break
			}
			p = f.files[p.Parents[0]]
		}
		sum := sha256.Sum256(df.data)
		out[hex.EncodeToString(sum[:])] = strings.Join(path, " / ")
	}
	return out
}

func driveNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"error":{"code":404,"message":"File not found","errors":[{"reason":"notFound"}]}}`)
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}
