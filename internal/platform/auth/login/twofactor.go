package login

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/audit"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/loginbackoff"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/mfatoken"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/twofa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/loginattempt"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/mfa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// Token TTL fallbacks (overridable via Config).
const (
	defaultPendingTokenTTL = 10 * time.Minute
	defaultEnrollTokenTTL  = 30 * time.Minute
)

// Trusted-device cookie. In production (Secure) we use the __Host- prefix for
// the strongest binding the platform offers; over plain-HTTP localhost the
// prefix is invalid, so dev falls back to a bare name.
const (
	trustedDeviceCookieProd = "__Host-fc_td"
	trustedDeviceCookieDev  = "fc_td"
)

// twoFactorResponse is the body for the two pending outcomes of /auth/login.
type twoFactorResponse struct {
	Status string `json:"status"` // "mfa_required" | "enrollment_required"
	// MFAToken gates the challenge endpoints (status mfa_required).
	MFAToken string `json:"mfaToken,omitempty"`
	// EnrollToken gates the enrollment endpoints (status enrollment_required).
	EnrollToken string `json:"enrollToken,omitempty"`
	// Methods are the factor types the user can challenge with (mfa_required).
	Methods []string `json:"methods,omitempty"`
	// AllowedMethods are the factor types the user may enroll
	// (enrollment_required).
	AllowedMethods []string `json:"allowedMethods,omitempty"`
	// RememberDeviceAllowed advertises the per-domain remember-device option.
	RememberDeviceAllowed bool `json:"rememberDeviceAllowed,omitempty"`
}

// RegisterTwoFactorRoutes mounts the /auth/2fa/* endpoints. No-op when MFA
// isn't wired, so callers can register unconditionally.
func (e *Endpoint) RegisterTwoFactorRoutes(r chi.Router) {
	if e.cfg.MFA == nil || e.cfg.MFATokens == nil {
		return
	}
	r.Post("/auth/2fa/verify", e.handle2FAVerify)
	r.Post("/auth/2fa/challenge/email", e.handle2FAChallengeEmail)
	r.Post("/auth/2fa/enroll/totp/begin", e.handle2FAEnrollTOTPBegin)
	r.Post("/auth/2fa/enroll/totp/confirm", e.handle2FAEnrollTOTPConfirm)
	r.Post("/auth/2fa/enroll/email/begin", e.handle2FAEnrollEmailBegin)
	r.Post("/auth/2fa/enroll/email/confirm", e.handle2FAEnrollEmailConfirm)
}

// ── decision (called from handleLogin after the password verifies) ─────────

// maybeChallenge2FA decides whether this just-authenticated user needs a second
// factor. It writes the mfa_required / enrollment_required response and returns
// handled=true when a challenge is owed; returns handled=false to let the
// caller complete the login normally. A non-nil error means "couldn't decide"
// and the caller fails closed.
func (e *Endpoint) maybeChallenge2FA(w http.ResponseWriter, r *http.Request, p *principal.Principal) (bool, error) {
	// Federated users never carry a password, so this is defensive: no 2FA for
	// external identities.
	if p.ExternalIdentity != nil {
		return false, nil
	}
	email := ""
	if p.UserIdentity != nil {
		email = p.UserIdentity.Email
	}
	mapping, internalDomain := e.domainPolicy(r.Context(), email)
	domainRequires := mapping != nil && mapping.Require2FA && internalDomain
	rememberAllowed := mapping != nil && mapping.RememberDeviceEnabled

	confirmed, err := e.cfg.MFA.ConfirmedMethods(r.Context(), p.ID)
	if err != nil {
		return false, err
	}

	// Methods the user can actually challenge with: their confirmed factors,
	// narrowed to the domain's allowed set when the domain enforces 2FA.
	usable := methodStrings(confirmed)
	if domainRequires {
		usable = intersect(usable, mapping.Allowed2FAMethods)
	}

	if len(usable) > 0 {
		// A remembered device skips the challenge for this browser.
		if rememberAllowed {
			if raw := e.readTrustedDeviceCookie(r); raw != "" {
				ok, verr := e.cfg.MFA.VerifyTrustedDevice(r.Context(), p.ID, raw)
				if verr != nil {
					return false, verr
				}
				if ok {
					return false, nil // satisfied → proceed
				}
			}
		}
		tok, err := e.cfg.MFATokens.Mint(p.ID, mfatoken.PurposePending, e.pendingTTL())
		if err != nil {
			return false, err
		}
		writeJSON(w, http.StatusOK, twoFactorResponse{
			Status:                "mfa_required",
			MFAToken:              tok,
			Methods:               usable,
			RememberDeviceAllowed: rememberAllowed,
		})
		return true, nil
	}

	// No usable factor. Only the domain can compel enrollment. A passkey does
	// NOT exempt the password path: a passkey holder who chooses to sign in
	// with a password must still complete 2FA — and therefore enroll a factor.
	// (Signing in WITH the passkey skips all of this on its own route.)
	if !domainRequires {
		return false, nil
	}
	tok, err := e.cfg.MFATokens.Mint(p.ID, mfatoken.PurposeEnroll, e.enrollTTL())
	if err != nil {
		return false, err
	}
	writeJSON(w, http.StatusOK, twoFactorResponse{
		Status:         "enrollment_required",
		EnrollToken:    tok,
		AllowedMethods: mapping.Allowed2FAMethods,
	})
	return true, nil
}

