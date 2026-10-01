package spool

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"

	rawspool "github.com/Raposa-Industries/adhunters/shared/spool"
)

// Key is what a sealed raw file's path says about it. The path under the
// spool is the archive key.
type Key struct {
	Network  string
	Instance string
	Minute   time.Time // UTC
}

// instanceRe is an instance name: lowercase words joined by "-", so a host
// name such as adhunters-worker (tracks-walker runs with -instance %H) is one.
var instanceRe = regexp.MustCompile(`^[a-z0-9_]+(?:-[a-z0-9_]+)*$`)

var keyRe = regexp.MustCompile(`^([a-z0-9]+)/(\d{4})/(\d{2})/(\d{2})/(\d{2})/capture-([a-z0-9_]+(?:-[a-z0-9_]+)*?)-(\d{2})(\d{2})(?:-\d+)?\.ndjson\.zst$`)

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

// MaxLine is the longest record a raw file can hold (shared/spool).
const MaxLine = rawspool.MaxLine

// ReadRecords decompresses a sealed raw file and calls fn with each record
// in order. The record passed to fn is fresh each call.
func ReadRecords(r io.Reader, fn func(line int, rec *Record) error) error {
	return ReadLines(r, func(n int, b []byte) error {
		rec := &Record{}
		if err := json.Unmarshal(b, rec); err != nil {
			return fmt.Errorf("spool: line %d: %w", n, err)
		}
		return fn(n, rec)
	})
}

// ReadLines decompresses a sealed raw file and calls fn with each non-empty
// line, for files whose lines are not capture's records (tracks-walker's
// walks). The bytes passed to fn are only valid during the call.
func ReadLines(r io.Reader, fn func(line int, b []byte) error) error {
	return rawspool.ReadLines(r, fn)
}
