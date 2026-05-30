package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeClock lets tests advance time deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestGovernor(perMin, burst uint32, clk *fakeClock) *Governor {
	g := NewGovernor(GovernorConfig{PerMinute: perMin, Burst: burst})
	g.now = clk.now
	return g
}

func TestGovernorBurstThenThrottle(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	// 60/min = 1 token/sec, burst 5.
	g := newTestGovernor(60, 5, clk)

	// The burst of 5 is admitted instantly (no time advance).
	for i := 0; i < 5; i++ {
		if ok, _ := g.Check("1.2.3.4"); !ok {
			t.Fatalf("burst request %d should be allowed", i+1)
		}
	}
	// 6th in the same instant is throttled, ~1s wait (refill is 1/sec).
	ok, retry := g.Check("1.2.3.4")
	if ok {
		t.Fatal("6th request should be throttled")
	}
	if retry != 1 {
		t.Errorf("retryAfter = %d, want 1", retry)
	}

	// After 1s a single token has refilled.
	clk.add(time.Second)
	if ok, _ := g.Check("1.2.3.4"); !ok {
		t.Error("request after 1s refill should be allowed")
	}
	// ...and the next is throttled again.
	if ok, _ := g.Check("1.2.3.4"); ok {
		t.Error("immediate follow-up should be throttled")
	}
}

func TestGovernorKeysAreIndependent(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	g := newTestGovernor(60, 1, clk)

	if ok, _ := g.Check("a"); !ok {
		t.Fatal("first request for key a should be allowed")
	}
	if ok, _ := g.Check("a"); ok {
		t.Fatal("second request for key a should be throttled")
	}
	// A different key has its own bucket.
	if ok, _ := g.Check("b"); !ok {
		t.Error("first request for key b should be allowed despite a being exhausted")
	}
}

func TestGovernorPrune(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	g := newTestGovernor(60, 5, clk)
	// Disable the opportunistic in-Check sweep so this test exercises the
	// explicit Prune in isolation.
	g.pruneInterval = time.Hour

	g.Check("stale")
	clk.add(20 * time.Minute)
	g.Check("fresh")

	removed := g.Prune(10 * time.Minute)
	if removed != 1 {
		t.Fatalf("Prune removed %d, want 1 (only the stale bucket)", removed)
	}
	if _, exists := g.buckets["stale"]; exists {
		t.Error("stale bucket should have been pruned")
	}
	if _, exists := g.buckets["fresh"]; !exists {
		t.Error("fresh bucket should remain")
	}
}

func TestGovernorClampsZeroConfig(t *testing.T) {
	g := NewGovernor(GovernorConfig{PerMinute: 0, Burst: 0})
	// burst clamps to 1: exactly one request admitted, the next throttled.
	if ok, _ := g.Check("x"); !ok {
		t.Fatal("clamped governor should admit one request")
	}
	if ok, _ := g.Check("x"); ok {
		t.Error("clamped governor should throttle the second request")
	}
}

func TestGovernorMiddleware(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	g := newTestGovernor(60, 1, clk)
	mw := GovernorMiddleware(g, "slow down")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/oauth/token", nil)
		req.Header.Set("X-Forwarded-For", "9.9.9.9")
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", rec.Code)
	}
	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second call status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 should carry a Retry-After header")
	}
}

func TestGovernorMiddlewareNoIPPassesThrough(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	g := newTestGovernor(60, 1, clk)
	mw := GovernorMiddleware(g, "slow down")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No XFF and a blank RemoteAddr → no resolvable IP → pass through.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/oauth/token", nil)
		req.RemoteAddr = ""
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d with no IP should pass through, got %d", i+1, rec.Code)
		}
	}
}
