// Package oauthapi is the hand-rolled OAuth2 endpoint surface, a 1:1 port
// of crates/fc-platform/src/auth/oauth_api.rs. It owns
// /oauth/{token,authorize,introspect,revoke,userinfo} end-to-end (the
// former fosite-backed provider was removed — see ADR-0001).
//
// Wire contract notes carried over from Rust:
//   - Error bodies are RFC-6749 {error, error_description?} — NOT the
//     platform {error, message} envelope.
//   - Successful token responses carry Cache-Control: no-store and
//     Pragma: no-cache, token_type "Bearer", expires_in 3600.
package oauthapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/authservice"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/grantstore"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/loginattempt"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	sharedauth "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/ratelimit"
)

// OAuthClientFinder resolves OAuth clients by their public client_id. It is
// satisfied by *auth.OAuthClientRepo in production; narrowing it to an
// interface lets the authorize/token handler tests inject clients (and the
// "unknown client" case) without a database.
type OAuthClientFinder interface {
	FindByClientID(ctx context.Context, clientID string) (*auth.OAuthClient, error)
}

// State bundles the dependencies the OAuth endpoints need.
type State struct {
	OAuthClients  OAuthClientFinder
	Principals    *principal.Repository
	Auth          *authservice.AuthService
	AuthCodes     *grantstore.AuthorizationCodeRepository
	RefreshTokens *grantstore.RefreshTokenRepository
	PendingAuth   *grantstore.PendingAuthRepository
	// ValidateSession resolves the principal id + token issue time from a
	// session-cookie / bearer token on /oauth/authorize, returning ok=false
	// when the token is absent, invalid, or expired (authorize then
	// redirects to login rather than rejecting). issuedAt drives OIDC
	// max_age enforcement (zero time = unknown). Injected so this package
	// stays decoupled from the session-token validator.
	ValidateSession func(token string) (subject string, issuedAt time.Time, ok bool)
	// FlattenPermissions resolves a principal's role names into their full
	// permission set (the grant ceiling), used to compute the granted "scope"
	// claim. Injected from provider.FlattenPermissions to keep this package
	// decoupled from the role repository. When nil, scope narrowing is
	// disabled: tokens carry no scope claim and the requested scope parameter
	// is ignored (permissions are then derived downstream from roles).
	FlattenPermissions func(ctx context.Context, roleNames []string) ([]string, error)
	// Encryption verifies confidential-client secrets (decrypt + compare).
	// May be nil when no app key is configured — confidential auth then
	// fails closed.
	Encryption *encryption.Service
	// BaseURL is the external issuer/base URL the discovery document
	// advertises its endpoint URLs from (e.g. https://flowcatalyst.example).
	BaseURL string
	// LoginAttempts records SERVICE_ACCOUNT_TOKEN outcomes on
	// client_credentials. Optional (nil disables recording).
	LoginAttempts *loginattempt.Repository
	// RateLimit is the cluster-wide per-client_id throttle on
	// /oauth/{token,authorize}. Optional (nil disables it; the per-IP
	// middleware layer still applies when mounted).
	RateLimit         ratelimit.Store
	RateLimitPolicies ratelimit.Policies
	// ClientGovernor is the per-instance in-memory per-client_id limiter
	// checked before the distributed RateLimit on /oauth/token (sheds a
	// flood locally before the network round-trip). Optional (nil skips it).
	ClientGovernor *ratelimit.Governor
}

// recordAttempt best-effort logs a login attempt; failures are swallowed
// (a logging miss must never fail the auth flow). No-op when the repo is
// unset.
func (s *State) recordAttempt(ctx context.Context, t loginattempt.AttemptType, outcome loginattempt.Outcome, identifier string, principalID, failureReason *string) {
	if s.LoginAttempts == nil {
		return
	}
	a := loginattempt.New(t, outcome)
	a.Identifier = &identifier
	a.PrincipalID = principalID
	a.FailureReason = failureReason
	_ = s.LoginAttempts.Record(ctx, a)
}

// RegisterTokenRoutes mounts POST /oauth/token.
func (s *State) RegisterTokenRoutes(r chi.Router) {
	r.Post("/oauth/token", s.Token)
}

