package policy

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultRoots are the Policy & Content Review collection and the policy
// articles that live outside it: declaring AI-made content, and branding text.
var DefaultRoots = []string{
	"https://realize.com/help/en/collections/11915686-policy-content-review",
	"https://realize.com/help/en/articles/16002528-declare-ai-generated-content-in-your-ads",
	"https://realize.com/help/en/articles/3878080-campaign-branding-text",
}

// Snapshot is every page one crawl read, by key.
type Snapshot struct {
	Taken time.Time       `json:"taken"`
	Pages map[string]Page `json:"pages"`
}

// Articles is how many articles the snapshot holds.
func (s *Snapshot) Articles() int {
	n := 0
	for _, p := range s.Pages {
		if p.Kind == Article {
			n++
		}
	}
	return n
}

// Crawler reads the help center politely: one page at a time, a pause
// between pages, each answer saved raw before it is parsed.
type Crawler struct {
	HTTP  *http.Client
	Roots []string
	Max   int           // most pages one crawl may read; more means it left the collection
	Pause time.Duration // between requests
	Dir   string        // raw crawls go in Dir/<UTC stamp>/
}

// userAgent is a plain browser's: the help center sits behind a bot check.
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"

// Crawl reads every collection and article under the roots and returns the
// snapshot and the folder its raw pages were saved in. Any page that cannot
// be read fails the whole crawl, so a half crawl never looks like removed
// pages.
func (c *Crawler) Crawl(ctx context.Context, now time.Time) (*Snapshot, string, error) {
	dir := filepath.Join(c.Dir, now.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, "", err
	}
	index, err := os.Create(filepath.Join(dir, "index.tsv"))
	if err != nil {
		return nil, "", err
	}
	defer index.Close()

	snap := &Snapshot{Taken: now.UTC(), Pages: map[string]Page{}}
	queue := append([]string(nil), c.Roots...)
	queued := map[string]bool{}
	for _, r := range queue {
		u, err := url.Parse(r)
		if err != nil {
			return nil, dir, err
		}
		k, _ := KeyOf(u)
		if k == "" {
			return nil, dir, fmt.Errorf("root %s is not a help center collection", r)
		}
		queued[k] = true
	}
	max := c.Max
	if max <= 0 {
		max = 300
	}
	for len(queue) > 0 {
		if len(snap.Pages) >= max {
			return nil, dir, fmt.Errorf("more than %d pages: is the crawl leaving the collection?", max)
		}
		u := queue[0]
		queue = queue[1:]
		if len(snap.Pages) > 0 && c.Pause > 0 {
			select {
			case <-ctx.Done():
				return nil, dir, ctx.Err()
			case <-time.After(c.Pause):
			}
		}
		raw, final, err := c.fetch(ctx, u)
		if err != nil {
			return nil, dir, err
		}
		name := fileName(final)
		if err := writeGz(filepath.Join(dir, name), raw); err != nil {
			return nil, dir, err
		}
		if _, err := fmt.Fprintf(index, "%s\t%s\n", final, name); err != nil {
			return nil, dir, err
		}
		p, err := Parse(final, raw)
		if err != nil {
			return nil, dir, err
		}
		snap.Pages[p.Key] = p
		for _, l := range p.Links {
			lu, _ := url.Parse(l)
			if k, _ := KeyOf(lu); k != "" && !queued[k] {
				queued[k] = true
				queue = append(queue, l)
			}
		}
	}
	return snap, dir, nil
}

// fetch gets one page, trying three times. It returns the body and the URL
// it ended at after redirects.
func (c *Crawler) fetch(ctx context.Context, u string) ([]byte, string, error) {
	var last error
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(time.Duration(try*try) * 5 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("%s: %s", u, resp.Status)
			if resp.StatusCode == http.StatusNotFound {
				break // a listed page that is gone: trying again will not help
			}
			continue
		}
		return b, resp.Request.URL.String(), nil
	}
	return nil, "", fmt.Errorf("read %s: %w", u, last)
}

func fileName(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return "page.html.gz"
	}
	k, _ := KeyOf(pu)
	return strings.ReplaceAll(k, "/", "-") + ".html.gz"
}

func writeGz(path string, b []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Reparse reads a saved raw crawl again, as Crawl would have read it.
func Reparse(dir string) (*Snapshot, error) {
	taken, err := time.Parse("20060102T150405Z", filepath.Base(dir))
	if err != nil {
		return nil, fmt.Errorf("%s is not a saved crawl: %w", dir, err)
	}
	f, err := os.Open(filepath.Join(dir, "index.tsv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	snap := &Snapshot{Taken: taken, Pages: map[string]Page{}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		u, name, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		raw, err := readGz(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		p, err := Parse(u, raw)
		if err != nil {
			return nil, err
		}
		snap.Pages[p.Key] = p
	}
	return snap, sc.Err()
}

// Crawls lists the saved raw crawls in dir, oldest first.
func Crawls(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if _, err := time.Parse("20060102T150405Z", e.Name()); err == nil && e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func readGz(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// Load reads the snapshot saved by Save; a missing file is (nil, nil).
func Load(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// Save writes the snapshot atomically.
func Save(path string, s *Snapshot) error {
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
