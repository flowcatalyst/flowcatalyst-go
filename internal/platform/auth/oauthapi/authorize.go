package oauthapi

import (
	"crypto/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/authservice"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/grantstore"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/ratelimit"
)

// RegisterAuthorizeRoutes mounts GET /oauth/authorize. It MUST be mounted
// OUTSIDE the session-auth middleware: an absent/expired session is a
// redirect-to-login case here, not a 401.
func (s *State) RegisterAuthorizeRoutes(r chi.Router) {
	r.Get("/oauth/authorize", s.Authorize)
}

// Authorize is GET /oauth/authorize (OAuth2 authorization-code flow with
// PKCE), a 1:1 port of oauth_api.rs::authorize. When the caller already
// has a valid session it issues a code and redirects to redirect_uri;
// otherwise it stashes the request and redirects to the SPA login page.
func (s *State) Authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	responseType := q.Get("response_type")
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	scope := q.Get("scope")
	stateParam := q.Get("state")
	nonce := q.Get("nonce")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	providerID := q.Get("provider")
	prompt := q.Get("prompt")
	maxAge := q.Get("max_age")

	if responseType != "code" {
		errorRedirect(w, r, redirectURI, "unsupported_response_type", "Only 'code' response type is supported", stateParam)
		return
	}
	// `state` is mandatory for CSRF protection on the callback. Reject with
	// 400 (not a redirect) — we can't safely bounce the UA without it.
	if strings.TrimSpace(stateParam) == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "`state` parameter is required for CSRF protection")
		return
	}

	// Cluster-wide per-client_id throttle, before the DB lookup.
	if rej := ratelimit.Enforce(r.Context(), s.RateLimit, ratelimit.BucketOAuthAuthorizeClient, clientID, s.RateLimitPolicies.OAuthAuthorizeClient); rej != nil {
		ratelimit.WriteTooManyRequests(w, rej.RetryAfterSecs, "rate limit exceeded")
		return
	}

	client, err := s.OAuthClients.FindByClientID(r.Context(), clientID)
	switch {
	case err != nil:
		errorRedirect(w, r, redirectURI, "server_error", "Internal error", stateParam)
		return
	case client == nil:
		errorRedirect(w, r, redirectURI, "unauthorized_client", "Unknown client", stateParam)
		return
	case !client.Active:
		errorRedirect(w, r, redirectURI, "unauthorized_client", "Client is not active", stateParam)
		return
	}

	if !matchesRedirectURI(redirectURI, client.RedirectURIs) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Invalid redirect_uri")
		return
	}
	if client.PKCERequired && codeChallenge == "" {
		errorRedirect(w, r, redirectURI, "invalid_request", "PKCE code_challenge is required", stateParam)
		return
	}
	if codeChallengeMethod != "" && codeChallengeMethod != "S256" && codeChallengeMethod != "plain" {
		errorRedirect(w, r, redirectURI, "invalid_request", "Invalid code_challenge_method", stateParam)
		return
	}
	if scope != "" {
		if invalid := invalidScopes(scope, client.Scopes); len(invalid) > 0 {
			errorRedirect(w, r, redirectURI, "invalid_scope", "Invalid scope(s): "+strings.Join(invalid, ", "), stateParam)
			return
		}
	}

	// Resolve the session once. A session older than max_age must
	// re-authenticate (OIDC Core §3.1.2.1), so treat it as stale below.
	sessTok := s.sessionToken(r)
	var sessSubject string
	var sessIssuedAt time.Time
	sessOK := false
	if sessTok != "" && s.ValidateSession != nil {
		sessSubject, sessIssuedAt, sessOK = s.ValidateSession(sessTok)
	}
	sessionStale := sessOK && maxAgeExceeded(maxAge, sessIssuedAt)

	// prompt handling (OIDC Core §3.1.2.1).
	forceLogin := false
	switch prompt {
	case "none":
		// prompt=none forbids any UI — a missing or stale session is an error.
		if !sessOK || sessionStale {
			errorRedirect(w, r, redirectURI, "login_required", "User is not authenticated", stateParam)
			return
		}
	case "login":
		forceLogin = true
	}

	// Authenticated, fresh session → issue the code immediately.
	if !forceLogin && sessOK && !sessionStale {
		code := grantstore.NewAuthorizationCode(randomString(64), clientID, sessSubject, redirectURI)
		code.Scope = strPtrOrNil(scope)
		code.Nonce = strPtrOrNil(nonce)
		code.State = strPtrOrNil(stateParam)
		if codeChallenge != "" && codeChallengeMethod != "" {
			code.CodeChallenge = &codeChallenge
			code.CodeChallengeMethod = &codeChallengeMethod
		}
		if err := s.AuthCodes.Insert(r.Context(), code); err != nil {
			errorRedirect(w, r, redirectURI, "server_error", "Failed to create authorization code", stateParam)
			return
		}
		redirectURL := redirectURI + "?code=" + pctEncode(code.Code) + "&state=" + pctEncode(stateParam)
		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
		return
	}

	// Not authenticated → stash the request and bounce to login.
	pending := &grantstore.PendingAuth{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Scope:               strPtrOrNil(scope),
		CodeChallenge:       strPtrOrNil(codeChallenge),
		CodeChallengeMethod: strPtrOrNil(codeChallengeMethod),
		Nonce:               strPtrOrNil(nonce),
		CreatedAt:           time.Now().UTC(),
	}
	if err := s.PendingAuth.Insert(r.Context(), stateParam, pending); err != nil {
		errorRedirect(w, r, redirectURI, "server_error", "Internal error", stateParam)
		return
	}

	if providerID != "" {
		// TODO(oidc-by-provider): the ?provider= direct-IDP entry point is
		// not wired — the Go OIDC bridge resolves IDPs by email domain, not
		// by provider id. The SPA's SSO path goes through /auth/oidc/login
		// instead. Port the Rust oidc_service.get_authorization_url path
		// (resolve IDP by id + build the authorization URL) to close this.
		errorRedirect(w, r, redirectURI, "server_error", "Direct provider authorization is not supported", stateParam)
		return
	}

	// Redirect to the SPA login page with the OAuth params so it can rebuild
	// the authorize URL after the user signs in.
	loginURL := "/auth/login?oauth=true&response_type=code" +
		"&client_id=" + pctEncode(clientID) +
		"&redirect_uri=" + pctEncode(redirectURI) +
		"&state=" + pctEncode(stateParam)
	if scope != "" {
		loginURL += "&scope=" + pctEncode(scope)
	}
	if codeChallenge != "" {
		loginURL += "&code_challenge=" + pctEncode(codeChallenge)
	}
	if codeChallengeMethod != "" {
		loginURL += "&code_challenge_method=" + pctEncode(codeChallengeMethod)
	}
	if nonce != "" {
		loginURL += "&nonce=" + pctEncode(nonce)
	}
	http.Redirect(w, r, loginURL, http.StatusTemporaryRedirect)
}

