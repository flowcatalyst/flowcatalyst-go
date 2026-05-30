// Package auth is the port of fc-platform/src/auth. Houses:
//
//   - OAuthClient (entries for SDK consumers issued tokens via OAuth)
//   - AnchorDomain (email domains that grant anchor scope on signup)
//   - ClientAuthConfig (per-tenant auth configuration with optional IDP)
//   - IdpRoleMapping (external IDP role name → platform role name)
//
// Plus runtime adapters for:
//
//   - The OAuth/OIDC provider role (hand-rolled in auth/oauthapi)
//   - The OIDC client role / bridge to external IDPs (coreos/go-oidc)
//
// Token issuance, validation, refresh, and the OIDC login flow are handled
// by auth/oauthapi + auth/authservice (see docs/architecture.md §Auth).
// This subdomain owns the admin-side CRUD of the OAuth/IDP configuration
// plus the storage adapters that oauthapi/authservice call into.
package auth

import (
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// ── OAuthClient ───────────────────────────────────────────────────────────

// OAuthClientType is PUBLIC (no secret — SPA/mobile) or CONFIDENTIAL (server-side).
type OAuthClientType string

const (
	OAuthClientPublic       OAuthClientType = "PUBLIC"
	OAuthClientConfidential OAuthClientType = "CONFIDENTIAL"
)

// ParseOAuthClientType — lenient parser. Unknown → PUBLIC.
func ParseOAuthClientType(s string) OAuthClientType {
	if s == string(OAuthClientConfidential) {
		return OAuthClientConfidential
	}
	return OAuthClientPublic
}

// OAuthClient is a registered client of the OAuth provider.
// Maps to oauth_clients.
type OAuthClient struct {
	ID         string          `json:"id"`
	ClientID   string          `json:"clientId"`
	ClientName string          `json:"clientName"`
	ClientType OAuthClientType `json:"clientType"`
	// SecretRef stores the client_secret for CONFIDENTIAL clients as a
	// reversibly-encrypted blob (AES-GCM via the encryption package),
	// matching Rust's oauth_clients.client_secret_ref. Verification
	// decrypts and compares; nil for PUBLIC. Set via rotate-secret.
	SecretRef    *string  `json:"-"`
	RedirectURIs []string `json:"redirectUris"`
	// PostLogoutRedirectURIs is the OIDC RP-Initiated Logout whitelist
	// (oauth_client_post_logout_redirect_uris). /auth/oidc/session/end
	// validates a supplied post_logout_redirect_uri against this list.
	PostLogoutRedirectURIs []string `json:"postLogoutRedirectUris"`
	GrantTypes             []string `json:"grantTypes"` // "authorization_code", "client_credentials", "refresh_token"
	Scopes                 []string `json:"scopes"`
	// PKCERequired gates whether /oauth/authorize demands a code_challenge.
	// Maps to oauth_clients.pkce_required (DEFAULT TRUE).
	PKCERequired bool      `json:"pkceRequired"`
	Active       bool      `json:"active"`
	PrincipalID  *string   `json:"principalId,omitempty"` // owning principal (for token-issued-on-behalf claims)
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (c OAuthClient) IDStr() string { return c.ID }

// NewOAuthClient constructs an OAuthClient with sensible defaults.
func NewOAuthClient(clientID, name string, t OAuthClientType) *OAuthClient {
	now := time.Now().UTC()
	return &OAuthClient{
		ID:                     tsid.Generate(tsid.OAuthClient),
		ClientID:               clientID,
		ClientName:             name,
		ClientType:             t,
		RedirectURIs:           []string{},
		PostLogoutRedirectURIs: []string{},
		GrantTypes:             []string{},
		Scopes:                 []string{},
		PKCERequired:           true,
		Active:                 true,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
}

// ── AnchorDomain ──────────────────────────────────────────────────────────

// AnchorDomain is an email domain that grants ANCHOR scope at signup.
// Maps to tnt_anchor_domains.
type AnchorDomain struct {
	ID        string    `json:"id"`
	Domain    string    `json:"domain"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (a AnchorDomain) IDStr() string { return a.ID }

// NewAnchorDomain constructs an AnchorDomain, lower-casing the input.
func NewAnchorDomain(domain string) *AnchorDomain {
	now := time.Now().UTC()
	return &AnchorDomain{
		ID:        tsid.Generate(tsid.AnchorDomain),
		Domain:    strings.ToLower(strings.TrimSpace(domain)),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// MatchesEmail reports whether the given email ends in @<domain>.
func (a *AnchorDomain) MatchesEmail(email string) bool {
	return strings.HasSuffix(strings.ToLower(email), "@"+a.Domain)
}

// ── ClientAuthConfig ──────────────────────────────────────────────────────

// AuthProvider identifies how users in this config authenticate.
type AuthProvider string

const (
	ProviderInternal AuthProvider = "INTERNAL"
	ProviderOIDC     AuthProvider = "OIDC"
)

// ParseAuthProvider — lenient parser. Unknown → INTERNAL.
func ParseAuthProvider(s string) AuthProvider {
	if s == string(ProviderOIDC) {
		return ProviderOIDC
	}
	return ProviderInternal
}

// AuthConfigType is the scope of an auth config (ANCHOR/PARTNER/CLIENT).
type AuthConfigType string

const (
	ConfigAnchor  AuthConfigType = "ANCHOR"
	ConfigPartner AuthConfigType = "PARTNER"
	ConfigClient  AuthConfigType = "CLIENT"
)

// ParseAuthConfigType — lenient parser. Unknown → CLIENT.
func ParseAuthConfigType(s string) AuthConfigType {
	switch s {
	case string(ConfigAnchor):
		return ConfigAnchor
	case string(ConfigPartner):
		return ConfigPartner
	default:
		return ConfigClient
	}
}

// ClientAuthConfig is a per-domain auth configuration: which IDP serves
// users from this email domain, with what scope, granting access to which
// clients. Maps to tnt_client_auth_configs.
type ClientAuthConfig struct {
	ID                  string         `json:"id"`
	EmailDomain         string         `json:"emailDomain"`
	ConfigType          AuthConfigType `json:"configType"`
	PrimaryClientID     *string        `json:"primaryClientId,omitempty"`
	AdditionalClientIDs []string       `json:"additionalClientIds"`
	GrantedClientIDs    []string       `json:"grantedClientIds"`
	AuthProvider        AuthProvider   `json:"authProvider"`
	OIDCIssuerURL       *string        `json:"oidcIssuerUrl,omitempty"`
	OIDCClientID        *string        `json:"oidcClientId,omitempty"`
	OIDCMultiTenant     bool           `json:"oidcMultiTenant"`
	OIDCIssuerPattern   *string        `json:"oidcIssuerPattern,omitempty"`
	OIDCClientSecretRef *string        `json:"oidcClientSecretRef,omitempty"`
	CreatedAt           time.Time      `json:"createdAt"`
	UpdatedAt           time.Time      `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (c ClientAuthConfig) IDStr() string { return c.ID }

// NewClientAuthConfig constructs an INTERNAL-provider config.
func NewClientAuthConfig(emailDomain string, configType AuthConfigType) *ClientAuthConfig {
	now := time.Now().UTC()
	return &ClientAuthConfig{
		ID:                  tsid.Generate(tsid.ClientAuthConfig),
		EmailDomain:         strings.ToLower(strings.TrimSpace(emailDomain)),
		ConfigType:          configType,
		AdditionalClientIDs: []string{},
		GrantedClientIDs:    []string{},
		AuthProvider:        ProviderInternal,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

// ── IdpRoleMapping ────────────────────────────────────────────────────────

// IdpRoleMapping translates an external IDP role name to a platform role
// name. Maps to oauth_idp_role_mappings.
type IdpRoleMapping struct {
	ID               string    `json:"id"`
	IdpType          string    `json:"idpType"`          // e.g. "keycloak", "entra"
	IdpRoleName      string    `json:"idpRoleName"`      // upstream role name
	PlatformRoleName string    `json:"platformRoleName"` // FlowCatalyst role name (app:role)
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (m IdpRoleMapping) IDStr() string { return m.ID }

// NewIdpRoleMapping constructs an IdpRoleMapping.
func NewIdpRoleMapping(idpType, idpRoleName, platformRoleName string) *IdpRoleMapping {
	now := time.Now().UTC()
	return &IdpRoleMapping{
		ID:               tsid.Generate(tsid.IdpRoleMapping),
		IdpType:          idpType,
		IdpRoleName:      idpRoleName,
		PlatformRoleName: platformRoleName,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// ── Lifecycle helpers ─────────────────────────────────────────────────────

// Activate flips an OAuthClient to Active=true.
func (c *OAuthClient) Activate() {
	c.Active = true
	c.UpdatedAt = time.Now().UTC()
}

// Deactivate flips an OAuthClient to Active=false.
func (c *OAuthClient) Deactivate() {
	c.Active = false
	c.UpdatedAt = time.Now().UTC()
}

// SetSecretRef records a rotated encrypted secret reference. The
// plaintext lives only in memory long enough to return it once via the
// rotate API.
func (c *OAuthClient) SetSecretRef(ref string) {
	c.SecretRef = &ref
	c.UpdatedAt = time.Now().UTC()
}
