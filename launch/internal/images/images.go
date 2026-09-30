// Package images keeps the pictures people bring to Launch, by their
// SHA-256, before anything else reads them (the repo's "saved raw" rule).
// A new pair and its drafts name pictures by that hash; the network adapter
// reads them back to upload, once per picture.
package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for the formats Taboola takes
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// MaxBytes is the largest picture kept. Taboola takes up to 2.5 MB; the
// page warns above that, and a person may shrink it first.
const MaxBytes = 10 << 20

// Info is what the page needs about a kept picture.
type Info struct {
	SHA    string `json:"sha256"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Bytes  int    `json:"bytes"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Store is a folder of pictures, one file per hash, with a .name beside it.
type Store struct{ Dir string }

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrNotImage is a file that is not a JPEG, PNG, GIF or WebP.
var ErrNotImage = errors.New("o arquivo não é uma imagem JPEG, PNG, GIF ou WebP")

// Put keeps data (written whole, then renamed) and returns what it is.
// Keeping the same picture twice is harmless.
func (s *Store) Put(name string, data []byte) (Info, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return Info{}, fmt.Errorf("a imagem deve ter até %d MB", MaxBytes>>20)
	}
	typ := http.DetectContentType(data)
	info := Info{Name: filepath.Base(name), Type: typ, Bytes: len(data)}
	switch typ {
	case "image/jpeg", "image/png", "image/gif":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return Info{}, ErrNotImage
		}
		info.Width, info.Height = cfg.Width, cfg.Height
	case "image/webp":
		// No decoder in the standard library; the page reads its size.
	default:
		return Info{}, ErrNotImage
	}
	sum := sha256.Sum256(data)
	info.SHA = hex.EncodeToString(sum[:])
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Info{}, err
	}
	path := filepath.Join(s.Dir, info.SHA)
	if _, err := os.Stat(path); err != nil {
		if err := write(path, data); err != nil {
			return Info{}, err
		}
	}
	if err := write(path+".name", []byte(info.Name)); err != nil {
		return Info{}, err
	}
	return info, nil
}

// Get returns a kept picture's bytes and file name.
func (s *Store) Get(sha string) ([]byte, string, error) {
	if !shaRe.MatchString(sha) {
		return nil, "", os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, sha))
	if err != nil {
		return nil, "", err
	}
	name, _ := os.ReadFile(filepath.Join(s.Dir, sha+".name"))
	if len(name) == 0 {
		name = []byte(sha[:10] + ".jpg")
	}
	return data, string(name), nil
}

func write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".img-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
