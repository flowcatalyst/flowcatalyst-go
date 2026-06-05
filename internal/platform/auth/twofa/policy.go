// Package twofa holds the shared 2FA domain-policy evaluation used by both the
// login flow and the password-reset/invite flow, so the "does this user's
// domain require 2FA, and with which methods" decision lives in exactly one
// place.
package twofa

import (
	"context"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider"
)

// Policy resolves a domain's 2FA stance from the email-domain mapping + the
// linked identity provider's type.
type Policy struct {
	Mappings *emaildomainmapping.Repository
	IDPs     *identityprovider.Repository
}

// Eval is the resolved stance for one email's domain.
type Eval struct {
	// Mapping is the email-domain mapping (nil when the domain is unmapped).
	Mapping *emaildomainmapping.EmailDomainMapping
	// Internal reports whether the domain authenticates internally (an unmapped
	// or non-OIDC domain). 2FA only ever applies to internal domains.
	Internal bool
}

// Evaluate looks up the mapping + IdP type for email's domain. An unmapped
// domain is treated as internal with no policy.
func (p Policy) Evaluate(ctx context.Context, email string) Eval {
	at := strings.IndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return Eval{Internal: true}
	}
	domain := strings.ToLower(email[at+1:])
	edm, err := p.Mappings.FindByEmailDomain(ctx, domain)
	if err != nil || edm == nil {
		return Eval{Internal: true}
	}
	internal := true
	if p.IDPs != nil {
		if idp, err := p.IDPs.FindByID(ctx, edm.IdentityProviderID); err == nil && idp != nil {
			internal = idp.Type != identityprovider.TypeOIDC
		}
	}
	return Eval{Mapping: edm, Internal: internal}
}

// Requires2FA reports whether the domain compels a second factor for internal
// (password) sign-in.
func (e Eval) Requires2FA() bool {
	return e.Mapping != nil && e.Mapping.Require2FA && e.Internal
}

// AllowedMethods is the permitted second-factor set (nil when unmapped).
func (e Eval) AllowedMethods() []string {
	if e.Mapping == nil {
		return nil
	}
	return e.Mapping.Allowed2FAMethods
}

// RememberEnabled reports whether the domain offers remember-this-device.
func (e Eval) RememberEnabled() bool {
	return e.Mapping != nil && e.Mapping.RememberDeviceEnabled && e.Internal
}

// RememberDays is the remember-device lifetime in days (default 30).
func (e Eval) RememberDays() int {
	if e.Mapping == nil || e.Mapping.RememberDeviceDays <= 0 {
		return 30
	}
	return e.Mapping.RememberDeviceDays
}
