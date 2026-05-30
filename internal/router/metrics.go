package router

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// MetricsConfig configures the windowed sample buffer.
// Mirrors crates/fc-router/src/metrics.rs::MetricsConfig.
type MetricsConfig struct {
	MaxSamples  int
	ShortWindow time.Duration
	LongWindow  time.Duration
}

// DefaultMetricsConfig returns the Rust-default tuning (10k samples,
// 5 min short, 30 min long).
func DefaultMetricsConfig() MetricsConfig {
	return MetricsConfig{
		MaxSamples:  10000,
		ShortWindow: 5 * time.Minute,
		LongWindow:  30 * time.Minute,
	}
}

// metricSample is one record in the rolling buffer. Kept small (24 B)
// so retaining 10k entries costs ~240 KB per pool.
type metricSample struct {
	ts         time.Time
	durationMs uint64
	success    bool
}

// PoolMetricsCollector aggregates per-pool throughput + latency for the
// dashboard. Mirrors the Rust PoolMetricsCollector, but uses a sorted
// snapshot for percentiles instead of HdrHistogram to avoid pulling in
// a new dep — at MaxSamples=10000 a sort.Slice on a snapshot copy runs
// in well under a millisecond, and the snapshot only happens when
// /monitoring/pool-stats is requested.
type PoolMetricsCollector struct {
	cfg MetricsConfig

	totalSuccess     atomic.Uint64
	totalFailure     atomic.Uint64
	totalRateLimited atomic.Uint64

	// Cumulative mediation-latency histogram, emitted as the Prometheus
	// fc_mediation_duration_seconds histogram. Monotonic across the process
	// lifetime — distinct from the sliding `samples` window used for the
	// dashboard percentiles. durationBuckets[i] counts observations <=
	// mediationBucketsSeconds[i] (cumulative "le" semantics).
	durationCount   atomic.Uint64
	durationSumMs   atomic.Uint64
	durationBuckets [len(mediationBucketsSeconds)]atomic.Uint64

	mu                sync.Mutex
	samples           []metricSample // ring-trimmed; oldest first
	rateLimitedEvents []time.Time    // ring-trimmed; oldest first
}

// mediationBucketsSeconds are the Prometheus histogram upper bounds (seconds)
// for fc_mediation_duration_seconds — the default client_golang latency
// buckets. (+Inf is implicit in the exposition.)
var mediationBucketsSeconds = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// MediationHistogram is a cumulative-histogram snapshot for Prometheus.
type MediationHistogram struct {
	Bounds     []float64
	Counts     []uint64 // cumulative count of observations <= Bounds[i]
	SumSeconds float64
	Count      uint64
}

// NewPoolMetricsCollector builds a collector with default tuning.
func NewPoolMetricsCollector() *PoolMetricsCollector {
	return NewPoolMetricsCollectorWithConfig(DefaultMetricsConfig())
}

// NewPoolMetricsCollectorWithConfig builds a collector with the supplied
// tuning. Test/bench code uses this to dial windows down to seconds.
func NewPoolMetricsCollectorWithConfig(cfg MetricsConfig) *PoolMetricsCollector {
	if cfg.MaxSamples <= 0 {
		cfg.MaxSamples = 10000
	}
	if cfg.ShortWindow <= 0 {
		cfg.ShortWindow = 5 * time.Minute
	}
	if cfg.LongWindow <= 0 {
		cfg.LongWindow = 30 * time.Minute
	}
	return &PoolMetricsCollector{cfg: cfg}
}

// RecordSuccess records a successful delivery. duration_ms = mediator
// wall time, used for latency percentiles.
func (c *PoolMetricsCollector) RecordSuccess(durationMs uint64) {
	c.totalSuccess.Add(1)
	c.addSample(durationMs, true)
}

// RecordFailure records a permanent failure (ERROR_CONFIG / ERROR_CONNECTION).
// Counts toward the failure total and lowers success rate.
func (c *PoolMetricsCollector) RecordFailure(durationMs uint64) {
	c.totalFailure.Add(1)
	c.addSample(durationMs, false)
}

