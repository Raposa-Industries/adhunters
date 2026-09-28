package logx

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLinesCarryServiceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	NewTo(&buf, "tracks-loader", "v1.2.3", "debug").Debug("hello", "file", "capture-a-1403")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %v: %s", err, buf.String())
	}
	for k, want := range map[string]string{"service": "tracks-loader", "version": "v1.2.3", "msg": "hello", "file": "capture-a-1403"} {
		if line[k] != want {
			t.Errorf("%s = %v, want %s", k, line[k], want)
		}
	}
}

func TestDefaultLevelIsInfo(t *testing.T) {
	var buf bytes.Buffer
	NewTo(&buf, "s", "v", "").Debug("hidden")
	if buf.Len() != 0 {
		t.Fatalf("debug line written at default level: %s", buf.String())
	}
}
