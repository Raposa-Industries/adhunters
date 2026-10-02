package pg

import (
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/kit/ops"
)

// Every pool Open returns shows on the process's /metrics (kit/ops) under
// its AppName: how many connections are in use, and how often and how long
// queries waited for one because all were.

var (
	poolConns    = prometheus.NewDesc("adhunters_db_pool_connections", "Connections in the pool now, by state: acquired (in use by a query or a transaction), idle, constructing.", []string{"pool", "state"}, nil)
	poolMax      = prometheus.NewDesc("adhunters_db_pool_max_connections", "Most connections the pool may hold (MaxConns).", []string{"pool"}, nil)
	poolAcquires = prometheus.NewDesc("adhunters_db_pool_acquires_total", "Connections taken from the pool.", []string{"pool"}, nil)
	poolWaits    = prometheus.NewDesc("adhunters_db_pool_waits_total", "Times a query waited for a connection because all were in use.", []string{"pool"}, nil)
	poolWaited   = prometheus.NewDesc("adhunters_db_pool_wait_seconds_total", "Time queries spent waiting for a connection because all were in use.", []string{"pool"}, nil)
	poolCanceled = prometheus.NewDesc("adhunters_db_pool_canceled_acquires_total", "Waits for a connection given up, at the request's deadline or cancel.", []string{"pool"}, nil)
	poolNew      = prometheus.NewDesc("adhunters_db_pool_new_connections_total", "Connections opened to Postgres.", []string{"pool"}, nil)
)

// pools are the pools this process opened. Two with one AppName add up.
var (
	pools         = &poolSet{}
	registerPools sync.Once
)

type poolSet struct {
	mu    sync.Mutex
	names []string
	pools []*pgxpool.Pool
}

func (s *poolSet) add(name string, p *pgxpool.Pool) {
	registerPools.Do(func() { ops.Register(s) })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names = append(s.names, name)
	s.pools = append(s.pools, p)
}

func (s *poolSet) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{poolConns, poolMax, poolAcquires, poolWaits, poolWaited, poolCanceled, poolNew} {
		ch <- d
	}
}

func (s *poolSet) Collect(ch chan<- prometheus.Metric) {
	type sums struct {
		acquired, idle, constructing, max                float64
		acquires, waits, waitSeconds, canceled, newConns float64
	}
	s.mu.Lock()
	names := append([]string(nil), s.names...)
	ps := append([]*pgxpool.Pool(nil), s.pools...)
	s.mu.Unlock()

	by := map[string]*sums{}
	var order []string
	for i, p := range ps {
		st := p.Stat()
		t := by[names[i]]
		if t == nil {
			t = &sums{}
			by[names[i]] = t
			order = append(order, names[i])
		}
		t.acquired += float64(st.AcquiredConns())
		t.idle += float64(st.IdleConns())
		t.constructing += float64(st.ConstructingConns())
		t.max += float64(st.MaxConns())
		t.acquires += float64(st.AcquireCount())
		t.waits += float64(st.EmptyAcquireCount())
		t.waitSeconds += st.EmptyAcquireWaitTime().Seconds()
		t.canceled += float64(st.CanceledAcquireCount())
		t.newConns += float64(st.NewConnsCount())
	}
	for _, n := range order {
		t := by[n]
		ch <- prometheus.MustNewConstMetric(poolConns, prometheus.GaugeValue, t.acquired, n, "acquired")
		ch <- prometheus.MustNewConstMetric(poolConns, prometheus.GaugeValue, t.idle, n, "idle")
		ch <- prometheus.MustNewConstMetric(poolConns, prometheus.GaugeValue, t.constructing, n, "constructing")
		ch <- prometheus.MustNewConstMetric(poolMax, prometheus.GaugeValue, t.max, n)
		ch <- prometheus.MustNewConstMetric(poolAcquires, prometheus.CounterValue, t.acquires, n)
		ch <- prometheus.MustNewConstMetric(poolWaits, prometheus.CounterValue, t.waits, n)
		ch <- prometheus.MustNewConstMetric(poolWaited, prometheus.CounterValue, t.waitSeconds, n)
		ch <- prometheus.MustNewConstMetric(poolCanceled, prometheus.CounterValue, t.canceled, n)
		ch <- prometheus.MustNewConstMetric(poolNew, prometheus.CounterValue, t.newConns, n)
	}
}
