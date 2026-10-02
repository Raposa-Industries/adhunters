package walk

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// Lines hands out proxy lines (tracks/capture/lines.Pool).
type Lines interface {
	Next() (key string, rt http.RoundTripper, ok bool)
	Failed(key string)
}

// Spool takes raw file lines (tracks/capture/spool.Writer).
type Spool interface {
	WriteLine(network string, line []byte) error
}

// Config is one walker.
type Config struct {
	DB          *pgxpool.Pool
	Lines       Lines
	Spool       Spool
	Instance    string
	Version     string
	Workers     int           // parallel walks (2)
	Pause       time.Duration // after each walk, per worker (1 s)
	PageTimeout time.Duration // per page, redirects included (15 s)
	Rewalk      time.Duration // how soon an ad is walked again (6 h)
	Poll        time.Duration // how often the due list is read when idle (30 s)
	BacklogPoll time.Duration // how often the backlog gauges are read (1 min)
	Now         func() time.Time
	Log         *slog.Logger
	Metrics     *Metrics
}

// Metrics are the walker's counters.
type Metrics struct {
	Walks         *prometheus.CounterVec
	Seconds       prometheus.Histogram
	Due           prometheus.Gauge
	Backlog       prometheus.Gauge
	OldestOverdue prometheus.Gauge
	LastWalkUnix  atomic.Int64
	LastErrorUnix atomic.Int64
}

// NewMetrics registers the walker's metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Walks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_walker_walks_total", Help: "Walks by outcome (ok, http_error, error) and whether a next step followed.",
		}, []string{"outcome", "step"}),
		Seconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "tracks_walker_walk_seconds", Help: "How long a walk took, both pages.",
			Buckets: []float64{0.5, 1, 2, 4, 8, 15, 30},
		}),
		Due: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_walker_due", Help: "Ads due for a walk at the last read of the list (at most one batch).",
		}),
		Backlog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_walker_backlog", Help: "Ads the walker would be handed now (tracks.walks_due), never walked included.",
		}),
		OldestOverdue: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_walker_oldest_overdue_seconds", Help: "The longest any of those ads walked before has waited, since it fell due or was seen again after that (0: none).",
		}),
	}
	reg.MustRegister(m.Walks, m.Seconds, m.Due, m.Backlog, m.OldestOverdue)
	return m
}

func (c *Config) defaults() {
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.Pause <= 0 {
		c.Pause = time.Second
	}
	if c.PageTimeout <= 0 {
		c.PageTimeout = 15 * time.Second
	}
	if c.Rewalk <= 0 {
		c.Rewalk = 6 * time.Hour
	}
	if c.Poll <= 0 {
		c.Poll = 30 * time.Second
	}
	if c.BacklogPoll <= 0 {
		c.BacklogPoll = time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Metrics == nil {
		c.Metrics = NewMetrics(prometheus.NewRegistry())
	}
}

// Run walks due ads until ctx ends. A walk in flight finishes and is written.
func Run(ctx context.Context, c Config) error {
	c.defaults()
	work := make(chan Due)
	// Ads handed to a worker and not recorded yet: the next read of the due
	// list still has them.
	var mu sync.Mutex
	flying := map[int]bool{}
	var wg sync.WaitGroup
	for range c.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range work {
				c.walkOne(d)
				mu.Lock()
				delete(flying, d.AdID)
				mu.Unlock()
				select {
				case <-ctx.Done():
				case <-time.After(c.Pause):
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.watchBacklog(ctx)
	}()
	defer func() {
		close(work)
		wg.Wait()
	}()
	for ctx.Err() == nil {
		dues, err := Dues(ctx, c.DB, c.Workers*20, c.Now())
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.Log.Error("reading the ads due for a walk", "err", err)
			c.Metrics.LastErrorUnix.Store(c.Now().Unix())
		}
		c.Metrics.Due.Set(float64(len(dues)))
		sent := 0
		for _, d := range dues {
			mu.Lock()
			busy := flying[d.AdID]
			flying[d.AdID] = true
			mu.Unlock()
			if busy {
				continue
			}
			select {
			case work <- d:
				sent++
			case <-ctx.Done():
				return nil
			}
		}
		if sent == 0 || len(dues) < c.Workers*20 {
			select {
			case <-ctx.Done():
			case <-time.After(c.Poll):
			}
		}
	}
	return nil
}

// watchBacklog sets the backlog gauges every BacklogPoll until ctx ends.
func (c *Config) watchBacklog(ctx context.Context) {
	for {
		n, oldest, err := Backlog(ctx, c.DB, c.Now())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.Log.Error("reading the walker backlog", "err", err)
			c.Metrics.LastErrorUnix.Store(c.Now().Unix())
		} else {
			c.Metrics.Backlog.Set(float64(n))
			c.Metrics.OldestOverdue.Set(oldest.Seconds())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.BacklogPoll):
		}
	}
}

// walkOne walks one ad, writes the raw walk, then saves what it said. It
// runs to the end even while stopping, so what was walked is kept.
func (c *Config) walkOne(d Due) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*c.PageTimeout)
	defer cancel()
	key, rt, ok := c.Lines.Next()
	if !ok {
		c.Log.Error("no proxy line to walk with")
		time.Sleep(c.Pause)
		return
	}
	at := c.Now().UTC()
	rec := &Record{
		ID: ulid.MustNew(ulid.Timestamp(at), rand.Reader).String(), At: at, Instance: c.Instance, Version: c.Version,
		Line: key, AdID: d.AdID, CreativeID: d.CreativeID, AccountID: d.AccountID, LinkID: d.LinkID, PublisherID: d.PublisherID,
	}
	referer := ""
	if d.Referer != nil {
		referer = *d.Referer
	}
	t0 := time.Now()
	rec.Pages = Trace(ctx, rt, d.URL, referer, c.PageTimeout)
	rec.LatencyMS = time.Since(t0).Milliseconds()
	outcome := rec.Outcome()
	if outcome == "error" {
		c.Lines.Failed(key)
	}
	step := "no"
	if len(rec.Pages) > 1 {
		step = "yes"
	}
	c.Metrics.Walks.WithLabelValues(outcome, step).Inc()
	c.Metrics.Seconds.Observe(time.Since(t0).Seconds())

	line, err := rec.Marshal()
	if err == nil {
		err = c.Spool.WriteLine(Network, line)
	}
	if err != nil {
		// Nothing is saved that is not in a raw file.
		c.Log.Error("writing a walk to the spool", "ad", d.AdID, "err", err)
		c.Metrics.LastErrorUnix.Store(c.Now().Unix())
		return
	}
	if err := Save(ctx, c.DB, rec, Parse(rec)); err != nil {
		c.Log.Error("saving a walk", "ad", d.AdID, "walk", rec.ID, "err", err)
		c.Metrics.LastErrorUnix.Store(c.Now().Unix())
	}
	if err := Walked(ctx, c.DB, d.AdID, at, outcome == "ok", c.Rewalk); err != nil {
		c.Log.Error("recording a walk", "ad", d.AdID, "err", err)
	}
	c.Metrics.LastWalkUnix.Store(c.Now().Unix())
}
