package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Governor is a per-instance, in-memory keyed token-bucket limiter that sits
// in FRONT of the distributed Store as defence-in-depth: a flood from a
// single IP (or client_id) is shed locally, before the network round-trip
// to Redis/Postgres. Unlike the Store it is NOT cluster-wide — each replica
// keeps its own buckets — so it complements, never replaces, the Store.
//
// 1:1 with Rust's rate_limit_middleware.rs. Rust uses the GCRA-based
// `governor` crate; golang.org/x/time/rate is the token-bucket equivalent
// (allow a Burst instantly, then sustain PerMinute).
type Governor struct {
	limit rate.Limit // tokens per second (PerMinute / 60)
	burst int        // bucket capacity (instantaneous allowance)
	now   func() time.Time

	pruneInterval time.Duration
	idleTTL       time.Duration

	mu        sync.Mutex
	buckets   map[string]*govEntry
	lastPrune time.Time
}

type govEntry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// GovernorConfig is one in-memory quota: PerMinute sustained rate with an
// instantaneous Burst allowance. Mirrors Rust's RateLimitConfig.
type GovernorConfig struct {
	PerMinute uint32
	Burst     uint32
}

// OAuthTokenIPGovernorFromEnv reads FC_OAUTH_TOKEN_IP_RATE_PER_MIN (120) and
// FC_OAUTH_TOKEN_IP_BURST (60) — the per-IP quota at /oauth/token.
func OAuthTokenIPGovernorFromEnv() GovernorConfig {
	return GovernorConfig{
		PerMinute: envU32("FC_OAUTH_TOKEN_IP_RATE_PER_MIN", 120),
		Burst:     envU32("FC_OAUTH_TOKEN_IP_BURST", 60),
	}
}

// OAuthTokenClientGovernorFromEnv reads FC_OAUTH_TOKEN_CLIENT_RATE_PER_MIN
// (60) and FC_OAUTH_TOKEN_CLIENT_BURST (30) — the per-client_id quota at
// /oauth/token.
func OAuthTokenClientGovernorFromEnv() GovernorConfig {
	return GovernorConfig{
		PerMinute: envU32("FC_OAUTH_TOKEN_CLIENT_RATE_PER_MIN", 60),
		Burst:     envU32("FC_OAUTH_TOKEN_CLIENT_BURST", 30),
	}
}

// OIDCBridgeGovernorFromEnv reads FC_OIDC_RATE_PER_MIN (60) and FC_OIDC_BURST
// (30) — the per-IP quota on the /auth/oidc/* bridge routes (login start +
// callback). Blunts authorization-code probing / DoS without impeding a real
// interactive login.
func OIDCBridgeGovernorFromEnv() GovernorConfig {
	return GovernorConfig{
		PerMinute: envU32("FC_OIDC_RATE_PER_MIN", 60),
		Burst:     envU32("FC_OIDC_BURST", 30),
	}
}

// NewGovernor builds a Governor for the supplied quota. PerMinute or Burst
// below 1 are clamped to 1 (matching Rust's max(1)).
func NewGovernor(cfg GovernorConfig) *Governor {
	perMin := cfg.PerMinute
	if perMin < 1 {
		perMin = 1
	}
	burst := cfg.Burst
	if burst < 1 {
		burst = 1
	}
	return &Governor{
		limit:         rate.Limit(float64(perMin) / 60.0),
		burst:         int(burst),
		now:           time.Now,
		pruneInterval: 5 * time.Minute,
		idleTTL:       10 * time.Minute,
		buckets:       make(map[string]*govEntry),
	}
}

// Check consumes one token for key. Returns ok=true when admitted now, else
// ok=false with whole seconds (≥1) to wait before retrying. Mirrors Rust's
// IpRateLimiterState::check.
func (g *Governor) Check(key string) (ok bool, retryAfterSecs uint32) {
	now := g.now()

	g.mu.Lock()
	g.maybePruneLocked(now)
	e := g.buckets[key]
	if e == nil {
		e = &govEntry{lim: rate.NewLimiter(g.limit, g.burst)}
		g.buckets[key] = e
	}
	e.lastSeen = now
	lim := e.lim
	g.mu.Unlock()

	// *rate.Limiter is safe for concurrent use, so we can reserve outside
	// the map lock. Reserve (not Allow) so we can report the wait; cancel
	// the reservation when we're going to reject, returning the token.
	rsv := lim.ReserveN(now, 1)
	if !rsv.OK() {
		return false, 1
	}
	if delay := rsv.DelayFrom(now); delay > 0 {
		rsv.CancelAt(now)
		return false, ceilSeconds(delay)
	}
	return true, 0
}

// Prune drops buckets idle longer than idleFor and returns how many were
// removed. Called opportunistically from Check; exposed for tests.
func (g *Governor) Prune(idleFor time.Duration) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pruneLocked(g.now(), idleFor)
}

func (g *Governor) maybePruneLocked(now time.Time) {
	if !g.lastPrune.IsZero() && now.Sub(g.lastPrune) < g.pruneInterval {
		return
	}
	g.lastPrune = now
	g.pruneLocked(now, g.idleTTL)
}

func (g *Governor) pruneLocked(now time.Time, idleFor time.Duration) int {
	cutoff := now.Add(-idleFor)
	n := 0
	for k, e := range g.buckets {
		if e.lastSeen.Before(cutoff) {
			delete(g.buckets, k)
			n++
		}
	}
	return n
}

func ceilSeconds(d time.Duration) uint32 {
	secs := uint32(d / time.Second)
	if time.Duration(secs)*time.Second < d {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	return secs
}

// GovernorMiddleware sheds requests whose source IP exhausts the in-memory
// bucket, before any distributed check. Requests with no resolvable IP pass
// through (the LB's job). Layer it OUTSIDE IPLimitMiddleware so the local
// bucket is consulted first.
func GovernorMiddleware(g *Governor, message string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			if ip == "" {
				next.ServeHTTP(w, r)
				return
			}
			if ok, retry := g.Check(ip); !ok {
				WriteTooManyRequests(w, retry, message)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
