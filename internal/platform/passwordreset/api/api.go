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

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/mfatoken"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/twofa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/mfa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/notify"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/passwordreset"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	principalops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/email"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// resetTokenTTL is the single-use token lifetime. 1:1 with Rust (15 min).
const resetTokenTTL = 15 * time.Minute

// inviteTokenTTL is the first-time "set your password" lifetime — longer than a
// reset so a newly-invited user has time to act on the email.
const inviteTokenTTL = 72 * time.Hour

// Emailer delivers the reset link to the user. Optional: when nil the token is
// still created and stored (best-effort delivery, mirroring Rust's "email
// failure is logged not propagated"). Use NewEmailer to build one from an
// email.Service (wired in WirePlatform from the SMTP_* env).
type Emailer interface {
	SendResetLink(ctx context.Context, to, resetLink string) error
	SendInviteLink(ctx context.Context, to, inviteLink string) error
}

// linkEmailer wraps an email.Service and renders the reset-link email. The
// subject + body are 1:1 with Rust password_reset_api's EmailMessage.
type linkEmailer struct{ svc email.Service }

// NewEmailer adapts an email.Service to the Emailer interface.
func NewEmailer(svc email.Service) Emailer { return linkEmailer{svc: svc} }

func (e linkEmailer) SendResetLink(ctx context.Context, to, resetLink string) error {
	return e.svc.Send(ctx, email.Message{
		To:      to,
		Subject: "Reset your password",
		HTMLBody: "<p>You requested a password reset.</p>" +
			"<p><a href=\"" + resetLink + "\">Click here to reset your password</a></p>" +
			"<p>This link expires in 15 minutes.</p>" +
			"<p>If you did not request this, you can safely ignore this email.</p>",
	})
}

func (e linkEmailer) SendInviteLink(ctx context.Context, to, inviteLink string) error {
	return e.svc.Send(ctx, email.Message{
		To:      to,
		Subject: "Set your password",
		HTMLBody: "<p>An account has been created for you on FlowCatalyst.</p>" +
			"<p><a href=\"" + inviteLink + "\">Click here to set your password</a></p>" +
			"<p>If two-factor authentication is required for your organisation, " +
			"you'll be guided through setting it up.</p>" +
			"<p>This link expires in 72 hours.</p>",
	})
}

// principalEmailer mints a reset token for a principal and emails the link. It
// implements principal/operations.PasswordResetEmailer, powering the admin
// trigger POST /api/principals/{id}/send-password-reset. Lives here so it can
// reuse the token generation/hashing + the same email template.
type principalEmailer struct {
	tokens *passwordreset.Repository
	base   string
	mail   Emailer
}

// NewPrincipalEmailer adapts the token repo + email.Service to the admin
// SendResetEmail(ctx, principal) shape.
func NewPrincipalEmailer(tokens *passwordreset.Repository, baseURL string, svc email.Service) *principalEmailer {
	return &principalEmailer{tokens: tokens, base: baseURL, mail: NewEmailer(svc)}
}

// SendResetEmail creates a fresh single-use token for p and emails the link.
// reset2FA flags the token to also clear the user's 2FA on confirm.
func (e *principalEmailer) SendResetEmail(ctx context.Context, p *principal.Principal, reset2FA bool) error {
	if p == nil || p.UserIdentity == nil || strings.TrimSpace(p.UserIdentity.Email) == "" {
		return nil
	}
	if err := e.tokens.DeleteByPrincipalID(ctx, p.ID); err != nil {
		return err
	}
	raw, err := generateRawToken()
	if err != nil {
		return err
	}
	tok := passwordreset.New(p.ID, hashToken(raw), time.Now().UTC().Add(resetTokenTTL))
	tok.Reset2FA = reset2FA
	if err := e.tokens.Insert(ctx, tok); err != nil {
		return err
	}
	link := strings.TrimRight(e.base, "/") + "/auth/reset-password?token=" + raw
	return e.mail.SendResetLink(ctx, p.UserIdentity.Email, link)
}

