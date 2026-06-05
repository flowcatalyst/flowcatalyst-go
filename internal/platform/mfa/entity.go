// Package mfa is the data layer for two-factor authentication of internal
// users: enrolled factors (TOTP / email PIN), single-use recovery codes,
// pending email-PIN challenges, and remembered ("trusted") devices.
//
// Federated (OIDC) users never have rows here — enforcement is gated at the
// application layer by the email-domain mapping, same as webauthn_credentials.
// See docs/2fa-implementation-plan.md.
package mfa

import (
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// MethodType is an enrolled second-factor mechanism.
type MethodType string

const (
	// MethodTOTP is a virtual-device authenticator (RFC 6238 TOTP).
	MethodTOTP MethodType = "TOTP"
	// MethodEmailPin is a one-time numeric PIN delivered by email.
	MethodEmailPin MethodType = "EMAIL_PIN"
)

// ValidMethodType reports whether s is a known second-factor mechanism.
func ValidMethodType(s string) bool {
	switch MethodType(s) {
	case MethodTOTP, MethodEmailPin:
		return true
	default:
		return false
	}
}

// Method is a user's enrolled second factor.
//
// SecretEncrypted holds the AES-256-GCM envelope of the TOTP shared secret
// (nil for EMAIL_PIN, which uses the user's email). ConfirmedAt is nil until
// the user verifies a first code; an unconfirmed Method is a pending
// enrollment and never satisfies a challenge.
type Method struct {
	ID              string     `json:"id"`
	PrincipalID     string     `json:"principalId"`
	Type            MethodType `json:"type"`
	SecretEncrypted *string    `json:"-"`
	ConfirmedAt     *time.Time `json:"confirmedAt,omitempty"`
	LastUsedAt      *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// NewMethod builds an unconfirmed factor of the given type.
func NewMethod(principalID string, t MethodType) *Method {
	return &Method{
		ID:          tsid.Generate(tsid.MfaMethod),
		PrincipalID: principalID,
		Type:        t,
		CreatedAt:   time.Now().UTC(),
	}
}

// IsConfirmed reports whether the factor has completed enrollment.
func (m *Method) IsConfirmed() bool { return m.ConfirmedAt != nil }

// RecoveryCode is a single-use backup code. Only the SHA-256 hash of the
// printable code is stored; UsedAt is stamped on redemption.
type RecoveryCode struct {
	ID          string     `json:"id"`
	PrincipalID string     `json:"principalId"`
	CodeHash    string     `json:"-"`
	UsedAt      *time.Time `json:"usedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// NewRecoveryCode builds a recovery-code row from a pre-computed hash.
func NewRecoveryCode(principalID, codeHash string) *RecoveryCode {
	return &RecoveryCode{
		ID:          tsid.Generate(tsid.MfaRecoveryCode),
		PrincipalID: principalID,
		CodeHash:    codeHash,
		CreatedAt:   time.Now().UTC(),
	}
}

// EmailPinPurpose distinguishes a login challenge from an enrollment
// inbox-control check.
type EmailPinPurpose string

const (
	// EmailPinLogin is a second-factor challenge during sign-in.
	EmailPinLogin EmailPinPurpose = "login"
	// EmailPinEnroll proves inbox control while enrolling the email factor.
	EmailPinEnroll EmailPinPurpose = "enroll"
)

// EmailPin is a pending email-PIN challenge. Only the SHA-256 hash of the
// numeric PIN is stored.
type EmailPin struct {
	ID          string          `json:"id"`
	PrincipalID string          `json:"principalId"`
	Purpose     EmailPinPurpose `json:"purpose"`
	PinHash     string          `json:"-"`
	Attempts    int             `json:"attempts"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// NewEmailPin builds a pending email-PIN challenge from a pre-computed hash.
func NewEmailPin(principalID string, purpose EmailPinPurpose, pinHash string, expiresAt time.Time) *EmailPin {
	return &EmailPin{
		ID:          tsid.Generate(tsid.MfaEmailPin),
		PrincipalID: principalID,
		Purpose:     purpose,
		PinHash:     pinHash,
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now().UTC(),
	}
}

// IsExpired reports whether the challenge's expiry has passed.
func (p *EmailPin) IsExpired() bool { return time.Now().After(p.ExpiresAt) }

// TrustedDevice is a remembered browser that may skip the 2FA challenge until
// expiry. Only the SHA-256 hash of the cookie token is stored, so a DB read
// cannot mint a cookie.
type TrustedDevice struct {
	ID          string     `json:"id"`
	PrincipalID string     `json:"principalId"`
	TokenHash   string     `json:"-"`
	Label       *string    `json:"label,omitempty"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
}

// NewTrustedDevice builds a remembered-device row from a pre-computed token
// hash.
func NewTrustedDevice(principalID, tokenHash string, label *string, expiresAt time.Time) *TrustedDevice {
	return &TrustedDevice{
		ID:          tsid.Generate(tsid.MfaTrustedDevice),
		PrincipalID: principalID,
		TokenHash:   tokenHash,
		Label:       label,
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now().UTC(),
	}
}

// IsExpired reports whether the remembered device has expired.
func (d *TrustedDevice) IsExpired() bool { return time.Now().After(d.ExpiresAt) }
