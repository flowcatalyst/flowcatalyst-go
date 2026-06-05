package mfa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/email"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
)

// Sentinel errors returned by the service. Handlers map these to HTTP statuses.
var (
	// ErrEncryptionUnavailable is returned by TOTP paths when no encryption key
	// is configured (FLOWCATALYST_APP_KEY). Email-PIN paths are unaffected.
	ErrEncryptionUnavailable = errors.New("mfa: encryption not configured (set FLOWCATALYST_APP_KEY)")
	// ErrAlreadyEnrolled is returned when beginning enrollment for a factor the
	// user has already confirmed.
	ErrAlreadyEnrolled = errors.New("mfa: method already enrolled")
	// ErrNoPendingEnrollment is returned when confirming a factor that was never
	// begun (or already confirmed).
	ErrNoPendingEnrollment = errors.New("mfa: no pending enrollment")
)

// Config tunes the service. Use DefaultConfig and override as needed.
type Config struct {
	// Issuer is the label shown in authenticator apps (the otpauth issuer).
	Issuer string
	// EmailPinLength is the number of digits in an email PIN.
	EmailPinLength int
	// EmailPinTTL is how long an email PIN stays valid.
	EmailPinTTL time.Duration
	// EmailPinMaxAttempts is the wrong-guess ceiling before a PIN is burned.
	EmailPinMaxAttempts int
	// RecoveryCodeCount is how many backup codes a batch contains.
	RecoveryCodeCount int
	// TrustedDeviceTTL is the default remember-device lifetime when a domain
	// doesn't specify days.
	TrustedDeviceTTL time.Duration
}

// DefaultConfig returns the agreed defaults (see docs/2fa-implementation-plan.md).
func DefaultConfig() Config {
	return Config{
		Issuer:              "FlowCatalyst",
		EmailPinLength:      6,
		EmailPinTTL:         10 * time.Minute,
		EmailPinMaxAttempts: 5,
		RecoveryCodeCount:   10,
		TrustedDeviceTTL:    30 * 24 * time.Hour,
	}
}

// Service is the 2FA business layer: TOTP + email-PIN enrollment/verification,
// recovery codes, and trusted devices. It is decoupled from the principal
// aggregate — callers pass the user's id and (for email) address.
type Service struct {
	repo *Repository
	enc  *encryption.Service // may be nil → TOTP disabled
	mail email.Service
	cfg  Config
}

// NewService wires the service. enc may be nil (TOTP then returns
// ErrEncryptionUnavailable); mail may be a LogService.
func NewService(repo *Repository, enc *encryption.Service, mail email.Service, cfg Config) *Service {
	return &Service{repo: repo, enc: enc, mail: mail, cfg: cfg}
}

// ── status ───────────────────────────────────────────────────────────────

// ConfirmedMethods returns the user's confirmed factor types (the ones that
// can satisfy a challenge), oldest-first.
func (s *Service) ConfirmedMethods(ctx context.Context, principalID string) ([]MethodType, error) {
	all, err := s.repo.FindMethodsByPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	var out []MethodType
	for _, m := range all {
		if m.IsConfirmed() {
			out = append(out, m.Type)
		}
	}
	return out, nil
}

// HasConfirmedMethod reports whether the user has any confirmed factor.
func (s *Service) HasConfirmedMethod(ctx context.Context, principalID string) (bool, error) {
	methods, err := s.ConfirmedMethods(ctx, principalID)
	if err != nil {
		return false, err
	}
	return len(methods) > 0, nil
}

// ── TOTP enrollment ──────────────────────────────────────────────────────

// TOTPEnrollment is the data the SPA needs to render a setup screen.
type TOTPEnrollment struct {
	// Secret is the base32 shared secret (for manual entry).
	Secret string `json:"secret"`
	// URI is the otpauth:// provisioning URI (rendered as a QR code).
	URI string `json:"uri"`
}

