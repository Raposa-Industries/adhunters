package engine

import "testing"

func TestClientRedirect(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"meta refresh at once",
			`<html><head><meta http-equiv="refresh" content="0; url=https://track.example.com/abc?a=1&amp;b=2"><title>Page is Loading...</title></head></html>`,
			"https://track.example.com/abc?a=1&b=2"},
		{"meta refresh, relative",
			`<meta http-equiv='refresh' content='1;URL=/next'>`,
			"https://lander.example.com/next"},
		{"a slow refresh is a page", `<meta http-equiv="refresh" content="30; url=https://x.example.com/">`, ""},
		{"a tiny script redirect", `<script>window.location.href = "https://dark.example.com/vsl";</script>`, "https://dark.example.com/vsl"},
		{"location.replace", `<script>location.replace('https://dark.example.com/a')</script>`, "https://dark.example.com/a"},
		{"a plain page", `<html><body><h1>Hello</h1></body></html>`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clientRedirect([]byte(c.body), "https://lander.example.com/start"); got != c.want {
				t.Fatalf("clientRedirect = %q, want %q", got, c.want)
			}
		})
	}
}

// A refresh tag quoted in a script comment is not a redirect.
func TestClientRedirectIgnoresScripts(t *testing.T) {
	body := `<html><head><title>Article</title><script>
    // <meta http-equiv="refresh" content="0; url=...">
    </script></head><body>` + string(make([]byte, 8000)) + `</body></html>`
	if got := clientRedirect([]byte(body), "https://tracker.example.com/x"); got != "" {
		t.Fatalf("followed a refresh quoted in a script: %q", got)
	}
}
