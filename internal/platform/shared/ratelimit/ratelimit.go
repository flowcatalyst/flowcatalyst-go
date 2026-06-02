// Package ratelimit is a 1:1 port of fc-platform/shared/rate_limit_store/
// (the distributed Store) and rate_limit_middleware.rs (the in-memory
// Governor). The cluster-wide Store caps a coordinated attacker spreading
// load across replicas; the per-instance Governor sheds a local flood in
// front of it, before the network round-trip (defence in depth). They
// compose — see Governor in governor.go.
//
// Two backends implement one Store contract, selected at startup by
// Build: Redis (fixed-window INCR+EXPIRE) when FC_REDIS_URL is reachable,
// else Postgres (the iam_rate_limit_events table), else a Noop store when
// FC_RATE_LIMIT_DISABLE=1. The rest of the platform depends only on the
// Store interface and is indifferent to which backend won.
package ratelimit

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Bucket names a limiter scope. Keys/rows are scoped under it so distinct
// buckets never collide. Values appear verbatim in SQL/Redis keys.
type Bucket string

const (
	BucketOAuthTokenIP         Bucket = "oauth_token_ip"
	BucketOAuthTokenClient     Bucket = "oauth_token_client"
	BucketOAuthAuthorizeIP     Bucket = "oauth_authorize_ip"
	BucketOAuthAuthorizeClient Bucket = "oauth_authorize_client"
	BucketOAuthIntrospectIP    Bucket = "oauth_introspect_ip"
	BucketOAuthRevokeIP        Bucket = "oauth_revoke_ip"
	BucketPasswordResetIP      Bucket = "password_reset_ip"
	BucketPasswordResetEmail   Bucket = "password_reset_email"
	BucketCheckDomainIP        Bucket = "check_domain_ip"
)

// Policy is "at most Limit events in any Window" for one (bucket, key).
type Policy struct {
	Window time.Duration
	Limit  uint32
}

// Policies holds the per-bucket defaults, loaded once at startup. Defaults
// are deliberately generous — this is the long-tail cluster-wide ceiling.
type Policies struct {
	OAuthTokenIP         Policy
	OAuthTokenClient     Policy
	OAuthAuthorizeIP     Policy
	OAuthAuthorizeClient Policy
	PasswordResetIP      Policy
	PasswordResetEmail   Policy
}

// PoliciesFromEnv reads the FC_RL_* knobs, matching the Rust defaults.
func PoliciesFromEnv() Policies {
	return Policies{
		OAuthTokenIP:         Policy{time.Minute, envU32("FC_RL_OAUTH_TOKEN_IP_PER_MIN", 600)},
		OAuthTokenClient:     Policy{time.Minute, envU32("FC_RL_OAUTH_TOKEN_CLIENT_PER_MIN", 300)},
		OAuthAuthorizeIP:     Policy{time.Minute, envU32("FC_RL_OAUTH_AUTHORIZE_IP_PER_MIN", 600)},
		OAuthAuthorizeClient: Policy{time.Minute, envU32("FC_RL_OAUTH_AUTHORIZE_CLIENT_PER_MIN", 300)},
		PasswordResetIP:      Policy{time.Hour, envU32("FC_RL_PASSWORD_RESET_IP_PER_HOUR", 20)},
		PasswordResetEmail:   Policy{time.Hour, envU32("FC_RL_PASSWORD_RESET_EMAIL_PER_HOUR", 5)},
	}
}

// MaxWindow is the longest window across all policies — used by the prune
// task to know how far back to keep history.
func (p Policies) MaxWindow() time.Duration {
	max := time.Hour
	for _, w := range []time.Duration{
		p.OAuthTokenIP.Window, p.OAuthTokenClient.Window,
		p.OAuthAuthorizeIP.Window, p.OAuthAuthorizeClient.Window,
		p.PasswordResetIP.Window, p.PasswordResetEmail.Window,
	} {
		if w > max {
			max = w
		}
	}
	return max
}

func envU32(name string, def uint32) uint32 {
	if v, err := strconv.ParseUint(os.Getenv(name), 10, 32); err == nil {
		return uint32(v)
	}
	return def
}

// Decision is the outcome of CheckAndRecord. RetryAfterSecs is a
// worst-case estimate (≤ window) of when room frees up.
type Decision struct {
	Allowed        bool
	RetryAfterSecs uint32
}

