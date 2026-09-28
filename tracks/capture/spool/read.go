package spool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Key is what a sealed raw file's path says about it. The path under the
// spool is the archive key.
type Key struct {
	Network  string
	Instance string
	Minute   time.Time // UTC
}

var keyRe = regexp.MustCompile(`^([a-z0-9]+)/(\d{4})/(\d{2})/(\d{2})/(\d{2})/capture-([a-z0-9_]+)-(\d{2})(\d{2})(?:-\d+)?\.ndjson\.zst$`)

// ParseKey reads a sealed raw file's key:
// <network>/<yyyy>/<mm>/<dd>/<hh>/capture-<instance>-<hhmm>[-n].ndjson.zst.
func ParseKey(key string) (Key, error) {
	m := keyRe.FindStringSubmatch(key)
	if m == nil {
		return Key{}, fmt.Errorf("spool: %q is not a sealed raw file key", key)
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	if m[5] != m[7] {
		return Key{}, fmt.Errorf("spool: %q: hour folder %s and file hour %s differ", key, m[5], m[7])
	}
	t := time.Date(n(m[2]), time.Month(n(m[3])), n(m[4]), n(m[5]), n(m[8]), 0, 0, time.UTC)
	if t.Format("2006/01/02/15") != m[2]+"/"+m[3]+"/"+m[4]+"/"+m[5] || n(m[8]) > 59 {
		return Key{}, fmt.Errorf("spool: %q has no valid minute", key)
	}
	return Key{Network: m[1], Instance: m[6], Minute: t}, nil
}

// MaxLine is the longest record a raw file can hold: an 8 MB answer, escaped
// or in base64, plus the rest of the record.
const MaxLine = 64 << 20

// ReadRecords decompresses a sealed raw file and calls fn with each record
// in order. The record passed to fn is fresh each call.
func ReadRecords(r io.Reader, fn func(line int, rec *Record) error) error {
	dec, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<30))
	if err != nil {
		return err
	}
	defer dec.Close()
	sc := bufio.NewScanner(dec)
	sc.Buffer(make([]byte, 0, 1<<20), MaxLine)
	n := 0
	for sc.Scan() {
		n++
		if len(sc.Bytes()) == 0 {
			continue
		}
		rec := &Record{}
		if err := json.Unmarshal(sc.Bytes(), rec); err != nil {
			return fmt.Errorf("spool: line %d: %w", n, err)
		}
		if err := fn(n, rec); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("spool: after line %d: %w", n, err)
	}
	return nil
}
