// Package keep saves what a service receives from outside (an OpenAI reply,
// a Taboola answer) before the service reads it or hands it back, so nothing
// that was paid for or changed exists only in a browser tab (the repo's
// "saved raw before parsing" rule).
//
// A folder holds one sub-folder per UTC day. An image is <time>-<random>.jpg
// with a <time>-<random>.json sidecar beside it; a plan is <time>-<random>-plan.json.
// Files are written whole to a temporary name, synced, then renamed, so a
// crash never leaves half a file under a real name.
package keep

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Folder is one keep folder on disk.
type Folder struct {
	Dir string
	// now is time.Now outside tests.
	now func() time.Time
}

// New returns the keep folder at dir. The folder is made on first write.
func New(dir string) *Folder { return &Folder{Dir: dir, now: time.Now} }

// Image keeps one decoded picture (ext is ".jpg" or ".png") and its sidecar,
// and returns the picture's path.
func (f *Folder) Image(data []byte, ext string, sidecar any) (string, error) {
	dir, stem, err := f.stem()
	if err != nil {
		return "", err
	}
	pic := filepath.Join(dir, stem+ext)
	if err := writeFile(pic, data); err != nil {
		return "", err
	}
	meta, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return "", fmt.Errorf("keep: sidecar: %w", err)
	}
	if err := writeFile(filepath.Join(dir, stem+".json"), meta); err != nil {
		return "", err
	}
	return pic, nil
}

// JSON keeps v as <time>-<random>-<kind>.json and returns its path.
func (f *Folder) JSON(kind string, v any) (string, error) {
	dir, stem, err := f.stem()
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("keep: %s: %w", kind, err)
	}
	path := filepath.Join(dir, stem+"-"+kind+".json")
	return path, writeFile(path, body)
}

// stem makes today's folder and a name no other call will get.
func (f *Folder) stem() (dir, stem string, err error) {
	now := f.now().UTC()
	dir = filepath.Join(f.Dir, now.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("keep: %w", err)
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("keep: %w", err)
	}
	return dir, now.Format("150405") + "-" + hex.EncodeToString(b[:]), nil
}

func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keep-*")
	if err != nil {
		return fmt.Errorf("keep: %w", err)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("keep: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("keep: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("keep: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("keep: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("keep: %w", err)
	}
	done = true
	return nil
}