// tokenRequest mirrors the Rust TokenRequest (form-urlencoded).
type tokenRequest struct {
	GrantType    string
	Code         string
	RedirectURI  string
	ClientID     string
	ClientSecret string
	CodeVerifier string
	RefreshToken string
	Scope        string
}

func parseTokenRequest(r *http.Request) (tokenRequest, error) {
	if err := r.ParseForm(); err != nil {
		return tokenRequest{}, err
	}
	return tokenRequest{
		GrantType:    r.PostFormValue("grant_type"),
		Code:         r.PostFormValue("code"),
		RedirectURI:  r.PostFormValue("redirect_uri"),
		ClientID:     r.PostFormValue("client_id"),
		ClientSecret: r.PostFormValue("client_secret"),
		CodeVerifier: r.PostFormValue("code_verifier"),
		RefreshToken: r.PostFormValue("refresh_token"),
		Scope:        r.PostFormValue("scope"),
	}, nil
}

// tokenResponse mirrors the Rust TokenResponse.
type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int64   `json:"expires_in"`
	RefreshToken *string `json:"refresh_token,omitempty"`
	IDToken      *string `json:"id_token,omitempty"`
	Scope        *string `json:"scope,omitempty"`
}

// Token is POST /oauth/token. It authenticates the client (except for
// client_credentials, which authenticates inside its handler) then
// dispatches on grant_type.
func (s *State) Token(w http.ResponseWriter, r *http.Request) {
	req, err := parseTokenRequest(r)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Malformed form body")
		return
	}

	// Per-client_id throttle. Runs before the DB lookup so a client
	// spamming us can't amplify load on the client cache. The in-memory
	// governor sheds a local flood first; the distributed Store is the
	// cluster-wide ceiling. Composes with the per-IP middleware wrapping
	// /oauth/*.
	if req.ClientID != "" {
		if s.ClientGovernor != nil {
			if ok, retry := s.ClientGovernor.Check(req.ClientID); !ok {
				writeOAuthRateLimited(w, retry, "this client_id has exceeded its token endpoint rate limit")
				return
			}
		}
		if rej := ratelimit.Enforce(r.Context(), s.RateLimit, ratelimit.BucketOAuthTokenClient, req.ClientID, s.RateLimitPolicies.OAuthTokenClient); rej != nil {
			writeOAuthRateLimited(w, rej.RetryAfterSecs, "this client_id has exceeded its token endpoint rate limit")
			return
		}
	}

	// Authenticate the client up-front for code/refresh grants;
	// client_credentials does its own auth (including the CONFIDENTIAL check).
	var authenticatedClient *auth.OAuthClient
	if req.GrantType != "client_credentials" {
		c, errResp := s.authenticateClient(r, req.ClientID, req.ClientSecret)
		if errResp != nil {
			errResp.write(w)
			return
		}
		authenticatedClient = c
	}

	// Enforce the client's registered grant-type allowlist for the two
	// client-authenticated grants. client_credentials is checked inside its
	// own handler (it authenticates there).
	if authenticatedClient != nil && !grantAllowed(authenticatedClient, req.GrantType) {
		writeOAuthError(w, http.StatusBadRequest, "unauthorized_client",
			"Client is not permitted to use the '"+req.GrantType+"' grant type")
		return
	}

	// A body client_id supplied alongside Basic auth must match the
	// authenticated identity (RFC 6749 §3.2.1 — a request must not use two
	// client identities). Basic auth wins inside authenticateClient, so
	// without this check a divergent body client_id would silently ride
	// along into the grant handlers.
	if authenticatedClient != nil && req.ClientID != "" && req.ClientID != authenticatedClient.ClientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"client_id does not match the authenticated client")
		return
	}

	switch req.GrantType {
	case "authorization_code":
		s.handleAuthorizationCodeGrant(w, r, req, authenticatedClient)
	case "refresh_token":
		s.handleRefreshTokenGrant(w, r, req, authenticatedClient)
	case "client_credentials":
		s.handleClientCredentialsGrant(w, r, req)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"Grant type '"+req.GrantType+"' is not supported")
	}
}

// ─── client authentication ──────────────────────────────────────────────

