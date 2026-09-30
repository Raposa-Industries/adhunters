package spool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Sealed describes one raw file that was closed and compressed.
type Sealed struct {
	Network  string
	Path     string // the .ndjson.zst file
	Rows     int
	RawBytes int64
	ZstBytes int64
	Took     time.Duration
}

// Writer writes records to the open minute's file of their network and seals
// each file when its minute ends. Files are cut by the minute a record is
// written in, so a record's "at" can be up to one request timeout earlier.
type Writer struct {
	dir      string
	instance string
	log      *slog.Logger
	now      func() time.Time
	onSealed func(Sealed)

	mu     sync.Mutex
	open   map[string]*openFile
	closed bool

	queue chan pending
	done  chan struct{}
}

type openFile struct {
	minute time.Time
	path   string
	f      *os.File
	bw     *bufio.Writer
}

type pending struct {
	network string
	path    string
}

// Options tune a Writer. Zero values are the production defaults.
type Options struct {
	Now      func() time.Time
	OnSealed func(Sealed)
}

// Open starts a writer under dir for instance. Files a previous run left
// unsealed (it crashed, or was killed) are sealed first.
func Open(dir, instance string, log *slog.Logger, opt Options) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	w := &Writer{
		dir:      dir,
		instance: instance,
		log:      log,
		now:      opt.Now,
		onSealed: opt.OnSealed,
		open:     map[string]*openFile{},
		queue:    make(chan pending, 4096),
		done:     make(chan struct{}),
	}
	if w.now == nil {
		w.now = time.Now
	}
	leftovers, err := w.leftovers()
	if err != nil {
		return nil, err
	}
	go w.sealLoop()
	for _, p := range leftovers {
		w.log.Warn("sealing a raw file a previous run left open", "file", p.path)
		w.queue <- p
	}
	return w, nil
}

// leftovers finds plain files of this instance and removes half-written
// compressed ones, which are redone from the plain file.
func (w *Writer) leftovers() ([]pending, error) {
	var out []pending
	prefix := "capture-" + w.instance + "-"
	err := filepath.WalkDir(w.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), prefix) {
			return err
		}
		switch {
		case strings.HasSuffix(path, ".ndjson.zst.tmp"):
			return os.Remove(path)
		case strings.HasSuffix(path, ".ndjson"):
			rel, _ := filepath.Rel(w.dir, path)
			network, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
			out = append(out, pending{network: network, path: path})
		}
		return nil
	})
	return out, err
}

// Write appends one record to its network's open file.
func (w *Writer) Write(rec *Record) error {
	line, err := rec.Marshal()
	if err != nil {
		return err
	}
	return w.WriteLine(rec.Network, line)
}

// WriteLine appends one line, ending in a newline, to network's open file:
// for writers of lines other than capture's records (tracks-walker).
func (w *Writer) WriteLine(network string, line []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("spool: writer is closed")
	}
	minute := w.now().UTC().Truncate(time.Minute)
	of := w.open[network]
	if of != nil && !of.minute.Equal(minute) {
		w.closeLocked(network, of)
		of = nil
	}
	if of == nil {
		var err error
		if of, err = w.create(network, minute); err != nil {
			return err
		}
		w.open[network] = of
	}
	_, err := of.bw.Write(line)
	return err
}

func (w *Writer) create(network string, minute time.Time) (*openFile, error) {
	dir := filepath.Join(w.dir, network, minute.Format("2006/01/02/15"))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	// A restart inside the same minute finds that minute's file already there
	// (sealed, or being sealed), and starts a second one beside it.
	stem := fmt.Sprintf("capture-%s-%s", w.instance, minute.Format("1504"))
	for n := 1; ; n++ {
		name := stem
		if n > 1 {
			name = fmt.Sprintf("%s-%d", stem, n)
		}
		path := filepath.Join(dir, name+".ndjson")
		if exists(path + ".zst") {
			continue
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &openFile{minute: minute, path: path, f: f, bw: bufio.NewWriterSize(f, 1<<20)}, nil
	}
}

// closeLocked flushes and closes a file and queues it for sealing.
func (w *Writer) closeLocked(network string, of *openFile) {
	err := of.bw.Flush()
	if err == nil {
		err = of.f.Sync()
	}
	if cerr := of.f.Close(); err == nil {
		err = cerr
	}
	delete(w.open, network)
	if err != nil {
		// The plain file stays on disk and is sealed at the next start.
		w.log.Error("closing raw file", "file", of.path, "err", err)
		return
	}
	w.queue <- pending{network: network, path: of.path}
}

// Tick closes files whose minute has ended. Run calls it every second.
func (w *Writer) Tick() {
	w.mu.Lock()
	defer w.mu.Unlock()
	minute := w.now().UTC().Truncate(time.Minute)
	for network, of := range w.open {
		if of.minute.Before(minute) {
			w.closeLocked(network, of)
		}
	}
}

// Run closes ended minutes until ctx is done. Call Close after it returns.
func (w *Writer) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick()
		}
	}
}

// Close closes every open file, seals them, and waits for the sealing to end.
func (w *Writer) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	for network, of := range w.open {
		w.closeLocked(network, of)
	}
	w.closed = true
	close(w.queue)
	w.mu.Unlock()
	<-w.done
}

// Pending is how many closed files wait to be sealed.
func (w *Writer) Pending() int { return len(w.queue) }

func (w *Writer) sealLoop() {
	defer close(w.done)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(9)), zstd.WithEncoderConcurrency(1))
	if err != nil {
		panic(err) // only fails on invalid options
	}
	defer func() { _ = enc.Close() }()
	for p := range w.queue {
		start := time.Now()
		s, err := seal(enc, p.path)
		if err != nil {
			w.log.Error("sealing raw file; the plain file stays and is sealed at the next start", "file", p.path, "err", err)
			continue
		}
		s.Network = p.network
		s.Took = time.Since(start)
		w.log.Info("raw file sealed", "file", s.Path, "network", s.Network, "rows", s.Rows,
			"raw_bytes", s.RawBytes, "zst_bytes", s.ZstBytes, "took_ms", s.Took.Milliseconds())
		if w.onSealed != nil {
			w.onSealed(s)
		}
	}
}

// seal compresses path to path.zst and removes path once the compressed file
// is on disk.
func seal(enc *zstd.Encoder, path string) (Sealed, error) {
	in, err := os.Open(path)
	if err != nil {
		return Sealed{}, err
	}
	defer func() { _ = in.Close() }()
	final := path + ".zst"
	tmp := final + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return Sealed{}, err
	}
	enc.Reset(out)
	lc := &lineCounter{}
	raw, err := io.Copy(enc, io.TeeReader(in, lc))
	if err == nil {
		err = enc.Close()
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return Sealed{}, err
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return Sealed{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return Sealed{}, err
	}
	syncDir(filepath.Dir(final))
	if err := os.Remove(path); err != nil {
		return Sealed{}, err
	}
	return Sealed{Path: final, Rows: lc.n, RawBytes: raw, ZstBytes: st.Size()}, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

type lineCounter struct{ n int }

func (c *lineCounter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			c.n++
		}
	}
	return len(p), nil
}
