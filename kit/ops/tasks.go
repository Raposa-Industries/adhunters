package ops

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Tasks reports a binary's periodic tasks (an hour close, a refresh, a pull)
// in one shape, so one alert rule watches them all: a task is late when its
// last success is older than its promise.
//
//	tasks := srv.Tasks()
//	tasks.Promise("hour_close", 75*time.Minute)
//	start := time.Now()
//	err := closeHour(ctx)
//	tasks.Done("hour_close", start, rows, err)
type Tasks struct {
	promise     *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
	duration    *prometheus.GaugeVec
	rows        *prometheus.GaugeVec
	runs        *prometheus.CounterVec
}

// Tasks returns the binary's task metrics, registering them on first use.
func (s *Server) Tasks() *Tasks {
	s.tasksOnce.Do(func() { s.tasks = newTasks(s.Registry) })
	return s.tasks
}

func newTasks(reg prometheus.Registerer) *Tasks {
	t := &Tasks{
		promise: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_task_promise_seconds",
			Help: "How old a task's last success may get before it is late.",
		}, []string{"task"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_task_last_success_timestamp_seconds",
			Help: "When the task last finished without an error.",
		}, []string{"task"}),
		duration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_task_last_duration_seconds",
			Help: "How long the task's last run took, failed or not.",
		}, []string{"task"}),
		rows: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adhunters_task_last_rows",
			Help: "Rows the task's last successful run wrote.",
		}, []string{"task"}),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "adhunters_task_runs_total",
			Help: "Task runs by result (ok, error).",
		}, []string{"task", "result"}),
	}
	reg.MustRegister(t.promise, t.lastSuccess, t.duration, t.rows, t.runs)
	return t
}

// Promise declares how often task must succeed. Its last success starts at
// the moment of the promise, so a task that never succeeds after a restart
// is late one promise later, not at once.
func (t *Tasks) Promise(task string, every time.Duration) {
	t.promise.WithLabelValues(task).Set(every.Seconds())
	t.lastSuccess.WithLabelValues(task).Set(float64(time.Now().Unix()))
	t.runs.WithLabelValues(task, "ok")
	t.runs.WithLabelValues(task, "error")
}

// Done records one run of task that began at start.
func (t *Tasks) Done(task string, start time.Time, rows int64, err error) {
	now := time.Now()
	t.duration.WithLabelValues(task).Set(now.Sub(start).Seconds())
	if err != nil {
		t.runs.WithLabelValues(task, "error").Inc()
		return
	}
	t.runs.WithLabelValues(task, "ok").Inc()
	t.lastSuccess.WithLabelValues(task).Set(float64(now.Unix()))
	t.rows.WithLabelValues(task).Set(float64(rows))
}