// sessionToken pulls the session JWT from the fc_session cookie, falling
// back to the Authorization: Bearer header (cookie takes precedence, as
// in Rust).
func (s *State) sessionToken(r *http.Request) string {
	if c, err := r.Cookie("fc_session"); err == nil && c.Value != "" {
		return c.Value
	}
	return authservice.ExtractBearerToken(r.Header.Get("Authorization"))
}

// maxAgeExceeded reports whether the OIDC max_age (seconds) has elapsed
// since the session token was issued. An absent/invalid max_age, or an
// unknown issue time, is treated as "not exceeded" (lenient — max_age is
// optional and we never want to gratuitously force re-login).
func maxAgeExceeded(maxAge string, issuedAt time.Time) bool {
	if maxAge == "" || issuedAt.IsZero() {
		return false
	}
	secs, err := strconv.Atoi(maxAge)
	if err != nil || secs < 0 {
		return false
	}
	return time.Since(issuedAt) > time.Duration(secs)*time.Second
}

// ─── redirect-uri matching (1:1 with Rust) ──────────────────────────────

func matchesRedirectURI(uri string, registered []string) bool {
	for _, r := range registered {
		if r == uri {
			return true
		}
	}
	for _, pattern := range registered {
		if strings.Contains(pattern, "*") && wildcardMatches(uri, pattern) {
			return true
		}
	}
	return false
}

// wildcardMatches matches uri against a pattern with `*` wildcards, where
// each `*` matches exactly one path/subdomain segment (no dots, non-empty).
func wildcardMatches(uri, pattern string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 0 {
		return false
	}
	remaining, ok := strings.CutPrefix(uri, parts[0])
	if !ok {
		return false
	}
	for i, part := range parts[1:] {
		isLast := i == len(parts)-2
		if isLast {
			if !strings.HasSuffix(remaining, part) {
				return false
			}
			seg := remaining[:len(remaining)-len(part)]
			if strings.Contains(seg, ".") || seg == "" {
				return false
			}
			return true
		}
		pos := strings.Index(remaining, part)
		if pos < 0 {
			return false
		}
		seg := remaining[:pos]
		if strings.Contains(seg, ".") || seg == "" {
			return false
		}
		remaining = remaining[pos+len(part):]
	}
	return !strings.Contains(remaining, ".") && remaining != ""
}

// ─── helpers ─────────────────────────────────────────────────────────────

// errorRedirect bounces the user-agent back to redirect_uri with the OAuth
// error params (302-equivalent temporary redirect, matching Rust).
func errorRedirect(w http.ResponseWriter, r *http.Request, redirectURI, errCode, desc, state string) {
	url := redirectURI + "?error=" + pctEncode(errCode) + "&error_description=" + pctEncode(desc)
	if state != "" {
		url += "&state=" + pctEncode(state)
	}
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func invalidScopes(scope string, clientScopes []string) []string {
	standard := map[string]bool{"openid": true, "profile": true, "email": true, "offline_access": true}
	var invalid []string
	for _, sc := range strings.Fields(scope) {
		if standard[sc] {
			continue
		}
		found := false
		for _, ds := range clientScopes {
			if ds == sc {
				found = true
				break
			}
		}
		if !found {
			invalid = append(invalid, sc)
		}
	}
	return invalid
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const randomCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// randomString returns a cryptographically-random alphanumeric string.
func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = randomCharset[int(b[i])%len(randomCharset)]
	}
	return string(b)
}

// pctEncode percent-encodes per RFC 3986 (unreserved set preserved),
// matching Rust's urlencoding::encode (space → %20, not '+').
func pctEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0F])
		}
	}
	return b.String()
}