// authenticateClient resolves the client from Basic auth or body params
// and verifies the secret for confidential clients. Mirrors Rust's
// authenticate_client.
func (s *State) authenticateClient(r *http.Request, clientIDBody, clientSecretBody string) (*auth.OAuthClient, *oauthError) {
	clientID, clientSecret, ok := basicAuthCreds(r)
	if !ok {
		if clientIDBody == "" {
			return nil, newOAuthError(http.StatusUnauthorized, "invalid_client", "Missing client credentials")
		}
		clientID = clientIDBody
		clientSecret = clientSecretBody
	}

	client, err := s.OAuthClients.FindByClientID(r.Context(), clientID)
	if err != nil {
		return nil, newOAuthError(http.StatusInternalServerError, "server_error", "")
	}
	if client == nil {
		return nil, newOAuthError(http.StatusUnauthorized, "invalid_client", "Unknown client")
	}
	if !client.Active {
		return nil, newOAuthError(http.StatusUnauthorized, "invalid_client", "Client is not active")
	}

	// Public client (no stored secret) must not present one.
	if client.SecretRef == nil {
		if clientSecret != "" {
			return nil, newOAuthError(http.StatusUnauthorized, "invalid_client",
				"Public clients must not provide a client_secret")
		}
		return client, nil
	}

	// Confidential client: verify the provided secret against the
	// encrypted ref (decrypt + compare).
	if clientSecret == "" {
		return nil, newOAuthError(http.StatusUnauthorized, "invalid_client",
			"Client secret required for confidential clients")
	}
	if !s.verifyClientSecret(*client.SecretRef, clientSecret) {
		return nil, newOAuthError(http.StatusUnauthorized, "invalid_client", "Invalid client credentials")
	}
	return client, nil
}

// verifyClientSecret decrypts the stored ref and compares it to the
// provided secret in constant time (a naive == short-circuits on the first
// differing byte, leaking prefix length to a timing observer). Fails closed
// when no encryption service is configured.
func (s *State) verifyClientSecret(secretRef, provided string) bool {
	if s.Encryption == nil {
		return false
	}
	decrypted, err := s.Encryption.Decrypt(secretRef)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(decrypted), []byte(provided)) == 1
}

// basicAuthCreds decodes an HTTP Basic Authorization header
// (base64(client_id:client_secret)). Returns ok=false when the header is
// absent or not Basic.
func basicAuthCreds(r *http.Request) (id, secret string, ok bool) {
	h := r.Header.Get("Authorization")
	enc, found := strings.CutPrefix(h, "Basic ")
	if !found {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", "", false
	}
	id, secret, found = strings.Cut(string(decoded), ":")
	if !found {
		return "", "", false
	}
	return id, secret, true
}

// ─── client_credentials grant ───────────────────────────────────────────

func (s *State) handleClientCredentialsGrant(w http.ResponseWriter, r *http.Request, req tokenRequest) {
	if req.ClientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Missing client_id")
		return
	}
	if req.ClientSecret == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Missing client_secret")
		return
	}

	client, err := s.OAuthClients.FindByClientID(r.Context(), req.ClientID)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if client == nil || !client.Active {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "Invalid client credentials")
		return
	}
	if client.ClientType != auth.OAuthClientConfidential {
		writeOAuthError(w, http.StatusUnauthorized, "unauthorized_client",
			"Public clients cannot use client_credentials grant")
		return
	}
	if !grantAllowed(client, "client_credentials") {
		reason := "client_credentials grant not permitted for this client"
		s.recordAttempt(r.Context(), loginattempt.AttemptServiceAccountToken, loginattempt.OutcomeFailure, req.ClientID, nil, &reason)
		writeOAuthError(w, http.StatusUnauthorized, "unauthorized_client",
			"Client is not permitted to use the client_credentials grant type")
		return
	}
	if client.SecretRef == nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "Invalid client credentials")
		return
	}
	if !s.verifyClientSecret(*client.SecretRef, req.ClientSecret) {
		reason := "Invalid client secret"
		s.recordAttempt(r.Context(), loginattempt.AttemptServiceAccountToken, loginattempt.OutcomeFailure, req.ClientID, nil, &reason)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "Invalid client credentials")
		return
	}

	if client.PrincipalID == nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "Client not properly configured")
		return
	}
	p, err := s.Principals.FindByID(r.Context(), *client.PrincipalID)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if p == nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "Client not properly configured")
		return
	}
	if !p.Active {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "Service account is not active")
		return
	}

	granted, explicit, err := s.grantedScope(r.Context(), p, req.Scope)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if explicit && len(granted) == 0 {
		// The client explicitly requested permission scopes but holds none of
		// them — a scope request can't escalate, so this is a client error.
		reason := "requested scope exceeds granted permissions"
		s.recordAttempt(r.Context(), loginattempt.AttemptServiceAccountToken, loginattempt.OutcomeFailure, req.ClientID, &p.ID, &reason)
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope",
			"Requested scope exceeds the service account's granted permissions")
		return
	}
	accessToken, err := s.Auth.GenerateAccessTokenWithScope(p, granted)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	s.recordAttempt(r.Context(), loginattempt.AttemptServiceAccountToken, loginattempt.OutcomeSuccess, req.ClientID, &p.ID, nil)
	writeToken(w, tokenResponse{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		ExpiresIn:   3600,
		Scope:       scopeResponse(granted),
	})
}

