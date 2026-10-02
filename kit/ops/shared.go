package ops

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Register adds c to the /metrics of every Server in the process, for shared
// code that has no Server at hand: kit/pg's pools, Transport's calls. c must
// never send a series twice, nor one another collector sends, or /metrics
// fails as a whole; register it once.
func Register(c prometheus.Collector) {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	shared.cs = append(shared.cs, c)
}

var shared = &sharedCollectors{cs: []prometheus.Collector{outbound.requests, outbound.seconds}}

// sharedCollectors is one collector over all that Register added. It
// describes nothing, so a registry takes it unchecked and what registers
// after a Server exists still shows on its /metrics.
type sharedCollectors struct {
	mu sync.Mutex
	cs []prometheus.Collector
}

func (s *sharedCollectors) Describe(chan<- *prometheus.Desc) {}

func (s *sharedCollectors) Collect(ch chan<- prometheus.Metric) {
	s.mu.Lock()
	cs := append([]prometheus.Collector(nil), s.cs...)
	s.mu.Unlock()
	for _, c := range cs {
		c.Collect(ch)
	}
}
