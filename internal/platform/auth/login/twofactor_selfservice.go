package login

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/mfa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
)

// allMethods is the menu offered when a domain doesn't restrict 2FA methods
// (voluntary enrollment).
var allMethods = []string{string(mfa.MethodTOTP), string(mfa.MethodEmailPin)}

// RegisterTwoFactorSelfServiceRoutes mounts the session-gated 2FA management
// endpoints (Profile screen). Mount INSIDE the auth middleware. No-op when MFA
// isn't wired.
func (e *Endpoint) RegisterTwoFactorSelfServiceRoutes(r chi.Router) {
	if e.cfg.MFA == nil {
		return
	}
	r.Get("/auth/2fa/status", e.handle2FAStatus)
	r.Post("/auth/2fa/methods/totp/begin", e.handle2FASelfTOTPBegin)
	r.Post("/auth/2fa/methods/totp/confirm", e.handle2FASelfTOTPConfirm)
	r.Post("/auth/2fa/methods/email/begin", e.handle2FASelfEmailBegin)
	r.Post("/auth/2fa/methods/email/confirm", e.handle2FASelfEmailConfirm)
	r.Delete("/auth/2fa/methods/{method}", e.handle2FARemoveMethod)
	r.Post("/auth/2fa/recovery-codes/regenerate", e.handle2FARegenRecovery)
	r.Get("/auth/2fa/trusted-devices", e.handle2FAListTrustedDevices)
	r.Delete("/auth/2fa/trusted-devices/{id}", e.handle2FARevokeTrustedDevice)
}

// principalFromSession loads the authenticated principal, writing 401 (and
// returning nil) when there's no valid session.
func (e *Endpoint) principalFromSession(w http.ResponseWriter, r *http.Request) *principal.Principal {
	ac := auth.FromContext(r.Context())
	if ac == nil || ac.PrincipalID == "" {
		writeUnauthorized(w, "Not authenticated")
		return nil
	}
	p, err := e.cfg.Principals.FindByID(r.Context(), ac.PrincipalID)
	if err != nil || p == nil || !p.Active {
		writeUnauthorized(w, "Not authenticated")
		return nil
	}
	return p
}

// ── status ─────────────────────────────────────────────────────────────────

type twoFactorStatusResponse struct {
	Methods               []string `json:"methods"`               // confirmed factor types
	Required              bool     `json:"required"`              // domain compels 2FA
	AllowedMethods        []string `json:"allowedMethods"`        // enrollable methods
	RecoveryCodesLeft     int      `json:"recoveryCodesLeft"`     //
	RememberDeviceEnabled bool     `json:"rememberDeviceEnabled"` //
	TrustedDeviceCount    int      `json:"trustedDeviceCount"`    //
}

func (e *Endpoint) handle2FAStatus(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	confirmed, err := e.cfg.MFA.ConfirmedMethods(r.Context(), p.ID)
	if err != nil {
		writeServerError(w, "STATUS_FAILED", "could not load 2FA status")
		return
	}
	mapping, internal := e.domainPolicy(r.Context(), emailOf(p))
	required := mapping != nil && mapping.Require2FA && internal
	allowed := allMethods
	remember := false
	if required {
		allowed = mapping.Allowed2FAMethods
	}
	if mapping != nil && mapping.RememberDeviceEnabled && internal {
		remember = true
	}
	left, _ := e.cfg.MFA.RemainingRecoveryCodes(r.Context(), p.ID)
	devices, _ := e.cfg.MFA.ListTrustedDevices(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, twoFactorStatusResponse{
		Methods:               methodStrings(confirmed),
		Required:              required,
		AllowedMethods:        allowed,
		RecoveryCodesLeft:     left,
		RememberDeviceEnabled: remember,
		TrustedDeviceCount:    len(devices),
	})
}

// ── voluntary enrollment ─────────────────────────────────────────────────

func (e *Endpoint) handle2FASelfTOTPBegin(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	if !e.methodAllowed(r.Context(), p, mfa.MethodTOTP) {
		writeJSON(w, http.StatusForbidden, errBody("METHOD_NOT_ALLOWED", "authenticator app is not permitted for this domain"))
		return
	}
	enr, err := e.cfg.MFA.BeginTOTPEnrollment(r.Context(), p.ID, emailOf(p))
	if err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": enr.Secret, "uri": enr.URI})
}

func (e *Endpoint) handle2FASelfTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ok, err := e.cfg.MFA.ConfirmTOTPEnrollment(r.Context(), p.ID, req.Code)
	if err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_CODE", "that code didn't match — try again"))
		return
	}
	e.cfg.Notifier.TwoFactorEnrolled(r.Context(), emailOf(p), string(mfa.MethodTOTP))
	e.auditMFA(r.Context(), p.ID, "2FA_TOTP_ENROLLED")
	writeJSON(w, http.StatusOK, recoveryCodesBody(e.ensureRecoveryCodes(r.Context(), p)))
}

