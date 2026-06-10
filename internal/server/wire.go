package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	applicationapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/application/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/audit"
	auditapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/audit/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	authapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/authservice"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/bridge"
	clientselectionapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/clientselection"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/grantstore"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/login"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/loginbackoff"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/mfatoken"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/oauthapi"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/provider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/twofa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/branding"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/client"
	clientapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/client/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/connection"
	connectionapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/connection/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/cors"
	corsapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/cors/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchjob"
	dispatchjobapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchjob/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool"
	dispatchpoolapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	emaildomainapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/event"
	eventapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/event/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype"
	eventtypeapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider"
	identityproviderapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/loginattempt"
	loginattemptapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/loginattempt/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/mfa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/notify"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/passwordreset"
	passwordresetapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/passwordreset/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig"
	platformconfigapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	principalapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/process"
	processapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/process/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/publicapi"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/resetapproval"
	resetapprovalapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/resetapproval/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	roleapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/role/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	scheduledjobapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/sdksync"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	serviceaccountapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount/api"
	bff "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/bff"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/email"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httpcompat"
	meapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/me"
	platformmw "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/middleware"
	platformsink "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/platformsink"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/ratelimit"
	sdkapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/sdk"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription"
	subscriptionapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/webauthn"
	webauthnapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/webauthn/api"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// WirePlatform instantiates every subdomain's repository + operations +
