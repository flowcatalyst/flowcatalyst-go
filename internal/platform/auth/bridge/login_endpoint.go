package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/oauthapi"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	principalops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	platformmw "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/middleware"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// LoginEndpoint serves the OIDC bridge HTTP surface:
//
//	GET  /auth/oidc/login
//	GET  /auth/oidc/callback
//
// The paths match Rust fc-platform (`/auth/oidc/*`) so the frontend and
// Rust clients work against either backend without per-implementation
// hacks.
//
// /auth/check-domain is owned by the login package, not the bridge —
// it does the email-domain-mapping lookup and only needs the bridge for
// the redirect URL it returns.
//
// The handlers do the redirect-and-exchange dance; the per-request
// session-cookie write (and any consent UI) is owned by the
// SessionWriter callback the platform server installs.
type LoginEndpoint struct {
	bridge       *Bridge
	states       *LoginStateRepo
	principals   *principal.Repository
	mappings     *emaildomainmapping.Repository
	roles        *role.Repository
	idpMappings  *auth.IdpRoleMappingRepo
	uow          *usecasepgx.UnitOfWork
	oauthClients *auth.OAuthClientRepo

	// SessionWriter is called after the callback exchange completes
	// successfully. It receives the resolved principal ID + the
	// redirect-back URL the front-channel should land on next, and must
	// either set a session cookie + redirect, or render a server-rendered
	// response. The default implementation just emits a 200 with the
	// principal ID — replace at startup.
	SessionWriter func(w http.ResponseWriter, r *http.Request, principalID string, returnURL string)
}

// NewLoginEndpoint wires the bridge HTTP handlers. The mappings repo
// and UoW power the auto-provisioning path in handleCallback — when a
// successful OIDC handshake yields an email that doesn't match a
// FlowCatalyst principal, the bridge creates one using the scope and
// primary-client-id carried by the matching email-domain mapping. The
// idpMappings repo + roles repo power the IDP role-sync that runs on
// every callback (both new and existing users): the roles claim is
// translated through oauth_idp_role_mappings, filtered through the
// mapping's allowed_role_ids, and applied with source=IDP_SYNC so
// admin-assigned roles aren't trampled. Drop-in parity with Rust's
// sync_oidc_login_with_allowed_roles.
func NewLoginEndpoint(
	b *Bridge,
	states *LoginStateRepo,
	principals *principal.Repository,
	mappings *emaildomainmapping.Repository,
	roles *role.Repository,
	idpMappings *auth.IdpRoleMappingRepo,
	uow *usecasepgx.UnitOfWork,
	oauthClients *auth.OAuthClientRepo,
) *LoginEndpoint {
	return &LoginEndpoint{
		bridge:       b,
		states:       states,
		principals:   principals,
		mappings:     mappings,
		roles:        roles,
		idpMappings:  idpMappings,
		uow:          uow,
		oauthClients: oauthClients,
		SessionWriter: func(w http.ResponseWriter, _ *http.Request, principalID string, returnURL string) {
			if returnURL != "" {
				http.Redirect(w, nil, returnURL, http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"principalId": principalID})
		},
	}
}

// RegisterRoutes mounts the OIDC bridge endpoints. /auth/check-domain
// is intentionally NOT registered here — the login package owns that
// path and uses identity_provider / email_domain_mapping to compute
// the redirect URL the SPA follows back into this bridge.
func (e *LoginEndpoint) RegisterRoutes(r chi.Router) {
	r.Get("/auth/oidc/login", e.handleLogin)
	r.Get("/auth/oidc/callback", e.handleCallback)
	r.Get("/auth/oidc/session/end", e.handleSessionEnd)
}