func (e *Endpoint) handle2FASelfEmailBegin(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	if !e.methodAllowed(r.Context(), p, mfa.MethodEmailPin) {
		writeJSON(w, http.StatusForbidden, errBody("METHOD_NOT_ALLOWED", "email codes are not permitted for this domain"))
		return
	}
	email := emailOf(p)
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errBody("NO_EMAIL", "account has no email"))
		return
	}
	if err := e.cfg.MFA.BeginEmailEnrollment(r.Context(), p.ID, email); err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "A verification code has been sent to your email."})
}

func (e *Endpoint) handle2FASelfEmailConfirm(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ok, err := e.cfg.MFA.ConfirmEmailEnrollment(r.Context(), p.ID, req.Code)
	if err != nil {
		e.writeEnrollErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_CODE", "that code didn't match — try again"))
		return
	}
	e.cfg.Notifier.TwoFactorEnrolled(r.Context(), emailOf(p), string(mfa.MethodEmailPin))
	e.auditMFA(r.Context(), p.ID, "2FA_EMAIL_ENROLLED")
	writeJSON(w, http.StatusOK, recoveryCodesBody(e.ensureRecoveryCodes(r.Context(), p)))
}

// ── remove / regenerate ────────────────────────────────────────────────────

func (e *Endpoint) handle2FARemoveMethod(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	method := chi.URLParam(r, "method")
	if !mfa.ValidMethodType(method) {
		writeJSON(w, http.StatusBadRequest, errBody("INVALID_METHOD", "unknown 2FA method"))
		return
	}
	// Policy guard: a 2FA-required user can't remove their last confirmed factor
	// (a passkey does not satisfy the password path).
	confirmed, err := e.cfg.MFA.ConfirmedMethods(r.Context(), p.ID)
	if err != nil {
		writeServerError(w, "REMOVE_FAILED", "could not load methods")
		return
	}
	if mapping, internal := e.domainPolicy(r.Context(), emailOf(p)); mapping != nil && mapping.Require2FA && internal {
		if lastConfirmedFactor(confirmed, method) {
			writeJSON(w, http.StatusConflict, errBody("LAST_FACTOR",
				"your organisation requires 2FA — add another method before removing this one"))
			return
		}
	}
	if err := e.cfg.MFA.RemoveMethod(r.Context(), p.ID, mfa.MethodType(method)); err != nil {
		writeServerError(w, "REMOVE_FAILED", "could not remove method")
		return
	}
	e.cfg.Notifier.TwoFactorMethodRemoved(r.Context(), emailOf(p), method)
	e.auditMFA(r.Context(), p.ID, "2FA_METHOD_REMOVED")
	writeJSON(w, http.StatusOK, map[string]any{"message": "Two-factor method removed."})
}

func (e *Endpoint) handle2FARegenRecovery(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	codes, err := e.cfg.MFA.GenerateRecoveryCodes(r.Context(), p.ID)
	if err != nil {
		writeServerError(w, "REGEN_FAILED", "could not generate recovery codes")
		return
	}
	e.cfg.Notifier.RecoveryCodesRegenerated(r.Context(), emailOf(p))
	e.auditMFA(r.Context(), p.ID, "2FA_RECOVERY_REGENERATED")
	writeJSON(w, http.StatusOK, recoveryCodesBody(codes))
}

// ── trusted devices ────────────────────────────────────────────────────────

func (e *Endpoint) handle2FAListTrustedDevices(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	devices, err := e.cfg.MFA.ListTrustedDevices(r.Context(), p.ID)
	if err != nil {
		writeServerError(w, "LIST_FAILED", "could not list devices")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (e *Endpoint) handle2FARevokeTrustedDevice(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	id := chi.URLParam(r, "id")
	if err := e.cfg.MFA.RevokeTrustedDevice(r.Context(), p.ID, id); err != nil {
		writeServerError(w, "REVOKE_FAILED", "could not revoke device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Device removed."})
}

// ── small helpers ──────────────────────────────────────────────────────────

// lastConfirmedFactor reports whether removing method would drop the user's
// last confirmed factor.
func lastConfirmedFactor(confirmed []mfa.MethodType, method string) bool {
	remaining := 0
	for _, m := range confirmed {
		if string(m) != method {
			remaining++
		}
	}
	return remaining == 0
}

func recoveryCodesBody(codes []string) map[string]any {
	if codes == nil {
		codes = []string{}
	}
	return map[string]any{"recoveryCodes": codes}
}

func errBody(code, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

func writeServerError(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusInternalServerError, errBody(code, msg))
}