// HTTP routes against the supplied pool and registers them on r. The
// resulting router is the same surface the Rust fc-platform exposes.
//
// This function is the single source of truth for which subdomains the
// platform API serves. Adding a new subdomain is a four-line ritual:
// build the repo, build the use cases, build the api.State, call
// api.RegisterRoutes(r, &state).
func WirePlatform(r chi.Router, pool *pgxpool.Pool, cfg EnvCfg) error {
	// Wire the huma error transformer so handler-returned *usecase.Error
	// values flow out as the canonical {code, message, details} envelope.
	httpcompat.Init()

	sink := platformsink.New()
	uow := usecasepgx.New(pool, sink)

	// ── Repos ───────────────────────────────────────────────────────────
	clientRepo := client.NewRepository(pool)
	roleRepo := role.NewRepository(pool)
	applicationRepo := application.NewRepository(pool)
	applicationClientConfigRepo := application.NewClientConfigRepo(pool)
	principalRepo := principal.NewRepository(pool)
	principalGrantRepo := principal.NewClientAccessGrantRepo(pool)
	serviceAccountRepo := serviceaccount.NewRepository(pool)
	authRepo := auth.NewRepository(pool)
	corsRepo := cors.NewRepository(pool)
	connectionRepo := connection.NewRepository(pool)
	subscriptionRepo := subscription.NewRepository(pool)
	dispatchPoolRepo := dispatchpool.NewRepository(pool)
	dispatchJobRepo := dispatchjob.NewRepository(pool)
	eventTypeRepo := eventtype.NewRepository(pool)
	eventRepo := event.NewRepository(pool)
	auditRepo := audit.NewRepository(pool)
	idpRepo := identityprovider.NewRepository(pool)
	edmRepo := emaildomainmapping.NewRepository(pool)
	loginAttemptRepo := loginattempt.NewRepository(pool)
	platformConfigRepo := platformconfig.NewRepository(pool)
	processRepo := process.NewRepository(pool)
	scheduledJobRepo := scheduledjob.NewRepository(pool)
	webauthnCredRepo := webauthn.NewRepository(pool)
	webauthnCeremonyRepo := webauthn.NewCeremonyRepository(pool)

	// ── Auth provider (claims projection + session JWTs) ───────────────
	// SigningKey is supplied via cfg.JWTSigningKeyPath in production. In
	// dev we fall back to a generated ephemeral key so the binary can
	// boot without filesystem deps. See fc-dev for the persistent-key
	// path used by local development.
	signingKey := LoadSigningKeyOrEphemeral(cfg.JWTSigningKeyPath)
	authProvider, err := provider.NewProvider(provider.Config{
		Issuer: cfg.JWTIssuer,
		// Must match authservice's access-token `aud` below so bearers it
		// mints validate, while OIDC ID tokens (aud = an RP's client_id,
		// same signing key) are rejected by the middleware.
		Audience:   cfg.JWTIssuer,
		SigningKey: signingKey,
	}, principalRepo, roleRepo)
	if err != nil {
		return fmt.Errorf("auth provider init: %w", err)
	}

	// ── Hand-rolled OAuth token service (/oauth/token) ────────────────
	// authservice signs/validates with the same RSA key the auth provider
	// loaded, so the JWKS + session-cookie paths line up. encSvc verifies
	// confidential client secrets (decrypt + compare).
	// Validation-only previous public key for zero-downtime key rotation —
	// tokens signed with the prior key still verify. Matches Rust's
	// FLOWCATALYST_JWT_PREVIOUS_PUBLIC_KEY. Normalize the SSM/env PEM (same \n
	// mangling as the private key) and skip it unless it's a real PEM: it's
	// optional, so a missing or unparseable value must NOT stop the platform
	// booting. (The current key's public half is derived from signingKey.)
	prevPubKey := NormalizePEM(os.Getenv("FLOWCATALYST_JWT_PREVIOUS_PUBLIC_KEY"))
	if !strings.Contains(prevPubKey, "-----BEGIN") {
		prevPubKey = ""
	}
	authSvc, err := authservice.New(authservice.Config{
		Issuer:                  cfg.JWTIssuer,
		Audience:                cfg.JWTIssuer,
		RSAPrivateKeyPEM:        string(signingKey),
		RSAPublicKeyPreviousPEM: prevPubKey,
		AccessTokenExpirySecs:   3600,
	})
	if err != nil {
		return fmt.Errorf("authservice init: %w", err)
	}
	encSvc, err := encryption.FromEnv()
	if err != nil {
		return fmt.Errorf("encryption init: %w", err)
	}
	// Distributed rate-limit store: Redis when FC_REDIS_URL is reachable,
	// else Postgres, else Noop (FC_RATE_LIMIT_DISABLE=1). Throttles
	// /oauth/{token,authorize} per-client_id (+ per-IP via middleware).
	rlStore := ratelimit.Build(context.Background(), pool)
	rlPolicies := ratelimit.PoliciesFromEnv()
	// In-memory per-instance governors layered in front of the distributed
	// store on /oauth/token (defence-in-depth; 1:1 with Rust's
	// rate_limit_middleware.rs). They shed a local flood before the network
	// round-trip; the distributed store remains the cluster-wide ceiling.
	oauthTokenIPGov := ratelimit.NewGovernor(ratelimit.OAuthTokenIPGovernorFromEnv())
	oauthTokenClientGov := ratelimit.NewGovernor(ratelimit.OAuthTokenClientGovernorFromEnv())
	oauthTokenEP := &oauthapi.State{
		OAuthClients:      authRepo.OAuthClients,
		Principals:        principalRepo,
		Auth:              authSvc,
		AuthCodes:         grantstore.NewAuthorizationCodeRepository(pool),
		RefreshTokens:     grantstore.NewRefreshTokenRepository(pool),
		PendingAuth:       grantstore.NewPendingAuthRepository(pool),
		Encryption:        encSvc,
		BaseURL:           cfg.JWTIssuer,
		LoginAttempts:     loginAttemptRepo,
		RateLimit:         rlStore,
		RateLimitPolicies: rlPolicies,
		ClientGovernor:    oauthTokenClientGov,
		// /oauth/authorize treats an invalid/absent session as
		// redirect-to-login, so it validates the session cookie itself
		// (it's mounted outside the rejecting auth middleware).
		ValidateSession: func(token string) (string, time.Time, bool) {
			c, err := authProvider.ValidateSessionToken(context.Background(), token)
			if err != nil || c == nil {
				return "", time.Time{}, false
			}
			return c.Subject, c.IssuedAt, true
		},
		// Flatten roles → permission ceiling for the granted "scope" claim and
		// requested-scope narrowing on /oauth/token.
		FlattenPermissions: authProvider.FlattenPermissions,
	}

	// ── Webauthn service ───────────────────────────────────────────────
	// go-webauthn matches the browser's origin against RPOrigins by exact
	// scheme+host (no wildcard/subdomain support), so every allowed origin must
	// be listed verbatim. RPID is the registrable parent domain (e.g.
	// inhanceapps.com) and validly covers any subdomain origin. Origins come from
	// FC_WEBAUTHN_ORIGINS (comma-separated, the deploy env's name); the singular
	// FC_WEBAUTHN_RP_ORIGIN is kept as a fallback for older configs.
	webauthnService, err := webauthn.NewService(webauthn.Config{
		// Read once at startup: the passkey prompt shows this. A platform-name
		// change takes effect on next restart (the library fixes RPDisplayName at
		// construction); the live-read paths (2FA issuer, emails) update instantly.
		RPDisplayName: branding.PlatformName(context.Background(), platformConfigRepo),
		RPID:          envOr("FC_WEBAUTHN_RP_ID", "localhost"),
		RPOrigins:     webauthnOrigins(),
	}, webauthnCredRepo, webauthnCeremonyRepo)
	if err != nil {
		return fmt.Errorf("webauthn service init: %w", err)
	}

	// ── Platform middleware + routes ────────────────────────────────────
	// Wrap the platform routes in a chi Group so the middleware applies
	// only to platform routes, not to whatever surrounding routes the
	// caller (fc-dev, fc-server) registered around us (e.g. /health).
	// chi requires middleware to be defined before any routes on a given
	// mux; the Group creates its own scope so that ordering rule is
	// satisfied locally regardless of caller ordering.
	// humaAPI is assigned inside the auth Group below so its routes
	// inherit chi auth middleware. Captured at function scope so the
	// spec/docs handlers (mounted on the parent router OUTSIDE the
	// Group, so they remain unauthenticated for tooling — oasdiff,
	// hey-api codegen, the Hey-API frontend client) can access it.
	var humaAPI huma.API

	// Email + 2FA services. emailSvc is shared by the MFA challenge mailer and
	// the password-reset mailer below (SMTP_* env; logs when unconfigured).
	// mfaSvc carries TOTP/email-PIN/recovery-code/trusted-device logic; TOTP
	// secrets are encrypted with encSvc (TOTP degrades gracefully if no key).
	// mfaTokens signs the short-lived pending/enroll tokens with a secret
	// derived from the session-signing key (rejected by the RS256 middleware).
	emailSvc := email.FromEnv()
	// Resolve the configurable platform/brand name live for the authenticator-app
	// issuer and security emails (re-read per use, so a change applies instantly).
	platformName := branding.Provider(platformConfigRepo)
	mfaCfg := mfa.DefaultConfig()
	mfaCfg.PlatformName = platformName
	mfaSvc := mfa.NewService(mfa.NewRepository(pool), encSvc, emailSvc, mfaCfg)
	mfaTokens := mfatoken.NewIssuer(authProvider.SigningKey(), authProvider.Issuer())
	notifier := notify.New(emailSvc).WithName(platformName)
	twofaPolicy := twofa.Policy{Mappings: edmRepo, IDPs: idpRepo}

	// Public auth surface: SPA login + cookie acquisition. MUST live
	// outside the bearer-token middleware below — a stale fc_session
	// cookie from a previous run would otherwise 401 the request before
	// the SPA could re-authenticate.
	loginEP := login.New(login.Config{
		Provider:          authProvider,
		Principals:        principalRepo,
		Mappings:          edmRepo,
		IdentityProviders: idpRepo,
		CookieSecure:      !cfg.AuthAllowTestHeaders,
		LoginAttempts:     loginAttemptRepo,
		BackoffPolicy:     loginbackoff.PolicyFromEnv(),
		// /auth/refresh shares the OAuth refresh-token store + access-token
		// signer so a token issued via either path rotates identically.
		RefreshTokens: oauthTokenEP.RefreshTokens,
		Auth:          authSvc,
		// 2FA: challenge/enroll endpoints. (A passkey does not exempt the
		// password path, so no webauthn dependency here.)
		MFA:       mfaSvc,
		MFATokens: mfaTokens,
		Notifier:  notifier,
		Audit:     auditRepo,
	})
	loginEP.RegisterPublicRoutes(r)

	// Public read-only endpoints the SPA hits before sign-in
	// (login-theme branding, platform feature flags). Mounted outside
	// the auth middleware for the same reason as the login surface.
	publicapi.New(platformConfigRepo).RegisterRoutes(r)

	// Unauthenticated password-reset flow (request/validate/confirm). Public
	// like /auth/login. Email is delivered via the SMTP_* env (SendGrid in
	// prod); when SMTP isn't configured the message is logged instead. Delivery
	// is best-effort — a send failure never fails the request (matching Rust).
	// emailSvc is the shared mailer constructed above with the 2FA services.
	resetTokenRepo := passwordreset.NewRepository(pool)
	resetApprovalRepo := resetapproval.NewRepository(pool)
	passwordresetapi.RegisterRoutes(r, &passwordresetapi.State{
		Principals:      principalRepo,
		Tokens:          resetTokenRepo,
		UoW:             uow,
		ExternalBaseURL: cfg.JWTIssuer,
		Emailer:         passwordresetapi.NewEmailer(emailSvc),
		// 2FA hand-off: clear-on-reset_2fa, revoke remembered devices, and
		// return enrollment_required when the domain compels a second factor.
		MFA:       mfaSvc,
		MFATokens: mfaTokens,
		Policy:    twofaPolicy,
		Notifier:  notifier,
		// Phase 8: a self-service reset with no strong factor queues for
		// client-admin approval and notifies them, instead of issuing a token.
		Approvals:    resetApprovalRepo,
		ClientAdmins: principalRepo,
	})
	// Admin-triggered reset (POST /api/principals/{id}/send-password-reset)
	// shares the same token repo + mailer.
	principalResetEmailer := passwordresetapi.NewPrincipalEmailer(resetTokenRepo, cfg.JWTIssuer, emailSvc)

	// /oauth/authorize is mounted OUTSIDE the auth middleware: an absent or
	// expired session must redirect to login (not 401), and the handler
	// validates the session cookie itself. Wrapped in the per-IP throttle.
	oauthTokenEP.RegisterAuthorizeRoutes(r.With(ratelimit.IPLimitMiddleware(rlStore, ratelimit.BucketOAuthAuthorizeIP, rlPolicies.OAuthAuthorizeIP)))

	r.Group(func(r chi.Router) {
		r.Use(platformmw.CorrelationID)
		r.Use(platformmw.Authenticator(platformmw.AuthConfig{
			Provider:         authProvider,
			AllowTestHeaders: cfg.AuthAllowTestHeaders,
		}))
		// /auth/me — needs the AuthContext, so mounted INSIDE the auth
		// group. /auth/check-domain + /auth/login + /auth/logout are
		// public (see RegisterPublicRoutes above).
		loginEP.RegisterAuthenticatedRoutes(r)

		// huma API shared by every aggregate's Register call. Routes
		// register against this; the chi router scope above gives them
		// the same middleware (CorrelationID + Authenticator) as the
		// remaining chi handlers. OpenAPIPath/DocsPath cleared so huma
		// doesn't auto-mount inside the auth Group — we serve the spec
		// from the parent router below.
		humaCfg := huma.DefaultConfig("FlowCatalyst Platform API", "dev")
		humaCfg.OpenAPIPath = ""
		humaCfg.DocsPath = ""
		// Drop huma's $schema link injection. The Rust API never emits it
		// and the field clutters response bodies that SPAs / SDKs parse
		// strictly. The OpenAPI document still describes every response
		// (served from the parent router via /openapi.json) — clients
		// that want the schema can fetch it there.
		humaCfg.SchemasPath = ""
		humaAPI = humachi.New(r, humaCfg)

		// ── api.State + RegisterRoutes per subdomain ───────────────────
		clientapi.Register(humaAPI, &clientapi.State{
			Repo:          clientRepo,
			Applications:  applicationRepo,
			ClientConfigs: applicationClientConfigRepo,
			UoW:           uow,
		})

		roleapi.Register(humaAPI, &roleapi.State{
			Repo:        roleRepo,
			Permissions: role.NewPermissionRepo(pool),
			UoW:         uow,
		})

		applicationapi.Register(humaAPI, &applicationapi.State{
			Repo:             applicationRepo,
			ClientConfigRepo: applicationClientConfigRepo,
			ClientRepo:       clientRepo,
			Principals:       principalRepo,
			Roles:            roleRepo,
			ServiceAccounts:  serviceAccountRepo,
			OAuthClients:     authRepo.OAuthClients,
			UoW:              uow,
		})

		principalapi.Register(humaAPI, &principalapi.State{
			Repo:              principalRepo,
			GrantRepo:         principalGrantRepo,
			Roles:             roleRepo,
			Applications:      applicationRepo,
			ClientConfigs:     applicationClientConfigRepo,
			Clients:           clientRepo,
			Mappings:          edmRepo,
			IdentityProviders: idpRepo,
			AnchorDomains:     authRepo.AnchorDomains,
			PasswordEmailer:   principalResetEmailer,
			InviteEmailer:     principalResetEmailer,
			Notifier:          notifier,
			MFA:               mfaSvc,
			Audit:             auditRepo,
			UoW:               uow,
		})

		// Phase 8: lost-device reset approval queue (client-admin gated).
		resetapprovalapi.Register(humaAPI, &resetapprovalapi.State{
			Approvals:  resetApprovalRepo,
			Principals: principalRepo,
			Sender:     principalResetEmailer,
		})

		serviceaccountapi.Register(humaAPI, &serviceaccountapi.State{
			Repo:         serviceAccountRepo,
			Principals:   principalRepo,
			OAuthClients: authRepo.OAuthClients,
			UoW:          uow,
		})

		authapi.Register(humaAPI, &authapi.State{
			Repo:         authRepo,
			Applications: applicationRepo,
			UoW:          uow,
			Enc:          encSvc,
		})

		// OAuth provider routes — all hand-rolled (authservice +
		// encryption). /oauth/authorize is registered above, outside this
		// auth group.
		oauthTokenEP.RegisterTokenRoutes(r.With(
			ratelimit.GovernorMiddleware(oauthTokenIPGov, "rate limit exceeded for this IP"),
			ratelimit.IPLimitMiddleware(rlStore, ratelimit.BucketOAuthTokenIP, rlPolicies.OAuthTokenIP),
		))
		oauthTokenEP.RegisterIntrospectRoutes(r)
		oauthTokenEP.RegisterRevokeRoutes(r)
		oauthTokenEP.RegisterUserinfoRoutes(r)
		oauthTokenEP.RegisterDiscoveryRoutes(r)

		// OIDC bridge — POST /auth/check-domain, GET /auth/oidc/login,
		// GET /auth/oidc/callback. The bridge resolves the external IDP
		// for an email's domain, drives the redirect dance, and on
		// callback either uses the existing FlowCatalyst Principal or
		// auto-provisions one via the EmailDomainMapping that drove the
		// login. The default SessionWriter just emits JSON; we override
		// here to mint a session-cookie JWT (same path as /auth/login)
		// so a successful SSO round-trip produces a usable browser
		// session.
		// Field-level encryption (FLOWCATALYST_APP_KEY) — nil-safe; the
		// bridge will surface a clear error if a confidential OIDC config
		// needs a secret and the key isn't set.
		appEnc, _ := encryption.FromEnv()
		bridgeClient := bridge.NewBridge(edmRepo, idpRepo, appEnc)
		loginStateRepo := bridge.NewLoginStateRepo(pool)
		bridgeLoginEP := bridge.NewLoginEndpoint(bridgeClient, loginStateRepo, principalRepo, edmRepo,
			roleRepo, authRepo.IdpRoleMappings, uow, authRepo.OAuthClients)
		bridgeLoginEP.SessionWriter = func(w http.ResponseWriter, r *http.Request, principalID, returnURL string) {
			token, err := authProvider.MintSessionToken(r.Context(), principalID, login.SessionTTL)
			if err != nil {
				http.Error(w, "session mint failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     platformmw.SessionCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				Secure:   !cfg.AuthAllowTestHeaders,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int(login.SessionTTL.Seconds()),
			})
			if returnURL != "" {
				http.Redirect(w, r, returnURL, http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"principalId": principalID})
		}
		// Per-IP rate limit on the public OIDC bridge routes (login start +
		// callback + session/end) — blunts authorization-code probing / DoS
		// without impeding a real interactive login.
		oidcGov := ratelimit.NewGovernor(ratelimit.OIDCBridgeGovernorFromEnv())
		r.Group(func(g chi.Router) {
			g.Use(ratelimit.GovernorMiddleware(oidcGov, "Too many authentication requests"))
			bridgeLoginEP.RegisterRoutes(g)
		})

		corsapi.Register(humaAPI, &corsapi.State{
			Repo: corsRepo,
			UoW:  uow,
		})

		connectionapi.Register(humaAPI, &connectionapi.State{
			Repo: connectionRepo,
			UoW:  uow,
		})

		subscriptionapi.Register(humaAPI, &subscriptionapi.State{
			Repo: subscriptionRepo,
			UoW:  uow,
		})

		dispatchpoolapi.Register(humaAPI, &dispatchpoolapi.State{
			Repo: dispatchPoolRepo,
			UoW:  uow,
		})

		eventtypeapi.Register(humaAPI, &eventtypeapi.State{
			Repo: eventTypeRepo,
			UoW:  uow,
		})

		// SDK self-registration ("sync") endpoints, scoped under
		// /api/applications/{appCode}. Mirrors the Rust sdk_sync_router.
		sdksync.Register(humaAPI, &sdksync.State{
			Apps:          applicationRepo,
			EventTypes:    eventTypeRepo,
			Roles:         roleRepo,
			Subscriptions: subscriptionRepo,
			Connections:   connectionRepo,
			Processes:     processRepo,
			DispatchPools: dispatchPoolRepo,
			Principals:    principalRepo,
			ScheduledJobs: scheduledJobRepo,
			Specs:         openapispecs.NewRepository(pool),
			UoW:           uow,
		})

		eventapi.Register(humaAPI, &eventapi.State{Repo: eventRepo})
		auditapi.Register(humaAPI, &auditapi.State{Repo: auditRepo})
		dispatchjobapi.Register(humaAPI, &dispatchjobapi.State{Repo: dispatchJobRepo})

		identityproviderapi.Register(humaAPI, &identityproviderapi.State{
			Repo: idpRepo,
			UoW:  uow,
			Enc:  encSvc,
		})

		emaildomainapi.Register(humaAPI, &emaildomainapi.State{
			Repo:    edmRepo,
			IDPRepo: idpRepo,
			UoW:     uow,
		})

		loginattemptapi.Register(humaAPI, &loginattemptapi.State{Repo: loginAttemptRepo})

		platformconfigapi.Register(humaAPI, &platformconfigapi.State{
			Repo: platformConfigRepo,
			UoW:  uow,
		})

		processapi.Register(humaAPI, &processapi.State{
			Repo: processRepo,
			UoW:  uow,
		})

		scheduledjobapi.Register(humaAPI, &scheduledjobapi.State{
			Repo:      scheduledJobRepo,
			Instances: scheduledjob.NewInstanceRepository(pool),
			UoW:       uow,
		})

		webauthnapi.Register(humaAPI, &webauthnapi.State{
			Service:      webauthnService,
			Principals:   principalRepo,
			Creds:        webauthnCredRepo,
			UoW:          uow,
			Provider:     authProvider,
			CookieSecure: !cfg.AuthAllowTestHeaders,
			SessionTTL:   login.SessionTTL,
			Notifier:     notifier,
		})

		// Shared BFF/SDK endpoints (dashboard + SDK ingest)
		bff.RegisterRoutes(r, &bff.DashboardState{Pool: pool})
		bff.RegisterFilterOptions(r, &bff.FilterOptionsState{
			Clients:    clientRepo,
			EventTypes: eventTypeRepo,
		})
		bff.RegisterEventTypes(r, &bff.EventTypesState{
			Repo: eventTypeRepo,
			UoW:  uow,
		})
		bff.RegisterRoles(r, &bff.RolesState{
			Roles:        roleRepo,
			Applications: applicationRepo,
			UoW:          uow,
		})
		bff.RegisterScheduledJobs(r, &bff.ScheduledJobsState{
			Jobs:      scheduledJobRepo,
			Instances: scheduledjob.NewInstanceRepository(pool),
			Clients:   clientRepo,
		})
		bff.RegisterDeveloper(r, &bff.DeveloperState{
			Applications: applicationRepo,
			Specs:        openapispecs.NewRepository(pool),
			EventTypes:   eventTypeRepo,
			UoW:          uow,
			PlatformOpenAPI: func() (json.RawMessage, error) {
				// humaAPI is captured by closure — the OpenAPI() call
				// reflects whatever routes are registered at request time.
				return humaAPI.OpenAPI().MarshalJSON()
			},
		})
		meapi.RegisterRoutes(r, &meapi.State{Principals: principalRepo, Applications: applicationRepo, Clients: clientRepo, AppConfigs: applicationClientConfigRepo})
		clientselectionapi.RegisterRoutes(r, &clientselectionapi.State{
			Principals: principalRepo,
			Clients:    clientRepo,
			Roles:      roleRepo,
			Grants:     principalGrantRepo,
			Auth:       authSvc,
		})
		sdkapi.RegisterRoutes(r, &sdkapi.DispatchJobsBatchState{Repo: dispatchJobRepo})
		sdkapi.RegisterAuditRoutes(r, &sdkapi.AuditBatchState{Repo: auditRepo, Apps: applicationRepo, Clients: clientRepo})
	})

	// Accept-and-ignore unknown request-body fields (serde-style leniency) so
	// the SPA's superset payloads stop 400-ing. Must run after every route has
	// registered; keep in sync with the dump-spec tool so the lockfile matches.
	httpcompat.RelaxRequestBodies(humaAPI)

	// Match Rust: exclude /bff/* from the published OpenAPI spec (the BFF
	// handlers stay mounted and keep serving). Must run after every route
	// has registered.
	httpcompat.StripBFFPaths(humaAPI)

	// Spec + Swagger UI mounted on the PARENT router (outside the
	// Authenticator Group) so tooling — oasdiff, the Hey-API codegen
	// in the Vue frontend, browser visitors to /api/docs — can fetch
	// them without a bearer token. The huma API itself was created
	// inside the Group above so every *route* it owns inherits auth.
	r.Get("/api/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		spec, err := humaAPI.OpenAPI().MarshalJSON()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	})
	r.Get("/api/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		spec, err := humaAPI.OpenAPI().YAML()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(spec)
	})

	// Rust serves the spec at /q/openapi and Swagger UI at /swagger-ui;
	// alias both for drop-in tooling parity. /api/openapi.json is kept for
	// the existing make/Hey-API codegen tooling.
	r.Get("/q/openapi", func(w http.ResponseWriter, _ *http.Request) {
		spec, err := humaAPI.OpenAPI().MarshalJSON()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	})
	r.Get("/swagger-ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerUIHTML))
	})

	return nil
}
