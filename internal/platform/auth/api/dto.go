// dto.go contains the wire-format types for the auth admin API.
package api

import (
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httpcompat"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/jsontime"
)

// ── OAuthClient ───────────────────────────────────────────────────────────

// CreateOAuthClientRequest is the wire body for POST /api/oauth-clients.
// Matches the SPA's CreateOAuthClientRequest (oauth-clients.ts:33-43).
type CreateOAuthClientRequest struct {
	ClientID     string   `json:"clientId"`
	ClientName   string   `json:"clientName"`
	ClientType   string   `json:"clientType" doc:"PUBLIC or CONFIDENTIAL"`
	RedirectURIs []string `json:"redirectUris,omitempty"`
	GrantTypes   []string `json:"grantTypes,omitempty"`
	// DefaultScopes is sent by the SPA as a space-delimited string (e.g.
	// "openid profile"); legacy callers may still send Scopes. Either is
	// accepted and folded into the entity's Scopes slice.
	DefaultScopes string `json:"defaultScopes,omitempty"`
	// Scopes is the legacy array form (kept for back-compat with older callers).
	Scopes []string `json:"scopes,omitempty"`
	// PKCERequired is accepted but not persisted — the Go entity does not
	// store a pkce flag. See OAuthClientResponse.PKCERequired (always false).
	PKCERequired bool `json:"pkceRequired,omitempty"`
	// PostLogoutRedirectURIs is the OIDC RP-Initiated Logout whitelist,
	// persisted and validated by /auth/oidc/session/end.
	PostLogoutRedirectURIs []string `json:"postLogoutRedirectUris,omitempty"`
	// AllowedOrigins is accepted but not persisted (no entity field).
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`
	// ApplicationIDs is accepted but not persisted (no entity field).
	ApplicationIDs []string `json:"applicationIds,omitempty"`
	PrincipalID    *string  `json:"principalId,omitempty"`
}

func (r CreateOAuthClientRequest) toCommand() operations.CreateOAuthClientCommand {
	scopes := r.Scopes
	// SPA sends defaultScopes as a space-delimited string; split it into the
	// entity's scope slice. Falls back to the legacy Scopes array if empty.
	if s := strings.TrimSpace(r.DefaultScopes); s != "" {
		scopes = strings.Fields(s)
	}
	return operations.CreateOAuthClientCommand{
		ClientID:               r.ClientID,
		ClientName:             r.ClientName,
		ClientType:             r.ClientType,
		RedirectURIs:           r.RedirectURIs,
		PostLogoutRedirectURIs: r.PostLogoutRedirectURIs,
		GrantTypes:             r.GrantTypes,
		Scopes:                 scopes,
		PrincipalID:            r.PrincipalID,
	}
}

// UpdateOAuthClientRequest is the wire body for PUT /api/oauth-clients/{id}.
type UpdateOAuthClientRequest struct {
	ClientName             *string  `json:"clientName,omitempty"`
	RedirectURIs           []string `json:"redirectUris,omitempty"`
	PostLogoutRedirectURIs []string `json:"postLogoutRedirectUris,omitempty"`
	GrantTypes             []string `json:"grantTypes,omitempty"`
	Scopes                 []string `json:"scopes,omitempty"`
}

func (r UpdateOAuthClientRequest) toCommand(id string) operations.UpdateOAuthClientCommand {
	return operations.UpdateOAuthClientCommand{
		ID:                     id,
		ClientName:             r.ClientName,
		RedirectURIs:           r.RedirectURIs,
		PostLogoutRedirectURIs: r.PostLogoutRedirectURIs,
		GrantTypes:             r.GrantTypes,
		Scopes:                 r.Scopes,
	}
}

// OAuthClientApplicationRef is the {id, name} shape the SPA's ApplicationRef
// expects (oauth-clients.ts:5-8). The Go entity does not track applications,
// so this is only ever emitted as an empty slice today.
type OAuthClientApplicationRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// OAuthClientResponse mirrors the SPA's OAuthClient (oauth-clients.ts:10-26).
// Several fields have no backing column on the Go auth.OAuthClient entity and
// are emitted as safe defaults (see oauthClientFromEntity) so the SPA — which
// reads e.g. data.applications.length unconditionally — never crashes.
type OAuthClientResponse struct {
	ID           string   `json:"id"`
	ClientID     string   `json:"clientId"`
	ClientName   string   `json:"clientName"`
	ClientType   string   `json:"clientType"`
	RedirectURIs []string `json:"redirectUris"`
	// PostLogoutRedirectURIs has no entity field — always []. (defaulted)
	PostLogoutRedirectURIs []string `json:"postLogoutRedirectUris"`
	// AllowedOrigins has no entity field — always []. (defaulted)
	AllowedOrigins []string `json:"allowedOrigins"`
	GrantTypes     []string `json:"grantTypes"`
	// DefaultScopes is the entity's Scopes slice (renamed for the SPA).
	DefaultScopes []string `json:"defaultScopes"`
	// PKCERequired has no entity field — always false. (defaulted)
	PKCERequired bool `json:"pkceRequired"`
	// ApplicationIDs has no entity field — always []. (defaulted)
	ApplicationIDs []string `json:"applicationIds"`
	// Applications has no entity field — always []. The SPA list page reads
	// data.applications.length unconditionally, so this MUST be present. (defaulted)
	Applications []OAuthClientApplicationRef `json:"applications"`
	Active       bool                        `json:"active"`
	// ServiceAccountPrincipalID maps from the entity's PrincipalID.
	ServiceAccountPrincipalID *string         `json:"serviceAccountPrincipalId,omitempty"`
	CreatedAt                 httpcompat.Time `json:"createdAt"`
	UpdatedAt                 httpcompat.Time `json:"updatedAt"`
}

func oauthClientFromEntity(c *auth.OAuthClient) OAuthClientResponse {
	uris := c.RedirectURIs
	if uris == nil {
		uris = []string{}
	}
	grants := c.GrantTypes
	if grants == nil {
		grants = []string{}
	}
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	plUris := c.PostLogoutRedirectURIs
	if plUris == nil {
		plUris = []string{}
	}
	return OAuthClientResponse{
		ID:                        c.ID,
		ClientID:                  c.ClientID,
		ClientName:                c.ClientName,
		ClientType:                string(c.ClientType),
		RedirectURIs:              uris,
		PostLogoutRedirectURIs:    plUris,
		AllowedOrigins:            []string{},
		GrantTypes:                grants,
		DefaultScopes:             scopes,
		PKCERequired:              false,
		ApplicationIDs:            []string{},
		Applications:              []OAuthClientApplicationRef{},
		Active:                    c.Active,
		ServiceAccountPrincipalID: c.PrincipalID,
		CreatedAt:                 jsontime.New(c.CreatedAt),
		UpdatedAt:                 jsontime.New(c.UpdatedAt),
	}
}

// OAuthClientListResponse is the wire shape for GET /api/oauth-clients.
// Matches the Rust fc-platform shape `{clients}` (no total). SPA's
// OAuthClientListPage reads `response.clients`.
type OAuthClientListResponse struct {
	Clients []OAuthClientResponse `json:"clients"`
}

// CreateOAuthClientResponse is the wire shape for POST /api/oauth-clients.
// The SPA reads response.client.* and response.clientSecret
// (oauth-clients.ts:56-60, OAuthClientCreatePage.vue:176-184).
// The client_secret plaintext is only emitted once, on creation.
type CreateOAuthClientResponse struct {
	Client       OAuthClientResponse `json:"client"`
	ClientSecret string              `json:"clientSecret,omitempty"`
}

// RotateOAuthClientSecretResponse is returned on rotate. The SPA reads
// {clientId, clientSecret} (oauth-clients.ts:62-65) — note clientId is the
// public client_id string, not the internal id.
type RotateOAuthClientSecretResponse struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// ── AnchorDomain ──────────────────────────────────────────────────────────

// CreateAnchorDomainRequest is the wire body for POST /api/anchor-domains.
type CreateAnchorDomainRequest struct {
	Domain string `json:"domain"`
}

func (r CreateAnchorDomainRequest) toCommand() operations.CreateAnchorDomainCommand {
	return operations.CreateAnchorDomainCommand{Domain: r.Domain}
}

// UpdateAnchorDomainRequest is the wire body for PUT /api/anchor-domains/{id}.
type UpdateAnchorDomainRequest struct {
	Domain string `json:"domain"`
}

func (r UpdateAnchorDomainRequest) toCommand(id string) operations.UpdateAnchorDomainCommand {
	return operations.UpdateAnchorDomainCommand{ID: id, Domain: r.Domain}
}

// AnchorDomainResponse mirrors auth.AnchorDomain.
type AnchorDomainResponse struct {
	ID        string          `json:"id"`
	Domain    string          `json:"domain"`
	CreatedAt httpcompat.Time `json:"createdAt"`
	UpdatedAt httpcompat.Time `json:"updatedAt"`
}

func anchorDomainFromEntity(a *auth.AnchorDomain) AnchorDomainResponse {
	return AnchorDomainResponse{
		ID:        a.ID,
		Domain:    a.Domain,
		CreatedAt: jsontime.New(a.CreatedAt),
		UpdatedAt: jsontime.New(a.UpdatedAt),
	}
}

// AnchorDomainListResponse is the wire shape for GET /api/anchor-domains.
type AnchorDomainListResponse struct {
	Items []AnchorDomainResponse `json:"items"`
}

// ── ClientAuthConfig ──────────────────────────────────────────────────────

// CreateAuthConfigRequest is the wire body for POST /api/auth-configs.
type CreateAuthConfigRequest struct {
	EmailDomain         string   `json:"emailDomain"`
	ConfigType          string   `json:"configType"`
	PrimaryClientID     *string  `json:"primaryClientId,omitempty"`
	AdditionalClientIDs []string `json:"additionalClientIds,omitempty"`
	GrantedClientIDs    []string `json:"grantedClientIds,omitempty"`
	AuthProvider        string   `json:"authProvider"`
	OIDCIssuerURL       *string  `json:"oidcIssuerUrl,omitempty"`
	OIDCClientID        *string  `json:"oidcClientId,omitempty"`
	OIDCMultiTenant     bool     `json:"oidcMultiTenant"`
	OIDCIssuerPattern   *string  `json:"oidcIssuerPattern,omitempty"`
	OIDCClientSecretRef *string  `json:"oidcClientSecretRef,omitempty"`
}

func (r CreateAuthConfigRequest) toCommand() operations.CreateAuthConfigCommand {
	return operations.CreateAuthConfigCommand{
		EmailDomain:         r.EmailDomain,
		ConfigType:          r.ConfigType,
		PrimaryClientID:     r.PrimaryClientID,
		AdditionalClientIDs: r.AdditionalClientIDs,
		GrantedClientIDs:    r.GrantedClientIDs,
		AuthProvider:        r.AuthProvider,
		OIDCIssuerURL:       r.OIDCIssuerURL,
		OIDCClientID:        r.OIDCClientID,
		OIDCMultiTenant:     r.OIDCMultiTenant,
		OIDCIssuerPattern:   r.OIDCIssuerPattern,
		OIDCClientSecretRef: r.OIDCClientSecretRef,
	}
}

// UpdateAuthConfigRequest is the wire body for PUT /api/auth-configs/{id}.
type UpdateAuthConfigRequest struct {
	PrimaryClientID     *string  `json:"primaryClientId,omitempty"`
	AdditionalClientIDs []string `json:"additionalClientIds,omitempty"`
	GrantedClientIDs    []string `json:"grantedClientIds,omitempty"`
	AuthProvider        *string  `json:"authProvider,omitempty"`
	OIDCIssuerURL       *string  `json:"oidcIssuerUrl,omitempty"`
	OIDCClientID        *string  `json:"oidcClientId,omitempty"`
	OIDCMultiTenant     *bool    `json:"oidcMultiTenant,omitempty"`
	OIDCIssuerPattern   *string  `json:"oidcIssuerPattern,omitempty"`
	OIDCClientSecretRef *string  `json:"oidcClientSecretRef,omitempty"`
}

func (r UpdateAuthConfigRequest) toCommand(id string) operations.UpdateAuthConfigCommand {
	return operations.UpdateAuthConfigCommand{
		ID:                  id,
		PrimaryClientID:     r.PrimaryClientID,
		AdditionalClientIDs: r.AdditionalClientIDs,
		GrantedClientIDs:    r.GrantedClientIDs,
		AuthProvider:        r.AuthProvider,
		OIDCIssuerURL:       r.OIDCIssuerURL,
		OIDCClientID:        r.OIDCClientID,
		OIDCMultiTenant:     r.OIDCMultiTenant,
		OIDCIssuerPattern:   r.OIDCIssuerPattern,
		OIDCClientSecretRef: r.OIDCClientSecretRef,
	}
}

// AuthConfigResponse mirrors auth.ClientAuthConfig.
type AuthConfigResponse struct {
	ID                  string          `json:"id"`
	EmailDomain         string          `json:"emailDomain"`
	ConfigType          string          `json:"configType"`
	PrimaryClientID     *string         `json:"primaryClientId,omitempty"`
	AdditionalClientIDs []string        `json:"additionalClientIds"`
	GrantedClientIDs    []string        `json:"grantedClientIds"`
	AuthProvider        string          `json:"authProvider"`
	OIDCIssuerURL       *string         `json:"oidcIssuerUrl,omitempty"`
	OIDCClientID        *string         `json:"oidcClientId,omitempty"`
	OIDCMultiTenant     bool            `json:"oidcMultiTenant"`
	OIDCIssuerPattern   *string         `json:"oidcIssuerPattern,omitempty"`
	OIDCClientSecretRef *string         `json:"oidcClientSecretRef,omitempty"`
	CreatedAt           httpcompat.Time `json:"createdAt"`
	UpdatedAt           httpcompat.Time `json:"updatedAt"`
}

func authConfigFromEntity(c *auth.ClientAuthConfig) AuthConfigResponse {
	addl := c.AdditionalClientIDs
	if addl == nil {
		addl = []string{}
	}
	granted := c.GrantedClientIDs
	if granted == nil {
		granted = []string{}
	}
	return AuthConfigResponse{
		ID:                  c.ID,
		EmailDomain:         c.EmailDomain,
		ConfigType:          string(c.ConfigType),
		PrimaryClientID:     c.PrimaryClientID,
		AdditionalClientIDs: addl,
		GrantedClientIDs:    granted,
		AuthProvider:        string(c.AuthProvider),
		OIDCIssuerURL:       c.OIDCIssuerURL,
		OIDCClientID:        c.OIDCClientID,
		OIDCMultiTenant:     c.OIDCMultiTenant,
		OIDCIssuerPattern:   c.OIDCIssuerPattern,
		OIDCClientSecretRef: c.OIDCClientSecretRef,
		CreatedAt:           jsontime.New(c.CreatedAt),
		UpdatedAt:           jsontime.New(c.UpdatedAt),
	}
}

// AuthConfigListResponse is the wire shape for GET /api/auth-configs.
type AuthConfigListResponse struct {
	Items []AuthConfigResponse `json:"items"`
}

// ── IdpRoleMapping ────────────────────────────────────────────────────────

// CreateIdpRoleMappingRequest is the wire body for POST /api/idp-role-mappings.
type CreateIdpRoleMappingRequest struct {
	IdpType          string `json:"idpType"`
	IdpRoleName      string `json:"idpRoleName"`
	PlatformRoleName string `json:"platformRoleName"`
}

func (r CreateIdpRoleMappingRequest) toCommand() operations.CreateIdpRoleMappingCommand {
	return operations.CreateIdpRoleMappingCommand{
		IdpType:          r.IdpType,
		IdpRoleName:      r.IdpRoleName,
		PlatformRoleName: r.PlatformRoleName,
	}
}

// IdpRoleMappingResponse mirrors auth.IdpRoleMapping.
type IdpRoleMappingResponse struct {
	ID               string          `json:"id"`
	IdpType          string          `json:"idpType"`
	IdpRoleName      string          `json:"idpRoleName"`
	PlatformRoleName string          `json:"platformRoleName"`
	CreatedAt        httpcompat.Time `json:"createdAt"`
	UpdatedAt        httpcompat.Time `json:"updatedAt"`
}

func idpRoleMappingFromEntity(m *auth.IdpRoleMapping) IdpRoleMappingResponse {
	return IdpRoleMappingResponse{
		ID:               m.ID,
		IdpType:          m.IdpType,
		IdpRoleName:      m.IdpRoleName,
		PlatformRoleName: m.PlatformRoleName,
		CreatedAt:        jsontime.New(m.CreatedAt),
		UpdatedAt:        jsontime.New(m.UpdatedAt),
	}
}

// IdpRoleMappingListResponse is the wire shape for GET /api/idp-role-mappings.
type IdpRoleMappingListResponse struct {
	Items []IdpRoleMappingResponse `json:"items"`
}
