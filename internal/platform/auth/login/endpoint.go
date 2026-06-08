// Package login wires the SPA's session-cookie authentication surface:
//
//	POST /auth/check-domain     — does this email use password or SSO?
//	POST /auth/login            — password → session cookie + JSON principal
//	POST /auth/logout           — clear the session cookie
//	GET  /auth/me               — read the current cookie, return principal
//
// Mirrors crates/fc-platform/src/auth/auth_api.rs. The session cookie
// is a JWT minted via provider.MintSessionToken so the existing
// platformmw.Authenticator can verify it identically to a Bearer
// Authorization header.
package login

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/audit"
	platformauth "github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/authservice"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/grantstore"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/loginbackoff"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/mfatoken"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/passwordhash"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/provider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/loginattempt"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/mfa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/notify"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	platformmw "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/middleware"
)

// SessionTTL is the cookie lifetime fc-server uses. Matches the Rust
// session_token_expiry_secs default — except the user picked 24h flat
// for both dev and regular use, so we hardcode it here rather than
// thread it through env. Override at link time if you need to.
var SessionTTL = 24 * time.Hour

// Config bundles the dependencies the handlers need.
type Config struct {
	Provider          *provider.Provider
	Principals        *principal.Repository
	Mappings          *emaildomainmapping.Repository
	IdentityProviders *identityprovider.Repository

	// RefreshTokens + Auth back POST /auth/refresh (dashboard refresh-token
	// rotation). Optional: when either is nil the route is not registered.
	RefreshTokens *grantstore.RefreshTokenRepository
	Auth          *authservice.AuthService

	// CookieSecure flips the cookie Secure flag. False in fc-dev (HTTP
	// localhost). True in fc-server (HTTPS).
	CookieSecure bool

	// LoginAttempts records USER_LOGIN outcomes and feeds the brute-force
	// backoff. Optional (nil disables backoff + recording).
	LoginAttempts *loginattempt.Repository
	// BackoffPolicy tunes the failed-login backoff/ceiling.
	BackoffPolicy loginbackoff.Policy

	// MFA + MFATokens back the 2FA flow. When MFA or MFATokens is nil the
	// /auth/2fa/* routes are not mounted and login never challenges (2FA
	// disabled). A passkey does NOT exempt the password path: a passkey user
	// who chooses to sign in with a password must still complete 2FA — so the
	// webauthn repo plays no part here (passkey LOGIN bypasses 2FA on its own
	// route).
	MFA       *mfa.Service
	MFATokens *mfatoken.Issuer
	// Notifier sends best-effort 2FA security emails (enrolled, recovery codes,
	// trusted device). Optional — nil is a safe no-op.
	Notifier *notify.Notifier
	// Audit (optional) records 2FA state changes (enroll / remove / regenerate)
	// to the audit trail. Challenge success/failure is already in login attempts.
	Audit *audit.Repository
	// PendingTokenTTL / EnrollTokenTTL bound the short-lived between-step
	// tokens. Zero falls back to defaults (10m / 30m).
	PendingTokenTTL time.Duration
	EnrollTokenTTL  time.Duration
}

// Endpoint is the bag of HTTP handlers.
type Endpoint struct{ cfg Config }

// New builds an Endpoint.
func New(cfg Config) *Endpoint { return &Endpoint{cfg: cfg} }

// RegisterRoutes mounts the four endpoints on r. Convenience that calls
// both RegisterPublicRoutes and RegisterAuthenticatedRoutes on the same
// router — callers that DON'T put the router behind an auth middleware
// can use this. fc-server/fc-dev split the two to keep check-domain +
// login outside the auth gate.
func (e *Endpoint) RegisterRoutes(r chi.Router) {
	e.RegisterPublicRoutes(r)
	e.RegisterAuthenticatedRoutes(r)
}

// RegisterPublicRoutes mounts the endpoints that must be reachable
// without an existing session:
//
//	POST /auth/check-domain  — pre-flight to choose password vs SSO
//	POST /auth/login         — issues the session cookie
//	POST /auth/logout        — accepts stale/missing cookies (clears anyway)
//
// Callers should mount these OUTSIDE any bearer-token middleware so a
// stale fc_session cookie from a previous run doesn't 401 the request
// before the SPA can re-authenticate.
func (e *Endpoint) RegisterPublicRoutes(r chi.Router) {
	r.Post("/auth/check-domain", e.handleCheckDomain)
	// GET variant mirrors Rust's auth_api.rs check_domain — a distinct
	// (legacy) query-param shape. See handleCheckDomainQuery.
	r.Get("/auth/check-domain", e.handleCheckDomainQuery)
	r.Post("/auth/login", e.handleLogin)
	r.Post("/auth/logout", e.handleLogout)
	// 2FA challenge + enrollment endpoints (no-op when MFA isn't wired).
	e.RegisterTwoFactorRoutes(r)
	// /auth/refresh rotates a refresh token without an existing session, so
	// it lives on the public router. Only mounted when its deps are wired.
	if e.cfg.RefreshTokens != nil && e.cfg.Auth != nil {
		r.Post("/auth/refresh", e.handleRefresh)
	}
}