// BeginTOTPEnrollment generates a secret, stores it as an unconfirmed factor
// (encrypted at rest), and returns the provisioning data. Re-running replaces
// a prior *unconfirmed* attempt; a confirmed factor yields ErrAlreadyEnrolled.
func (s *Service) BeginTOTPEnrollment(ctx context.Context, principalID, accountName string) (*TOTPEnrollment, error) {
	if s.enc == nil {
		return nil, ErrEncryptionUnavailable
	}
	existing, err := s.repo.FindMethod(ctx, principalID, MethodTOTP)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.IsConfirmed() {
		return nil, ErrAlreadyEnrolled
	}
	if existing != nil {
		// Replace the stale unconfirmed attempt.
		if err := s.repo.DeleteMethod(ctx, principalID, MethodTOTP); err != nil {
			return nil, err
		}
	}

	key, err := newTOTPKey(s.cfg.Issuer, accountName)
	if err != nil {
		return nil, fmt.Errorf("mfa: generate totp: %w", err)
	}
	enc, err := s.enc.Encrypt(key.Secret())
	if err != nil {
		return nil, fmt.Errorf("mfa: encrypt totp secret: %w", err)
	}
	m := NewMethod(principalID, MethodTOTP)
	m.SecretEncrypted = &enc
	if err := s.repo.InsertMethod(ctx, m); err != nil {
		return nil, err
	}
	return &TOTPEnrollment{Secret: key.Secret(), URI: key.URL()}, nil
}

// ConfirmTOTPEnrollment validates the first code against the pending secret and
// marks the factor confirmed. It stamps last_used_at so the same code can't be
// immediately replayed at login. Returns false on a bad code (factor stays
// unconfirmed).
func (s *Service) ConfirmTOTPEnrollment(ctx context.Context, principalID, code string) (bool, error) {
	if s.enc == nil {
		return false, ErrEncryptionUnavailable
	}
	m, err := s.repo.FindMethod(ctx, principalID, MethodTOTP)
	if err != nil {
		return false, err
	}
	if m == nil || m.SecretEncrypted == nil {
		return false, ErrNoPendingEnrollment
	}
	if m.IsConfirmed() {
		return false, ErrAlreadyEnrolled
	}
	secret, err := s.enc.Decrypt(*m.SecretEncrypted)
	if err != nil {
		return false, fmt.Errorf("mfa: decrypt totp secret: %w", err)
	}
	ok, step := validateTOTP(secret, code, time.Now())
	if !ok {
		return false, nil
	}
	if err := s.repo.ConfirmMethod(ctx, m.ID, time.Now().UTC()); err != nil {
		return false, err
	}
	if err := s.repo.TouchMethodUsed(ctx, m.ID, timeForStep(step)); err != nil {
		return false, err
	}
	return true, nil
}

// ── email-PIN enrollment ─────────────────────────────────────────────────

// BeginEmailEnrollment records an unconfirmed email-PIN factor and emails a
// verification PIN to prove inbox control.
func (s *Service) BeginEmailEnrollment(ctx context.Context, principalID, emailAddr string) error {
	existing, err := s.repo.FindMethod(ctx, principalID, MethodEmailPin)
	if err != nil {
		return err
	}
	if existing != nil && existing.IsConfirmed() {
		return ErrAlreadyEnrolled
	}
	if existing == nil {
		if err := s.repo.InsertMethod(ctx, NewMethod(principalID, MethodEmailPin)); err != nil {
			return err
		}
	}
	return s.issueEmailPin(ctx, principalID, emailAddr, EmailPinEnroll)
}

// ConfirmEmailEnrollment verifies the enrollment PIN and confirms the factor.
func (s *Service) ConfirmEmailEnrollment(ctx context.Context, principalID, code string) (bool, error) {
	ok, err := s.verifyEmailPin(ctx, principalID, code, EmailPinEnroll)
	if err != nil || !ok {
		return false, err
	}
	m, err := s.repo.FindMethod(ctx, principalID, MethodEmailPin)
	if err != nil {
		return false, err
	}
	if m == nil {
		return false, ErrNoPendingEnrollment
	}
	if !m.IsConfirmed() {
		if err := s.repo.ConfirmMethod(ctx, m.ID, time.Now().UTC()); err != nil {
			return false, err
		}
	}
	return true, nil
}

// ── login challenge ──────────────────────────────────────────────────────

// SendLoginEmailPin issues and emails a login-challenge PIN.
func (s *Service) SendLoginEmailPin(ctx context.Context, principalID, emailAddr string) error {
	return s.issueEmailPin(ctx, principalID, emailAddr, EmailPinLogin)
}

