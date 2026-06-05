// Package tsid is the FlowCatalyst platform's typed-entity overlay on
// top of the SDK's Crockford Base32 TSID primitives
// (pkg/fcsdk/tsid). The primitives — GenerateRaw, GenerateUntyped,
// encodeCrockford, decodeCrockford, ToLong, FromLong — live in the SDK
// so consumer apps can mint and validate IDs in the same format the
// platform issues. This package adds the FlowCatalyst-specific
// EntityType catalog + its three-character prefixes (Client → "clt",
// Principal → "prn", etc.) — domain knowledge that does not belong in
// the generic SDK module.
//
// Layout (unchanged, defined in pkg/fcsdk/tsid):
//
//	bits 63..22  timestamp (42 bits, millis since epoch)
//	bits 21..12  random  (10 bits)
//	bits 11..0   counter (12 bits)
package tsid

import (
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/tsid"
)

// EntityType is a strongly-typed enum of platform-known entity prefixes.
// Matches the Rust EntityType exactly.
type EntityType int

const (
	Client EntityType = iota
	Principal
	Application
	ServiceAccount
	Role
	Permission
	OAuthClient
	AuthCode
	LoginAttempt
	ClientAuthConfig
	AppClientConfig
	IdpRoleMapping
	CorsOrigin
	AnchorDomain
	IdentityProvider
	EmailDomainMapping
	ClientAccessGrant
	EventType
	Event
	EventRead
	Connection
	Subscription
	DispatchPool
	DispatchJob
	DispatchJobRead
	Schema
	AuditLog
	PlatformConfig
	ConfigAccess
	PasswordResetToken
	WebauthnCredential
	ScheduledJob
	ScheduledJobInstance
	ScheduledJobInstanceLog
	ApplicationOpenApiSpec
	Process
	OAuthAccessToken
	OAuthRefreshToken
	// 2FA / MFA entities. Go-only (not present in the Rust EntityType) —
	// they back a net-new feature. Appended at the end so existing prefixes
	// keep their iota positions. See docs/2fa-implementation-plan.md.
	MfaMethod
	MfaRecoveryCode
	MfaEmailPin
	MfaTrustedDevice
)

// Prefix returns the 3-character prefix for this entity type. Mirrors
// the Rust EntityType::prefix() exactly.
func (e EntityType) Prefix() string {
	switch e {
	case Client:
		return "clt"
	case Principal:
		return "prn"
	case Application:
		return "app"
	case ServiceAccount:
		return "sac"
	case Role:
		return "rol"
	case Permission:
		return "prm"
	case OAuthClient:
		return "oac"
	case AuthCode:
		return "acd"
	case LoginAttempt:
		return "lat"
	case ClientAuthConfig:
		return "cac"
	case AppClientConfig:
		return "apc"
	case IdpRoleMapping:
		return "irm"
	case CorsOrigin:
		return "cor"
	case AnchorDomain:
		return "anc"
	case IdentityProvider:
		return "idp"
	case EmailDomainMapping:
		return "edm"
	case ClientAccessGrant:
		return "gnt"
	case EventType:
		return "evt"
	case Event:
		return "evn"
	case EventRead:
		return "evr"
	case Connection:
		return "con"
	case Subscription:
		return "sub"
	case DispatchPool:
		return "dpl"
	case DispatchJob:
		return "djb"
	case DispatchJobRead:
		return "djr"
	case Schema:
		return "sch"
	case AuditLog:
		return "aud"
	case PlatformConfig:
		return "pcf"
	case ConfigAccess:
		return "cfa"
	case PasswordResetToken:
		return "prt"
	case WebauthnCredential:
		return "pkc"
	case ScheduledJob:
		return "sjb"
	case ScheduledJobInstance:
		return "sji"
	case ScheduledJobInstanceLog:
		return "sjl"
	case ApplicationOpenApiSpec:
		return "oas"
	case Process:
		return "prc"
	case OAuthAccessToken:
		return "oat"
	case OAuthRefreshToken:
		return "ort"
	case MfaMethod:
		return "mfm"
	case MfaRecoveryCode:
		return "mrc"
	case MfaEmailPin:
		return "mep"
	case MfaTrustedDevice:
		return "mtd"
	default:
		return "unk"
	}
}

// Generate returns a typed TSID: "{prefix}_{raw}".
func Generate(e EntityType) string {
	return tsid.GenerateWithPrefix(e.Prefix())
}

// The following are re-exports of the SDK primitives so existing
// internal callers continue to compile without churn. Prefer the SDK
// package (pkg/fcsdk/tsid) directly in new code.

// GenerateWithPrefix returns a typed TSID with a custom prefix.
func GenerateWithPrefix(prefix string) string { return tsid.GenerateWithPrefix(prefix) }

// GenerateUntyped returns a raw 13-character TSID with no prefix.
func GenerateUntyped() string { return tsid.GenerateUntyped() }

// GenerateRaw produces the 13-character Crockford Base32 TSID.
func GenerateRaw() string { return tsid.GenerateRaw() }

// ToLong converts a TSID string (typed or raw) to its numeric form.
func ToLong(s string) (int64, bool) { return tsid.ToLong(s) }

// FromLong converts a numeric TSID to its raw string form (no prefix).
func FromLong(v int64) string { return tsid.FromLong(v) }
