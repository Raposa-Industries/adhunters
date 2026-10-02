package logins

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyMadeOnceAndKept(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "login.key")
	a, err := OpenKey(p)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 || st.Size() != keySize {
		t.Fatalf("key file %v %v", st, err)
	}
	sealed, err := a.Seal([]byte("x"), []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Open(sealed, []byte("b")); err != nil || string(got) != "x" {
		t.Fatalf("reopened key: %q %v", got, err)
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKey(p); err == nil {
		t.Fatal("a short key was accepted")
	}
}

func TestKeyFromSetting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "login.key")
	file, err := OpenKey(p)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := file.Seal([]byte("x"), []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	same := base64.StdEncoding.EncodeToString(raw)

	// The setting holding the file's key opens what the file's key sealed.
	b, differs, err := KeyFrom(" "+same+"\n", p)
	if err != nil || differs {
		t.Fatalf("same key: differs %v, %v", differs, err)
	}
	if got, err := b.Open(sealed, []byte("b")); err != nil || string(got) != "x" {
		t.Fatalf("opened with the setting: %q %v", got, err)
	}

	// Another key in the setting wins, says so, and leaves the file alone.
	other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, keySize))
	b, differs, err = KeyFrom(other, p)
	if err != nil || !differs {
		t.Fatalf("other key: differs %v, %v", differs, err)
	}
	if _, err := b.Open(sealed, []byte("b")); err == nil {
		t.Fatal("another key opened the file key's secret")
	}
	if now, _ := os.ReadFile(p); !bytes.Equal(now, raw) {
		t.Fatal("the key file changed")
	}

	// With the setting, no key file is made.
	none := filepath.Join(dir, "none", "login.key")
	if _, differs, err := KeyFrom(same, none); err != nil || differs {
		t.Fatalf("no file: differs %v, %v", differs, err)
	}
	if _, err := os.Stat(none); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a key file was made: %v", err)
	}

	// Bad settings are refused without the setting in the error.
	for _, bad := range []string{"not base64 !!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		_, _, err := KeyFrom(bad, p)
		if err == nil {
			t.Fatalf("%q was accepted", bad)
		}
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("the error holds the setting: %v", err)
		}
	}
}
