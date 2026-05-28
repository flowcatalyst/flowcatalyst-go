package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/grantstore"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/login"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/loginbackoff"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/oauthapi"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/provider"
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
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig"
	platformconfigapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	principalapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/process"
	processapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/process/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/publicapi"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	roleapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/role/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	scheduledjobapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	serviceaccountapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount/api"
	bff "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/bff"
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
		Issuer:     cfg.JWTIssuer,
		SigningKey: signingKey,
	}, principalRepo, roleRepo)
	if err != nil {
		return fmt.Errorf("auth provider init: %w", err)
	}

	// ── Hand-rolled OAuth token service (/oauth/token) ────────────────
	// authservice signs/validates with the same RSA key the auth provider
	// loaded, so the JWKS + session-cookie paths line up. encSvc verifies
	// confidential client secrets (decrypt + compare).
	authSvc, err := authservice.New(authservice.Config{
		Issuer:                cfg.JWTIssuer,
		Audience:              cfg.JWTIssuer,
		RSAPrivateKeyPEM:      string(signingKey),
		AccessTokenExpirySecs: 3600,
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
	}

	// ── Webauthn service ───────────────────────────────────────────────
	webauthnService, err := webauthn.NewService(webauthn.Config{
		RPDisplayName: "FlowCatalyst",
		RPID:          envOr("FC_WEBAUTHN_RP_ID", "localhost"),
		RPOrigins:     []string{envOr("FC_WEBAUTHN_RP_ORIGIN", "http://localhost:8080")},
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
	})
	loginEP.RegisterPublicRoutes(r)

	// Public read-only endpoints the SPA hits before sign-in
	// (login-theme branding, platform feature flags). Mounted outside
	// the auth middleware for the same reason as the login surface.
	publicapi.New(platformConfigRepo).RegisterRoutes(r)

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
			Clients:           clientRepo,
			Mappings:          edmRepo,
			IdentityProviders: idpRepo,
			UoW:               uow,
		})

		serviceaccountapi.Register(humaAPI, &serviceaccountapi.State{
			Repo:         serviceAccountRepo,
			Principals:   principalRepo,
			OAuthClients: authRepo.OAuthClients,
			UoW:          uow,
		})

		authapi.Register(humaAPI, &authapi.State{
			Repo: authRepo,
			UoW:  uow,
		})

		// OAuth provider routes — all hand-rolled (authservice +
		// encryption). /oauth/authorize is registered above, outside this
		// auth group.
		oauthTokenEP.RegisterTokenRoutes(r.With(ratelimit.IPLimitMiddleware(rlStore, ratelimit.BucketOAuthTokenIP, rlPolicies.OAuthTokenIP)))
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
		bridgeClient := bridge.NewBridge(authRepo, appEnc)
		loginStateRepo := bridge.NewLoginStateRepo(pool)
		bridgeLoginEP := bridge.NewLoginEndpoint(bridgeClient, loginStateRepo, principalRepo, edmRepo,
			roleRepo, authRepo.IdpRoleMappings, uow)
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
		bridgeLoginEP.RegisterRoutes(r)

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

		eventapi.Register(humaAPI, &eventapi.State{Repo: eventRepo})
		auditapi.Register(humaAPI, &auditapi.State{Repo: auditRepo})
		dispatchjobapi.Register(humaAPI, &dispatchjobapi.State{Repo: dispatchJobRepo})

		identityproviderapi.Register(humaAPI, &identityproviderapi.State{
			Repo: idpRepo,
			UoW:  uow,
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
			Service:    webauthnService,
			Principals: principalRepo,
			Creds:      webauthnCredRepo,
			UoW:        uow,
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
		meapi.RegisterRoutes(r, &meapi.State{Principals: principalRepo})
		sdkapi.RegisterRoutes(r, &sdkapi.DispatchJobsBatchState{Repo: dispatchJobRepo})
	})

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
