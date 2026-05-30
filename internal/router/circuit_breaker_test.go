package router_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
)

// rateCfg is a small, fast config for the failure-rate tests.
func rateCfg() router.BreakerConfig {
	return router.BreakerConfig{
		FailureRateThreshold: 0.5,
		MinCalls:             4,
		SuccessThreshold:     2,
		ResetTimeout:         20 * time.Millisecond,
		BufferSize:           10,
	}
}

func TestCircuitBreakerTripsOnFailureRateAfterMinCalls(t *testing.T) {
	cb := router.NewCircuitBreaker(rateCfg())

	require.NoError(t, cb.Allow())
	// 3 failures: below MinCalls (4) → must stay closed even at 100% rate.
	for range 3 {
		cb.RecordFailure()
	}
	assert.Equal(t, router.CircuitClosed, cb.State(), "below MinCalls the breaker must not trip")

	// 4th failure: count==MinCalls, rate 1.0 ≥ 0.5 → open.
	cb.RecordFailure()
	assert.Equal(t, router.CircuitOpen, cb.State())
	assert.ErrorIs(t, cb.Allow(), router.ErrCircuitOpen)
}

func TestCircuitBreakerStaysClosedBelowRate(t *testing.T) {
	cb := router.NewCircuitBreaker(rateCfg())
	// 6 successes + 4 failures = 10 calls, rate 0.4 < 0.5 → closed.
	for range 6 {
		cb.RecordSuccess()
	}
	for range 4 {
		cb.RecordFailure()
	}
	assert.Equal(t, router.CircuitClosed, cb.State())
}

func TestCircuitBreakerHalfOpenAfterResetTimeout(t *testing.T) {
	cb := router.NewCircuitBreaker(rateCfg())
	for range 4 {
		cb.RecordFailure()
	}
	require.Equal(t, router.CircuitOpen, cb.State())

	// State() does not auto-transition; Allow() does, once ResetTimeout
	// elapses since the last failure.
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, router.CircuitOpen, cb.State(), "State alone must not transition")
	assert.NoError(t, cb.Allow(), "Allow after reset timeout transitions to half-open")
	assert.Equal(t, router.CircuitHalfOpen, cb.State())
}

func TestCircuitBreakerClosesAfterSuccessThreshold(t *testing.T) {
	cb := router.NewCircuitBreaker(rateCfg())
	for range 4 {
		cb.RecordFailure()
	}
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, cb.Allow()) // → half-open

	// SuccessThreshold is 2: one success is not enough.
	cb.RecordSuccess()
	assert.Equal(t, router.CircuitHalfOpen, cb.State(), "one success below threshold keeps it half-open")
	cb.RecordSuccess()
	assert.Equal(t, router.CircuitClosed, cb.State(), "reaching SuccessThreshold closes it")
}

func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	cb := router.NewCircuitBreaker(rateCfg())
	for range 4 {
		cb.RecordFailure()
	}
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, cb.Allow()) // → half-open
	require.Equal(t, router.CircuitHalfOpen, cb.State())

	cb.RecordFailure()
	assert.Equal(t, router.CircuitOpen, cb.State(), "any failure in half-open re-opens")
}

func TestBreakerRegistryDeduplicates(t *testing.T) {
	r := router.NewBreakerRegistry(router.DefaultBreakerConfig())
	a := r.Get("https://example.com/webhook")
	b := r.Get("https://example.com/webhook")
	c := r.Get("https://example.com/other")
	assert.Same(t, a, b)
	assert.NotSame(t, a, c)
}

func TestBreakerRegistryEvictsIdle(t *testing.T) {
	r := router.NewBreakerRegistry(router.DefaultBreakerConfig())
	idle := r.Get("https://example.com/idle")
	active := r.Get("https://example.com/active")
	require.NotNil(t, idle)
	require.NotNil(t, active)
	require.Equal(t, 2, r.Len())

	// Keep the active breaker fresh; let the idle one age out by using
	// a very short maxIdle window.
	time.Sleep(15 * time.Millisecond)
	require.NoError(t, active.Allow())

	evicted := r.Evict(10 * time.Millisecond)
	assert.Equal(t, 1, evicted)
	assert.Equal(t, 1, r.Len())

	// Re-getting the evicted URL must produce a new breaker, not the
	// old pointer.
	after := r.Get("https://example.com/idle")
	assert.NotSame(t, idle, after)
}

func TestBreakerRegistryEvictNoop(t *testing.T) {
	r := router.NewBreakerRegistry(router.DefaultBreakerConfig())
	_ = r.Get("https://example.com/keep")
	assert.Equal(t, 0, r.Evict(time.Hour))
	assert.Equal(t, 0, r.Evict(0)) // zero/negative maxIdle is a no-op
	assert.Equal(t, 1, r.Len())
}
