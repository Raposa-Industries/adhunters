package logins

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// keySize is AES-256's key length.
const keySize = 32

// ErrSealed is a secret that cannot be opened: the key file changed or the
// row was altered. The login has to be added again.
var ErrSealed = errors.New("logins: secret cannot be opened with this key")

// Box seals and opens secrets with AES-256-GCM. The key lives in a file on
// the server's disk, apart from the database that holds what it seals.
type Box struct{ aead cipher.AEAD }

// OpenKey reads the key at path, making a new random one (mode 0600, its
// folder 0700) when there is none yet.
func OpenKey(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		// O_EXCL: two starts at once never write two keys.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return OpenKey(path)
		}
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("logins: key file %s holds %d bytes, not %d", path, len(key), keySize)
	}
	return NewBox(key)
}

// NewBox is a box with the given 32-byte key.
func NewBox(key []byte) (*Box, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce and ciphertext. bound is data the secret is tied to
// (its network and client id), so a sealed secret moved to another row does
// not open.
func (b *Box) Seal(secret, bound []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, secret, bound), nil
}

// Open reverses Seal.
func (b *Box) Open(sealed, bound []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrSealed
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], bound)
	if err != nil {
		return nil, ErrSealed
	}
	return out, nil
}
