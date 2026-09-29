// Package ops serves the operations endpoints every binary exposes:
// /healthz for deploys and outside checks, and /metrics for Grafana Alloy.
//
// In production the address is bound to the box's Tailscale IP only
// (OPS_ADDR), so nothing here is reachable from the internet.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Check reports whether one dependency or loop is healthy. It must return
// quickly; it runs on every /healthz request with a 2 s budget.
type Check func(ctx context.Context) error

// Server holds the registry and the health checks of one binary.
type Server struct {
	Registry *prometheus.Registry

	mu     sync.RWMutex
	checks map[string]Check

	tasksOnce sync.Once
	tasks     *Tasks

	creditOnce  sync.Once
	outOfCredit *prometheus.CounterVec
}

// New returns a Server whose registry already carries Go runtime and process
// metrics plus a build_info gauge for the service.
func New(service, version string) *Server {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	info := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "adhunters_build_info",
		Help:        "Always 1; labels carry the service and version that is running.",
		ConstLabels: prometheus.Labels{"service": service, "version": version},
	})
	info.Set(1)
	reg.MustRegister(info)
	return &Server{Registry: reg, checks: map[string]Check{}}
}

// AddCheck registers a named health check.
func (s *Server) AddCheck(name string, c Check) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = c
}

// Handler returns the mux serving /healthz and /metrics.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.Registry, promhttp.HandlerOpts{}))
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	s.mu.RLock()
	names := make([]string, 0, len(s.checks))
	for n := range s.checks {
		names = append(names, n)
	}
	s.mu.RUnlock()
	sort.Strings(names)

	result := map[string]string{}
	healthy := true
	for _, n := range names {
		s.mu.RLock()
		c := s.checks[n]
		s.mu.RUnlock()
		if err := c(ctx); err != nil {
			healthy = false
			result[n] = err.Error()
		} else {
			result[n] = "ok"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"healthy": healthy, "checks": result})
}

// Addr returns OPS_ADDR, or 127.0.0.1:9100 when it is unset.
func Addr() string {
	if a := os.Getenv("OPS_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:9100"
}

// Serve listens on addr until ctx is cancelled, then shuts down within 5 s.
func (s *Server) Serve(ctx context.Context, log *slog.Logger, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Info("ops endpoints listening", "addr", ln.Addr().String())

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
