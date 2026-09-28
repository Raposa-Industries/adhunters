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
}

// Summarize reads every sealed raw file under dir and sums them per network
// and day. It decompresses each file, so it measures what the loader will read.
func Summarize(dir string) ([]Totals, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	sums := map[string]*Totals{}
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
		return addFile(dec, path, t)
	})
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

func addFile(dec *zstd.Decoder, path string, t *Totals) error {
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
	_, _ = fmt.Fprintln(tw, "day\tnetwork\tfiles\tscrapes\terrors\tempty\tbody KB/scrape\traw KB/scrape\tzst KB/scrape\tratio\t")
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
	}
	row(tw, "all", "all", all)
	_ = tw.Flush()
	if all.Scrapes == 0 {
		return
	}
	perDay := float64(all.ZstBytes) / float64(all.Scrapes) * float64(scrapesPerHour) * 24
	_, _ = fmt.Fprintf(w, "\nAt %d scrapes an hour, the archive grows %.2f GB a day (%.0f GB a year) compressed,\nand the spool holds %.2f GB a day before sealing.\n",
		scrapesPerHour, perDay/1e9, perDay*365/1e9, float64(all.RawBytes)/float64(all.Scrapes)*float64(scrapesPerHour)*24/1e9)
}

func row(tw io.Writer, day, network string, t Totals) {
	if t.Scrapes == 0 {
		return
	}
	kb := func(b int64) string { return fmt.Sprintf("%.1f", float64(b)/float64(t.Scrapes)/1000) }
	ratio := 0.0
	if t.ZstBytes > 0 {
		ratio = float64(t.RawBytes) / float64(t.ZstBytes)
	}
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%.1fx\t\n",
		day, network, t.Files, t.Scrapes, t.Errors, t.Empty, kb(t.BodyBytes), kb(t.RawBytes), kb(t.ZstBytes), ratio)
}
