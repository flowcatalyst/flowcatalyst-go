package auth

import (
	"encoding/json"
	"fmt"
)

// Audience is a JWT aud claim. RFC 7519 allows aud to be either a
// string or an array of strings. FlowCatalyst-minted tokens emit a
// single string (matching the Rust SDK), but tokens minted with other
// tooling (e.g. jwx itself) emit an array. We accept both forms on
// unmarshal and always marshal as a single string for wire parity
// with Rust.
type Audience string

// UnmarshalJSON accepts either a string or a one-element string array.
// Multi-element arrays take the first entry, matching how the Rust SDK
// would behave if aud were typed as String.
func (a *Audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = Audience(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		if len(arr) == 0 {
			*a = ""
		} else {
			*a = Audience(arr[0])
		}
		return nil
	}
	return fmt.Errorf("aud claim must be a string or array of strings, got %s", b)
}

func (a Audience) String() string { return string(a) }

// AccessTokenClaims is the JWT payload shape issued by FlowCatalyst's
// /oauth/token endpoint. JSON tags match the Rust SDK byte-for-byte so
// the same token deserialises identically across SDKs.
type AccessTokenClaims struct {
	// Sub is the principal id, e.g. "prn_0HZXEQ5Y8JY5Z".
	Sub string `json:"sub"`
	// Iss is the issuer URL.
	Iss string `json:"iss"`
	// Aud is the audience. Accepts string or []string on the wire.
	Aud Audience `json:"aud"`
	// Exp is the expiration time (Unix seconds).
	Exp int64 `json:"exp"`
	// Iat is the issued-at time (Unix seconds).
	Iat int64 `json:"iat"`
	// Nbf is the not-before time (Unix seconds).
	Nbf int64 `json:"nbf"`
	// Jti is the JWT id.
	Jti string `json:"jti"`
	// PrincipalType is "USER" or "SERVICE". The wire field name is
	// "type" to match the Rust SDK's serde rename.
	PrincipalType string `json:"type"`
	// Scope is "ANCHOR", "PARTNER", or "CLIENT".
	Scope string `json:"scope"`
	// Email is present for USER principals, absent for SERVICE.
	Email string `json:"email,omitempty"`
	// Name is the display name.
	Name string `json:"name"`
	// Clients are the client ids this principal can access. ["*"] for
	// anchor users.
	Clients []string `json:"clients"`
	// Roles are the role codes assigned to this principal.
	Roles []string `json:"roles,omitempty"`
	// Applications are the application codes derived from roles, e.g.
	// "operant" from "operant:admin". Always present on tokens issued
	// by FC but absent on older tokens.
	Applications []string `json:"applications,omitempty"`
}

// HasClientAccess reports whether the principal can access the given
// client. Anchor principals (clients == ["*"]) always return true.
func (c *AccessTokenClaims) HasClientAccess(clientID string) bool {
	for _, id := range c.Clients {
		if id == "*" || id == clientID {
			return true
		}
	}
	return false
}

// HasRole reports whether the principal has the given role code.
func (c *AccessTokenClaims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsAnchor reports whether this is an anchor (platform-wide) principal.
func (c *AccessTokenClaims) IsAnchor() bool { return c.Scope == "ANCHOR" }

// IsService reports whether this is a service account.
func (c *AccessTokenClaims) IsService() bool { return c.PrincipalType == "SERVICE" }

// PrincipalID returns the Sub claim.
func (c *AccessTokenClaims) PrincipalID() string { return c.Sub }

// AuthContext is a validated token plus the raw JWT, ready for
// authorization checks and forwarding to downstream services.
type AuthContext struct {
	Claims AccessTokenClaims
	Token  string
}

// NewAuthContext constructs an AuthContext.
func NewAuthContext(claims AccessTokenClaims, token string) *AuthContext {
	return &AuthContext{Claims: claims, Token: token}
}

func (a *AuthContext) PrincipalID() string { return a.Claims.Sub }
func (a *AuthContext) Email() string       { return a.Claims.Email }
func (a *AuthContext) Name() string        { return a.Claims.Name }
func (a *AuthContext) IsAnchor() bool      { return a.Claims.IsAnchor() }
func (a *AuthContext) IsService() bool     { return a.Claims.IsService() }
func (a *AuthContext) HasClientAccess(clientID string) bool {
	return a.Claims.HasClientAccess(clientID)
}
func (a *AuthContext) HasRole(role string) bool { return a.Claims.HasRole(role) }
func (a *AuthContext) ClientIDs() []string      { return a.Claims.Clients }
func (a *AuthContext) Roles() []string          { return a.Claims.Roles }

// BearerToken returns the raw JWT for forwarding to downstream services.
func (a *AuthContext) BearerToken() string { return a.Token }
