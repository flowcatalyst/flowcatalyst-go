// Package provider hosts the JWT claims projection and session-cookie
// token helpers shared across FlowCatalyst's auth surfaces. The OAuth/OIDC
// HTTP endpoints are hand-rolled in internal/platform/auth/oauthapi
// (token/authorize/introspect/revoke/userinfo + .well-known), backed by
// authservice (token mint/validate) and grantstore (codes/refresh).
//
// What remains here:
//
//	Config              — issuer + signing key + access-token TTL
//	Claims, BuildClaims — project a principal onto the JWT claim shape
//	FlattenPermissions  — resolve role names → permission set
//	Mint/ValidateSessionToken — /auth/login session cookies
//	SigningKey, Issuer, AccessTokenTTL — shared config accessors
package provider

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/sessiontoken"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
)

// Config bundles the construction-time settings for the auth provider.
type Config struct {
	// Issuer is the JWT iss claim, e.g. "https://flowcatalyst.example.com".
	Issuer string

	// AccessTokenTTL is how long access tokens are valid.
	AccessTokenTTL time.Duration

	// SigningKey is the RS256 private key used to sign JWTs. PEM-encoded.
	SigningKey []byte
}

// Claims is the FlowCatalyst-specific JWT payload. These fields are
// projected onto the wire token by the token-issuing layer (authservice
// for /oauth/token, sessiontoken for /auth/login cookies). Keep names in
// sync with what SDK consumers expect.
type Claims struct {
	Issuer       string
	Subject      string
	Audience     string
	Scope        string   // "ANCHOR" | "PARTNER" | "CLIENT"
	Clients      []string // tenant IDs accessible
	Roles        []string
	Applications []string
	Permissions  []string // de-duplicated, flattened from Roles
	Email        string
	Name         string // user display name (OIDC "name" claim)

	// OIDC ID-token-specific claims. These are populated only when the
	// caller is minting an ID token, not a plain access token. Rust's
	// auth_service.rs ships them on the ID token; we match the same set
	// (nonce, azp, auth_time, email_verified). acr/amr are not populated
	// by Rust either, so we leave them off.
	Nonce           string // OIDC nonce echoed from the authorize request
	AuthorizedParty string // OIDC "azp" — typically the client_id
	AuthTime        int64  // OIDC "auth_time" — Unix seconds
	EmailVerified   *bool  // OIDC "email_verified" — pointer to distinguish "unset"
}

// BuildClaims projects a principal onto our Claims shape. roles may be
// nil — in that case Permissions is left empty (handlers without
// permission gates still work, gated handlers reject with
// PERMISSION_REQUIRED).
func BuildClaims(ctx context.Context, cfg Config, principals *principal.Repository, roles *role.Repository, principalID string) (*Claims, error) {
	p, err := principals.FindByID(ctx, principalID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("principal not found")
	}
	if !p.Active {
		return nil, errors.New("principal is deactivated")
	}
	roleNames := make([]string, 0, len(p.Roles))
	for _, ra := range p.Roles {
		roleNames = append(roleNames, ra.Role)
	}
	clients := append([]string(nil), p.AssignedClients...)
	if p.ClientID != nil && *p.ClientID != "" {
		clients = append(clients, *p.ClientID)
	}
	apps := append([]string(nil), p.AccessibleApplicationIDs...)
	email := ""
	if p.UserIdentity != nil {
		email = p.UserIdentity.Email
	}
	perms, err := flattenPermissions(ctx, roles, roleNames)
	if err != nil {
		return nil, fmt.Errorf("flatten permissions: %w", err)
	}
	return &Claims{
		Issuer:       cfg.Issuer,
		Subject:      p.ID,
		Scope:        string(p.Scope),
		Clients:      clients,
		Roles:        roleNames,
		Applications: apps,
		Permissions:  perms,
		Email:        email,
		Name:         p.Name,
	}, nil
}

// FlattenPermissions resolves a principal's role names into their
// de-duplicated permission set. The auth middleware calls this to derive
// permissions for tokens that carry roles but no permissions claim
// (e.g. OAuth access tokens minted by authservice, matching Rust which
// never bakes permissions into the JWT).
func (p *Provider) FlattenPermissions(ctx context.Context, roleNames []string) ([]string, error) {
	return flattenPermissions(ctx, p.roles, roleNames)
}

