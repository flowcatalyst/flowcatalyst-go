package router

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// CircuitState is the three-state lifecycle.
type CircuitState int32

const (
	// CircuitClosed allows all requests; failures tallied in a sliding window.
	CircuitClosed CircuitState = iota
	// CircuitOpen rejects all requests until ResetTimeout elapses since the
	// last failure, then the next Allow transitions to HalfOpen.
	CircuitOpen
	// CircuitHalfOpen allows trial requests; SuccessThreshold consecutive
	// successes close it, any failure re-opens it.
	CircuitHalfOpen
)

// ErrCircuitOpen is returned by Allow when the breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker open")

// BreakerConfig tunes the failure-RATE thresholds. 1:1 with the Rust
// CircuitBreakerConfig (Java MicroProfile defaults): trip when the failure
// rate over a sliding window reaches a threshold, given a minimum number of
// buffered calls — NOT a raw failure count.
type BreakerConfig struct {
	// FailureRateThreshold trips the breaker when the windowed failure rate
	// reaches it (0.0–1.0). Java failureRatio = 0.5.
	FailureRateThreshold float64
	// MinCalls is the minimum buffered calls before the rate is evaluated.
	// Java requestVolumeThreshold = 10.
	MinCalls int
	// SuccessThreshold is the consecutive half-open successes needed to close.
	// Java successThreshold = 3.
	SuccessThreshold int
	// ResetTimeout is how long after the last failure an Open breaker waits
	// before allowing a half-open trial. Java delay = 5s.
	ResetTimeout time.Duration
	// BufferSize is the sliding-window length in samples. Java = 100.
	BufferSize int
}

// DefaultBreakerConfig matches the Rust/Java defaults.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureRateThreshold: 0.5,
		MinCalls:             10,
		SuccessThreshold:     3,
		ResetTimeout:         5 * time.Second,
		BufferSize:           100,
	}
}

// CircuitBreaker is a per-endpoint failure-rate state machine. A single mutex
// guards the state, sliding window, half-open success count, and last-failure
// time (mirrors the Rust BreakerInner); the cumulative counters and
// lastActivity are independent atomics.
type CircuitBreaker struct {
	cfg BreakerConfig

	successes    atomic.Uint64 // cumulative successes (metrics)
	failures     atomic.Uint64 // cumulative failures (metrics)
	lastActivity atomic.Int64  // unix nano of the most recent Allow/Record*; for Evict

	mu                sync.Mutex
	state             CircuitState
	window            []bool // ring buffer, len == BufferSize; true=success
	head              int
	count             int
	halfOpenSuccesses int
	lastFailureNano   int64
}

// NewCircuitBreaker builds a fresh breaker in Closed state.
func NewCircuitBreaker(cfg BreakerConfig) *CircuitBreaker {
	bs := cfg.BufferSize
	if bs < 1 {
		bs = 1
	}
	cb := &CircuitBreaker{cfg: cfg, state: CircuitClosed, window: make([]bool, bs)}
	cb.lastActivity.Store(time.Now().UnixNano())
	return cb
}

// State returns the current stored state (no transition — Open→HalfOpen
// happens in Allow, matching Rust get_stats).
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ResetTimeout returns the configured open→half-open wait. The mediator uses it
// to set the defer delay when it returns a circuit-open outcome.
func (cb *CircuitBreaker) ResetTimeout() time.Duration { return cb.cfg.ResetTimeout }

// Allow reports whether a request is permitted. Open transitions to HalfOpen
// once ResetTimeout has elapsed since the last failure (1:1 with Rust
// allow_request).
func (cb *CircuitBreaker) Allow() error {
	now := time.Now()
	cb.lastActivity.Store(now.UnixNano())
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case CircuitOpen:
		if cb.lastFailureNano != 0 && now.Sub(time.Unix(0, cb.lastFailureNano)) >= cb.cfg.ResetTimeout {
			cb.state = CircuitHalfOpen
			cb.halfOpenSuccesses = 0
			return nil
		}
		return ErrCircuitOpen
	default: // Closed, HalfOpen
		return nil
	}
}

// RecordSuccess records a successful request. In HalfOpen, SuccessThreshold
// consecutive successes close the breaker and clear the window.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.successes.Add(1)
	cb.lastActivity.Store(time.Now().UnixNano())
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.pushLocked(true)
	if cb.state == CircuitHalfOpen {
		cb.halfOpenSuccesses++
		if cb.halfOpenSuccesses >= cb.cfg.SuccessThreshold {
			cb.state = CircuitClosed
			cb.clearWindowLocked()
			cb.halfOpenSuccesses = 0
		}
	}
}

// RecordFailure records a failure. In Closed it trips the breaker when the
// windowed failure rate reaches the threshold (with at least MinCalls
// buffered); in HalfOpen any failure immediately re-opens.
func (cb *CircuitBreaker) RecordFailure() {
	cb.failures.Add(1)
	now := time.Now()
	cb.lastActivity.Store(now.UnixNano())
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.lastFailureNano = now.UnixNano()
	cb.pushLocked(false)
	switch cb.state {
	case CircuitClosed:
		if cb.count >= cb.cfg.MinCalls && cb.failureRateLocked() >= cb.cfg.FailureRateThreshold {
			cb.state = CircuitOpen
		}
	case CircuitHalfOpen:
		cb.state = CircuitOpen
		cb.halfOpenSuccesses = 0
	case CircuitOpen:
		// stay open
	}
}