// handleSessionEnd implements OIDC RP-Initiated Logout 1.0 — a 1:1 port of
// Rust oidc_login_api.rs::session_end. It always clears the fc_session
// cookie, and when a post_logout_redirect_uri is supplied it verifies the
// URI is registered for the requesting client before redirecting. The
// client is identified via the unverified `aud` claim of id_token_hint;
// the registered whitelist (not the token signature) is the security
// boundary, so a URI we cannot tie to a client is refused rather than
// redirected to (CWE-601 open-redirect defence).
func (e *LoginEndpoint) handleSessionEnd(w http.ResponseWriter, r *http.Request) {
	// Always clear the session cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     platformmw.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	q := r.URL.Query()
	redirectURI := q.Get("post_logout_redirect_uri")
	idTokenHint := q.Get("id_token_hint")
	state := q.Get("state")

	// No redirect requested — session ended.
	if redirectURI == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Session ended"})
		return
	}

	reject := func(reason string) {
		slog.Warn("rejected post_logout_redirect_uri", "redirect_uri", redirectURI, "reason", reason)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_request",
			"error_description": "Invalid post_logout_redirect_uri: " + reason,
		})
	}

	if idTokenHint == "" {
		reject("id_token_hint is required to verify post_logout_redirect_uri")
		return
	}
	clientID := extractAudFromIDTokenHint(idTokenHint)
	if clientID == "" {
		reject("id_token_hint is malformed")
		return
	}
	client, err := e.oauthClients.FindByClientID(r.Context(), clientID)
	if err != nil {
		reject("internal error verifying client")
		return
	}
	if client == nil {
		reject("id_token_hint audience does not match any registered client")
		return
	}
	if !oauthapi.MatchesRedirectURI(redirectURI, client.PostLogoutRedirectURIs) {
		reject("not in the client's registered post_logout_redirect_uris")
		return
	}

	target := redirectURI
	if state != "" {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + "state=" + url.QueryEscape(state)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// extractAudFromIDTokenHint pulls the `aud` (client_id) claim from an
// id_token_hint WITHOUT verifying its signature — the registered
// post_logout_redirect_uris whitelist is the security boundary, not the
// hint. Returns "" on any structural malformation. 1:1 with Rust
// extract_aud_from_id_token_hint. `aud` may be a string or an array of
// strings (OIDC Core §2); for an array the first entry is used.
func extractAudFromIDTokenHint(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(claims.Aud, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(claims.Aud, &arr); err == nil && len(arr) > 0 {
		return arr[0]
	}
	return ""
}

// handleLogin starts an OIDC login. Takes ?domain=X (required) and
// optional ?return_url=Y. Generates state/nonce/PKCE verifier, persists
// the state row, then 302-redirects to the IDP's authorize URL. Matches
// Rust's /auth/oidc/login signature (snake_case `return_url`,
// `domain` over `email` so users don't leak the local part).
func (e *LoginEndpoint) handleLogin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	domain := q.Get("domain")
	if domain == "" {
		// Back-compat: also accept ?email= and derive the domain.
		if email := q.Get("email"); email != "" {
			domain = emailDomain(email)
		}
	}
	returnURL := q.Get("return_url")
	if returnURL == "" {
		returnURL = q.Get("returnUrl") // tolerate camelCase legacy callers
	}
	if domain == "" {
		httperror.Write(w, httperror.BadRequest("DOMAIN_REQUIRED", "domain query param is required"))
		return
	}

	// Resolve uses email; synthesise one with a throwaway local-part.
	resolved, cfg, err := e.bridge.ResolveForEmail(r.Context(), "x@"+domain)
	if err != nil || resolved == nil {
		httperror.Write(w, httperror.BadRequest("OIDC_NOT_CONFIGURED",
			"OIDC is not configured for this domain"))
		return
	}

	state := randString(32)
	nonce := randString(32)
	verifier := randString(64)
	challenge := pkceChallenge(verifier)
	loginState := NewLoginState(state, domain,
		"", // identityProviderID — populated once the IDP table is wired in
		cfg.ID, nonce, verifier)
	if returnURL != "" {
		loginState.ReturnURL = &returnURL
	}
	if err := e.states.Insert(r.Context(), loginState); err != nil {
		httperror.Write(w, usecase.Internal("OIDC_STATE", "persist state failed", err))
		return
	}

	redirectURI := absoluteCallbackURL(r)
	authURL := resolved.AuthCodeURL(state, redirectURI) +
		"&nonce=" + url.QueryEscape(nonce) +
		"&code_challenge=" + url.QueryEscape(challenge) +
		"&code_challenge_method=S256"
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleCallback completes the login: validate state, exchange code,
// verify ID token, resolve/create the FlowCatalyst principal, and hand
// off to SessionWriter.
func (e *LoginEndpoint) handleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		httperror.Write(w, httperror.BadRequest("MISSING_PARAM", "state and code are required"))
		return
	}

	loginState, err := e.states.FindByState(r.Context(), state)
	if err != nil {
		httperror.Write(w, usecase.Internal("OIDC_STATE", "lookup state failed", err))
		return
	}
	if loginState == nil {
		httperror.Write(w, httperror.BadRequest("INVALID_STATE", "unknown state"))
		return
	}
	if loginState.IsExpired() {
		_ = e.states.Delete(r.Context(), state)
		httperror.Write(w, httperror.BadRequest("STATE_EXPIRED", "login state expired"))
		return
	}

	resolved, _, err := e.bridge.ResolveForEmail(r.Context(),
		"x@"+loginState.EmailDomain)
	if err != nil || resolved == nil {
		httperror.Write(w, httperror.BadRequest("OIDC_NOT_CONFIGURED", "OIDC config lost"))
		return
	}

	redirectURI := absoluteCallbackURL(r)
	tok, err := resolved.Exchange(r.Context(), code, redirectURI)
	if err != nil {
		httperror.Write(w, usecase.Internal("OIDC_EXCHANGE", "code exchange failed", err))
		return
	}
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		httperror.Write(w, httperror.BadRequest("NO_ID_TOKEN", "IDP did not return id_token"))
		return
	}
	idToken, err := resolved.VerifyIDToken(r.Context(), rawIDToken)
	if err != nil {
		httperror.Write(w, usecase.Authorization("OIDC_VERIFY", "id_token verification failed: "+err.Error()))
		return
	}
	var claims struct {
		Email string   `json:"email"`
		Nonce string   `json:"nonce"`
		Roles []string `json:"roles"`
	}
	if err := idToken.Claims(&claims); err != nil {
		httperror.Write(w, httperror.BadRequest("OIDC_CLAIMS", "id_token claims malformed"))
		return
	}
	if claims.Nonce != loginState.Nonce {
		httperror.Write(w, usecase.Authorization("NONCE_MISMATCH", "nonce did not match"))
		return
	}

	// Resolve or create the FlowCatalyst principal. Drop-in parity with
	// Rust's sync_oidc_login_with_allowed_roles: lookup by email; if
	// missing, auto-provision using the scope + primary-client-id from
	// the email-domain mapping. Then translate IDP roles → platform
	// roles (filtered by the mapping's allowed_role_ids) and apply via
	// SyncIdpRoles. Existing users get the same role sync — if HR
	// removed someone from a group upstream, their next login drops the
	// corresponding platform role.
	p, err := e.principals.FindByEmail(r.Context(), claims.Email)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "principal lookup failed", err))
		return
	}
	if p == nil {
		p, err = e.autoProvision(r.Context(), claims.Email, loginState.EmailDomainMappingID)
		if err != nil {
			httperror.Write(w, err)
			return
		}
	}
	if err := e.syncIdpRoles(r.Context(), p, claims.Roles, loginState.EmailDomainMappingID); err != nil {
		// Role sync failure shouldn't block login — the principal is
		// already valid. Log and continue with whatever role set is in
		// place. Mirrors Rust's behaviour where the role sync is a
		// best-effort step after auth.
		slog.Warn("OIDC role sync failed; continuing without role update",
			"principalId", p.ID, "err", err)
	}

	// Best-effort cleanup of the state row.
	_ = e.states.Delete(r.Context(), state)

	returnURL := ""
	if loginState.ReturnURL != nil {
		returnURL = *loginState.ReturnURL
	}
	e.SessionWriter(w, r, p.ID, returnURL)
}

