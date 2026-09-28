package prom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		if r.URL.Path != "/api/prom/api/v1/query" || u != "42" || p != "tok" || r.URL.Query().Get("time") != "1000" {
			http.Error(w, `{"status":"error","error":"bad request"}`, 400)
			return
		}
		switch r.URL.Query().Get("query") {
		case "empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"box":"data"},"value":[1000,"12.5"]}]}}`))
		}
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL + "/api/prom", User: "42", Token: "tok"}
	s, err := c.Query(context.Background(), "up", time.Unix(1000, 0))
	if err != nil || len(s) != 1 || s[0].Labels["box"] != "data" || s[0].Value != 12.5 {
		t.Fatalf("%v %v", s, err)
	}
	if _, ok, err := c.One(context.Background(), "empty", time.Unix(1000, 0)); ok || err != nil {
		t.Fatalf("empty: %v %v", ok, err)
	}
	c.Token = "wrong"
	if _, err := c.Query(context.Background(), "up", time.Unix(1000, 0)); err == nil {
		t.Fatal("want an error")
	}
}