// flattenPermissions looks up each role by name and concatenates its
// permissions, de-duplicated. Skips roles the repo can't find (a known
// role was deleted out from under the principal) — the principal keeps
// whatever permissions the remaining roles grant.
func flattenPermissions(ctx context.Context, roles *role.Repository, roleNames []string) ([]string, error) {
	if roles == nil || len(roleNames) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, name := range roleNames {
		r, err := roles.FindByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if r == nil {
			continue
		}
		for _, p := range r.Permissions {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out, nil
}

// Provider bundles the session/claims helpers plus the deps the HTTP
// layer needs (principal + role repos for BuildClaims, config for TTLs).
type Provider struct {
	cfg        Config
	principals *principal.Repository
	roles      *role.Repository
	// signingKey is the RSA key shared with the sessiontoken package (for
	// /auth/login cookies) and authservice (for /oauth/token JWTs) — all
	// three sign with the same pair so JWKS + cookie validation line up.
	signingKey *rsa.PrivateKey
}

// NewProvider parses the RSA signing key and wires the claims/session
// helpers. Returns an error if the RSA key is missing or malformed.
func NewProvider(cfg Config, principals *principal.Repository, roles *role.Repository) (*Provider, error) {
	key, err := parseRSAPrivateKey(cfg.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("auth provider: %w", err)
	}
	if cfg.AccessTokenTTL == 0 {
		cfg.AccessTokenTTL = 1 * time.Hour
	}
	return &Provider{
		cfg:        cfg,
		signingKey: key,
		principals: principals,
		roles:      roles,
	}, nil
}

// SigningKey exposes the RSA private key the provider was constructed
// with. Used by sessiontoken-aware callers (the auth middleware, the
// /auth/login handler) to share the same key pair token issuance uses.
func (p *Provider) SigningKey() *rsa.PrivateKey { return p.signingKey }

// Issuer returns the configured JWT issuer claim.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// AccessTokenTTL returns the configured access-token lifetime.
func (p *Provider) AccessTokenTTL() time.Duration { return p.cfg.AccessTokenTTL }

// ResolveClaims is the exported BuildClaims wrapper bound to this
// provider's repos + config. Used by callers that need the flattened
// claim set for a principal (e.g. /auth/login's response body, which
// includes the `permissions` list so the SPA's route guards can run).
func (p *Provider) ResolveClaims(ctx context.Context, principalID string) (*Claims, error) {
	return BuildClaims(ctx, p.cfg, p.principals, p.roles, principalID)
}

// MintSessionToken issues a self-contained JWT access token for the
// supplied principal. Used by /auth/login: the resulting token is set
// as the session cookie. Backed by the sessiontoken package directly, so
// the claim shape is exactly what we put in.
//
// ttl=0 falls back to AccessTokenTTL.
func (p *Provider) MintSessionToken(ctx context.Context, principalID string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = p.cfg.AccessTokenTTL
	}
	c, err := BuildClaims(ctx, p.cfg, p.principals, p.roles, principalID)
	if err != nil {
		return "", fmt.Errorf("build claims: %w", err)
	}
	// The session cookie carries ONLY stable identity (subject + email). All
	// mutable authorization data — scope, roles, clients, applications,
	// permissions — is intentionally left out and resolved fresh from the DB on
	// every request by the auth middleware (see middleware.introspect). Two
	// reasons:
	//   1. Correctness: a signed cookie can't be updated until re-login, so
	//      baking in roles/permissions would serve stale authz after a change.
	//   2. Size: the flattened permission set for a privileged principal can
	//      push the JWT past the browser's ~4KB per-cookie limit, making the
	//      browser silently DROP fc_session so the session never establishes.
	return sessiontoken.Mint(sessiontoken.Claims{
		Subject: c.Subject,
		Email:   c.Email,
	}, p.signingKey, p.cfg.Issuer, ttl)
}

// ValidateSessionToken verifies a session-cookie JWT (signature + std
// claim checks) and returns the parsed claims. Used by the platform's
// auth middleware to verify both Authorization: Bearer tokens and
// fc_session cookies. Both transports carry tokens signed with the same
// RSA key (sessiontoken for cookies, authservice for /oauth/token), so
// the signature path lines up.
func (p *Provider) ValidateSessionToken(_ context.Context, token string) (*sessiontoken.Claims, error) {
	return sessiontoken.Validate(token, &p.signingKey.PublicKey)
}

// parseRSAPrivateKey accepts PKCS#1 or PKCS#8 PEM blocks.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	if len(pemBytes) == 0 {
		return nil, errors.New("signing key is empty")
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	any8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse pkcs8: %w", err)
	}
	rsaKey, ok := any8.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rsaKey, nil
}
