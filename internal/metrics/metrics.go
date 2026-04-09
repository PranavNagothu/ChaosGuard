package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Collector holds all Prometheus metrics for ChaosGuard
type Collector struct {
	chaosInjections  *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	activePolicies   *prometheus.GaugeVec
	policyEvalTotal  *prometheus.CounterVec
}

// NewCollector registers and returns all ChaosGuard metrics
func NewCollector() *Collector {
	return &Collector{
		chaosInjections: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "chaosguard",
				Name:      "injections_total",
				Help:      "Total number of chaos faults injected, by service and fault type.",
			},
			[]string{"service", "fault_type"},
		),

		requestDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "chaosguard",
				Name:      "request_duration_seconds",
				Help:      "Request latency including injected chaos delay, by service and path.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"service", "path", "outcome"},
		),

		activePolicies: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "chaosguard",
				Name:      "active_policies",
				Help:      "Number of currently enabled chaos policies, by service.",
			},
			[]string{"service"},
		),

		policyEvalTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "chaosguard",
				Name:      "policy_evaluations_total",
				Help:      "Total number of policy evaluations, by service and result.",
			},
			[]string{"service", "result"},
		),
	}
}

// IncrChaosInjections increments the chaos injection counter
func (c *Collector) IncrChaosInjections(serviceID string) {
	c.chaosInjections.WithLabelValues(serviceID, "injected").Inc()
}

// IncrChaosInjectionsWithType increments the counter with a specific fault type
func (c *Collector) IncrChaosInjectionsWithType(serviceID, faultType string) {
	c.chaosInjections.WithLabelValues(serviceID, faultType).Inc()
}

// ObserveRequestDuration records a request's duration
func (c *Collector) ObserveRequestDuration(serviceID, path, outcome string, seconds float64) {
	c.requestDuration.WithLabelValues(serviceID, path, outcome).Observe(seconds)
}

// SetActivePolicies updates the gauge for active policy count
func (c *Collector) SetActivePolicies(serviceID string, count float64) {
	c.activePolicies.WithLabelValues(serviceID).Set(count)
}

// IncrPolicyEval increments the policy evaluation counter
func (c *Collector) IncrPolicyEval(serviceID, result string) {
	c.policyEvalTotal.WithLabelValues(serviceID, result).Inc()
}
