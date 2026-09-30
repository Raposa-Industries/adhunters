package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Sites are the landing sites the edge hosts, one folder per host name:
//
//	<root>/<host>/versions/<version>/…   each published copy, kept
//	<root>/<host>/current                a link to the version being served
//	<root>/<host>/site.json              the site's settings (SiteConfig)
//
// Publishing copies a folder into a new version and moves the link in one
// rename, so a visitor never sees half of two versions; rolling back moves
// the link to an older one.
type Sites struct{ Root string }

// SiteConfig is a site's settings, kept beside its versions.
type SiteConfig struct {
	// Clarity is the Microsoft Clarity project id for recordings and
	// heatmaps. Empty turns Clarity off.
	Clarity string `json:"clarity,omitempty"`
}

var (
	hostRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	clarityRe = regexp.MustCompile(`^[a-z0-9]{6,20}$`)
)

// maxHTML is the largest HTML file the edge adds the page script to; a
// larger one is served as it is.
const maxHTML = 8 << 20

// CheckHost says whether h is a host name a site may have.
func CheckHost(h string) error {
	if !hostRe.MatchString(h) {
		return fmt.Errorf("%q is not a host name (lower case, like lp.example.com)", h)
	}
	return nil
}

func (s *Sites) dir(host string) string { return filepath.Join(s.Root, host) }

