// Package loginbackoff is a 1:1 port of auth/login_backoff.rs — layered
// brute-force protection for the password login endpoint.
//
// Two checks run before credentials are evaluated:
//
//  1. Per-(identifier, IP) exponential backoff — the first few failures
//     are free, then each additional failure doubles the required delay up
//     to a cap. Slows targeted brute force from one source without locking
//     out the legitimate user coming from a different IP.
//  2. Per-identifier global ceiling — caps total failures across all IPs in
//     a sliding window, catching distributed attacks. A high threshold so
//     it never trips on normal usage.
//
// Federated principals must be screened out before calling Check (the
// email-domain gate redirects them to their IdP before any credential
// check).
package loginbackoff

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/loginattempt"
)

// Policy holds the backoff/ceiling knobs (all env-overridable).
type Policy struct {
	FreeAttempts     uint32 // failures allowed with no delay
	BaseDelaySecs    uint32 // delay applied at FreeAttempts+1
	MaxDelaySecs     uint32 // cap on the per-pair backoff delay
	GlobalWindowSecs int64  // window for the global ceiling
	GlobalCeiling    int64  // failures (any IP) in-window that trigger a lock
	GlobalLockSecs   int64  // lock duration when the ceiling trips
}

// PolicyFromEnv builds a Policy from FC_LOGIN_* env vars, falling back to
// the Rust defaults.
func PolicyFromEnv() Policy {
	return Policy{
		FreeAttempts:     uint32(envInt("FC_LOGIN_BACKOFF_FREE_ATTEMPTS", 3)),
		BaseDelaySecs:    uint32(envInt("FC_LOGIN_BACKOFF_BASE_SECS", 2)),
		MaxDelaySecs:     uint32(envInt("FC_LOGIN_BACKOFF_MAX_SECS", 300)),
		GlobalWindowSecs: int64(envInt("FC_LOGIN_GLOBAL_WINDOW_SECS", 3600)),
		GlobalCeiling:    int64(envInt("FC_LOGIN_GLOBAL_CEILING", 100)),
		GlobalLockSecs:   int64(envInt("FC_LOGIN_GLOBAL_LOCK_SECS", 900)),
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

// ComputeDelaySecs returns the required delay given the failure count since
// the last success from the same (identifier, IP) pair: 0 below
// FreeAttempts, then base*2^(n-free-1), capped at MaxDelaySecs.
func (p Policy) ComputeDelaySecs(failureCount uint32) uint32 {
	if failureCount <= p.FreeAttempts {
		return 0
	}
	exponent := failureCount - p.FreeAttempts - 1
	if exponent > 31 {
		exponent = 31
	}
	scaled := uint64(p.BaseDelaySecs) << exponent
	if scaled > uint64(p.MaxDelaySecs) {
		return p.MaxDelaySecs
	}
	return uint32(scaled)
}

// Reason identifies which gate rejected an attempt.
type Reason string

const (
	ReasonPairBackoff   Reason = "pair_backoff"
	ReasonGlobalCeiling Reason = "global_ceiling"
)

// Decision is the outcome of a Check. Allowed=false carries the seconds the
// caller should wait (surfaced as a 429 + Retry-After).
type Decision struct {
	Allowed        bool
	RetryAfterSecs uint32
	Reason         Reason
}

// statsRepo is the subset of loginattempt.Repository the backoff needs.
type statsRepo interface {
	LastSuccessAt(ctx context.Context, identifier string) (*time.Time, error)
	FailureStatsByIdentifierIPSince(ctx context.Context, identifier, ip string, since time.Time) (int, *time.Time, error)
	FailureCountByIdentifierSince(ctx context.Context, identifier string, since time.Time) (int, error)
}

var _ statsRepo = (*loginattempt.Repository)(nil)

// Check runs the per-pair backoff + global ceiling. ip is best-effort —
// pass "" when unknown (local dev) and only the global ceiling applies.
//
// The identifier is lower-cased before querying: all current callers key on
// an email, attempts are recorded lower-cased, and a raw `identifier = $1`
// compare on the typed casing would let an attacker dodge the per-email
// ceiling by rotating case.
func Check(ctx context.Context, repo statsRepo, policy Policy, identifier, ip string) (Decision, error) {
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	now := time.Now().UTC()

	// Window 1: failures since the last success bound the per-pair count.
	lastSuccess, err := repo.LastSuccessAt(ctx, identifier)
	if err != nil {
		return Decision{}, err
	}
	lastSuccessCutoff := now.AddDate(0, 0, -30)
	if lastSuccess != nil {
		lastSuccessCutoff = *lastSuccess
	}

	if ip != "" {
		count, lastFailure, err := repo.FailureStatsByIdentifierIPSince(ctx, identifier, ip, lastSuccessCutoff)
		if err != nil {
			return Decision{}, err
		}
		if count < 0 {
			count = 0
		}
		required := policy.ComputeDelaySecs(uint32(count))
		if required > 0 {
			last := now
			if lastFailure != nil {
				last = *lastFailure
			}
			elapsed := int64(now.Sub(last).Seconds())
			if elapsed < 0 {
				elapsed = 0
			}
			if uint32(elapsed) < required {
				return Decision{Allowed: false, RetryAfterSecs: required - uint32(elapsed), Reason: ReasonPairBackoff}, nil
			}
		}
	}

	// Window 2: per-identifier global ceiling within GlobalWindowSecs.
	globalCutoff := now.Add(-time.Duration(policy.GlobalWindowSecs) * time.Second)
	cutoff := globalCutoff
	if lastSuccessCutoff.After(cutoff) {
		cutoff = lastSuccessCutoff
	}
	globalCount, err := repo.FailureCountByIdentifierSince(ctx, identifier, cutoff)
	if err != nil {
		return Decision{}, err
	}
	if int64(globalCount) >= policy.GlobalCeiling {
		lock := policy.GlobalLockSecs
		if lock < 0 {
			lock = 0
		}
		return Decision{Allowed: false, RetryAfterSecs: uint32(lock), Reason: ReasonGlobalCeiling}, nil
	}

	return Decision{Allowed: true}, nil
}