func (cb *CircuitBreaker) pushLocked(ok bool) {
	cb.window[cb.head] = ok
	cb.head = (cb.head + 1) % len(cb.window)
	if cb.count < len(cb.window) {
		cb.count++
	}
}

func (cb *CircuitBreaker) failuresLocked() int {
	f := 0
	for i := 0; i < cb.count; i++ {
		if !cb.window[i] {
			f++
		}
	}
	return f
}

func (cb *CircuitBreaker) failureRateLocked() float64 {
	if cb.count == 0 {
		return 0
	}
	return float64(cb.failuresLocked()) / float64(cb.count)
}

func (cb *CircuitBreaker) clearWindowLocked() {
	cb.head = 0
	cb.count = 0
}

// Reset forces the breaker back to Closed and clears the window + counters.
func (cb *CircuitBreaker) Reset() {
	cb.successes.Store(0)
	cb.failures.Store(0)
	cb.lastActivity.Store(time.Now().UnixNano())
	cb.mu.Lock()
	cb.state = CircuitClosed
	cb.clearWindowLocked()
	cb.halfOpenSuccesses = 0
	cb.lastFailureNano = 0
	cb.mu.Unlock()
}

// BreakerStats is a snapshot for metrics export.
type BreakerStats struct {
	State          CircuitState
	Successes      uint64
	Failures       uint64
	RecentFailures int // failures currently in the sliding window
}

// Stats returns a snapshot (reports the stored state; no transition).
func (cb *CircuitBreaker) Stats() BreakerStats {
	cb.mu.Lock()
	st := cb.state
	rf := cb.failuresLocked()
	cb.mu.Unlock()
	return BreakerStats{
		State:          st,
		Successes:      cb.successes.Load(),
		Failures:       cb.failures.Load(),
		RecentFailures: rf,
	}
}

// BreakerRegistry is a per-endpoint URL → breaker map.
type BreakerRegistry struct {
	cfg BreakerConfig
	mu  sync.RWMutex
	m   map[string]*CircuitBreaker
}

// NewBreakerRegistry constructs an empty registry.
func NewBreakerRegistry(cfg BreakerConfig) *BreakerRegistry {
	return &BreakerRegistry{cfg: cfg, m: make(map[string]*CircuitBreaker)}
}

// Get returns the breaker for a target URL, creating one on first use.
func (r *BreakerRegistry) Get(url string) *CircuitBreaker {
	r.mu.RLock()
	cb, ok := r.m[url]
	r.mu.RUnlock()
	if ok {
		return cb
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cb, ok = r.m[url]; ok {
		return cb
	}
	cb = NewCircuitBreaker(r.cfg)
	r.m[url] = cb
	return cb
}

// Snapshot returns all breakers' stats, keyed by URL.
func (r *BreakerRegistry) Snapshot() map[string]BreakerStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]BreakerStats, len(r.m))
	for url, cb := range r.m {
		out[url] = cb.Stats()
	}
	return out
}

// Evict drops breakers whose last Allow/RecordSuccess/RecordFailure happened
// more than maxIdle ago. Returns the eviction count. Mirrors
// crates/fc-router/src/circuit_breaker_registry.rs::evict_idle — prevents
// unbounded growth when many short-lived endpoint URLs flow through the
// router. Safe to call concurrently with normal traffic.
func (r *BreakerRegistry) Evict(maxIdle time.Duration) int {
	if maxIdle <= 0 {
		return 0
	}
	// Two-phase: read-lock to collect candidates so the write-lock window
	// is as short as possible.
	r.mu.RLock()
	if len(r.m) == 0 {
		r.mu.RUnlock()
		return 0
	}
	cutoff := time.Now().Add(-maxIdle).UnixNano()
	idle := make([]string, 0, len(r.m))
	for url, cb := range r.m {
		if cb.lastActivity.Load() < cutoff {
			idle = append(idle, url)
		}
	}
	r.mu.RUnlock()
	if len(idle) == 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	evicted := 0
	for _, url := range idle {
		cb, ok := r.m[url]
		// Re-check under the write lock: activity may have resumed between
		// the read-lock collection and the write-lock acquisition.
		if !ok || cb.lastActivity.Load() >= cutoff {
			continue
		}
		delete(r.m, url)
		evicted++
	}
	return evicted
}

// Reset clears a single breaker's state (by URL key). Returns false if the
// URL has no registered breaker.
func (r *BreakerRegistry) Reset(url string) bool {
	r.mu.RLock()
	cb, ok := r.m[url]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	cb.Reset()
	return true
}

// ResetAll clears every registered breaker. Returns the number reset.
func (r *BreakerRegistry) ResetAll() int {
	r.mu.RLock()
	breakers := make([]*CircuitBreaker, 0, len(r.m))
	for _, cb := range r.m {
		breakers = append(breakers, cb)
	}
	r.mu.RUnlock()
	for _, cb := range breakers {
		cb.Reset()
	}
	return len(breakers)
}

// Len reports the number of active breakers in the registry.
func (r *BreakerRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.m)
}
