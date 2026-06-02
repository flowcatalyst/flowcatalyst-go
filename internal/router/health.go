package router

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// HealthServiceConfig tunes thresholds. Mirrors
// `HealthServiceConfig::default()` from the Rust source.
type HealthServiceConfig struct {
	// HealthyThreshold is the minimum pool success rate (0..1) that
	// counts as healthy. Default 0.90.
	HealthyThreshold float64
	// WarningThreshold is the minimum success rate for the Warning band
	// (Below this → still counted as unhealthy). Default 0.70. Reserved
	// for future per-band rendering; current report just uses
	// HealthyThreshold to bucket healthy vs unhealthy.
	WarningThreshold float64
	// RollingWindow is the duration over which success rates are computed.
	// Default 30m.
	RollingWindow time.Duration
	// WarningAgeMinutes caps how old a warning can be and still be counted
	// as "active" for status calculation. Default 30.
	WarningAgeMinutes int64
	// ConsumerStallThreshold flags a consumer as stalled when it hasn't
	// polled in this duration. Default 60s.
	ConsumerStallThreshold time.Duration
	// MaxWarningsHealthy — degrade from Healthy → Warning above this.
	// Default 5 (matches Java).
	MaxWarningsHealthy uint32
	// MaxWarningsWarning — degrade from Warning → Degraded above this.
	// Default 20 (matches Java).
	MaxWarningsWarning uint32
}

// DefaultHealthServiceConfig returns the Rust defaults.
func DefaultHealthServiceConfig() HealthServiceConfig {
	return HealthServiceConfig{
		HealthyThreshold:       0.90,
		WarningThreshold:       0.70,
		RollingWindow:          30 * time.Minute,
		WarningAgeMinutes:      30,
		ConsumerStallThreshold: 60 * time.Second,
		MaxWarningsHealthy:     5,
		MaxWarningsWarning:     20,
	}
}

// HealthService aggregates pool success rates + consumer liveness +
// warning counts into a HealthReport.
//
// Per-pool counters track outcome events in a rolling window (default
// 30 min). Expired events are evicted from the front of a slice on
// every record() — amortised O(1) per record because eviction only
// chews through the front prefix.
type HealthService struct {
	cfg            HealthServiceConfig
	warningService *WarningService

	mu               sync.RWMutex
	poolCounters     map[string]*rollingCounter
	consumerLastPoll map[string]time.Time
	consumerRunning  map[string]bool
}

// NewHealthService builds a service. Pass nil warningService to use a
// fresh NoopWarningService — useful for tests that don't care about
// warnings.
func NewHealthService(cfg HealthServiceConfig, ws *WarningService) *HealthService {
	if cfg.RollingWindow <= 0 {
		cfg = DefaultHealthServiceConfig()
	}
	if ws == nil {
		ws = NoopWarningService()
	}
	return &HealthService{
		cfg:              cfg,
		warningService:   ws,
		poolCounters:     make(map[string]*rollingCounter),
		consumerLastPoll: make(map[string]time.Time),
		consumerRunning:  make(map[string]bool),
	}
}

// RecordPoolResult ticks the rolling counter for the named pool.
func (s *HealthService) RecordPoolResult(poolCode string, success bool) {
	s.mu.Lock()
	c, ok := s.poolCounters[poolCode]
	if !ok {
		c = newRollingCounter(s.cfg.RollingWindow)
		s.poolCounters[poolCode] = c
	}
	s.mu.Unlock()
	c.record(success)
}

// PoolSuccessRate returns the rolling success rate (0..1) for a pool,
// or false if no events have been recorded yet within the window.
func (s *HealthService) PoolSuccessRate(poolCode string) (float64, bool) {
	s.mu.RLock()
	c, ok := s.poolCounters[poolCode]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return c.successRate()
}

// RecordConsumerPoll stamps the last-seen time for a consumer.
func (s *HealthService) RecordConsumerPoll(consumerID string) {
	s.mu.Lock()
	s.consumerLastPoll[consumerID] = time.Now()
	s.mu.Unlock()
}