// RegisterAuthenticatedRoutes mounts the endpoint that requires an
// authenticated context. Mount on a router scoped to the auth middleware
// so the AuthContext is populated by the time handleMe runs.
func (e *Endpoint) RegisterAuthenticatedRoutes(r chi.Router) {
	r.Get("/auth/me", e.handleMe)
	// Self-service password change (Profile screen). Requires the current
	// password and — when the user has 2FA enrolled — a current second factor.
	r.Post("/auth/change-password", e.handleChangePassword)
	r.Post("/auth/change-password/send-email-code", e.handleChangePasswordSendEmailCode)
	// Session-gated 2FA self-service (Profile screen). No-op when MFA unwired.
	e.RegisterTwoFactorSelfServiceRoutes(r)
}

// ── /auth/check-domain ───────────────────────────────────────────────────

// checkDomainResponse matches what the SPA expects (auth.ts):
//
//	{ "authMethod": "internal" | "external", "loginUrl"?, "idpIssuer"? }
type checkDomainResponse struct {
	AuthMethod string `json:"authMethod"`
	LoginURL   string `json:"loginUrl,omitempty"`
	IDPIssuer  string `json:"idpIssuer,omitempty"`
}

func (e *Endpoint) handleCheckDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		httperror.Write(w, httperror.BadRequest("EMAIL_REQUIRED", "email is required"))
		return
	}
	at := strings.IndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		// Don't leak that the domain is malformed — fall back to internal
		// so the SPA shows a password prompt.
		writeJSON(w, http.StatusOK, checkDomainResponse{AuthMethod: "internal"})
		return
	}
	domain := strings.ToLower(email[at+1:])

	edm, err := e.cfg.Mappings.FindByEmailDomain(r.Context(), domain)
	if err != nil || edm == nil {
		writeJSON(w, http.StatusOK, checkDomainResponse{AuthMethod: "internal"})
		return
	}
	idp, err := e.cfg.IdentityProviders.FindByID(r.Context(), edm.IdentityProviderID)
	if err != nil || idp == nil {
		writeJSON(w, http.StatusOK, checkDomainResponse{AuthMethod: "internal"})
		return
	}

	resp := checkDomainResponse{AuthMethod: "internal"}
	if idp.Type == identityprovider.TypeOIDC {
		resp.AuthMethod = "external"
		// LoginURL points to the OIDC bridge, which Rust mounts at
		// /auth/oidc/login. Pass the domain (not email) so we don't
		// leak the local-part through the redirect chain.
		resp.LoginURL = "/auth/oidc/login?domain=" + encodeURI(domain)
		if idp.OIDCIssuerURL != nil {
			resp.IDPIssuer = *idp.OIDCIssuerURL
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── GET /auth/check-domain (legacy query variant) ─────────────────────────

// domainCheckResponse mirrors Rust's DomainCheckResponse (auth_api.rs):
// {domain, authMethod, providerId?, authorizationUrl?} with authMethod in
// SCREAMING_SNAKE_CASE (INTERNAL|OIDC). Distinct from checkDomainResponse
// (the POST variant the SPA uses, which returns authMethod internal|external
// + loginUrl). Kept for parity with clients that use the GET form.
type domainCheckResponse struct {
	Domain           string  `json:"domain"`
	AuthMethod       string  `json:"authMethod"`
	ProviderID       *string `json:"providerId,omitempty"`
	AuthorizationURL *string `json:"authorizationUrl,omitempty"`
}

func (e *Endpoint) handleCheckDomainQuery(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	domain := ""
	if at := strings.IndexByte(email, '@'); at >= 0 && at < len(email)-1 {
		domain = strings.ToLower(email[at+1:])
	}
	resp := domainCheckResponse{Domain: domain, AuthMethod: "INTERNAL"}
	if domain != "" {
		if edm, err := e.cfg.Mappings.FindByEmailDomain(r.Context(), domain); err == nil && edm != nil {
			if idp, err := e.cfg.IdentityProviders.FindByID(r.Context(), edm.IdentityProviderID); err == nil && idp != nil {
				pid := idp.ID
				resp.ProviderID = &pid
				if idp.Type == identityprovider.TypeOIDC {
					resp.AuthMethod = "OIDC"
				}
				if idp.OIDCIssuerURL != nil {
					au := strings.TrimRight(*idp.OIDCIssuerURL, "/") + "/authorize"
					resp.AuthorizationURL = &au
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── POST /auth/refresh ─────────────────────────────────────────────────────

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// tokenRefreshResponse mirrors Rust's TokenRefreshResponse (camelCase).
type tokenRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"`
	RefreshToken string `json:"refreshToken"`
}

// handleRefresh exchanges a refresh token for a new access+refresh pair,
// rotating the presented token. 1:1 with Rust auth_api.rs::refresh_token:
// hash → find-valid → revoke → reissue (preserving accessible_clients).
func (e *Endpoint) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	if req.RefreshToken == "" {
		writeUnauthorized(w, "Invalid or expired refresh token")
		return
	}

	tokenHash := grantstore.HashToken(req.RefreshToken)
	stored, err := e.cfg.RefreshTokens.FindValidByHash(r.Context(), tokenHash)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	if stored == nil {
		writeUnauthorized(w, "Invalid or expired refresh token")
		return
	}

	// Rotate: revoke the presented token before issuing a replacement.
	if _, err := e.cfg.RefreshTokens.RevokeByHash(r.Context(), tokenHash); err != nil {
		httperror.Write(w, err)
		return
	}

	p, err := e.cfg.Principals.FindByID(r.Context(), stored.PrincipalID)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	if p == nil {
		writeUnauthorized(w, "Invalid or expired refresh token")
		return
	}
	if !p.Active {
		writeUnauthorized(w, "Account is not active")
		return
	}

	accessToken, err := e.cfg.Auth.GenerateAccessToken(p)
	if err != nil {
		httperror.Write(w, err)
		return
	}

	raw, entity, err := grantstore.GenerateTokenPair(p.ID)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	entity.AccessibleClients = stored.AccessibleClients
	if err := e.cfg.RefreshTokens.Insert(r.Context(), entity); err != nil {
		httperror.Write(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tokenRefreshResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		RefreshToken: raw,
	})
}

// ── /auth/login ──────────────────────────────────────────────────────────

type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	RememberMe bool   `json:"rememberMe"`
}

// loginResponse matches the Java/Rust LoginResponse the SPA expects,
// extended with `permissions` so route guards can run without a
// follow-up round-trip. Mirrors what was deferred-then-decided in the
// auth port: include the flattened permission set so the SPA's
// permission store + router guards have what they need at sign-in.
type loginResponse struct {
	// Status is "ok" for a completed login. The 2FA-pending responses use
	// twoFactorResponse with status "mfa_required" / "enrollment_required".
	Status      string   `json:"status"`
	PrincipalID string   `json:"principalId"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
	ClientID    *string  `json:"clientId"`
	// RecoveryCodes is populated only by the enroll-and-complete path, the one
	// time a freshly-generated backup-code set is returned to the user.
	RecoveryCodes []string `json:"recoveryCodes,omitempty"`
}

// buildPermissionList flattens the principal's roles into the permission
// list the SPA expects. Adds the literal "*" sentinel when the user
// holds the global wildcard so the SPA's `permissions.includes("*")`
// fast-path (in router/guards.ts) accepts the user without needing to
// duplicate the 4-segment pattern-matching here.
func buildPermissionList(claims *provider.Claims) []string {
	if claims == nil {
		return nil
	}
	out := append([]string(nil), claims.Permissions...)
	for _, p := range claims.Permissions {
		if p == "platform:*:*:*" {
			out = append(out, "*")
			break
		}
	}
	return out
}

func (e *Endpoint) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	email := strings.TrimSpace(req.Email)
	if email == "" || req.Password == "" {
		// Constant-shape error — don't leak which field is missing.
		writeUnauthorized(w, "Invalid credentials")
		return
	}
	ip := clientIP(r)

	// Brute-force backoff: per-(email, IP) exponential delay + per-email
	// global ceiling. Runs before credentials are evaluated.
	if e.cfg.LoginAttempts != nil {
		if d, err := loginbackoff.Check(r.Context(), e.cfg.LoginAttempts, e.cfg.BackoffPolicy, email, ip); err == nil && !d.Allowed {
			writeTooManyRequests(w, d.RetryAfterSecs)
			return
		}
	}

	// rejectInvalid records a failed USER_LOGIN attempt and returns 401.
	rejectInvalid := func() {
		e.recordAttempt(r.Context(), loginattempt.OutcomeFailure, email, nil, ip, "Invalid credentials")
		writeUnauthorized(w, "Invalid credentials")
	}

	p, err := e.cfg.Principals.FindByEmail(r.Context(), email)
	if err != nil || p == nil || !p.Active {
		rejectInvalid()
		return
	}
	if p.UserIdentity == nil || p.UserIdentity.PasswordHash == nil {
		rejectInvalid()
		return
	}
	if err := passwordhash.Verify(req.Password, *p.UserIdentity.PasswordHash); err != nil {
		rejectInvalid()
		return
	}

	// Transparent migration: if the stored hash is a legacy/foreign format (an
	// upstream Laravel bcrypt or argon2i hash, or off-params argon2id), re-encode
	// it to the native scheme now that we hold the plaintext. Best-effort — a
	// persist failure must not block a valid login.
	if passwordhash.NeedsRehash(*p.UserIdentity.PasswordHash) {
		if newHash, herr := passwordhash.Hash(req.Password); herr == nil {
			if uerr := e.cfg.Principals.UpdatePasswordHash(r.Context(), p.ID, newHash); uerr != nil {
				slog.Warn("password rehash persist failed; login continues", "principal", p.ID, "err", uerr)
			}
		}
	}

	// Same best-effort migration for a legacy mixed-case email: normalise it now
	// that the user has authenticated. No-op when already lower-case.
	if herr := e.cfg.Principals.LowercaseEmail(r.Context(), p); herr != nil {
		slog.Warn("email lowercase self-heal failed; login continues", "principal", p.ID, "err", herr)
	}

	// Second-factor gate. When MFA is wired and applies to this user, return a
	// pending/enrollment challenge instead of a session — the /auth/2fa/* flow
	// then completes the login (and records the success). We fail CLOSED: an
	// evaluation error denies rather than silently bypassing 2FA.
	if e.cfg.MFA != nil && e.cfg.MFATokens != nil {
		handled, err := e.maybeChallenge2FA(w, r, p)
		if err != nil {
			slog.Error("2FA evaluation failed; denying login", "principal", p.ID, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"code":    "MFA_EVAL_FAILED",
				"message": "could not evaluate two-factor requirement",
			})
			return
		}
		if handled {
			return
		}
	}

	e.completeLogin(w, r, p, nil)
}

// completeLogin mints the session cookie, records the successful attempt, and
// writes the OK login payload. recoveryCodes is non-nil only on the
// enroll-and-complete path (the one time a fresh backup-code set is surfaced).
// Shared by handleLogin and the 2FA verify / enroll-confirm handlers.
func (e *Endpoint) completeLogin(w http.ResponseWriter, r *http.Request, p *principal.Principal, recoveryCodes []string) {
	email := ""
	if p.UserIdentity != nil {
		email = p.UserIdentity.Email
	}
	token, err := e.cfg.Provider.MintSessionToken(r.Context(), p.ID, SessionTTL)
	if err != nil {
		httperror.Write(w, httperror.BadRequest("MINT_FAILED", err.Error()))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     platformmw.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   e.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(SessionTTL),
		MaxAge:   int(SessionTTL.Seconds()),
	})
	e.recordAttempt(r.Context(), loginattempt.OutcomeSuccess, email, &p.ID, clientIP(r), "")
	claims, err := e.cfg.Provider.ResolveClaims(r.Context(), p.ID)
	if err != nil {
		// Auth succeeded but we couldn't load roles/permissions — log
		// and continue with an empty permission list. The user is signed
		// in (cookie set) and can refresh; better than 500ing here.
		claims = &provider.Claims{}
	}
	roles := make([]string, 0, len(p.Roles))
	for _, ra := range p.Roles {
		roles = append(roles, ra.Role)
	}
	writeJSON(w, http.StatusOK, loginResponse{
		Status:        "ok",
		PrincipalID:   p.ID,
		Name:          p.Name,
		Email:         email,
		Roles:         roles,
		Permissions:   buildPermissionList(claims),
		ClientID:      p.ClientID,
		RecoveryCodes: recoveryCodes,
	})
}

// ── /auth/logout ─────────────────────────────────────────────────────────

func (e *Endpoint) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     platformmw.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   e.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── /auth/me ─────────────────────────────────────────────────────────────

// meResponse matches the LoginResponse shape — same fields the SPA
// stores on first sign-in, so checkSession() returns directly comparable
// data when the page is reloaded.
func (e *Endpoint) handleMe(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if ac == nil || ac.PrincipalID == "" {
		writeUnauthorized(w, "Not authenticated")
		return
	}
	// Reload the principal so we return fresh name / active state /
	// roles rather than whatever was stamped on the JWT minutes ago.
	p, err := e.cfg.Principals.FindByID(r.Context(), ac.PrincipalID)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	if p == nil || !p.Active {
		writeUnauthorized(w, "Not authenticated")
		return
	}
	claims, err := e.cfg.Provider.ResolveClaims(r.Context(), p.ID)
	if err != nil {
		claims = &provider.Claims{}
	}
	roles := make([]string, 0, len(p.Roles))
	for _, ra := range p.Roles {
		roles = append(roles, ra.Role)
	}
	email := ""
	if p.UserIdentity != nil {
		email = p.UserIdentity.Email
	}
	writeJSON(w, http.StatusOK, loginResponse{
		PrincipalID: p.ID,
		Name:        p.Name,
		Email:       email,
		Roles:       roles,
		Permissions: buildPermissionList(claims),
		ClientID:    p.ClientID,
	})
}

// ── helpers ──────────────────────────────────────────────────────────────

// writeJSON renders v as a JSON response body with the supplied status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// recordAttempt best-effort logs a USER_LOGIN outcome. No-op when the repo
// is unset; logging failures never fail the request.
func (e *Endpoint) recordAttempt(ctx context.Context, outcome loginattempt.Outcome, email string, principalID *string, ip, failureReason string) {
	if e.cfg.LoginAttempts == nil {
		return
	}
	a := loginattempt.New(loginattempt.AttemptUserLogin, outcome)
	a.Identifier = &email
	a.PrincipalID = principalID
	if ip != "" {
		a.IPAddress = &ip
	}
	if failureReason != "" {
		a.FailureReason = &failureReason
	}
	_ = e.cfg.LoginAttempts.Record(ctx, a)
}

// clientIP extracts the best-effort client IP: the first hop of
// X-Forwarded-For when present, else the request RemoteAddr (host only).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSpace(host)
}

// writeTooManyRequests emits a 429 with a Retry-After header, mirroring
// Rust's backoff rejection.
func writeTooManyRequests(w http.ResponseWriter, retryAfterSecs uint32) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.FormatUint(uint64(retryAfterSecs), 10))
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    "TOO_MANY_REQUESTS",
		"message": "too many failed login attempts; try again later",
	})
}

// writeUnauthorized emits a 401 with the platform's error envelope.
// 401 isn't part of the usecase.Error → status table (which only maps
// validation/forbidden/notfound/conflict/businessrule), so we render
// the envelope manually.
func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Cookie realm="fc_session"`)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    "UNAUTHENTICATED",
		"message": msg,
	})
}

// encodeURI minimally URL-encodes the value for use in a query string.
// We avoid net/url.QueryEscape so the '+' for spaces is suppressed —
// emails don't contain '+' commonly and this keeps the URL readable.
func encodeURI(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~', r == '@':
			b.WriteRune(r)
		default:
			b.WriteString(percentEncode(r))
		}
	}
	return b.String()
}