// ─── authorization_code grant ───────────────────────────────────────────

func (s *State) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request, req tokenRequest, client *auth.OAuthClient) {
	if req.Code == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Missing 'code' parameter")
		return
	}

	// Atomically consume the code (single-use enforcement).
	code, err := s.AuthCodes.FindAndConsume(r.Context(), req.Code)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if code == nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "Invalid or expired authorization code")
		return
	}
	if code.IsExpired() {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "Authorization code has expired")
		return
	}
	// Bind the code to the AUTHENTICATED client identity (resolved from
	// Basic auth or body by authenticateClient), not the body client_id: a
	// different valid client could otherwise authenticate as itself while
	// echoing the victim's client_id in the body and redeem a code that was
	// never issued to it. (handleTokenEndpoint guarantees client != nil for
	// this grant; the nil check is belt-and-braces.)
	if client == nil || client.ClientID != code.ClientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "Client ID mismatch")
		return
	}
	if req.RedirectURI != code.RedirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "Redirect URI mismatch")
		return
	}

	if code.CodeChallenge != nil {
		if errResp := verifyPKCE(*code.CodeChallenge, code.CodeChallengeMethod, req.CodeVerifier); errResp != nil {
			errResp.write(w)
			return
		}
	}

	p, err := s.Principals.FindByID(r.Context(), code.PrincipalID)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if p == nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "Principal not found")
		return
	}

	scope := ""
	if code.Scope != nil {
		scope = *code.Scope
	}

	// Narrow the token's permission scope to the ceiling ∩ the scope captured
	// at authorize time. Interactive logins commonly request only OIDC scopes
	// (openid/profile/…), which leaves the permission request empty → the
	// principal's full permissions, so we never reject here.
	granted, _, err := s.grantedScope(r.Context(), p, scope)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	accessToken, err := s.Auth.GenerateAccessTokenWithScope(p, granted)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}

	var idToken *string
	if scopeHas(scope, "openid") {
		t, err := s.Auth.GenerateIDToken(p, code.ClientID, code.Nonce)
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
			return
		}
		idToken = &t
	}

	var refreshToken *string
	if scopeHas(scope, "offline_access") {
		raw, entity, err := grantstore.GenerateTokenPair(p.ID)
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
			return
		}
		cid := code.ClientID
		entity.OAuthClientID = &cid
		entity.Scopes = strings.Fields(scope)
		// Root a rotation family on this first token so every later rotation
		// can be traced to it and the whole family revoked if a rotated-out
		// token is ever replayed (reuse detection in handleRefreshTokenGrant).
		fam := entity.ID
		entity.TokenFamily = &fam
		if err := s.RefreshTokens.Insert(r.Context(), entity); err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
			return
		}
		refreshToken = &raw
	}

	writeToken(w, tokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		RefreshToken: refreshToken,
		IDToken:      idToken,
		Scope:        code.Scope,
	})
}

// ─── refresh_token grant ────────────────────────────────────────────────