// ── challenge / verify ─────────────────────────────────────────────────────

type verifyRequest struct {
	MFAToken       string `json:"mfaToken"`
	Method         string `json:"method"`
	Code           string `json:"code"`
	RememberDevice bool   `json:"rememberDevice"`
}

func (e *Endpoint) handle2FAVerify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := e.principalFromToken(w, r, req.MFAToken, mfatoken.PurposePending)
	if p == nil {
		return
	}

	// Brute-force backoff on the second factor, keyed per (email, IP) — same
	// store + policy as the password step. Failed verifies are recorded below,
	// so repeated wrong codes throttle just like wrong passwords.
	ip := clientIP(r)
	email := emailOf(p)
	if e.cfg.LoginAttempts != nil {
		if d, derr := loginbackoff.Check(r.Context(), e.cfg.LoginAttempts, e.cfg.BackoffPolicy, email, ip); derr == nil && !d.Allowed {
			writeTooManyRequests(w, d.RetryAfterSecs)
			return
		}
	}

	var ok bool
	var err error
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	switch method {
	case string(mfa.MethodTOTP):
		ok, err = e.cfg.MFA.VerifyTOTP(r.Context(), p.ID, req.Code)
	case string(mfa.MethodEmailPin):
		ok, err = e.cfg.MFA.VerifyLoginEmailPin(r.Context(), p.ID, req.Code)
	case "RECOVERY_CODE":
		ok, err = e.cfg.MFA.VerifyRecoveryCode(r.Context(), p.ID, req.Code)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_METHOD", "message": "unknown 2FA method"})
		return
	}
	if err != nil {
		slog.Error("2FA verify failed", "principal", p.ID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "VERIFY_FAILED", "message": "could not verify code"})
		return
	}
	if !ok {
		e.recordAttempt(r.Context(), loginattempt.OutcomeFailure, email, &p.ID, ip, "Invalid 2FA code")
		writeUnauthorized(w, "Invalid or expired code")
		return
	}

	// A recovery code just got burned — let the user know out-of-band.
	if method == "RECOVERY_CODE" {
		e.cfg.Notifier.RecoveryCodeUsed(r.Context(), emailOf(p))
	}
	// Optional remember-this-device (only when the domain allows it).
	if req.RememberDevice {
		e.rememberDevice(w, r, p)
	}
	e.completeLogin(w, r, p, nil)
}

type challengeEmailRequest struct {
	MFAToken string `json:"mfaToken"`
}

func (e *Endpoint) handle2FAChallengeEmail(w http.ResponseWriter, r *http.Request) {
	var req challengeEmailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := e.principalFromToken(w, r, req.MFAToken, mfatoken.PurposePending)
	if p == nil {
		return
	}
	email := emailOf(p)
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "NO_EMAIL", "message": "account has no email"})
		return
	}
	if err := e.cfg.MFA.SendLoginEmailPin(r.Context(), p.ID, email); err != nil {
		slog.Error("send email pin failed", "principal", p.ID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "EMAIL_SEND_FAILED", "message": "could not send code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "A verification code has been sent to your email."})
}

// ── enrollment (gated by an enroll token) ──────────────────────────────────

type enrollBeginRequest struct {
	EnrollToken string `json:"enrollToken"`
}

type enrollConfirmRequest struct {
	EnrollToken string `json:"enrollToken"`
	Code        string `json:"code"`
}

func (e *Endpoint) handle2FAEnrollTOTPBegin(w http.ResponseWriter, r *http.Request) {
	var req enrollBeginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := e.principalFromToken(w, r, req.EnrollToken, mfatoken.PurposeEnroll)
	if p == nil {
		return
	}
	if !e.methodAllowed(r.Context(), p, mfa.MethodTOTP) {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "METHOD_NOT_ALLOWED", "message": "authenticator app is not permitted for this domain"})
		return
	}
	enr, err := e.cfg.MFA.BeginTOTPEnrollment(r.Context(), p.ID, emailOf(p))
	if err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": enr.Secret, "uri": enr.URI})
}

