// Package bridge implements the OIDC client / bridge side of auth:
// FlowCatalyst as an OIDC client of external IDPs (Entra, Keycloak,
// Google). On login, the user is redirected to the external IDP; on
// callback we exchange the auth code, validate the ID token, and
// resolve the FlowCatalyst principal via the configured ClientAuthConfig
// or EmailDomainMapping.
//
// Library: github.com/coreos/go-oidc/v3 + golang.org/x/oauth2.
//
// Phase 3d scope: the OIDC client construction is wired; the per-IDP
// configuration lookup (resolve issuer URL + client ID for an email
// domain) is in place; the actual login/callback HTTP handlers ship in
// the auth-runtime follow-up alongside the session-cookie middleware.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
)

// Bridge constructs and caches OIDC clients per (issuerURL, clientID)
// pair. The cache is keyed on the issuer URL + client ID because
// constructing a *oidc.Provider involves a discovery HTTP round-trip
// (to /.well-known/openid-configuration) that we only want to do once
// per IDP per process.
type Bridge struct {
	mappings *emaildomainmapping.Repository
	idps     *identityprovider.Repository
	enc      *encryption.Service // optional; decrypts OIDCClientSecretRef

	mu    sync.Mutex
	cache map[string]*resolved
}

type resolved struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

// NewBridge wires the bridge. enc may be nil — confidential OIDC clients
// will fail to authenticate against the external IDP in that case, but
// public clients (no client secret) still work.
func NewBridge(mappings *emaildomainmapping.Repository, idps *identityprovider.Repository, enc *encryption.Service) *Bridge {
	return &Bridge{mappings: mappings, idps: idps, enc: enc, cache: make(map[string]*resolved)}
}

// ResolveForEmail resolves the OIDC client for the user's email domain via the
// email-domain mapping → identity provider chain, exactly as Rust's oidc_login
// does (find_by_email_domain → identity_provider.find_by_id). Returns the OIDC
// client + the IdP + the mapping; the caller drives the redirect / callback and
// persists the IdP + mapping ids in the login state.
func (b *Bridge) ResolveForEmail(ctx context.Context, email string) (*resolved, *identityprovider.IdentityProvider, *emaildomainmapping.EmailDomainMapping, error) {
	domain := emailDomain(email)
	if domain == "" {
		return nil, nil, nil, errors.New("invalid email: no domain")
	}
	mapping, err := b.mappings.FindByEmailDomain(ctx, domain)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("email_domain_mapping lookup: %w", err)
	}
	if mapping == nil {
		return nil, nil, nil, errors.New("no email-domain mapping for " + domain)
	}
	idp, err := b.idps.FindByID(ctx, mapping.IdentityProviderID)
	if err != nil {
		return nil, nil, mapping, fmt.Errorf("identity_provider lookup: %w", err)
	}
	if idp == nil {
		return nil, nil, mapping, errors.New("identity provider not found: " + mapping.IdentityProviderID)
	}
	if idp.Type != identityprovider.TypeOIDC {
		return nil, idp, mapping, nil // internal provider; no OIDC bridge needed
	}
	if idp.OIDCIssuerURL == nil || idp.OIDCClientID == nil {
		return nil, idp, mapping, errors.New("OIDC config missing issuer or client ID")
	}

	key := *idp.OIDCIssuerURL + "|" + *idp.OIDCClientID
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.cache[key]; ok {
		return r, idp, mapping, nil
	}

	provider, err := oidc.NewProvider(ctx, *idp.OIDCIssuerURL)
	if err != nil {
		return nil, idp, mapping, fmt.Errorf("oidc.NewProvider: %w", err)
	}
	clientSecret, err := b.resolveClientSecret(idp.OIDCClientSecretRef)
	if err != nil {
		return nil, idp, mapping, err
	}
	r := &resolved{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: *idp.OIDCClientID}),
		oauth: &oauth2.Config{
			ClientID:     *idp.OIDCClientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}
	b.cache[key] = r
	return r, idp, mapping, nil
}

// resolveClientSecret decrypts the IdP's OIDCClientSecretRef using the
// configured encryption service. Empty ref → no secret (public client).
// If a ref is present but no encryption service is configured, or
// decryption fails, returns an error so the caller surfaces a clear
// misconfiguration rather than silently mis-authing.
func (b *Bridge) resolveClientSecret(secretRef *string) (string, error) {
	if secretRef == nil || *secretRef == "" {
		return "", nil
	}
	if b.enc == nil {
		return "", errors.New("OIDC client_secret_ref present but no encryption service configured (set FLOWCATALYST_APP_KEY)")
	}
	pt, err := b.enc.Decrypt(*secretRef)
	if err != nil {
		return "", fmt.Errorf("decrypt OIDC client secret: %w", err)
	}
	return pt, nil
}

// VerifyIDToken validates a raw ID token JWT against the bridge cache.
// The verifier checks signature, issuer, audience (== ClientID),
// expiration, and not-before.
func (r *resolved) VerifyIDToken(ctx context.Context, raw string) (*oidc.IDToken, error) {
	return r.verifier.Verify(ctx, raw)
}

// AuthCodeURL builds the redirect URL for an OIDC login start. The state
// param is a CSRF token the caller persists in the session.
func (r *resolved) AuthCodeURL(state, redirectURI string) string {
	cfg := *r.oauth
	cfg.RedirectURL = redirectURI
	return cfg.AuthCodeURL(state)
}

// Exchange swaps an authorization code for tokens.
func (r *resolved) Exchange(ctx context.Context, code, redirectURI string) (*oauth2.Token, error) {
	cfg := *r.oauth
	cfg.RedirectURL = redirectURI
	return cfg.Exchange(ctx, code)
}

func emailDomain(email string) string {
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			return email[i+1:]
		}
	}
	return ""
}