func percentEncode(r rune) string {
	const hex = "0123456789ABCDEF"
	if r < 0x80 {
		return string([]byte{'%', hex[r>>4], hex[r&0x0F]})
	}
	// UTF-8 encode then percent-encode each byte.
	buf := make([]byte, 4)
	n := utf8Encode(buf, r)
	out := make([]byte, 0, n*3)
	for i := 0; i < n; i++ {
		out = append(out, '%', hex[buf[i]>>4], hex[buf[i]&0x0F])
	}
	return string(out)
}

func utf8Encode(buf []byte, r rune) int {
	switch {
	case r < 0x80:
		buf[0] = byte(r)
		return 1
	case r < 0x800:
		buf[0] = 0xC0 | byte(r>>6)
		buf[1] = 0x80 | byte(r&0x3F)
		return 2
	case r < 0x10000:
		buf[0] = 0xE0 | byte(r>>12)
		buf[1] = 0x80 | byte((r>>6)&0x3F)
		buf[2] = 0x80 | byte(r&0x3F)
		return 3
	default:
		buf[0] = 0xF0 | byte(r>>18)
		buf[1] = 0x80 | byte((r>>12)&0x3F)
		buf[2] = 0x80 | byte((r>>6)&0x3F)
		buf[3] = 0x80 | byte(r&0x3F)
		return 4
	}
}

// _platformauth references the auth subdomain package so the import
// stays even if the only use disappears during edits. Removing the
// blank doesn't change behavior.
var (
	_ = platformauth.ProviderOIDC
	_ = errors.New
)
