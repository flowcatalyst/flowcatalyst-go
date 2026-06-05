// Package emaildomainmapping is the port of fc-platform/src/email_domain_mapping.
// Maps email domains to IDPs at signup; carries optional client + role
// grants applied to users who sign up via that domain.
package emaildomainmapping

import (
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// ScopeType is the scope at which a mapping operates.
type ScopeType string

const (
	ScopeAnchor  ScopeType = "ANCHOR"
	ScopePartner ScopeType = "PARTNER"
	ScopeClient  ScopeType = "CLIENT"
)

// ParseScopeType is the lenient parser. Unknown → ANCHOR.
func ParseScopeType(s string) ScopeType {
	switch s {
	case string(ScopePartner):
		return ScopePartner
	case string(ScopeClient):
		return ScopeClient
	default:
		return ScopeAnchor
	}
}

// MFAMethod is a second-factor mechanism a domain may permit.
type MFAMethod string

const (
	// MFATOTP is a virtual-device authenticator (RFC 6238 TOTP).
	MFATOTP MFAMethod = "TOTP"
	// MFAEmailPin is a one-time numeric PIN delivered by email.
	MFAEmailPin MFAMethod = "EMAIL_PIN"
)

// ValidMFAMethod reports whether s is a known second-factor mechanism.
func ValidMFAMethod(s string) bool {
	switch MFAMethod(s) {
	case MFATOTP, MFAEmailPin:
		return true
	default:
		return false
	}
}

// EmailDomainMapping is the aggregate root.
type EmailDomainMapping struct {
	ID                   string    `json:"id"`
	EmailDomain          string    `json:"emailDomain"`
	IdentityProviderID   string    `json:"identityProviderId"`
	ScopeType            ScopeType `json:"scopeType"`
	PrimaryClientID      *string   `json:"primaryClientId,omitempty"`
	AdditionalClientIDs  []string  `json:"additionalClientIds"`
	GrantedClientIDs     []string  `json:"grantedClientIds"`
	RequiredOIDCTenantID *string   `json:"requiredOidcTenantId,omitempty"`
	AllowedRoleIDs       []string  `json:"allowedRoleIds"`
	SyncRolesFromIDP     bool      `json:"syncRolesFromIdp"`
	// 2FA enforcement (internal-auth domains only; inert for OIDC domains).
	Require2FA bool `json:"require2fa"`
	// Allowed2FAMethods is the set of permitted mechanisms ("TOTP",
	// "EMAIL_PIN"). Must be non-empty when Require2FA is true.
	Allowed2FAMethods []string `json:"allowed2faMethods"`
	// RememberDeviceEnabled lets users skip the challenge on a remembered
	// browser for RememberDeviceDays. Only meaningful when Require2FA.
	RememberDeviceEnabled bool      `json:"rememberDeviceEnabled"`
	RememberDeviceDays    int       `json:"rememberDeviceDays"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (e EmailDomainMapping) IDStr() string { return e.ID }

// New constructs a mapping with a fresh TSID and empty junction slices.
func New(emailDomain, identityProviderID string, scope ScopeType) *EmailDomainMapping {
	now := time.Now().UTC()
	return &EmailDomainMapping{
		ID:                  tsid.Generate(tsid.EmailDomainMapping),
		EmailDomain:         emailDomain,
		IdentityProviderID:  identityProviderID,
		ScopeType:           scope,
		AdditionalClientIDs: []string{},
		GrantedClientIDs:    []string{},
		AllowedRoleIDs:      []string{},
		Allowed2FAMethods:   []string{},
		RememberDeviceDays:  30,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}