func (e *Endpoint) handle2FAEnrollTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var req enrollConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := e.principalFromToken(w, r, req.EnrollToken, mfatoken.PurposeEnroll)
	if p == nil {
		return
	}
	ok, err := e.cfg.MFA.ConfirmTOTPEnrollment(r.Context(), p.ID, req.Code)
	if err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_CODE", "message": "that code didn't match — try again"})
		return
	}
	e.cfg.Notifier.TwoFactorEnrolled(r.Context(), emailOf(p), string(mfa.MethodTOTP))
	e.auditMFA(r.Context(), p.ID, "2FA_TOTP_ENROLLED")
	e.completeEnrollment(w, r, p)
}

func (e *Endpoint) handle2FAEnrollEmailBegin(w http.ResponseWriter, r *http.Request) {
	var req enrollBeginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := e.principalFromToken(w, r, req.EnrollToken, mfatoken.PurposeEnroll)
	if p == nil {
		return
	}
	if !e.methodAllowed(r.Context(), p, mfa.MethodEmailPin) {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "METHOD_NOT_ALLOWED", "message": "email codes are not permitted for this domain"})
		return
	}
	email := emailOf(p)
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "NO_EMAIL", "message": "account has no email"})
		return
	}
	if err := e.cfg.MFA.BeginEmailEnrollment(r.Context(), p.ID, email); err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "A verification code has been sent to your email."})
}

func (e *Endpoint) handle2FAEnrollEmailConfirm(w http.ResponseWriter, r *http.Request) {
	var req enrollConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := e.principalFromToken(w, r, req.EnrollToken, mfatoken.PurposeEnroll)
	if p == nil {
		return
	}
	ok, err := e.cfg.MFA.ConfirmEmailEnrollment(r.Context(), p.ID, req.Code)
	if err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_CODE", "message": "that code didn't match — try again"})
		return
	}
	e.cfg.Notifier.TwoFactorEnrolled(r.Context(), emailOf(p), string(mfa.MethodEmailPin))
	e.auditMFA(r.Context(), p.ID, "2FA_EMAIL_ENROLLED")
	e.completeEnrollment(w, r, p)
}

// completeEnrollment generates a first recovery-code set (only if the user has
// none yet) and completes the login, returning the codes once.
func (e *Endpoint) completeEnrollment(w http.ResponseWriter, r *http.Request, p *principal.Principal) {
	e.completeLogin(w, r, p, e.ensureRecoveryCodes(r.Context(), p))
}

// auditMFA records a 2FA state change to the audit trail (best-effort).
func (e *Endpoint) auditMFA(ctx context.Context, principalID, operation string) {
	if e.cfg.Audit == nil {
		return
	}
	pid := principalID
	_ = e.cfg.Audit.Insert(ctx, &audit.Log{
		ID:          tsid.Generate(tsid.AuditLog),
		EntityType:  "PRINCIPAL",
		EntityID:    principalID,
		Operation:   operation,
		PrincipalID: &pid,
		PerformedAt: time.Now().UTC(),
	})
}

// ensureRecoveryCodes generates a first recovery-code set only when the user
// has none, returning the plaintext codes (shown once) or nil. Notifies on
// generation.
func (e *Endpoint) ensureRecoveryCodes(ctx context.Context, p *principal.Principal) []string {
	n, err := e.cfg.MFA.RemainingRecoveryCodes(ctx, p.ID)
	if err != nil || n > 0 {
		return nil
	}
	codes, gerr := e.cfg.MFA.GenerateRecoveryCodes(ctx, p.ID)
	if gerr != nil {
		slog.Error("recovery code generation failed", "principal", p.ID, "err", gerr)
		return nil
	}
	e.cfg.Notifier.RecoveryCodesRegenerated(ctx, emailOf(p))
	return codes
}

// ── helpers ────────────────────────────────────────────────────────────────

// principalFromToken parses tok for the wanted purpose, loads the active
// principal, and writes a 401 (returning nil) on any failure.
func (e *Endpoint) principalFromToken(w http.ResponseWriter, r *http.Request, tok string, purpose mfatoken.Purpose) *principal.Principal {
	claims, err := e.cfg.MFATokens.Parse(tok, purpose)
	if err != nil {
		writeUnauthorized(w, "Invalid or expired session")
		return nil
	}
	p, err := e.cfg.Principals.FindByID(r.Context(), claims.Subject)
	if err != nil || p == nil || !p.Active {
		writeUnauthorized(w, "Invalid or expired session")
		return nil
	}
	return p
}