// SetConsumerRunning flags a consumer as running or stopped. A consumer
// that has never been flagged running is treated as unhealthy.
func (s *HealthService) SetConsumerRunning(consumerID string, running bool) {
	s.mu.Lock()
	s.consumerRunning[consumerID] = running
	s.mu.Unlock()
}

// IsConsumerHealthy returns true iff the consumer is running AND has
// polled within ConsumerStallThreshold.
func (s *HealthService) IsConsumerHealthy(consumerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.consumerRunning[consumerID] {
		return false
	}
	last, ok := s.consumerLastPoll[consumerID]
	if !ok {
		return false
	}
	return time.Since(last) < s.cfg.ConsumerStallThreshold
}

// ConsumerHealth returns the per-consumer snapshot.
func (s *HealthService) ConsumerHealth(consumerID string) ConsumerHealth {
	s.mu.RLock()
	running := s.consumerRunning[consumerID]
	last, hasLast := s.consumerLastPoll[consumerID]
	s.mu.RUnlock()

	var lastMs, sinceMs *int64
	if hasLast {
		ms := time.Since(last).Milliseconds()
		lastMs = &ms
		sinceMs = &ms
	}

	healthy := running && hasLast && time.Since(last) < s.cfg.ConsumerStallThreshold

	return ConsumerHealth{
		QueueIdentifier:     consumerID,
		IsHealthy:           healthy,
		LastPollTimeMs:      lastMs,
		TimeSinceLastPollMs: sinceMs,
		IsRunning:           running,
	}
}

// StalledConsumers returns the ids of consumers flagged as running but
// either haven't polled at all or haven't polled within the stall threshold.
func (s *HealthService) StalledConsumers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id, running := range s.consumerRunning {
		if !running {
			continue
		}
		last, hasLast := s.consumerLastPoll[id]
		if !hasLast || time.Since(last) >= s.cfg.ConsumerStallThreshold {
			out = append(out, id)
		}
	}
	return out
}

// HealthReport assembles the overall verdict. Pass the current pool stats
// snapshot — HealthService doesn't own that data, the pool manager does.
func (s *HealthService) HealthReport(poolStats []PoolStats) HealthReport {
	issues := []string{}

	var poolsHealthy, poolsUnhealthy uint32
	for _, st := range poolStats {
		if rate, ok := s.PoolSuccessRate(st.PoolCode); ok {
			if rate >= s.cfg.HealthyThreshold {
				poolsHealthy++
			} else {
				poolsUnhealthy++
				issues = append(issues, fmt.Sprintf("Pool %s success rate: %.1f%%", st.PoolCode, rate*100))
			}
		} else {
			// No data yet → treat as healthy.
			poolsHealthy++
		}
	}

	s.mu.RLock()
	consumersTotal := uint32(len(s.consumerRunning))
	s.mu.RUnlock()
	stalled := s.StalledConsumers()
	consumersUnhealthy := uint32(len(stalled))
	consumersHealthy := consumersTotal
	if consumersHealthy >= consumersUnhealthy {
		consumersHealthy -= consumersUnhealthy
	} else {
		consumersHealthy = 0
	}
	for _, id := range stalled {
		issues = append(issues, fmt.Sprintf("Consumer %s is stalled", id))
	}

	activeWarnings := uint32(len(s.warningService.Active(s.cfg.WarningAgeMinutes)))
	criticalWarnings := uint32(s.warningService.CriticalCount())
	if criticalWarnings > 0 {
		issues = append(issues, fmt.Sprintf("%d critical warnings", criticalWarnings))
	}

	status := HealthHealthy
	switch {
	case criticalWarnings > 0,
		poolsUnhealthy > 0 && poolsHealthy == 0,
		consumersUnhealthy > 0 && consumersHealthy == 0,
		activeWarnings > s.cfg.MaxWarningsWarning:
		status = HealthDegraded
	case poolsUnhealthy > 0,
		consumersUnhealthy > 0,
		activeWarnings > s.cfg.MaxWarningsHealthy:
		status = HealthWarning
	}

	if status != HealthHealthy {
		slog.Debug("health report",
			"status", status,
			"poolsHealthy", poolsHealthy,
			"poolsUnhealthy", poolsUnhealthy,
			"consumersHealthy", consumersHealthy,
			"consumersUnhealthy", consumersUnhealthy,
			"activeWarnings", activeWarnings,
			"criticalWarnings", criticalWarnings)
	}

	return HealthReport{
		Status:             status,
		PoolsHealthy:       poolsHealthy,
		PoolsUnhealthy:     poolsUnhealthy,
		ConsumersHealthy:   consumersHealthy,
		ConsumersUnhealthy: consumersUnhealthy,
		ActiveWarnings:     activeWarnings,
		CriticalWarnings:   criticalWarnings,
		Issues:             issues,
	}
}

