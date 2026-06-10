// Package sessiontoken is the platform's session-cookie JWT layer.
//
// Split from the OAuth 2.0 surface by design (see ADR-0001): the
// cookie/middleware path is small enough that we own it end-to-end. The
// OAuth endpoints (/oauth/token, /oauth/authorize, /oauth/introspect, …)
// live in auth/oauthapi; session tokens — minted on /auth/login,
// validated by the auth middleware — live here.
//
// Wire format: RS256 JWT with the FlowCatalyst-standard claim shape:
//
//	{
//	  "iss":   <Issuer>,
//	  "sub":   <principal id>,
//	  "iat":   <unix>,
//	  "exp":   <unix>,
//	  "nbf":   <unix>,
//	  "tier":  "ANCHOR" | "PARTNER" | "CLIENT",
//	  "scope": "perm:a:b:c perm:d:e:f"   (space-delimited granted permissions),
//	  "email": "...",
//	  "clients":     [...],
//	  "roles":       [...],
//	  "applications": [...]
//	}
//
// Same claim names + types the auth middleware reads, so session-cookie
// tokens and authservice-minted OAuth tokens are interchangeable to
// downstream consumers. Note: "tier" carries the tenancy tier (formerly the
// "scope" claim) and "scope" now carries OAuth permission scopes — the field
// names changed together across sessiontoken and authservice.
package sessiontoken

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the payload sessiontoken mints + reads. Mirrors the
// fields populated by provider.BuildClaims so callers don't have to
// translate.
type Claims struct {
	Subject      string
	Scope        string
	Email        string
	Clients      []string
	Roles        []string
	Applications []string
	Permissions  []string
	// IssuedAt is the token's `iat` (when it was minted ≈ login time).
	// Zero if the token carried no iat. Used for OIDC max_age enforcement.
	IssuedAt time.Time
}

// Mint signs a JWT with the supplied claims using key. ttl == 0 mints a
// token with no expiry (use only in tests). Negative ttl mints an
// already-expired token (also test-only). Session-cookie callers must
// pass a positive ttl.
func Mint(c Claims, key *rsa.PrivateKey, issuer string, ttl time.Duration) (string, error) {
	if key == nil {
		return "", errors.New("sessiontoken: signing key is nil")
	}
	if c.Subject == "" {
		return "", errors.New("sessiontoken: subject is required")
	}

	now := time.Now().UTC()
	mc := jwt.MapClaims{
		"iss":  issuer,
		"sub":  c.Subject,
		"iat":  now.Unix(),
		"nbf":  now.Unix(),
		"tier": c.Scope, // tenancy tier (ANCHOR|PARTNER|CLIENT)
	}
	if ttl != 0 {
		mc["exp"] = now.Add(ttl).Unix()
	}
	if c.Email != "" {
		mc["email"] = c.Email
	}
	if len(c.Clients) > 0 {
		mc["clients"] = c.Clients
	}
	if len(c.Roles) > 0 {
		mc["roles"] = c.Roles
	}
	if len(c.Applications) > 0 {
		mc["applications"] = c.Applications
	}
	// Granted permissions ride the OAuth "scope" claim as a space-delimited
	// string (the standard scope wire form), not a JSON array.
	if len(c.Permissions) > 0 {
		mc["scope"] = strings.Join(c.Permissions, " ")
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, mc)
	signed, err := tok.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sessiontoken: sign: %w", err)
	}
	return signed, nil
}

// Expect carries the issuer/audience expectations Validate enforces on top
// of signature + time checks. Empty fields skip that check (test
// convenience; production callers must set both).
type Expect struct {
	// Issuer, when set, must equal the token's iss claim exactly.
	Issuer string
	// Audience, when set, is the PLATFORM audience: a token carrying an aud
	// claim must include it. Tokens with NO aud claim pass — session cookies
	// are minted without one.
	//
	// This is the cross-purpose guard: every JWT the platform mints (session
	// cookies, access tokens, OIDC ID tokens for third-party RPs) is signed
	// with the same RSA key, so signature+exp alone would let an ID token —
	// aud = the relying party's client_id, handed to an external service —
	// replay as a full platform bearer for its lifetime. Access tokens carry
	// aud = the platform audience and pass; ID tokens don't, and are
	// rejected here.
	Audience string
}

// Validate verifies the JWT signature + standard claim checks (exp,
// nbf, iat) plus the Expect issuer/audience rules, and returns the parsed
// Claims.
//
// key must be the public half of the key Mint used. Returns a wrapped
// error from jwt.Parse on signature / expiry failures so callers can
// pattern-match via errors.Is(err, jwt.ErrTokenExpired) etc.
func Validate(token string, key *rsa.PublicKey, expect Expect) (*Claims, error) {
	if key == nil {
		return nil, errors.New("sessiontoken: verification key is nil")
	}
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
	)
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("sessiontoken: token is invalid")
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("sessiontoken: unexpected claims type")
	}

	if expect.Issuer != "" {
		if iss := stringClaim(mc, "iss"); iss != expect.Issuer {
			return nil, errors.New("sessiontoken: issuer not accepted")
		}
	}
	if expect.Audience != "" {
		if auds := audienceClaim(mc); len(auds) > 0 && !containsString(auds, expect.Audience) {
			return nil, errors.New("sessiontoken: audience not accepted (not a platform token)")
		}
	}

	out := &Claims{
		Subject:      stringClaim(mc, "sub"),
		Scope:        stringClaim(mc, "tier"), // tenancy tier
		Email:        stringClaim(mc, "email"),
		Clients:      stringSliceClaim(mc, "clients"),
		Roles:        stringSliceClaim(mc, "roles"),
		Applications: stringSliceClaim(mc, "applications"),
		// Granted permissions arrive on the space-delimited "scope" claim.
		Permissions: strings.Fields(stringClaim(mc, "scope")),
		IssuedAt:    unixClaim(mc, "iat"),
	}
	if out.Subject == "" {
		return nil, errors.New("sessiontoken: token is missing sub claim")
	}
	return out, nil
}

func stringClaim(mc jwt.MapClaims, key string) string {
	if v, ok := mc[key].(string); ok {
		return v
	}
	return ""
}

// audienceClaim reads the aud claim in either RFC 7519 wire form: a bare
// string (what the platform mints) or an array of strings.
func audienceClaim(mc jwt.MapClaims) []string {
	switch v := mc["aud"].(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// unixClaim reads a numeric Unix-seconds claim. JWT numeric claims
// round-trip through JSON as float64. Returns the zero time if absent.
func unixClaim(mc jwt.MapClaims, key string) time.Time {
	switch v := mc[key].(type) {
	case float64:
		return time.Unix(int64(v), 0).UTC()
	case int64:
		return time.Unix(v, 0).UTC()
	default:
		return time.Time{}
	}
}

// stringSliceClaim coerces a claim into []string. Tokens we mint emit
// []string; tokens round-tripped through JSON arrive as []any with
// string elements (this is the standard JWT MapClaims quirk).
func stringSliceClaim(mc jwt.MapClaims, key string) []string {
	v, ok := mc[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []interface{}:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
