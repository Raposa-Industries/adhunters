package engine

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	steps       *prometheus.CounterVec
	stepSeconds prometheus.Histogram
	visits      *prometheus.CounterVec
	keeps       *prometheus.CounterVec
	deliveries  *prometheus.CounterVec
	burns       prometheus.Gauge
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		steps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "raposa_steps_total",
			Help: "Steps of investigations, by result: visit, wait, finished, error, lease_lost, stopped.",
		}, []string{"result"}),
		stepSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "raposa_step_seconds",
			Help:    "How long one step took, visit included.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20, 40, 80, 160},
		}),
		visits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "raposa_visits_total",
			Help: "Visits written, by engine and outcome.",
		}, []string{"engine", "outcome"}),
		keeps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "raposa_keeps_total",
			Help: "Pages the keeper opened, by result: complete, retry, failed, runner_down.",
		}, []string{"result"}),
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "raposa_watch_deliveries_total",
			Help: "Watch events sent to Pushcut, by status.",
		}, []string{"status"}),
		burns: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "raposa_line_burns",
			Help: "Burned lines active now, per site.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.steps, m.stepSeconds, m.visits, m.keeps, m.deliveries, m.burns)
	}
	return m
}

func itoa(n int) string { return strconv.Itoa(n) }
