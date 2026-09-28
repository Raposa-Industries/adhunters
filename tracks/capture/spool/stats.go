package spool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/klauspost/compress/zstd"
)

// Totals sums the sealed raw files of one network and UTC day.
type Totals struct {
	Network   string
	Day       string // yyyy-mm-dd, from the file's folder
	Files     int
	Scrapes   int
	Errors    int   // no answer, or a status other than 2xx
	Empty     int   // 2xx with an empty body
	BodyBytes int64 // answers as received
	RawBytes  int64 // the plain files, records and all
	ZstBytes  int64 // what goes to the archive

	// HourZstBytes is the same records compressed one hour per file. A slow
	// shadow run puts few scrapes in each minute's file, and zstd does better
	// with more of them; an hour of a slow run is about a minute at full rate,
	// so this is the realistic archive size.
	HourZstBytes int64
}

// Summarize reads every sealed raw file under dir and sums them per network
// and day. It decompresses each file, so it measures what the loader will read.
func Summarize(dir string) ([]Totals, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(9)), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	sums := map[string]*Totals{}
	// Files are walked in path order, so each network's hour comes whole.
	hp := &hourPack{enc: enc}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".ndjson.zst") {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 6 {
			return nil // not a raw file of this layout
		}
		key := parts[0] + " " + parts[1] + "-" + parts[2] + "-" + parts[3]
		t := sums[key]
		if t == nil {
			t = &Totals{Network: parts[0], Day: parts[1] + "-" + parts[2] + "-" + parts[3]}
			sums[key] = t
		}
		if err := hp.next(filepath.Dir(path), t); err != nil {
			return err
		}
		return addFile(dec, path, t, hp)
	})
	if err == nil {
		err = hp.next("", nil)
	}
	if err != nil {
		return nil, err
	}
	out := make([]Totals, 0, len(sums))
	for _, t := range sums {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].Network < out[j].Network
	})
	return out, nil
}

// hourPack compresses one hour folder's records as one stream and adds its
// size to that hour's totals.
type hourPack struct {
	enc  *zstd.Encoder
	dir  string
	into *Totals
	out  countingWriter
}

func (h *hourPack) next(dir string, t *Totals) error {
	if dir == h.dir && h.into != nil {
		return nil
	}
	if h.into != nil {
		if err := h.enc.Close(); err != nil {
			return err
		}
		h.into.HourZstBytes += h.out.n
	}
	h.dir, h.into, h.out = dir, t, countingWriter{}
	if t != nil {
		h.enc.Reset(&h.out)
	}
	return nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func addFile(dec *zstd.Decoder, path string, t *Totals, hp *hourPack) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := dec.Reset(f); err != nil {
		return err
	}
	cr := &countingReader{r: dec}
	sc := bufio.NewScanner(cr)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var rec Record
		if _, err := hp.enc.Write(append(sc.Bytes(), '\n')); err != nil {
			return err
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		body, err := rec.BodyBytes()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		t.Scrapes++
		t.BodyBytes += int64(len(body))
		switch {
		case rec.Status < 200 || rec.Status > 299:
			t.Errors++
		case len(body) == 0:
			t.Empty++
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	t.Files++
	t.RawBytes += cr.n
	t.ZstBytes += st.Size()
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Report prints totals as a table, with bytes per scrape and what the archive
// would take per day at scrapesPerHour (the live collector's rate), split
// between networks as they were in the measured files.
func Report(w io.Writer, totals []Totals, scrapesPerHour int) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(tw, "day\tnetwork\tfiles\tscrapes\terrors\tempty\tbody KB/scrape\traw KB/scrape\tzst KB/scrape\tratio\tzst per hour KB/scrape\tratio\t")
	var all Totals
	for _, t := range totals {
		row(tw, t.Day, t.Network, t)
		all.Files += t.Files
		all.Scrapes += t.Scrapes
		all.Errors += t.Errors
		all.Empty += t.Empty
		all.BodyBytes += t.BodyBytes
		all.RawBytes += t.RawBytes
		all.ZstBytes += t.ZstBytes
		all.HourZstBytes += t.HourZstBytes
	}
	row(tw, "all", "all", all)
	_ = tw.Flush()
	if all.Scrapes == 0 {
		return
	}
	day := func(b int64) float64 { return float64(b) / float64(all.Scrapes) * float64(scrapesPerHour) * 24 }
	perDay := day(all.HourZstBytes)
	_, _ = fmt.Fprintf(w, "\nAt %d scrapes an hour, the archive grows %.2f GB a day (%.0f GB a year) compressed\n"+
		"(%.2f GB a day as this run's minute files, the upper bound),\nand the spool holds %.2f GB a day before sealing.\n",
		scrapesPerHour, perDay/1e9, perDay*365/1e9, day(all.ZstBytes)/1e9, day(all.RawBytes)/1e9)
}

func row(tw io.Writer, day, network string, t Totals) {
	if t.Scrapes == 0 {
		return
	}
	kb := func(b int64) string { return fmt.Sprintf("%.1f", float64(b)/float64(t.Scrapes)/1000) }
	ratio := func(zst int64) float64 {
		if zst == 0 {
			return 0
		}
		return float64(t.RawBytes) / float64(zst)
	}
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%.1fx\t%s\t%.1fx\t\n",
		day, network, t.Files, t.Scrapes, t.Errors, t.Empty, kb(t.BodyBytes), kb(t.RawBytes),
		kb(t.ZstBytes), ratio(t.ZstBytes), kb(t.HourZstBytes), ratio(t.HourZstBytes))
}
