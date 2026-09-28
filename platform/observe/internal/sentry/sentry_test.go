package sentry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewSince(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/0/projects/raposa/adhunters-go/issues/" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", 403)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"3","shortId":"GO-3","title":"load raw file: timeout","permalink":"https://s/3","firstSeen":"2026-09-28T12:03:00Z","count":"7"},
			{"id":"2","shortId":"GO-2","title":"close hour","permalink":"https://s/2","firstSeen":"2026-09-28T12:01:00Z","count":"1"},
			{"id":"1","shortId":"GO-1","title":"old","permalink":"https://s/1","firstSeen":"2026-09-28T11:00:00Z","count":"99"}
		]`))
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Org: "raposa", Project: "adhunters-go", Token: "tok"}
	got, err := c.NewSince(context.Background(), time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ShortID != "GO-2" || got[1].ShortID != "GO-3" || got[1].Count != 7 {
		t.Fatalf("%+v", got)
	}
	c.Token = "bad"
	if _, err := c.NewSince(context.Background(), time.Time{}); err == nil {
		t.Fatal("want an error")
	}
}
