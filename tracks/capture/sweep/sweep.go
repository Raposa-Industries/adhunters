// Package sweep is capture's main loop: the collector's sweeper, writing each
// answer to the spool instead of parsing it.
//
// Each worker picks a random target and device, takes a proxy line (datacenter
// first), sends one request, records what came back, then waits the throttle
// (half a second after an error). Ported from adhunters-collector e20148c,
// internal/sweeper/engine.go.
package sweep

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/tracks/capture/feed"
	"github.com/Raposa-Industries/adhunters/tracks/capture/lines"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
)

// MaxBody is the most of one answer kept, as in the collector.
const MaxBody = 8 << 20

// Lines hands out proxy lines. *lines.Pool is the real one.
type Lines interface {
	Next() (key string, rt http.RoundTripper, ok bool)
	Failed(key string)
}

var _ Lines = (*lines.Pool)(nil)

// Config is one capture instance's loop.
type Config struct {
	Targets  []feed.Target
	Lines    Lines
	Spool    *spool.Writer
	Instance string
	Version  string
	Workers  int
	Throttle time.Duration // wait after a scrape
	Backoff  time.Duration // wait after a failed scrape
	Timeout  time.Duration // per request
	Log      *slog.Logger
	Metrics  *Metrics
}

// Metrics are capture's live signals on /metrics.
type Metrics struct {
	Scrapes   *prometheus.CounterVec   // network, publisher, device, outcome
	BodyBytes *prometheus.CounterVec   // network
	Latency   *prometheus.HistogramVec // network
	LastWrite prometheus.Gauge

	LastWriteUnix atomic.Int64 // the same, for the health check
}

// NewMetrics registers capture's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Scrapes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_capture_scrapes_total", Help: "Scrapes by network, publisher, device and outcome (ok, empty, http_<status>, error).",
		}, []string{"network", "publisher", "device", "outcome"}),
		BodyBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_capture_body_bytes_total", Help: "Answer bytes as received, by network.",
		}, []string{"network"}),
		Latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "tracks_capture_latency_seconds", Help: "Request latency by network.",
			Buckets: []float64{.1, .25, .5, 1, 2, 4, 8},
		}, []string{"network"}),
		LastWrite: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_capture_last_write_timestamp_seconds", Help: "When a scrape was last written to the spool.",
		}),
	}
	reg.MustRegister(m.Scrapes, m.BodyBytes, m.Latency, m.LastWrite)
	return m
}

// Run starts the workers and returns when ctx is done and every worker has
// written its last record. A request in flight when ctx ends still finishes
// (within Timeout) and is written, so a stop loses nothing that was received.
func Run(ctx context.Context, c Config) error {
	if len(c.Targets) == 0 {
		return errors.New("sweep: no targets")
	}
	if c.Workers <= 0 {
		c.Workers = 1
	}
	var wg sync.WaitGroup
	for i := range c.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, c, i+1)
		}()
	}
	wg.Wait()
	return nil
}

func worker(ctx context.Context, c Config, id int) {
	log := c.Log.With("worker", id)
	for ctx.Err() == nil {
		t := c.Targets[rand.IntN(len(c.Targets))]
		devices := t.DeviceList()
		device := devices[rand.IntN(len(devices))]
		rec, ok := scrape(context.WithoutCancel(ctx), c, t, device)
		if err := c.Spool.Write(rec); err != nil {
			log.Error("writing to the spool", "err", err)
		} else if c.Metrics != nil {
			c.Metrics.LastWrite.SetToCurrentTime()
			c.Metrics.LastWriteUnix.Store(time.Now().Unix())
		}
		if ctx.Err() != nil {
			return
		}
		wait := c.Throttle
		if !ok {
			wait = c.Backoff
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// scrape sends one request and returns its record; ok is false when the line
// failed (no answer or a status other than 2xx), which cools the line down.
func scrape(ctx context.Context, c Config, t feed.Target, device string) (*spool.Record, bool) {
	network := t.NetworkName()
	rec := &spool.Record{
		ID:        ulid.Make().String(),
		At:        time.Now().UTC(),
		Network:   network,
		Publisher: t.Name,
		Device:    device,
		Instance:  c.Instance,
		Version:   c.Version,
	}
	key, rt, ok := c.Lines.Next()
	if !ok {
		rec.Error = "no proxy line"
		c.count(rec, "error")
		return rec, false
	}
	rec.Line = key

	req, err := feed.Build(ctx, t, device)
	if err != nil {
		rec.Error = "build request: " + err.Error()
		c.count(rec, "error")
		return rec, false
	}
	rec.URL = req.HTTP.URL.String()
	if req.Body != nil {
		rec.RequestBody = string(req.Body)
	}

	client := &http.Client{Transport: rt, Timeout: c.Timeout}
	start := time.Now()
	resp, err := client.Do(req.HTTP)
	if err != nil {
		rec.LatencyMS = time.Since(start).Milliseconds()
		rec.Error = err.Error()
		c.Lines.Failed(key)
		c.count(rec, "error")
		return rec, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	_ = resp.Body.Close()
	rec.LatencyMS = time.Since(start).Milliseconds()
	rec.Status = resp.StatusCode
	rec.SetHeaders(resp.Header)
	if len(body) > MaxBody {
		body = body[:MaxBody]
		rec.Truncated = true
	}
	rec.SetBody(body)
	if err != nil {
		rec.Error = "read body: " + err.Error()
	}
	if c.Metrics != nil {
		c.Metrics.BodyBytes.WithLabelValues(network).Add(float64(len(body)))
		c.Metrics.Latency.WithLabelValues(network).Observe(float64(rec.LatencyMS) / 1000)
	}
	switch {
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		c.Lines.Failed(key)
		c.count(rec, "http_"+strconv.Itoa(resp.StatusCode))
		return rec, false
	case err != nil:
		c.Lines.Failed(key)
		c.count(rec, "error")
		return rec, false
	case len(body) == 0:
		c.count(rec, "empty")
	default:
		c.count(rec, "ok")
	}
	return rec, true
}

func (c Config) count(rec *spool.Record, outcome string) {
	if c.Metrics != nil {
		c.Metrics.Scrapes.WithLabelValues(rec.Network, rec.Publisher, rec.Device, outcome).Inc()
	}
}
