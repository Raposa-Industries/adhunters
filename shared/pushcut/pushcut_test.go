package pushcut

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	var got Message
	var path, key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key = r.URL.EscapedPath(), r.Header.Get("API-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	c := New("k1")
	c.API = srv.URL + "/v1/notifications/"
	if err := c.Send(context.Background(), "Spy watch", Message{Title: "OP7 está subindo", Text: "why", Input: "7"}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/notifications/Spy%20watch" || key != "k1" || got.Title != "OP7 está subindo" || got.Input != "7" {
		t.Fatalf("path %s key %s body %+v", path, key, got)
	}
}

func TestSendFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such notification", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New("k1")
	c.API = srv.URL + "/"
	err := c.Send(context.Background(), "x", Message{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err %v", err)
	}
}