// RecordTransient records a transient error (ERROR_PROCESS — message will
// be retried). Matches Rust: do NOT bump total_failure, but do add a
// non-success sample so windowed success-rate reflects current state.
func (c *PoolMetricsCollector) RecordTransient(durationMs uint64) {
	c.addSample(durationMs, false)
}

// RecordRateLimited records a rate-limit event (either internal limiter
// or HTTP 429 from destination). Does NOT add a latency sample — these
// aren't delivery attempts.
func (c *PoolMetricsCollector) RecordRateLimited() {
	c.totalRateLimited.Add(1)

	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-c.cfg.LongWindow)
	// trim front
	i := 0
	for i < len(c.rateLimitedEvents) && c.rateLimitedEvents[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		c.rateLimitedEvents = c.rateLimitedEvents[i:]
	}
	c.rateLimitedEvents = append(c.rateLimitedEvents, now)
}

// Reset clears every counter and sample. Test helper.
func (c *PoolMetricsCollector) Reset() {
	c.totalSuccess.Store(0)
	c.totalFailure.Store(0)
	c.totalRateLimited.Store(0)
	c.durationCount.Store(0)
	c.durationSumMs.Store(0)
	for i := range c.durationBuckets {
		c.durationBuckets[i].Store(0)
	}
	c.mu.Lock()
	c.samples = c.samples[:0]
	c.rateLimitedEvents = c.rateLimitedEvents[:0]
	c.mu.Unlock()
}

// observeDuration records one mediation into the cumulative histogram.
func (c *PoolMetricsCollector) observeDuration(durationMs uint64) {
	c.durationCount.Add(1)
	c.durationSumMs.Add(durationMs)
	secs := float64(durationMs) / 1000.0
	for i, ub := range mediationBucketsSeconds {
		if secs <= ub {
			c.durationBuckets[i].Add(1)
		}
	}
}

// HistogramSnapshot returns the cumulative mediation-latency histogram.
func (c *PoolMetricsCollector) HistogramSnapshot() MediationHistogram {
	counts := make([]uint64, len(c.durationBuckets))
	for i := range c.durationBuckets {
		counts[i] = c.durationBuckets[i].Load()
	}
	bounds := make([]float64, len(mediationBucketsSeconds))
	copy(bounds, mediationBucketsSeconds[:])
	return MediationHistogram{
		Bounds:     bounds,
		Counts:     counts,
		SumSeconds: float64(c.durationSumMs.Load()) / 1000.0,
		Count:      c.durationCount.Load(),
	}
}

func (c *PoolMetricsCollector) addSample(durationMs uint64, success bool) {
	c.observeDuration(durationMs)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	// Drop samples older than the long window — these can never count
	// toward last_30_min.
	cutoff := now.Add(-c.cfg.LongWindow)
	i := 0
	for i < len(c.samples) && c.samples[i].ts.Before(cutoff) {
		i++
	}
	if i > 0 {
		c.samples = c.samples[i:]
	}
	c.samples = append(c.samples, metricSample{ts: now, durationMs: durationMs, success: success})
	// Enforce MaxSamples bound by dropping oldest.
	if over := len(c.samples) - c.cfg.MaxSamples; over > 0 {
		c.samples = c.samples[over:]
	}
}

