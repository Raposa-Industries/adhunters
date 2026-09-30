package spool

import (
	"bufio"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// MaxLine is the longest line a raw file can hold: an 8 MB feed answer,
// escaped or in base64, plus the rest of its record.
const MaxLine = 64 << 20

// ReadLines decompresses a sealed raw file and calls fn with each non-empty
// line, in order, numbered from 1. The bytes passed to fn are only valid
// during the call.
func ReadLines(r io.Reader, fn func(line int, b []byte) error) error {
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
		if err := fn(n, sc.Bytes()); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("spool: after line %d: %w", n, err)
	}
	return nil
}