// autoProvision creates a Principal for `email` using the scope +
// primary-client-id carried by the EmailDomainMapping that drove this
// login. Returns the newly-created Principal, or an error suitable for
// surfacing to the user. The mapping ID is the same one the bridge
// resolved at login-time and persisted in the login_state row.
//
// Roles are intentionally NOT assigned here. Rust calls
// sync_oidc_login_with_allowed_roles to apply IDP-claim-derived role
// mappings; that step is a follow-up. The provisioned user has no
// roles and will only be useful for flows that don't depend on
// platform-permission gating (typical first-login UX).
func (e *LoginEndpoint) autoProvision(ctx context.Context, email, mappingID string) (*principal.Principal, error) {
	mapping, err := e.mappings.FindByID(ctx, mappingID)
	if err != nil {
		return nil, usecase.Internal("REPO", "email_domain_mapping lookup failed", err)
	}
	if mapping == nil {
		return nil, usecase.Authorization("MAPPING_GONE",
			"The email-domain mapping that drove this login no longer exists; cannot auto-provision")
	}

	idpType := "OIDC"
	cmd := principalops.CreateCommand{
		Email:    email,
		Scope:    string(mapping.ScopeType),
		ClientID: mapping.PrimaryClientID,
		IDPType:  &idpType,
	}
	// The execution context's PrincipalID is empty — the new user is
	// being created by the system in response to a self-service login,
	// not by an authenticated actor. Audit rows will record an empty
	// principal, matching the Rust convention for self-provisioning.
	ec := usecase.NewExecutionContext("")
	committed, err := principalops.CreateUser(ctx, e.principals, e.uow, cmd, ec)
	if err != nil {
		return nil, err
	}
	created, err := e.principals.FindByID(ctx, committed.Event().UserID)
	if err != nil {
		return nil, usecase.Internal("REPO", "post-create principal lookup failed", err)
	}
	if created == nil {
		// Shouldn't happen — Persist just succeeded.
		return nil, usecase.Internal("REPO", "post-create principal missing", errors.New("not found"))
	}
	return created, nil
}