func (s *State) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request, req tokenRequest, authenticatedClient *auth.OAuthClient) {
	if req.RefreshToken == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Missing refresh_token parameter")
		return
	}

	// Shared rotation core (grantstore.Rotate): BCP reuse detection, revoke-
	// before-reissue, scope/client/family lineage, replaced-by linking. The
	// client-binding check runs as the authorize hook so a binding failure
	// rotates nothing.
	errClientBinding := errors.New("client binding mismatch")
	res, err := grantstore.Rotate(r.Context(), s.RefreshTokens, req.RefreshToken,
		func(stored *grantstore.RefreshToken) error {
			// A token issued to a client may only be refreshed by that client.
			if stored.OAuthClientID != nil {
				var requesting string
				if authenticatedClient != nil {
					requesting = authenticatedClient.ClientID
				}
				if requesting != *stored.OAuthClientID {
					return errClientBinding
				}
			}
			return nil
		})
	if errors.Is(err, errClientBinding) {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "Token was not issued to this client")
		return
	}
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if res.Stored == nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "Invalid or expired refresh token")
		return
	}
	stored := res.Stored

	p, err := s.Principals.FindByID(r.Context(), stored.PrincipalID)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if p == nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "Principal not found")
		return
	}
	if !p.Active {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "Account is not active")
		return
	}

	// Re-narrow against the CURRENT ceiling intersected with the originally
	// granted scope: roles may have shrunk since the grant (the refreshed
	// token must reflect that) but a refresh can never widen scope.
	granted, _, err := s.grantedScope(r.Context(), p, strings.Join(stored.Scopes, " "))
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	accessToken, err := s.Auth.GenerateAccessTokenWithScope(p, granted)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}

	// ID token only when the original scope had openid AND we have a real
	// client_id for the audience. Non-fatal on failure.
	var idToken *string
	if scopesContain(stored.Scopes, "openid") && stored.OAuthClientID != nil {
		if t, err := s.Auth.GenerateIDToken(p, *stored.OAuthClientID, nil); err == nil {
			idToken = &t
		}
	}

	// The rotated refresh token (scopes + client binding + family preserved,
	// replaced-by link recorded) was issued inside grantstore.Rotate.
	var scope *string
	if len(stored.Scopes) > 0 {
		j := strings.Join(stored.Scopes, " ")
		scope = &j
	}

	writeToken(w, tokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		RefreshToken: &res.NewRaw,
		IDToken:      idToken,
		Scope:        scope,
	})
}

// ─── PKCE ────────────────────────────────────────────────────────────────

// verifyPKCE validates code_verifier against the stored challenge.
// Supports S256 (default) and plain. Mirrors RFC 7636 + Rust's checks.
func verifyPKCE(challenge string, method *string, verifier string) *oauthError {
	if verifier == "" {
		return newOAuthError(http.StatusBadRequest, "invalid_grant", "Missing code_verifier")
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		return newOAuthError(http.StatusBadRequest, "invalid_grant", "code_verifier must be 43-128 characters")
	}
	for i := 0; i < len(verifier); i++ {
		if !isUnreserved(verifier[i]) {
			return newOAuthError(http.StatusBadRequest, "invalid_grant", "code_verifier contains invalid characters")
		}
	}
	// RFC 7636 §4.3's "absent method ⇒ plain" default is applied at mint time
	// (authorize.go persists an explicit method), so a real code always carries
	// one here. This default therefore only guards malformed/corrupt input,
	// where the stricter S256 (preimage-resistant) interpretation is safer than
	// a plain string compare.
	m := "S256"
	if method != nil && *method != "" {
		m = *method
	}
	var computed string
	if m == "S256" {
		sum := sha256.Sum256([]byte(verifier))
		computed = base64.RawURLEncoding.EncodeToString(sum[:])
	} else {
		computed = verifier
	}
	// Constant-time: the challenge is attacker-visible only at authorize
	// time, but the plain method compares the raw verifier — don't leak
	// match-prefix timing either way.
	if subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) != 1 {
		return newOAuthError(http.StatusBadRequest, "invalid_grant", "Invalid code_verifier")
	}
	return nil
}