// IsHealthy is a shortcut for HealthReport(stats).Status == Healthy.
func (s *HealthService) IsHealthy(poolStats []PoolStats) bool {
	return s.HealthReport(poolStats).Status == HealthHealthy
}

// Cleanup runs the warning-service cleanup + logs any stalled consumers.
// Wire it onto a ticker; the LifecycleManager owns the period.
func (s *HealthService) Cleanup() {
	s.warningService.Cleanup()
	if stalled := s.StalledConsumers(); len(stalled) > 0 {
		slog.Warn("detected stalled consumers", "count", len(stalled), "consumers", stalled)
	}
}

// RunCleanupLoop drives Cleanup on a ticker until ctx is cancelled.
func (s *HealthService) RunCleanupLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Cleanup()
		}
	}
}

// RemoveStaleEntries drops pool counters + consumer entries for ids no
// longer present in the supplied active sets. Call after a config
// reload so stale entries don't accumulate forever.
func (s *HealthService) RemoveStaleEntries(activePoolCodes, activeConsumerIDs []string) {
	poolSet := stringSet(activePoolCodes)
	consumerSet := stringSet(activeConsumerIDs)

	s.mu.Lock()
	defer s.mu.Unlock()

	for code := range s.poolCounters {
		if _, ok := poolSet[code]; !ok {
			delete(s.poolCounters, code)
		}
	}
	for id := range s.consumerLastPoll {
		if _, ok := consumerSet[id]; !ok {
			delete(s.consumerLastPoll, id)
		}
	}
	for id := range s.consumerRunning {
		if _, ok := consumerSet[id]; !ok {
			delete(s.consumerRunning, id)
		}
	}
}

func stringSet(xs []string) map[string]struct{} {
	out := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		out[x] = struct{}{}
	}
	return out
}

// ── rolling counter ──────────────────────────────────────────────────────

// rollingCounter is a bounded-window success-rate counter. Events are
// recorded with their timestamps; expired entries are popped from the
// front on each record (amortised O(1)).
type rollingCounter struct {
	window time.Duration

	mu     sync.Mutex
	events []rcEvent
}

type rcEvent struct {
	at      time.Time
	success bool
}

func newRollingCounter(window time.Duration) *rollingCounter {
	return &rollingCounter{window: window}
}

func (c *rollingCounter) record(success bool) {
	now := time.Now()
	cutoff := now.Add(-c.window)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Drop expired front entries.
	i := 0
	for i < len(c.events) && !c.events[i].at.After(cutoff) {
		i++
	}
	if i > 0 {
		c.events = c.events[i:]
	}
	c.events = append(c.events, rcEvent{at: now, success: success})
}

func (c *rollingCounter) successRate() (float64, bool) {
	cutoff := time.Now().Add(-c.window)
	c.mu.Lock()
	defer c.mu.Unlock()
	var total, successes int
	for _, e := range c.events {
		if !e.at.After(cutoff) {
			continue
		}
		total++
		if e.success {
			successes++
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(successes) / float64(total), true
}
