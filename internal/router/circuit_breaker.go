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
	// CircuitClosed allows all requests; recent failures tallied in a sliding window.
	CircuitClosed CircuitState = iota
	// CircuitOpen rejects all requests for OpenTimeout, then transitions to HalfOpen.
	CircuitOpen
	// CircuitHalfOpen allows a single trial request; success closes, failure re-opens.
	CircuitHalfOpen
)

// ErrCircuitOpen is returned by Allow when the breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker open")

// BreakerConfig tunes the failure thresholds.
type BreakerConfig struct {
	// FailureThreshold is the number of failures within the window required to trip.
	FailureThreshold int
	// WindowSize is the sliding-window length in samples.
	WindowSize int
	// OpenTimeout is how long the circuit stays Open before going HalfOpen.
	OpenTimeout time.Duration
}

// DefaultBreakerConfig matches the Rust defaults.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureThreshold: 5,
		WindowSize:       10,
		OpenTimeout:      30 * time.Second,
	}
}

// CircuitBreaker is a per-endpoint state machine.
type CircuitBreaker struct {
	cfg BreakerConfig

	state      atomic.Int32 // CircuitState
	openedAt   atomic.Int64 // unix nano when state became Open
	successes  atomic.Uint64
	failures   atomic.Uint64
	// lastActivity is the unix nano of the most recent Allow/Record* call.
	// Read by BreakerRegistry.Evict to drop idle entries (Rust parity:
	// circuit_breaker_registry.rs::evict_idle).
	lastActivity atomic.Int64

	mu     sync.Mutex
	window []bool // true=success, false=failure; ring buffer
	head   int
	count  int
}

// NewCircuitBreaker builds a fresh breaker in Closed state.
func NewCircuitBreaker(cfg BreakerConfig) *CircuitBreaker {
	cb := &CircuitBreaker{cfg: cfg, window: make([]bool, cfg.WindowSize)}
	cb.state.Store(int32(CircuitClosed))
	cb.lastActivity.Store(time.Now().UnixNano())
	return cb
}

// State returns the current state, transitioning Open→HalfOpen if the
// open timeout has elapsed.
func (cb *CircuitBreaker) State() CircuitState {
	s := CircuitState(cb.state.Load())
	if s != CircuitOpen {
		return s
	}
	since := time.Since(time.Unix(0, cb.openedAt.Load()))
	if since >= cb.cfg.OpenTimeout {
		cb.state.CompareAndSwap(int32(CircuitOpen), int32(CircuitHalfOpen))
		return CircuitState(cb.state.Load())
	}
	return CircuitOpen
}

// Allow checks whether a request is permitted. Returns ErrCircuitOpen
// when the breaker is open and the open-timeout hasn't elapsed.
func (cb *CircuitBreaker) Allow() error {
	cb.lastActivity.Store(time.Now().UnixNano())
	if cb.State() == CircuitOpen {
		return ErrCircuitOpen
	}
	return nil
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.successes.Add(1)
	cb.lastActivity.Store(time.Now().UnixNano())
	cb.appendOutcome(true)
	// Half-open trial succeeded → close.
	cb.state.CompareAndSwap(int32(CircuitHalfOpen), int32(CircuitClosed))
}

// RecordFailure records a failure and may trip the breaker.
func (cb *CircuitBreaker) RecordFailure() {
	cb.failures.Add(1)
	cb.lastActivity.Store(time.Now().UnixNano())
	cb.appendOutcome(false)

	// Half-open trial failed → re-open.
	if cb.state.CompareAndSwap(int32(CircuitHalfOpen), int32(CircuitOpen)) {
		cb.openedAt.Store(time.Now().UnixNano())
		return
	}

	// Closed: count recent failures and trip if over threshold.
	if cb.State() == CircuitClosed {
		if cb.recentFailures() >= cb.cfg.FailureThreshold {
			if cb.state.CompareAndSwap(int32(CircuitClosed), int32(CircuitOpen)) {
				cb.openedAt.Store(time.Now().UnixNano())
			}
		}
	}
}

func (cb *CircuitBreaker) appendOutcome(ok bool) {
	cb.mu.Lock()
	cb.window[cb.head] = ok
	cb.head = (cb.head + 1) % len(cb.window)
	if cb.count < len(cb.window) {
		cb.count++
	}
	cb.mu.Unlock()
}

func (cb *CircuitBreaker) recentFailures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	failed := 0
	for i := range cb.count {
		if !cb.window[i] {
			failed++
		}
	}
	return failed
}

// Stats is a snapshot for metrics export.
type BreakerStats struct {
	State           CircuitState
	Successes       uint64
	Failures        uint64
	RecentFailures  int
}

// Stats returns a snapshot.
func (cb *CircuitBreaker) Stats() BreakerStats {
	return BreakerStats{
		State:          cb.State(),
		Successes:      cb.successes.Load(),
		Failures:       cb.failures.Load(),
		RecentFailures: cb.recentFailures(),
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

// Evict drops breakers whose last Allow/RecordSuccess/RecordFailure
// happened more than maxIdle ago. Returns the eviction count. Mirrors
// crates/fc-router/src/circuit_breaker_registry.rs::evict_idle —
// prevents unbounded growth when many short-lived endpoint URLs flow
// through the router. Safe to call concurrently with normal traffic.
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

// Len reports the number of active breakers in the registry.
func (r *BreakerRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.m)
}