// syncIdpRoles translates the IDP `roles` claim through
// oauth_idp_role_mappings, filters by the EmailDomainMapping's
// allowed_role_ids (when non-empty), and applies the resulting
// platform-role set with source=IDP_SYNC. Preserves admin-assigned
// roles untouched.
//
// An empty claim is a valid input: the user lost every group
// upstream, so all their IDP-sourced platform roles should drop. The
// caller treats any error here as non-fatal — the principal is
// already authenticated; we just log and continue.
func (e *LoginEndpoint) syncIdpRoles(ctx context.Context, p *principal.Principal, idpRoles []string, mappingID string) error {
	mapping, err := e.mappings.FindByID(ctx, mappingID)
	if err != nil {
		return usecase.Internal("REPO", "email_domain_mapping lookup failed", err)
	}
	if mapping == nil {
		// Mapping vanished between login and callback. Drop all
		// IDP-sourced roles defensively — we can't validate the claim
		// without a mapping.
		return e.applySyncIdpRoles(ctx, p, nil)
	}

	// Load every IDP role mapping; in-memory filter. Mirrors Rust's
	// find_idp_role_mapping which doesn't filter by IDP type either.
	allMappings, err := e.idpMappings.FindAll(ctx)
	if err != nil {
		return usecase.Internal("REPO", "idp_role_mappings list failed", err)
	}
	byIdpRoleName := make(map[string]string, len(allMappings))
	for _, m := range allMappings {
		byIdpRoleName[m.IdpRoleName] = m.PlatformRoleName
	}

	allowed := mapping.AllowedRoleIDs
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, n := range allowed {
		allowedSet[n] = struct{}{}
	}
	hasAllowList := len(allowed) > 0

	authorized := make(map[string]struct{}, len(idpRoles))
	for _, idpRole := range idpRoles {
		platformRole, ok := byIdpRoleName[idpRole]
		if !ok {
			// Unknown role — Rust logs this at warn as a security
			// rejection. Match that.
			slog.Warn("REJECTED unauthorized IDP role: not found in idp_role_mappings",
				"principalId", p.ID, "idpRole", idpRole, "email", principalEmail(p))
			continue
		}
		if hasAllowList {
			if _, ok := allowedSet[platformRole]; !ok {
				slog.Debug("skipped IDP role: not in email_domain_mapping allowed_role_ids",
					"principalId", p.ID, "idpRole", idpRole, "platformRole", platformRole)
				continue
			}
		}
		authorized[platformRole] = struct{}{}
	}
	platformRoles := make([]string, 0, len(authorized))
	for r := range authorized {
		platformRoles = append(platformRoles, r)
	}
	return e.applySyncIdpRoles(ctx, p, platformRoles)
}

func (e *LoginEndpoint) applySyncIdpRoles(ctx context.Context, p *principal.Principal, platformRoles []string) error {
	cmd := principalops.SyncIdpRolesCommand{
		UserID:        p.ID,
		PlatformRoles: platformRoles,
	}
	// The actor here is the system, not an authenticated user — match
	// the auto-provision pattern of an empty principal id.
	ec := usecase.NewExecutionContext("")
	if _, err := principalops.SyncIdpRoles(ctx, e.principals, e.roles, e.uow, cmd, ec); err != nil {
		return err
	}
	return nil
}

// principalEmail returns the user-identity email, or empty string when
// the principal has no UserIdentity attached (shouldn't happen for
// OIDC-resolved principals but defends against the nil case).
func principalEmail(p *principal.Principal) string {
	if p == nil || p.UserIdentity == nil {
		return ""
	}
	return p.UserIdentity.Email
}

// ── helpers ──────────────────────────────────────────────────────────────

// absoluteCallbackURL derives the public-facing /auth/oidc/callback
// URL the IDP will redirect to. Prefers the X-Forwarded-* headers when
// the platform is behind a load balancer.
func absoluteCallbackURL(r *http.Request) string {
	scheme := "https"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host + "/auth/oidc/callback"
}

// randString returns a URL-safe base64 string with at least n bytes of
// entropy. Crypto-grade.
func randString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkceChallenge returns the S256 PKCE challenge for the given verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Compile-time guard: ensure the context import stays live as the
// callback expands.
var _ = context.Background
var _ = errors.New