// VerifyLoginEmailPin checks a login-challenge PIN.
func (s *Service) VerifyLoginEmailPin(ctx context.Context, principalID, code string) (bool, error) {
	return s.verifyEmailPin(ctx, principalID, code, EmailPinLogin)
}

// VerifyTOTP checks a TOTP code for a confirmed factor, rejecting replay of an
// already-used time-step.
func (s *Service) VerifyTOTP(ctx context.Context, principalID, code string) (bool, error) {
	if s.enc == nil {
		return false, ErrEncryptionUnavailable
	}
	m, err := s.repo.FindMethod(ctx, principalID, MethodTOTP)
	if err != nil {
		return false, err
	}
	if m == nil || !m.IsConfirmed() || m.SecretEncrypted == nil {
		return false, nil
	}
	secret, err := s.enc.Decrypt(*m.SecretEncrypted)
	if err != nil {
		return false, fmt.Errorf("mfa: decrypt totp secret: %w", err)
	}
	ok, step := validateTOTP(secret, code, time.Now())
	if !ok {
		return false, nil
	}
	// Replay guard: reject a step at or before the last accepted one.
	if m.LastUsedAt != nil && step <= stepFromTime(*m.LastUsedAt) {
		return false, nil
	}
	if err := s.repo.TouchMethodUsed(ctx, m.ID, timeForStep(step)); err != nil {
		return false, err
	}
	return true, nil
}

// VerifyRecoveryCode consumes a single-use recovery code. Returns true once,
// then false (the code is burned).
func (s *Service) VerifyRecoveryCode(ctx context.Context, principalID, code string) (bool, error) {
	hash := sha256Hex(normalizeRecoveryCode(code))
	rc, err := s.repo.FindUnusedRecoveryCode(ctx, principalID, hash)
	if err != nil || rc == nil {
		return false, err
	}
	return s.repo.MarkRecoveryCodeUsed(ctx, rc.ID, time.Now().UTC())
}

// ── recovery codes ───────────────────────────────────────────────────────

// GenerateRecoveryCodes replaces the user's recovery-code set with a fresh
// batch and returns the plaintext codes (shown to the user exactly once).
func (s *Service) GenerateRecoveryCodes(ctx context.Context, principalID string) ([]string, error) {
	if err := s.repo.DeleteRecoveryCodes(ctx, principalID); err != nil {
		return nil, err
	}
	plain := make([]string, 0, s.cfg.RecoveryCodeCount)
	rows := make([]*RecoveryCode, 0, s.cfg.RecoveryCodeCount)
	for i := 0; i < s.cfg.RecoveryCodeCount; i++ {
		code, err := randomRecoveryCode()
		if err != nil {
			return nil, err
		}
		plain = append(plain, code)
		rows = append(rows, NewRecoveryCode(principalID, sha256Hex(normalizeRecoveryCode(code))))
	}
	if err := s.repo.InsertRecoveryCodes(ctx, rows); err != nil {
		return nil, err
	}
	return plain, nil
}

// RemainingRecoveryCodes returns how many unused backup codes the user has.
func (s *Service) RemainingRecoveryCodes(ctx context.Context, principalID string) (int, error) {
	return s.repo.CountUnusedRecoveryCodes(ctx, principalID)
}

// ── removal / reset ──────────────────────────────────────────────────────

// RemoveMethod deletes one factor. Policy (e.g. "can't remove your last factor
// in a 2FA-required domain") is enforced by the caller.
func (s *Service) RemoveMethod(ctx context.Context, principalID string, t MethodType) error {
	return s.repo.DeleteMethod(ctx, principalID, t)
}

// ResetAll clears every factor, recovery code, pending PIN and trusted device
// for the user (admin 2FA reset / lost-device recovery).
func (s *Service) ResetAll(ctx context.Context, principalID string) error {
	if err := s.repo.DeleteMethodsByPrincipal(ctx, principalID); err != nil {
		return err
	}
	if err := s.repo.DeleteRecoveryCodes(ctx, principalID); err != nil {
		return err
	}
	if err := s.repo.DeleteEmailPinsByPrincipal(ctx, principalID, EmailPinLogin); err != nil {
		return err
	}
	if err := s.repo.DeleteEmailPinsByPrincipal(ctx, principalID, EmailPinEnroll); err != nil {
		return err
	}
	return s.repo.DeleteTrustedDevicesByPrincipal(ctx, principalID)
}

