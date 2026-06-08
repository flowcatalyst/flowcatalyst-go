package login

import (
	"net/http"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/passwordhash"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/mfa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
)

// handleChangePassword lets an authenticated internal user change their own
// password from the Profile screen. It verifies the current password and — when
// the user has 2FA enrolled — a current second factor too, so a password change
// carries the same assurance as the user's configured 2FA posture (neither a
// hijacked session nor a known password alone is enough). OIDC / passwordless
// accounts have no password to change here.
func (e *Endpoint) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		Code            string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if p.UserIdentity == nil || p.UserIdentity.PasswordHash == nil {
		writeJSON(w, http.StatusBadRequest, errBody("NO_PASSWORD", "This account signs in without a password."))
		return
	}
	if err := passwordhash.Verify(req.CurrentPassword, *p.UserIdentity.PasswordHash); err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody("INVALID_CURRENT_PASSWORD", "Your current password is incorrect."))
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, errBody("WEAK_PASSWORD", "New password must be at least 8 characters."))
		return
	}

	// Respect the user's 2FA settings: any confirmed factor means a valid second
	// factor is required to change the password. The frontend first submits with
	// no code; we answer MFA_REQUIRED (+ the user's methods) so it can collect a
	// code, then resubmit.
	if e.cfg.MFA != nil {
		confirmed, err := e.cfg.MFA.ConfirmedMethods(r.Context(), p.ID)
		if err != nil {
			writeServerError(w, "MFA_STATUS_FAILED", "could not check two-factor status")
			return
		}
		if len(confirmed) > 0 {
			if req.Code == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"code":    "MFA_REQUIRED",
					"message": "Enter a code from your second factor to change your password.",
					"methods": methodStrings(confirmed),
				})
				return
			}
			if !e.verifyAnySecondFactor(r, p, confirmed, req.Code) {
				writeJSON(w, http.StatusBadRequest, errBody("INVALID_CODE", "That code didn't match — try again."))
				return
			}
		}
	}

	newHash, err := passwordhash.Hash(req.NewPassword)
	if err != nil {
		writeServerError(w, "HASH_FAILED", "could not set the new password")
		return
	}
	if err := e.cfg.Principals.UpdatePasswordHash(r.Context(), p.ID, newHash); err != nil {
		writeServerError(w, "UPDATE_FAILED", "could not save the new password")
		return
	}
	if e.cfg.Notifier != nil {
		e.cfg.Notifier.PasswordChanged(r.Context(), emailOf(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Your password has been changed."})
}

// verifyAnySecondFactor accepts a code matching any of the user's confirmed
// factors (TOTP or email PIN), or a valid recovery code (which backs TOTP) — so
// the user can present whichever factor they have, mirroring the login verify.
func (e *Endpoint) verifyAnySecondFactor(r *http.Request, p *principal.Principal, confirmed []mfa.MethodType, code string) bool {
	ctx := r.Context()
	for _, m := range confirmed {
		switch m {
		case mfa.MethodTOTP:
			if ok, _ := e.cfg.MFA.VerifyTOTP(ctx, p.ID, code); ok {
				return true
			}
		case mfa.MethodEmailPin:
			if ok, _ := e.cfg.MFA.VerifyLoginEmailPin(ctx, p.ID, code); ok {
				return true
			}
		}
	}
	if containsMethodType(confirmed, mfa.MethodTOTP) {
		if ok, _ := e.cfg.MFA.VerifyRecoveryCode(ctx, p.ID, code); ok {
			return true
		}
	}
	return false
}

// handleChangePasswordSendEmailCode sends an email 2FA PIN so a user whose
// second factor is email can complete a password change. Rejected when email
// isn't one of their confirmed factors.
func (e *Endpoint) handleChangePasswordSendEmailCode(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	if e.cfg.MFA == nil {
		writeJSON(w, http.StatusBadRequest, errBody("NO_MFA", "two-factor is not enabled"))
		return
	}
	confirmed, err := e.cfg.MFA.ConfirmedMethods(r.Context(), p.ID)
	if err != nil {
		writeServerError(w, "MFA_STATUS_FAILED", "could not check two-factor status")
		return
	}
	if !containsMethodType(confirmed, mfa.MethodEmailPin) {
		writeJSON(w, http.StatusBadRequest, errBody("NO_EMAIL_2FA", "email codes are not enabled for your account"))
		return
	}
	email := emailOf(p)
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errBody("NO_EMAIL", "account has no email"))
		return
	}
	if err := e.cfg.MFA.SendLoginEmailPin(r.Context(), p.ID, email); err != nil {
		writeServerError(w, "SEND_FAILED", "could not send the code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "A code has been sent to your email."})
}
