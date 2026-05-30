// Package api serves the unauthenticated /auth/password-reset/* flow:
// request → (email link) → validate → confirm. Port of the Rust
// auth/password_reset_api.rs. Tokens are stored as lowercase-hex SHA-256
// hashes; the raw token is only ever in the reset link.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/passwordreset"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	principalops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// resetTokenTTL is the single-use token lifetime. 1:1 with Rust (15 min).
const resetTokenTTL = 15 * time.Minute

// Emailer delivers the reset link to the user. Optional: when nil the token is
// still created and stored (best-effort delivery, mirroring Rust's "email
// failure is logged not propagated"). No mailer is wired in the Go platform
// yet, so in practice the token is created but not emailed — see the package
// note. Wire an implementation when an email service lands.
type Emailer interface {
	SendResetLink(ctx context.Context, to, resetLink string) error
}

// State holds the deps the password-reset handlers reach into.
type State struct {
	Principals      *principal.Repository
	Tokens          *passwordreset.Repository
	UoW             *usecasepgx.UnitOfWork
	ExternalBaseURL string  // base for the reset link (e.g. cfg.JWTIssuer)
	Emailer         Emailer // optional; nil = no delivery
}

// RegisterRoutes mounts the three unauthenticated endpoints. Mount OUTSIDE the
// auth middleware (alongside /auth/login).
func RegisterRoutes(r chi.Router, s *State) {
	r.Post("/auth/password-reset/request", s.requestReset)
	r.Get("/auth/password-reset/validate", s.validateToken)
	r.Post("/auth/password-reset/confirm", s.confirmReset)
}

type messageResponse struct {
	Message string `json:"message"`
}

type validateTokenResponse struct {
	Valid bool `json:"valid"`
	// Reason is null when valid (Rust emits null, not absent) → no omitempty.
	Reason *string `json:"reason"`
}

// ── POST /auth/password-reset/request ───────────────────────────────────────

// requestReset issues a reset token for the email's account, if any. Uses the
// silent-success pattern: it always returns the same message regardless of
// whether the account exists, so the endpoint can't enumerate accounts.
func (s *State) requestReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, usecase.Validation("INVALID_BODY", "malformed request body"))
		return
	}
	if err := s.tryIssueToken(r.Context(), strings.TrimSpace(body.Email)); err != nil {
		slog.Warn("password reset request error (suppressed)", "err", err)
	}
	writeJSON(w, http.StatusOK, messageResponse{
		Message: "If an account exists, a reset email has been sent.",
	})
}

// tryIssueToken looks up the principal by email and, when eligible, invalidates
// any outstanding tokens, mints + stores a fresh one (15-min TTL), and emails
// the link best-effort. Errors are returned to the caller for suppressed
// logging — they never reach the client (anti-enumeration).
func (s *State) tryIssueToken(ctx context.Context, email string) error {
	if email == "" {
		return nil
	}
	p, err := s.Principals.FindByEmail(ctx, email)
	if err != nil {
		return err
	}
	if p == nil {
		slog.Warn("password reset requested for unknown email", "email", email)
		return nil
	}
	// Eligibility (mirrors the admin send-password-reset op): USER, has email,
	// not OIDC-federated. Ineligible → silent skip (still silent-success).
	if !p.IsUser() || p.ExternalIdentity != nil || p.UserIdentity == nil || strings.TrimSpace(p.UserIdentity.Email) == "" {
		return nil
	}

	if err := s.Tokens.DeleteByPrincipalID(ctx, p.ID); err != nil {
		return err
	}
	raw, err := generateRawToken()
	if err != nil {
		return err
	}
	tok := passwordreset.New(p.ID, hashToken(raw), time.Now().UTC().Add(resetTokenTTL))
	if err := s.Tokens.Insert(ctx, tok); err != nil {
		return err
	}

	// Best-effort delivery — never fail the request on email error.
	if s.Emailer != nil {
		link := strings.TrimRight(s.ExternalBaseURL, "/") + "/auth/reset-password?token=" + raw
		if err := s.Emailer.SendResetLink(ctx, p.UserIdentity.Email, link); err != nil {
			slog.Warn("failed to send password reset email", "principal", p.ID, "err", err)
		}
	}
	return nil
}

// ── GET /auth/password-reset/validate?token= ────────────────────────────────

func (s *State) validateToken(w http.ResponseWriter, r *http.Request) {
	t, err := s.Tokens.FindByTokenHash(r.Context(), hashToken(r.URL.Query().Get("token")))
	switch {
	case err != nil:
		slog.Warn("token validation error", "err", err)
		writeJSON(w, http.StatusOK, validateTokenResponse{Valid: false, Reason: reason("not_found")})
	case t == nil:
		writeJSON(w, http.StatusOK, validateTokenResponse{Valid: false, Reason: reason("not_found")})
	case t.IsExpired():
		writeJSON(w, http.StatusOK, validateTokenResponse{Valid: false, Reason: reason("expired")})
	default:
		writeJSON(w, http.StatusOK, validateTokenResponse{Valid: true, Reason: nil})
	}
}

// ── POST /auth/password-reset/confirm ───────────────────────────────────────

func (s *State) confirmReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, usecase.Validation("INVALID_BODY", "malformed request body"))
		return
	}

	t, err := s.Tokens.FindByTokenHash(r.Context(), hashToken(body.Token))
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "token lookup failed", err))
		return
	}
	if t == nil {
		httperror.Write(w, usecase.Validation("INVALID_TOKEN", "Invalid or expired reset token."))
		return
	}
	if t.IsExpired() {
		_ = s.Tokens.DeleteByPrincipalID(r.Context(), t.PrincipalID) // best-effort cleanup
		httperror.Write(w, usecase.Validation("EXPIRED_TOKEN", "Reset token has expired."))
		return
	}

	// The password write + UserPasswordReset event + audit log commit
	// atomically via ResetPassword. The unauthenticated reset is "system".
	ec := usecase.NewExecutionContext("system")
	if _, err := principalops.ResetPassword(r.Context(), s.Principals, s.UoW,
		principalops.ResetPasswordCommand{ID: t.PrincipalID, NewPassword: body.Password}, ec); err != nil {
		httperror.Write(w, err)
		return
	}

	// Invalidate the whole token set for this principal (single-use). Best
	// effort: the password is already changed if this fails.
	if err := s.Tokens.DeleteByPrincipalID(r.Context(), t.PrincipalID); err != nil {
		slog.Warn("failed to clear consumed reset tokens", "principal", t.PrincipalID, "err", err)
	}
	slog.Info("password reset completed", "principal", t.PrincipalID)
	writeJSON(w, http.StatusOK, messageResponse{Message: "Password reset successfully."})
}

// ── helpers ─────────────────────────────────────────────────────────────────

// hashToken returns the lowercase-hex SHA-256 of the raw token (the stored
// token_hash). 1:1 with Rust hash_token.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// generateRawToken returns 32 cryptographically-random bytes as URL-safe
// base64 (no padding) — 43 chars. 1:1 with Rust generate_raw_token.
func generateRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func reason(s string) *string { return &s }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
