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

// Spent counts money one call cost on a prepaid service with no balance API,
// in USD, worked out from the vendor's reply (OpenAI's usage tokens times the
// price). observe-bot subtracts it from the balance the owner last entered
// in credits.conf ([estimate]), so the estimate is only as good as what every
// service reports: count every paid call, once, where its cost is known.
func (s *Server) Spent(provider string, usd float64) {
	s.spendOnce.Do(func() {
		s.spend = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "adhunters_spend_usd_total",
			Help: "Money spent on a prepaid service, in USD, as our services work it out per call.",
		}, []string{"provider"})
		s.Registry.MustRegister(s.spend)
	})
	if usd > 0 {
		s.spend.WithLabelValues(provider).Add(usd)
	}
}