func isUnreserved(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	case b == '-' || b == '.' || b == '_' || b == '~':
		return true
	default:
		return false
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────

// grantAllowed reports whether the client's registered grant-type
// allowlist (oauth_client_grant_types, managed via the admin API) permits
// the given grant. An empty allowlist means the client never declared one
// — e.g. registered before grant-type enforcement existed — and is treated
// as unrestricted so those clients keep working; a non-empty list is
// enforced strictly. Shared by /oauth/authorize (authorization_code) and
// /oauth/token (all grants).
func grantAllowed(client *auth.OAuthClient, grant string) bool {
	if client == nil || len(client.GrantTypes) == 0 {
		return true
	}
	for _, g := range client.GrantTypes {
		if g == grant {
			return true
		}
	}
	return false
}

// oidcReservedScopes are OAuth/OIDC flow scopes that are NOT authorization
// permissions. They drive separate behaviour (openid → id_token,
// offline_access → refresh_token) and are excluded from permission-scope
// narrowing so a login requesting only "openid profile" isn't mistaken for a
// permission request that intersects the ceiling to nothing.
var oidcReservedScopes = map[string]struct{}{
	"openid": {}, "profile": {}, "email": {}, "address": {}, "phone": {}, "offline_access": {},
}

// grantedScope computes the permission set to embed in a minted access token's
// "scope" claim: the principal's full permission ceiling (flattened from its
// roles), optionally narrowed to the intersection with the requested
// permission scopes.
//
//   - no permission scopes requested (OIDC flow scopes aside) → the full
//     ceiling, the conventional client_credentials / login default.
//   - permission scopes requested → only those the ceiling authorises;
//     requests for permissions the principal lacks are silently dropped, so a
//     scope request can never escalate beyond the principal's roles. Anchor
//     principals have an unbounded ceiling, so their requests pass through
//     verbatim. explicit reports whether any permission scopes were requested,
//     so callers can reject an empty intersection with invalid_scope.
//
// Returns (nil, false, nil) when scope support is unwired (FlattenPermissions
// nil): callers then mint with no scope claim (legacy behaviour, permissions
// derived downstream from roles).
func (s *State) grantedScope(ctx context.Context, p *principal.Principal, requested string) (granted []string, explicit bool, err error) {
	if s.FlattenPermissions == nil {
		return nil, false, nil
	}
	ceiling, err := s.FlattenPermissions(ctx, roleNamesOf(p))
	if err != nil {
		return nil, false, err
	}
	var reqPerms []string
	for _, f := range strings.Fields(requested) {
		if _, reserved := oidcReservedScopes[f]; reserved {
			continue
		}
		reqPerms = append(reqPerms, f)
	}
	if len(reqPerms) == 0 {
		return ceiling, false, nil
	}
	anchor := p.Scope == principal.ScopeAnchor
	granted = make([]string, 0, len(reqPerms))
	for _, r := range reqPerms {
		if anchor || sharedauth.Grants(ceiling, r) {
			granted = append(granted, r)
		}
	}
	return granted, true, nil
}

// roleNamesOf extracts a principal's assigned role names.
func roleNamesOf(p *principal.Principal) []string {
	out := make([]string, 0, len(p.Roles))
	for _, ra := range p.Roles {
		out = append(out, ra.Role)
	}
	return out
}

// scopeResponse renders a granted permission set as the optional response
// `scope` field, nil when empty.
func scopeResponse(granted []string) *string {
	if len(granted) == 0 {
		return nil
	}
	j := strings.Join(granted, " ")
	return &j
}

func scopeHas(scope, want string) bool { return scopesContain(strings.Fields(scope), want) }

func scopesContain(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// oauthError is an RFC-6749 error body plus its HTTP status.
type oauthError struct {
	status      int
	Code        string  `json:"error"`
	Description *string `json:"error_description,omitempty"`
}

func newOAuthError(status int, code, description string) *oauthError {
	e := &oauthError{status: status, Code: code}
	if description != "" {
		e.Description = &description
	}
	return e
}

func (e *oauthError) write(w http.ResponseWriter) {
	writeOAuthError(w, e.status, e.Code, derefOr(e.Description, ""))
}

// writeOAuthRateLimited emits a 429 in the RFC-6749 error shape the token
// endpoint uses ({"error":"rate_limit_exceeded", "error_description":...})
// with a Retry-After header — matching Rust's rate-limit rejection on
// /oauth/token, distinct from the platform {"error":"TOO_MANY_REQUESTS"}
// envelope used by non-OAuth endpoints.
func writeOAuthRateLimited(w http.ResponseWriter, retryAfterSecs uint32, description string) {
	if retryAfterSecs < 1 {
		retryAfterSecs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatUint(uint64(retryAfterSecs), 10))
	writeOAuthError(w, http.StatusTooManyRequests, "rate_limit_exceeded", description)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	body := map[string]any{"error": code}
	if description != "" {
		body["error_description"] = description
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeToken(w http.ResponseWriter, resp tokenResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