// Snapshot returns the dashboard-shaped metrics. Safe to call at any
// time; copies are taken under the lock.
func (c *PoolMetricsCollector) Snapshot() common.EnhancedPoolMetrics {
	totalSuccess := c.totalSuccess.Load()
	totalFailure := c.totalFailure.Load()
	totalRateLimited := c.totalRateLimited.Load()

	c.mu.Lock()
	// Take a copy so percentile sort doesn't reorder the live buffer.
	samples := make([]metricSample, len(c.samples))
	copy(samples, c.samples)
	rateLimitedEvents := make([]time.Time, len(c.rateLimitedEvents))
	copy(rateLimitedEvents, c.rateLimitedEvents)
	c.mu.Unlock()

	now := time.Now()
	shortCutoff := now.Add(-c.cfg.ShortWindow)
	longCutoff := now.Add(-c.cfg.LongWindow)

	short := filterSamples(samples, shortCutoff)
	long := filterSamples(samples, longCutoff)

	rateLimited5m := countSince(rateLimitedEvents, shortCutoff)
	rateLimited30m := countSince(rateLimitedEvents, longCutoff)

	successRate := 1.0
	if total := totalSuccess + totalFailure; total > 0 {
		successRate = float64(totalSuccess) / float64(total)
	}

	last5 := windowedFromSamples(short, c.cfg.ShortWindow)
	last5.RateLimitedCount = rateLimited5m
	last30 := windowedFromSamples(long, c.cfg.LongWindow)
	last30.RateLimitedCount = rateLimited30m

	return common.EnhancedPoolMetrics{
		TotalSuccess:     totalSuccess,
		TotalFailure:     totalFailure,
		TotalRateLimited: totalRateLimited,
		SuccessRate:      successRate,
		ProcessingTime:   processingTimeFromSamples(samples),
		Last5Min:         last5,
		Last30Min:        last30,
	}
}

// filterSamples returns the suffix of samples with ts >= cutoff. The
// caller's slice is sorted by ts (insert order is monotonic), so a
// linear scan from the front is O(n) once.
func filterSamples(samples []metricSample, cutoff time.Time) []metricSample {
	i := 0
	for i < len(samples) && samples[i].ts.Before(cutoff) {
		i++
	}
	return samples[i:]
}

func countSince(events []time.Time, cutoff time.Time) uint64 {
	i := 0
	for i < len(events) && events[i].Before(cutoff) {
		i++
	}
	return uint64(len(events) - i)
}

func windowedFromSamples(samples []metricSample, window time.Duration) common.WindowedMetrics {
	var success, failure uint64
	for _, s := range samples {
		if s.success {
			success++
		} else {
			failure++
		}
	}
	total := success + failure
	successRate := 1.0
	if total > 0 {
		successRate = float64(success) / float64(total)
	}
	throughput := 0.0
	if secs := window.Seconds(); secs > 0 {
		throughput = float64(total) / secs
	}
	return common.WindowedMetrics{
		SuccessCount:       success,
		FailureCount:       failure,
		SuccessRate:        successRate,
		ThroughputPerSec:   throughput,
		ProcessingTime:     processingTimeFromSamples(samples),
		WindowStart:        time.Now().Add(-window),
		WindowDurationSecs: uint64(window.Seconds()),
	}
}

// processingTimeFromSamples computes latency aggregates incl. p50/p95/p99.
// Empty input returns the zero value (matches Rust's default
// ProcessingTimeMetrics).
func processingTimeFromSamples(samples []metricSample) common.ProcessingTimeMetrics {
	if len(samples) == 0 {
		return common.ProcessingTimeMetrics{}
	}
	durations := make([]uint64, len(samples))
	var sum uint64
	for i, s := range samples {
		durations[i] = s.durationMs
		sum += s.durationMs
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	avg := float64(sum) / float64(len(durations))
	return common.ProcessingTimeMetrics{
		AvgMs:       avg,
		MinMs:       durations[0],
		MaxMs:       durations[len(durations)-1],
		P50Ms:       quantile(durations, 0.50),
		P95Ms:       quantile(durations, 0.95),
		P99Ms:       quantile(durations, 0.99),
		SampleCount: uint64(len(durations)),
	}
}

// quantile picks the value at percentile q (0..1) from a sorted slice.
// Uses nearest-rank — close to HdrHistogram's `value_at_quantile`.
func quantile(sorted []uint64, q float64) uint64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	// nearest-rank: ceil(q * n) - 1
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