// Config reads a site's settings; a site without site.json has none.
func (s *Sites) Config(host string) (SiteConfig, error) {
	var c SiteConfig
	b, err := os.ReadFile(filepath.Join(s.dir(host), "site.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// SetConfig writes a site's settings.
func (s *Sites) SetConfig(host string, c SiteConfig) error {
	if err := CheckHost(host); err != nil {
		return err
	}
	if c.Clarity != "" && !clarityRe.MatchString(c.Clarity) {
		return fmt.Errorf("%q is not a Clarity project id", c.Clarity)
	}
	if err := os.MkdirAll(s.dir(host), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return writeAtomic(filepath.Join(s.dir(host), "site.json"), append(b, '\n'))
}

// Publish copies the folder src into a new version of host and serves it.
// Hidden files and symbolic links in src are refused rather than copied.
func (s *Sites) Publish(host, src string, now time.Time) (string, error) {
	if err := CheckHost(host); err != nil {
		return "", err
	}
	st, err := os.Stat(filepath.Join(src, "index.html"))
	if err != nil || st.IsDir() {
		return "", fmt.Errorf("%s has no index.html", src)
	}
	versions := filepath.Join(s.dir(host), "versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		return "", err
	}
	version := now.UTC().Format("20060102T150405Z")
	for n := 2; exists(filepath.Join(versions, version)); n++ {
		version = fmt.Sprintf("%s-%d", now.UTC().Format("20060102T150405Z"), n)
	}
	tmp, err := os.MkdirTemp(versions, ".publish-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := copyTree(src, tmp); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(versions, version)); err != nil {
		return "", err
	}
	return version, s.Serve(host, version)
}

// Serve points host at one of its versions.
func (s *Sites) Serve(host, version string) error {
	if err := CheckHost(host); err != nil {
		return err
	}
	if version == "" || strings.ContainsAny(version, `/\`) || strings.HasPrefix(version, ".") {
		return fmt.Errorf("bad version %q", version)
	}
	if !exists(filepath.Join(s.dir(host), "versions", version)) {
		return fmt.Errorf("%s has no version %s", host, version)
	}
	link := filepath.Join(s.dir(host), "current")
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", version), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// Version is one published copy of a site.
type Version struct {
	Name    string
	Current bool
}

// Versions lists a site's versions, oldest first.
func (s *Sites) Versions(host string) ([]Version, error) {
	if err := CheckHost(host); err != nil {
		return nil, err
	}
	cur, _ := os.Readlink(filepath.Join(s.dir(host), "current"))
	ents, err := os.ReadDir(filepath.Join(s.dir(host), "versions"))
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, Version{Name: e.Name(), Current: filepath.Base(cur) == e.Name()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Hosts lists the sites that serve a version.
func (s *Sites) Hosts() ([]string, error) {
	ents, err := os.ReadDir(s.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if CheckHost(e.Name()) == nil && exists(filepath.Join(s.Root, e.Name(), "current")) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// errNoSite means the request's host is not a site here.
var errNoSite = errors.New("no such site")

// site finds the folder serving host, trying it without "www." too.
func (s *Sites) site(host string) (string, string, error) {
	for _, h := range []string{host, strings.TrimPrefix(host, "www.")} {
		if CheckHost(h) != nil {
			continue
		}
		cur := filepath.Join(s.dir(h), "current")
		if st, err := os.Stat(cur); err == nil && st.IsDir() {
			return h, cur, nil
		}
	}
	return "", "", errNoSite
}

// resolve maps a URL path to a file under a version: "/" is index.html, a
// folder is its index.html, and "/offer" is offer.html when no "offer"
// exists. Hidden and underscore names are never served.
func resolve(root *os.Root, urlPath string) (string, fs.FileInfo, error) {
	p := path.Clean("/" + urlPath)
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "_") {
			return "", nil, fs.ErrNotExist
		}
	}
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		name = "."
	}
	try := []string{name}
	if name == "." {
		try = []string{"index.html"}
	} else if path.Ext(name) == "" {
		try = append(try, name+".html")
	}
	for _, n := range try {
		st, err := root.Stat(n)
		if err != nil {
			continue
		}
		if st.IsDir() {
			n = path.Join(n, "index.html")
			if st, err = root.Stat(n); err != nil || st.IsDir() {
				continue
			}
		}
		return n, st, nil
	}
	return "", nil, fs.ErrNotExist
}

// scriptTag is what the edge adds to each HTML page it serves: the page
// script, and a style that keeps data-show-at elements hidden until the
// script reveals them.
func scriptTag(host string, c SiteConfig) []byte {
	var b bytes.Buffer
	b.WriteString(`<style>[data-show-at]:not(.ahp-shown){display:none!important}</style>`)
	b.WriteString(`<script async src="/ah.js" data-site="`)
	b.WriteString(html.EscapeString(host))
	b.WriteString(`"`)
	if c.Clarity != "" {
		b.WriteString(` data-clarity="`)
		b.WriteString(html.EscapeString(c.Clarity))
		b.WriteString(`"`)
	}
	b.WriteString(`></script>`)
	return b.Bytes()
}

var (
	headOpen  = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	headClose = regexp.MustCompile(`(?i)</head\s*>`)
)

// inject adds the tag at the end of <head> (after the page's charset), at
// the start of <body> content when the head has no end tag, or at the very
// start when the page has no head. A page that already loads ah.js is left
// alone.
func inject(page, tag []byte) []byte {
	if bytes.Contains(page, []byte("/ah.js")) {
		return page
	}
	if loc := headClose.FindIndex(page); loc != nil {
		out := make([]byte, 0, len(page)+len(tag))
		out = append(out, page[:loc[0]]...)
		out = append(out, tag...)
		return append(out, page[loc[0]:]...)
	}
	if loc := headOpen.FindIndex(page); loc != nil {
		out := make([]byte, 0, len(page)+len(tag))
		out = append(out, page[:loc[1]]...)
		out = append(out, tag...)
		return append(out, page[loc[1]:]...)
	}
	return append(append([]byte{}, tag...), page...)
}

// ServeHTTP serves the hosted site the request's Host names.
func (s *Sites) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host, dir, err := s.site(requestHost(r))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer func() { _ = root.Close() }()
	name, st, err := resolve(root, r.URL.Path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := root.Open(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ext := strings.ToLower(path.Ext(name))
	if (ext == ".html" || ext == ".htm") && st.Size() <= maxHTML {
		page, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		cfg, _ := s.Config(host)
		page = inject(page, scriptTag(host, cfg))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		// No Last-Modified: the added tag changes with site.json, not the file.
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(page))
		return
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// requestHost is the Host header, lower case, without a port.
func requestHost(r *http.Request) string {
	h := strings.ToLower(r.Host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return fmt.Errorf("%s: hidden files are not published", rel)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: links are not published", rel)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a plain file", rel)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
