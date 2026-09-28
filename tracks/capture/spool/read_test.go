package spool

import (
	"bytes"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestParseKey(t *testing.T) {
	k, err := ParseKey("taboola/2026/09/28/14/capture-a-1403-2.ndjson.zst")
	if err != nil {
		t.Fatal(err)
	}
	if k.Network != "taboola" || k.Instance != "a" || !k.Minute.Equal(time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC)) {
		t.Fatalf("got %+v", k)
	}
	for _, bad := range []string{
		"taboola/2026/09/28/14/capture-a-1403.ndjson",     // not sealed
		"taboola/2026/09/28/15/capture-a-1403.ndjson.zst", // folder and name disagree
		"taboola/2026/02/30/14/capture-a-1403.ndjson.zst", // no such day
		"taboola/2026/09/28/14/capture-a-1463.ndjson.zst", // no such minute
		"../etc/2026/09/28/14/capture-a-1403.ndjson.zst",
	} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestReadRecordsRoundTrips(t *testing.T) {
	var plain bytes.Buffer
	body := "caf\xe9" // not UTF-8: goes as base64
	in := []*Record{{ID: "1", Network: "taboola"}, {ID: "2", Network: "newsbreak"}}
	in[1].SetBody([]byte(body))
	for _, r := range in {
		line, err := r.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		plain.Write(line)
	}
	var zst bytes.Buffer
	enc, _ := zstd.NewWriter(&zst)
	_, _ = enc.Write(plain.Bytes())
	_ = enc.Close()

	var got []*Record
	if err := ReadRecords(&zst, func(_ int, r *Record) error { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "1" || got[1].Network != "newsbreak" {
		t.Fatalf("got %+v", got)
	}
	if b, _ := got[1].BodyBytes(); string(b) != body {
		t.Errorf("body %q", b)
	}
}
