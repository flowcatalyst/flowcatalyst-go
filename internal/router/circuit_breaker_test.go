package router_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
)

func TestCircuitBreakerTripsAfterThreshold(t *testing.T) {
	cb := router.NewCircuitBreaker(router.BreakerConfig{
		FailureThreshold: 3,
		WindowSize:       10,
		OpenTimeout:      50 * time.Millisecond,
	})

	require.NoError(t, cb.Allow())
	for range 3 {
		cb.RecordFailure()
	}
	assert.Equal(t, router.CircuitOpen, cb.State())
	assert.ErrorIs(t, cb.Allow(), router.ErrCircuitOpen)
}

func TestCircuitBreakerHalfOpenAfterTimeout(t *testing.T) {
	cb := router.NewCircuitBreaker(router.BreakerConfig{
		FailureThreshold: 1,
		WindowSize:       5,
		OpenTimeout:      20 * time.Millisecond,
	})

	cb.RecordFailure()
	require.Equal(t, router.CircuitOpen, cb.State())

	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, router.CircuitHalfOpen, cb.State())
	assert.NoError(t, cb.Allow())
}

func TestCircuitBreakerSuccessClosesAfterHalfOpen(t *testing.T) {
	cb := router.NewCircuitBreaker(router.BreakerConfig{
		FailureThreshold: 1,
		WindowSize:       5,
		OpenTimeout:      10 * time.Millisecond,
	})
	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	require.Equal(t, router.CircuitHalfOpen, cb.State())

	cb.RecordSuccess()
	assert.Equal(t, router.CircuitClosed, cb.State())
}

func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	cb := router.NewCircuitBreaker(router.BreakerConfig{
		FailureThreshold: 1,
		WindowSize:       5,
		OpenTimeout:      10 * time.Millisecond,
	})
	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	require.Equal(t, router.CircuitHalfOpen, cb.State())

	cb.RecordFailure()
	assert.Equal(t, router.CircuitOpen, cb.State())
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
