package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New("123:abc", "-10042")
	c.API = srv.URL
	if err := c.Send(context.Background(), "<b>hi</b>", true); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:abc/sendMessage" || got["chat_id"] != "-10042" || got["parse_mode"] != "HTML" || got["disable_notification"] != true {
		t.Fatalf("path %s body %v", path, got)
	}
}

func TestErrorsNeverCarryTheToken(t *testing.T) {
	c := New("123:secret", "-1")
	c.API = "http://127.0.0.1:1" // nothing listens
	err := c.Send(context.Background(), "x", false)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err %v", err)
	}
}