// domainPolicy returns the email-domain mapping for the address and whether the
// domain authenticates internally (no mapping → internal, no policy). Delegates
// to the shared twofa.Policy so login and password-reset agree.
func (e *Endpoint) domainPolicy(ctx context.Context, email string) (*emaildomainmapping.EmailDomainMapping, bool) {
	ev := twofa.Policy{Mappings: e.cfg.Mappings, IDPs: e.cfg.IdentityProviders}.Evaluate(ctx, email)
	return ev.Mapping, ev.Internal
}

// methodAllowed reports whether the user may enroll method t — always true
// unless the domain enforces 2FA with a restricted allow-list.
func (e *Endpoint) methodAllowed(ctx context.Context, p *principal.Principal, t mfa.MethodType) bool {
	mapping, internal := e.domainPolicy(ctx, emailOf(p))
	if mapping == nil || !mapping.Require2FA || !internal {
		return true
	}
	return containsString(mapping.Allowed2FAMethods, string(t))
}

// rememberDevice issues a trusted-device token and sets the cookie, honouring
// the domain's remember-device policy (no-op when the domain disallows it).
func (e *Endpoint) rememberDevice(w http.ResponseWriter, r *http.Request, p *principal.Principal) {
	mapping, internal := e.domainPolicy(r.Context(), emailOf(p))
	if mapping == nil || !mapping.RememberDeviceEnabled || !internal {
		return
	}
	days := mapping.RememberDeviceDays
	if days <= 0 {
		days = 30
	}
	ttl := time.Duration(days) * 24 * time.Hour
	label := userAgentLabel(r)
	raw, err := e.cfg.MFA.IssueTrustedDevice(r.Context(), p.ID, label, ttl)
	if err != nil {
		slog.Error("issue trusted device failed", "principal", p.ID, "err", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     e.trustedDeviceCookieName(),
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   e.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	})
	labelStr := ""
	if label != nil {
		labelStr = *label
	}
	e.cfg.Notifier.NewTrustedDevice(r.Context(), emailOf(p), labelStr)
}

func (e *Endpoint) trustedDeviceCookieName() string {
	if e.cfg.CookieSecure {
		return trustedDeviceCookieProd
	}
	return trustedDeviceCookieDev
}

func (e *Endpoint) readTrustedDeviceCookie(r *http.Request) string {
	c, err := r.Cookie(e.trustedDeviceCookieName())
	if err != nil {
		return ""
	}
	return c.Value
}

func (e *Endpoint) pendingTTL() time.Duration {
	if e.cfg.PendingTokenTTL > 0 {
		return e.cfg.PendingTokenTTL
	}
	return defaultPendingTokenTTL
}

func (e *Endpoint) enrollTTL() time.Duration {
	if e.cfg.EnrollTokenTTL > 0 {
		return e.cfg.EnrollTokenTTL
	}
	return defaultEnrollTokenTTL
}

// writeEnrollErr maps the mfa service's sentinel errors to HTTP statuses.
func (e *Endpoint) writeEnrollErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mfa.ErrAlreadyEnrolled):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "ALREADY_ENROLLED", "message": "that method is already set up"})
	case errors.Is(err, mfa.ErrEncryptionUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "TOTP_UNAVAILABLE", "message": "authenticator-app 2FA is not available"})
	case errors.Is(err, mfa.ErrNoPendingEnrollment):
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "NO_PENDING_ENROLLMENT", "message": "start enrollment first"})
	default:
		slog.Error("2FA enrollment error", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "ENROLL_FAILED", "message": "could not complete enrollment"})
	}
}

// ── small pure helpers ─────────────────────────────────────────────────────

func emailOf(p *principal.Principal) string {
	if p.UserIdentity != nil {
		return p.UserIdentity.Email
	}
	return ""
}

func methodStrings(ms []mfa.MethodType) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, string(m))
	}
	return out
}

func intersect(a, allowed []string) []string {
	out := make([]string, 0, len(a))
	for _, x := range a {
		if containsString(allowed, x) {
			out = append(out, x)
		}
	}
	return out
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func userAgentLabel(r *http.Request) *string {
	ua := strings.TrimSpace(r.UserAgent())
	if ua == "" {
		return nil
	}
	if len(ua) > 250 {
		ua = ua[:250]
	}
	return &ua
}

// decodeJSON decodes the request body into v, writing a 400 (returning false)
// on malformed input.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_JSON", "message": "malformed request body"})
		return false
	}
	return true
}