// Store is the rate-limit backend contract. CheckAndRecord is a single
// atomic call (counting and recording split across two round-trips leaves
// a bypass race under burst load).
type Store interface {
	CheckAndRecord(ctx context.Context, bucket Bucket, key string, policy Policy) (Decision, error)
	// Prune drops events older than the given age. Redis is a no-op
	// (TTLs auto-expire); Postgres deletes old rows. Best-effort.
	Prune(ctx context.Context, olderThan time.Duration) (int64, error)
}

// NoopStore always allows. Used when rate limiting is disabled.
type NoopStore struct{}

// CheckAndRecord always allows.
func (NoopStore) CheckAndRecord(context.Context, Bucket, string, Policy) (Decision, error) {
	return Decision{Allowed: true}, nil
}

// Prune is a no-op.
func (NoopStore) Prune(context.Context, time.Duration) (int64, error) { return 0, nil }

// Build selects the backend: Redis when FC_REDIS_URL is set and reachable
// (PING within a short timeout), else Postgres, else Noop when
// FC_RATE_LIMIT_DISABLE=1. The choice is logged at startup.
func Build(ctx context.Context, pool *pgxpool.Pool) Store {
	if os.Getenv("FC_RATE_LIMIT_DISABLE") == "1" {
		slog.Info("distributed rate-limit store: DISABLED (FC_RATE_LIMIT_DISABLE=1)")
		return NoopStore{}
	}
	if url := os.Getenv("FC_REDIS_URL"); url != "" {
		s, err := NewRedisStore(ctx, url)
		if err == nil {
			slog.Info("distributed rate-limit store: Redis", "redis_url", redactURL(url))
			return s
		}
		slog.Warn("FC_REDIS_URL set but Redis unreachable; falling back to Postgres rate-limit store", "err", err)
	} else {
		slog.Info("FC_REDIS_URL not set; using Postgres rate-limit store")
	}
	return NewPostgresStore(pool)
}

// redactURL masks credentials in a redis:// URL before logging.
func redactURL(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		scheme, rest := url[:i+3], url[i+3:]
		if at := strings.LastIndexByte(rest, '@'); at >= 0 {
			return scheme + "***@" + rest[at+1:]
		}
	}
	return url
}

// ─── enforcement helpers ────────────────────────────────────────────────

// Rejection signals an over-limit request and the suggested wait.
type Rejection struct{ RetryAfterSecs uint32 }

// Enforce runs the check for one (bucket, key) and returns a non-nil
// *Rejection when over the limit. Fails open (returns nil) on a backend
// error or a nil store — a degraded limiter must never take down auth.
func Enforce(ctx context.Context, store Store, bucket Bucket, key string, policy Policy) *Rejection {
	if store == nil {
		return nil
	}
	d, err := store.CheckAndRecord(ctx, bucket, key, policy)
	if err != nil {
		slog.Warn("distributed rate-limit backend error; failing open", "bucket", string(bucket), "err", err)
		return nil
	}
	if !d.Allowed {
		return &Rejection{RetryAfterSecs: d.RetryAfterSecs}
	}
	return nil
}

// WriteTooManyRequests emits a 429 with a Retry-After header and the
// platform {error, message} envelope (matching Rust's enforce_distributed).
func WriteTooManyRequests(w http.ResponseWriter, retryAfterSecs uint32, message string) {
	if retryAfterSecs < 1 {
		retryAfterSecs = 1
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.FormatUint(uint64(retryAfterSecs), 10))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"TOO_MANY_REQUESTS","message":"` + jsonEscape(message) + `"}`))
}

// IPLimitMiddleware rejects requests whose source IP exhausts the bucket.
// Requests with no resolvable IP pass through (that's the LB's job).
func IPLimitMiddleware(store Store, bucket Bucket, policy Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			if ip == "" {
				next.ServeHTTP(w, r)
				return
			}
			if rej := Enforce(r.Context(), store, bucket, ip, policy); rej != nil {
				WriteTooManyRequests(w, rej.RetryAfterSecs, "rate limit exceeded for this IP")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the best-effort client IP: the first hop of
// X-Forwarded-For when present, else the request RemoteAddr (host only).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSpace(host)
}

func jsonEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return r.Replace(s)
}