// ── trusted devices ──────────────────────────────────────────────────────

// IssueTrustedDevice mints a remember-device token, stores its hash, and
// returns the raw token for the caller to set as the __Host-fc_td cookie.
func (s *Service) IssueTrustedDevice(ctx context.Context, principalID string, label *string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = s.cfg.TrustedDeviceTTL
	}
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	d := NewTrustedDevice(principalID, sha256Hex(raw), label, time.Now().UTC().Add(ttl))
	if err := s.repo.InsertTrustedDevice(ctx, d); err != nil {
		return "", err
	}
	return raw, nil
}

// VerifyTrustedDevice reports whether raw matches a non-expired remembered
// device for the user, stamping last_used_at on a hit.
func (s *Service) VerifyTrustedDevice(ctx context.Context, principalID, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	d, err := s.repo.FindValidTrustedDevice(ctx, principalID, sha256Hex(raw))
	if err != nil || d == nil {
		return false, err
	}
	if err := s.repo.TouchTrustedDeviceUsed(ctx, d.ID, time.Now().UTC()); err != nil {
		return false, err
	}
	return true, nil
}

// ListTrustedDevices returns the user's remembered devices for self-service.
func (s *Service) ListTrustedDevices(ctx context.Context, principalID string) ([]TrustedDevice, error) {
	return s.repo.FindTrustedDevicesByPrincipal(ctx, principalID)
}

// RevokeTrustedDevice removes one remembered device (owner-scoped).
func (s *Service) RevokeTrustedDevice(ctx context.Context, principalID, id string) error {
	return s.repo.DeleteTrustedDevice(ctx, principalID, id)
}

// RevokeAllTrustedDevices revokes every remembered device (password change /
// 2FA reset).
func (s *Service) RevokeAllTrustedDevices(ctx context.Context, principalID string) error {
	return s.repo.DeleteTrustedDevicesByPrincipal(ctx, principalID)
}

// ── internal: email PIN ──────────────────────────────────────────────────

// issueEmailPin clears outstanding PINs of the purpose, mints + stores a fresh
// one, and emails it. Email delivery errors propagate (the user needs the PIN).
func (s *Service) issueEmailPin(ctx context.Context, principalID, emailAddr string, purpose EmailPinPurpose) error {
	if err := s.repo.DeleteEmailPinsByPrincipal(ctx, principalID, purpose); err != nil {
		return err
	}
	pin, err := randomDigits(s.cfg.EmailPinLength)
	if err != nil {
		return err
	}
	row := NewEmailPin(principalID, purpose, sha256Hex(pin), time.Now().UTC().Add(s.cfg.EmailPinTTL))
	if err := s.repo.InsertEmailPin(ctx, row); err != nil {
		return err
	}
	return s.mail.Send(ctx, email.Message{
		To:       emailAddr,
		Subject:  "Your verification code",
		HTMLBody: renderEmailPin(pin, s.cfg.EmailPinTTL),
	})
}

// verifyEmailPin checks code against the latest PIN of the purpose. A correct
// code burns the PIN; a wrong code increments attempts and burns the PIN once
// the ceiling is hit. Expired/absent/exhausted all return (false, nil).
func (s *Service) verifyEmailPin(ctx context.Context, principalID, code string, purpose EmailPinPurpose) (bool, error) {
	row, err := s.repo.FindLatestEmailPin(ctx, principalID, purpose)
	if err != nil || row == nil {
		return false, err
	}
	if row.IsExpired() || row.Attempts >= s.cfg.EmailPinMaxAttempts {
		_ = s.repo.DeleteEmailPin(ctx, row.ID) // best-effort cleanup
		return false, nil
	}
	if constantTimeEqual(row.PinHash, sha256Hex(trimPin(code))) {
		if err := s.repo.DeleteEmailPin(ctx, row.ID); err != nil {
			return false, err
		}
		return true, nil
	}
	n, err := s.repo.IncrementEmailPinAttempts(ctx, row.ID)
	if err != nil {
		return false, err
	}
	if n >= s.cfg.EmailPinMaxAttempts {
		_ = s.repo.DeleteEmailPin(ctx, row.ID)
	}
	return false, nil
}
