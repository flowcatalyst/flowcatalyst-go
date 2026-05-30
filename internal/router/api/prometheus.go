package api

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
)

// PrometheusHandler returns an http.Handler that emits the router's metrics
// in Prometheus text exposition format. Every call collects a fresh snapshot
// — no background goroutine, no global mutable state.
//
// The metric NAMES + label schema match the Rust router (router_metrics.rs)
// so Rust-targeted dashboards/alerts work against the Go router:
//
// Per pool (label: pool):
//   - fc_pool_queue_size, fc_pool_active_workers, fc_pool_message_groups (gauges)
//   - fc_messages_processed_total{success}                              (counter)
//   - fc_rate_limit_exceeded_total                                      (counter)
//   - fc_mediation_duration_seconds                                     (histogram)
//
// Global:
//   - fc_in_pipeline_messages                                          (gauge)
//
// Per queue/consumer:
//   - fc_queue_pending_messages, fc_queue_in_flight_messages           (gauges)
//   - fc_consumer_messages_received_total{consumer}                    (counter)
//   - fc_queue_messages_total{queue,outcome=acked|nacked|deferred}     (counter)
//
// Circuit breaker (label: target):
//   - fc_circuit_breaker_open                                          (gauge)
//   - fc_circuit_breaker_calls_total{outcome=success|failure}          (counter)
//
// Note (Rust parity gap, dashboards only): Rust additionally emits
// fc_messages_submitted_total, fc_messages_rejected_total{reason},
// fc_consumer_polls_total / fc_consumer_errors_total{type}, the `result`
// label on fc_messages_processed_total, and flowcatalyst_broker_*. Those are
// event-time labeled counters the Go pull-based collector does not currently
// track; emitting them faithfully needs the metrics collector reworked to the
// Rust push model. The primary panels above are covered.
func PrometheusHandler(s *State) http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(&routerCollector{state: s})
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorLog:      nil,
		ErrorHandling: promhttp.ContinueOnError,
	})
}

type routerCollector struct {
	state *State
}

// Describe is a no-op (untyped/const-metric collector pattern).
func (c *routerCollector) Describe(_ chan<- *prometheus.Desc) {}

// Collect builds one snapshot per scrape.
func (c *routerCollector) Collect(ch chan<- prometheus.Metric) {
	c.collectPools(ch)
	c.collectQueues(ch)
	c.collectBreakers(ch)
	c.collectInFlight(ch)
}

func (c *routerCollector) collectPools(ch chan<- prometheus.Metric) {
	if c.state.PoolStats == nil {
		return
	}
	poolLabel := []string{"pool"}
	for _, s := range c.state.PoolStats.PoolStats() {
		lv := []string{s.PoolCode}
		gauge(ch, "fc_pool_queue_size",
			"Messages buffered in group queues awaiting dispatch.",
			float64(s.QueueSize), poolLabel, lv)
		gauge(ch, "fc_pool_active_workers",
			"Currently active worker goroutines per pool.",
			float64(s.ActiveWorkers), poolLabel, lv)
		gauge(ch, "fc_pool_message_groups",
			"Distinct message groups currently holding buffered work.",
			float64(s.MessageGroupCount), poolLabel, lv)

		if s.Metrics != nil {
			m := s.Metrics
			counter(ch, "fc_messages_processed_total",
				"Cumulative messages processed, by success.",
				float64(m.TotalSuccess), []string{"pool", "success"}, []string{s.PoolCode, "true"})
			counter(ch, "fc_messages_processed_total",
				"Cumulative messages processed, by success.",
				float64(m.TotalFailure), []string{"pool", "success"}, []string{s.PoolCode, "false"})
			counter(ch, "fc_rate_limit_exceeded_total",
				"Cumulative rate-limit events.",
				float64(m.TotalRateLimited), poolLabel, lv)
		}

		// fc_mediation_duration_seconds — cumulative histogram.
		h := s.Histogram
		if len(h.Bounds) > 0 {
			buckets := make(map[float64]uint64, len(h.Bounds))
			for i, b := range h.Bounds {
				if i < len(h.Counts) {
					buckets[b] = h.Counts[i]
				}
			}
			desc := prometheus.NewDesc("fc_mediation_duration_seconds",
				"Mediation latency in seconds.", poolLabel, nil)
			ch <- prometheus.MustNewConstHistogram(desc, h.Count, h.SumSeconds, buckets, lv...)
		}
	}
}

func (c *routerCollector) collectQueues(ch chan<- prometheus.Metric) {
	if c.state.BrokerStats == nil {
		return
	}
	for _, m := range c.state.BrokerStats.GetWindowed(0) {
		q := normaliseQueueID(m.QueueIdentifier)
		gauge(ch, "fc_queue_pending_messages",
			"Approximate messages waiting on the broker.",
			float64(m.PendingMessages), []string{"queue"}, []string{q})
		gauge(ch, "fc_queue_in_flight_messages",
			"Approximate messages currently being processed by consumers.",
			float64(m.InFlightMessages), []string{"queue"}, []string{q})
		counter(ch, "fc_consumer_messages_received_total",
			"Cumulative messages received from the broker by this consumer.",
			float64(m.TotalPolled), []string{"consumer"}, []string{q})

		for outcome, v := range map[string]uint64{
			"acked":    m.TotalAcked,
			"nacked":   m.TotalNacked,
			"deferred": m.TotalDeferred,
		} {
			counter(ch, "fc_queue_messages_total",
				"Cumulative consumer ack/nack/defer outcomes.",
				float64(v), []string{"queue", "outcome"}, []string{q, outcome})
		}
	}
}

func (c *routerCollector) collectBreakers(ch chan<- prometheus.Metric) {
	if c.state.Breakers == nil {
		return
	}
	for name, st := range c.state.Breakers.Snapshot() {
		gauge(ch, "fc_circuit_breaker_open",
			"1 when the breaker is OPEN, 0 otherwise.",
			boolFloat(st.State == router.CircuitOpen),
			[]string{"target"}, []string{name})
		counter(ch, "fc_circuit_breaker_calls_total",
			"Cumulative breaker outcomes.",
			float64(st.Successes),
			[]string{"target", "outcome"}, []string{name, "success"})
		counter(ch, "fc_circuit_breaker_calls_total",
			"Cumulative breaker outcomes.",
			float64(st.Failures),
			[]string{"target", "outcome"}, []string{name, "failure"})
	}
}

func (c *routerCollector) collectInFlight(ch chan<- prometheus.Metric) {
	if c.state.InFlight == nil {
		return
	}
	count := len(c.state.InFlight.Snapshot())
	gauge(ch, "fc_in_pipeline_messages",
		"Total in-flight messages across all pools.",
		float64(count), nil, nil)
}

// gauge emits a single typed gauge metric.
func gauge(ch chan<- prometheus.Metric, name, help string, value float64, labels, labelValues []string) {
	desc := prometheus.NewDesc(name, help, labels, nil)
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, labelValues...)
}

// counter emits a single typed counter metric (cumulative).
func counter(ch chan<- prometheus.Metric, name, help string, value float64, labels, labelValues []string) {
	desc := prometheus.NewDesc(name, help, labels, nil)
	ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, value, labelValues...)
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// normaliseQueueID trims AWS SQS URL prefixes so the label cardinality stays
// bounded (we want `my-queue`, not the full `https://sqs.../my-queue`).
func normaliseQueueID(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		return id[i+1:]
	}
	return id
}
