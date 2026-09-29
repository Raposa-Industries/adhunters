package ops

import "github.com/prometheus/client_golang/prometheus"

// OutOfCredit counts one call that a paid service refused because the
// account's credit or quota ran out (an HTTP 402, OpenAI's
// "insufficient_quota", Anthropic's "credit balance is too low", a proxy
// refusing for lack of traffic). provider names the service ("openai",
// "iproyal"). The OutOfCredit alert pages on the first one, since work that
// needs the service has stopped. Count the refusal where the client first
// recognises it, once per call, not once per retry.
func (s *Server) OutOfCredit(provider string) {
	s.creditOnce.Do(func() {
		s.outOfCredit = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "adhunters_out_of_credit_total",
			Help: "Calls a paid service refused because the account ran out of credit or quota.",
		}, []string{"provider"})
		s.Registry.MustRegister(s.outOfCredit)
	})
	s.outOfCredit.WithLabelValues(provider).Inc()
}