// SendInvite mints a longer-lived invite token and emails a "set your password"
// link to a newly-created internal user. Same set-password page as reset, so
// the confirm flow (and its 2FA enrollment gate) is shared.
func (e *principalEmailer) SendInvite(ctx context.Context, p *principal.Principal) error {
	if p == nil || p.UserIdentity == nil || strings.TrimSpace(p.UserIdentity.Email) == "" {
		return nil
	}
	if err := e.tokens.DeleteByPrincipalID(ctx, p.ID); err != nil {
		return err
	}
	raw, err := generateRawToken()
	if err != nil {
		return err
	}
	tok := passwordreset.New(p.ID, hashToken(raw), time.Now().UTC().Add(inviteTokenTTL))
	tok.Purpose = passwordreset.PurposeInvite
	if err := e.tokens.Insert(ctx, tok); err != nil {
		return err
	}
	link := strings.TrimRight(e.base, "/") + "/auth/reset-password?token=" + raw
	return e.mail.SendInviteLink(ctx, p.UserIdentity.Email, link)
}

// State holds the deps the password-reset handlers reach into.
type State struct {
	Principals      *principal.Repository
	Tokens          *passwordreset.Repository
	UoW             *usecasepgx.UnitOfWork
	ExternalBaseURL string  // base for the reset link (e.g. cfg.JWTIssuer)
	Emailer         Emailer // optional; nil = no delivery

	// 2FA integration (all optional). When MFA + MFATokens are wired, the
	// confirm step clears 2FA on a reset_2fa token, revokes remembered devices,
	// and returns enrollment_required when the domain compels a second factor.
	MFA            *mfa.Service
	MFATokens      *mfatoken.Issuer
	Policy         twofa.Policy
	Notifier       *notify.Notifier
	EnrollTokenTTL time.Duration // default 30m
}

func (s *State) enrollTTL() time.Duration {
	if s.EnrollTokenTTL > 0 {
		return s.EnrollTokenTTL
	}
	return 30 * time.Minute
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

	// Post-reset side effects (best-effort) + the 2FA enrollment gate.
	resp := s.postResetTwoFactor(r.Context(), t)
	writeJSON(w, http.StatusOK, resp)
}

// confirmResponse is the confirm body, extended with the optional 2FA
// enrollment hand-off. status is "ok" or "enrollment_required".
type confirmResponse struct {
	Status         string   `json:"status"`
	Message        string   `json:"message"`
	EnrollToken    string   `json:"enrollToken,omitempty"`
	AllowedMethods []string `json:"allowedMethods,omitempty"`
}

// postResetTwoFactor runs the after-reset security side effects — optional 2FA
// clear (reset_2fa tokens), trusted-device revocation, the password-changed
// notification — then decides whether the user must now enroll a second factor.
func (s *State) postResetTwoFactor(ctx context.Context, t *passwordreset.Token) confirmResponse {
	ok := confirmResponse{Status: "ok", Message: "Password reset successfully."}

	p, err := s.Principals.FindByID(ctx, t.PrincipalID)
	if err != nil || p == nil {
		return ok
	}
	emailAddr := ""
	if p.UserIdentity != nil {
		emailAddr = p.UserIdentity.Email
	}

	if s.MFA != nil {
		// reset_2fa tokens clear all enrolled factors → forces re-enrollment.
		if t.Reset2FA {
			if err := s.MFA.ResetAll(ctx, p.ID); err != nil {
				slog.Warn("2FA reset during password reset failed", "principal", p.ID, "err", err)
			} else {
				s.Notifier.TwoFactorReset(ctx, emailAddr)
			}
		}
		// Any password change invalidates remembered devices (hygiene).
		if err := s.MFA.RevokeAllTrustedDevices(ctx, p.ID); err != nil {
			slog.Warn("revoke trusted devices failed", "principal", p.ID, "err", err)
		}
	}
	s.Notifier.PasswordChanged(ctx, emailAddr)

	// Enrollment gate: an internal 2FA-required user who isn't enrolled must set
	// up a factor before they can sign in — hand back an enroll token so the SPA
	// goes straight into setup.
	if s.MFA == nil || s.MFATokens == nil {
		return ok
	}
	ev := s.Policy.Evaluate(ctx, emailAddr)
	if !ev.Requires2FA() {
		return ok
	}
	enrolled, err := s.MFA.HasConfirmedMethod(ctx, p.ID)
	if err != nil {
		slog.Warn("2FA enrollment check failed", "principal", p.ID, "err", err)
		return ok
	}
	if enrolled {
		return ok
	}
	tok, err := s.MFATokens.Mint(p.ID, mfatoken.PurposeEnroll, s.enrollTTL())
	if err != nil {
		slog.Error("mint enroll token failed", "principal", p.ID, "err", err)
		return ok
	}
	return confirmResponse{
		Status:         "enrollment_required",
		Message:        "Password set. Set up two-factor authentication to finish.",
		EnrollToken:    tok,
		AllowedMethods: ev.AllowedMethods(),
	}
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
