package lines

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadPrefersDatacenterAndCoolsDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.env")
	os.WriteFile(path, []byte(`# lines
dc-1=10.0.0.1:8000:u:p
dc-2=10.0.0.2:8000:u:p
isp-1=10.0.0.3:8000:u:p
isp-6=10.0.0.4:8000:u:p
res-1=10.0.0.5:8000:u:p
broken
`), 0o600)
	p, err := Load(path, []string{"dc", "isp"}, []string{"isp-1"}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if p.Size() != 3 {
		t.Fatalf("size %d, want dc-1, dc-2, isp-6", p.Size())
	}
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }

	seen := map[string]bool{}
	for range 4 {
		k, rt, ok := p.Next()
		if !ok || rt == nil {
			t.Fatal("no line")
		}
		seen[k] = true
	}
	if !seen["dc-1"] || !seen["dc-2"] || len(seen) != 2 {
		t.Fatalf("round robin over dc lines only, got %v", seen)
	}

	p.Failed("dc-1")
	p.Failed("dc-2")
	if k, _, _ := p.Next(); k != "isp-6" {
		t.Fatalf("got %s, want isp-6 while dc lines cool down", k)
	}
	if p.CoolingDown() != 2 {
		t.Fatalf("cooling %d", p.CoolingDown())
	}
	p.Failed("isp-6")
	if k, _, ok := p.Next(); !ok || k == "" {
		t.Fatal("all cooling down should still give the least recently used line")
	}
	now = now.Add(31 * time.Second)
	if k, _, _ := p.Next(); k != "dc-1" && k != "dc-2" {
		t.Fatalf("got %s after cooldown", k)
	}
}
