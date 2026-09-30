package drive_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/library/internal/drive"
	"github.com/Raposa-Industries/adhunters/library/internal/drive/drivetest"
)

func TestCalls(t *testing.T) {
	fake := drivetest.New(t)
	fake.PageSize = 2
	c := drive.NewForTest(fake.App(), "refresh")
	ctx := context.Background()

	if who, err := c.Account(ctx); err != nil || who != "library@example.com" {
		t.Fatalf("account %q %v", who, err)
	}
	folder, err := c.CreateFolder(ctx, drivetest.Root, "Blood Pressure", nil)
	if err != nil || !folder.IsFolder() {
		t.Fatalf("folder %+v %v", folder, err)
	}
	f, err := c.Upload(ctx, folder.ID, "BPT1.png", "image/png", []byte("pixels\r\n"), map[string]string{"ahId": "1"})
	if err != nil || f.Bytes() != 8 || f.MD5 == "" || f.AppProperties["ahId"] != "1" {
		t.Fatalf("upload %+v %v", f, err)
	}
	for i := 0; i < 2; i++ {
		fake.Add(drivetest.Root, "x.txt", "text/plain", []byte("x"))
	}
	// Three things in the root, two to a page; a 429 on the way is waited out.
	fake.FailNext = 1
	var names []string
	token := ""
	for {
		p, err := c.List(ctx, drivetest.Root, token)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Files {
			names = append(names, f.Name)
		}
		if p.Next == "" {
			break
		}
		token = p.Next
	}
	if len(names) != 3 {
		t.Fatalf("listed %v", names)
	}
	got, err := c.Download(ctx, f.ID)
	if err != nil || string(got) != "pixels\r\n" {
		t.Fatalf("download %q %v", got, err)
	}
	if err := c.Label(ctx, f.ID, map[string]string{"ahId": "2"}); err != nil {
		t.Fatal(err)
	}
	if g, _ := fake.Get(f.ID); g.AppProperties["ahId"] != "2" {
		t.Errorf("label not set: %+v", g.AppProperties)
	}
	if _, err := c.Replace(ctx, f.ID, "image/png", []byte("more pixels")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "nope"); err == nil {
		t.Error("a missing file has no error")
	}

	revoked := drive.NewForTest(fake.App(), "revoked")
	if _, err := revoked.Account(ctx); !errors.Is(err, drive.ErrSignedOut) {
		t.Errorf("revoked sign-in: %v", err)
	}
}

func TestLogin(t *testing.T) {
	fake := drivetest.New(t)
	l, err := drive.StartLogin(fake.App())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(l.URL())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("access_type") != "offline" || q.Get("scope") != drive.Scope || q.Get("code_challenge") == "" {
		t.Fatalf("consent URL %s", u)
	}
	if _, err := l.Finish(context.Background(), drive.LoginRedirect+"?state=other&code=abc"); err == nil {
		t.Error("a pasted address from another sign-in was taken")
	}
	back := drive.LoginRedirect + "?state=" + url.QueryEscape(q.Get("state")) + "&code=abc&scope=" + url.QueryEscape(drive.Scope)
	tok, err := l.Finish(context.Background(), "  "+back+"\n")
	if err != nil || tok != "refresh-abc" {
		t.Fatalf("finish %q %v", tok, err)
	}
	if _, err := l.Finish(context.Background(), "nonsense"); err == nil || !strings.Contains(err.Error(), "address") {
		t.Errorf("nonsense: %v", err)
	}
}
