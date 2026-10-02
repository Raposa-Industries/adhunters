// Package drivetest is a fake Google Drive for tests: the token endpoint and
// the few Drive v3 calls the library makes, over one in-memory folder tree.
package drivetest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/library/internal/drive"
)

// Root is the id of the library folder in the fake.
const Root = "root-folder"

// File is one file in the fake.
type File struct {
	drive.File
	Data []byte
}

// Drive is the fake.
type Drive struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string]*File
	next  int
	// PageSize is how many files one listing page holds.
	PageSize int
	// FailNext makes the next n Drive calls answer 429.
	FailNext int
	// Calls counts Drive calls by "METHOD path".
	Calls map[string]int
}

// New starts a fake with an empty library folder.
func New(t testing.TB) *Drive {
	d := &Drive{files: map[string]*File{}, PageSize: 100, Calls: map[string]int{}}
	d.files[Root] = &File{File: drive.File{ID: Root, Name: "AdHunters library", MimeType: drive.FolderType}}
	d.Server = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.Close)
	return d
}

// App is an OAuth client pointed at the fake.
func (d *Drive) App() drive.App {
	return drive.App{ClientID: "id", ClientSecret: "secret", Endpoints: drive.Endpoints{Auth: d.URL + "/auth", Token: d.URL + "/token", API: d.URL}}
}

// Add puts a file in parent, as a person would, and returns its id.
func (d *Drive) Add(parent, name, mimeType string, data []byte) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.add(parent, name, mimeType, data, nil).ID
}

func (d *Drive) add(parent, name, mimeType string, data []byte, props map[string]string) *File {
	d.next++
	f := &File{File: drive.File{ID: fmt.Sprintf("f%03d", d.next), Name: name, MimeType: mimeType, Parents: []string{parent},
		ModifiedTime: time.Date(2026, 9, 30, 12, 0, d.next, 0, time.UTC), AppProperties: props}, Data: data}
	if mimeType != drive.FolderType && mimeType != drive.DocType {
		sum := md5.Sum(data)
		f.MD5 = hex.EncodeToString(sum[:])
		f.Size = strconv.Itoa(len(data))
	}
	d.files[f.ID] = f
	return f
}

// Remove deletes a file, as a person would.
func (d *Drive) Remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.files, id)
}

// Edit gives a file new contents, as a person editing it would.
func (d *Drive) Edit(id string, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.files[id]
	if !ok {
		return
	}
	f.Data = data
	f.ModifiedTime = f.ModifiedTime.Add(time.Minute)
	if f.MimeType != drive.DocType {
		sum := md5.Sum(data)
		f.MD5 = hex.EncodeToString(sum[:])
		f.Size = strconv.Itoa(len(data))
	}
}

// Trash puts a file in Drive's trash, or takes it out, as a person would.
func (d *Drive) Trash(id string, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f, ok := d.files[id]; ok {
		f.Trashed = on
	}
}

// Get returns a copy of a file, or false.
func (d *Drive) Get(id string) (File, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.files[id]
	if !ok {
		return File{}, false
	}
	return *f, true
}

// Find returns the files named name, in id order.
func (d *Drive) Find(name string) []File {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []File
	for _, f := range d.files {
		if f.Name == name {
			out = append(out, *f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var parentQ = regexp.MustCompile(`^'([^']+)' in parents and trashed = false$`)

func (d *Drive) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "refresh_token":
			if r.Form.Get("refresh_token") == "revoked" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			writeJSON(w, map[string]any{"access_token": "access", "expires_in": 3600})
		case "authorization_code":
			writeJSON(w, map[string]any{"access_token": "access", "expires_in": 3600, "refresh_token": "refresh-" + r.Form.Get("code")})
		}
		return
	}
	if r.Header.Get("Authorization") != "Bearer access" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	d.Calls[r.Method+" "+r.URL.Path]++
	if d.FailNext > 0 {
		d.FailNext--
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","errors":[{"reason":"rateLimitExceeded"}]}}`)
		return
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/drive/v3/about":
		writeJSON(w, map[string]any{"user": map[string]string{"emailAddress": "library@example.com"}})
	case r.Method == http.MethodGet && path == "/drive/v3/files":
		m := parentQ.FindStringSubmatch(r.URL.Query().Get("q"))
		if m == nil {
			http.Error(w, "bad q", http.StatusBadRequest)
			return
		}
		var kids []drive.File
		for _, f := range d.files {
			if len(f.Parents) > 0 && f.Parents[0] == m[1] && !f.Trashed {
				kids = append(kids, f.File)
			}
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].ID < kids[j].ID })
		start, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
		end := min(start+d.PageSize, len(kids))
		reply := map[string]any{"files": kids[start:end]}
		if end < len(kids) {
			reply["nextPageToken"] = strconv.Itoa(end)
		}
		writeJSON(w, reply)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/drive/v3/files/") && strings.HasSuffix(path, "/export"):
		f, ok := d.files[strings.TrimSuffix(strings.TrimPrefix(path, "/drive/v3/files/"), "/export")]
		if !ok || f.MimeType != drive.DocType || r.URL.Query().Get("mimeType") != "text/plain" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(f.Data)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/drive/v3/files/"):
		f, ok := d.files[strings.TrimPrefix(path, "/drive/v3/files/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"File not found","errors":[{"reason":"notFound"}]}}`)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			_, _ = w.Write(f.Data)
			return
		}
		writeJSON(w, f.File)
	case r.Method == http.MethodPost && path == "/drive/v3/files":
		var meta struct {
			Name          string            `json:"name"`
			MimeType      string            `json:"mimeType"`
			Parents       []string          `json:"parents"`
			AppProperties map[string]string `json:"appProperties"`
		}
		_ = json.NewDecoder(r.Body).Decode(&meta)
		writeJSON(w, d.add(meta.Parents[0], meta.Name, meta.MimeType, nil, meta.AppProperties).File)
	case r.Method == http.MethodPost && path == "/upload/drive/v3/files":
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		metaPart, err := mr.NextPart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var meta struct {
			Name          string            `json:"name"`
			Parents       []string          `json:"parents"`
			AppProperties map[string]string `json:"appProperties"`
		}
		_ = json.NewDecoder(metaPart).Decode(&meta)
		dataPart, err := mr.NextPart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(dataPart)
		writeJSON(w, d.add(meta.Parents[0], meta.Name, dataPart.Header.Get("Content-Type"), data, meta.AppProperties).File)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/upload/drive/v3/files/"):
		f, ok := d.files[strings.TrimPrefix(path, "/upload/drive/v3/files/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.Data, _ = io.ReadAll(r.Body)
		sum := md5.Sum(f.Data)
		f.MD5 = hex.EncodeToString(sum[:])
		f.Size = strconv.Itoa(len(f.Data))
		writeJSON(w, f.File)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/drive/v3/files/"):
		f, ok := d.files[strings.TrimPrefix(path, "/drive/v3/files/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var meta struct {
			Name          string            `json:"name"`
			AppProperties map[string]string `json:"appProperties"`
		}
		_ = json.NewDecoder(r.Body).Decode(&meta)
		if meta.Name != "" {
			f.Name = meta.Name
		}
		if f.AppProperties == nil {
			f.AppProperties = map[string]string{}
		}
		for k, v := range meta.AppProperties {
			f.AppProperties[k] = v
		}
		writeJSON(w, map[string]string{"id": f.ID})
	default:
		http.Error(w, "fake drive: no "+r.Method+" "+path, http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
