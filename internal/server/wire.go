package server

import (
	"fmt"
	"net/http"

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
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/bridge"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/payload"
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
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig"
	platformconfigapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	principalapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/process"
	processapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/process/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	roleapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/role/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	scheduledjobapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	serviceaccountapi "github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount/api"
	bff "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/bff"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httpcompat"
	platformmw "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/middleware"
	platformsink "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/platformsink"
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
	authPayloadRepo := payload.NewRepository(pool)
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
	platformConfigRepo := platformconfig.NewRepository(pool)
	processRepo := process.NewRepository(pool)
	scheduledJobRepo := scheduledjob.NewRepository(pool)
	webauthnCredRepo := webauthn.NewRepository(pool)
	webauthnCeremonyRepo := webauthn.NewCeremonyRepository(pool)

	// ── OAuth provider (fosite) ────────────────────────────────────────
	// SigningKey is supplied via cfg.JWTSigningKeyPath in production. In
	// dev we fall back to a generated ephemeral key so the binary can
	// boot without filesystem deps. See fc-dev for the persistent-key
	// path used by local development.
	signingKey := LoadSigningKeyOrEphemeral(cfg.JWTSigningKeyPath)
	authProvider, err := provider.NewProvider(provider.Config{
		Issuer:       cfg.JWTIssuer,
		SigningKey:   signingKey,
		SigningKeyID: cfg.JWTSigningKeyID,
		GlobalSecret: []byte(cfg.OAuthGlobalSecret),
	}, authRepo, authPayloadRepo, principalRepo, roleRepo)
	if err != nil {
		return fmt.Errorf("auth provider init: %w", err)
	}

	// ── Webauthn service ───────────────────────────────────────────────
	webauthnService, err := webauthn.NewService(webauthn.Config{
		RPDisplayName: "FlowCatalyst",
		RPID:          envOr("FC_WEBAUTHN_RP_ID", "localhost"),
		RPOrigins:     []string{envOr("FC_WEBAUTHN_RP_ORIGIN", "http://localhost:3000")},
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

	r.Group(func(r chi.Router) {
		r.Use(platformmw.CorrelationID)
		r.Use(platformmw.Authenticator(platformmw.AuthConfig{
			Provider:         authProvider,
			AllowTestHeaders: cfg.AuthAllowTestHeaders,
		}))

		// huma API shared by every aggregate's Register call. Routes
		// register against this; the chi router scope above gives them
		// the same middleware (CorrelationID + Authenticator) as the
		// remaining chi handlers. OpenAPIPath/DocsPath cleared so huma
		// doesn't auto-mount inside the auth Group — we serve the spec
		// from the parent router below.
		humaCfg := huma.DefaultConfig("FlowCatalyst Platform API", "dev")
		humaCfg.OpenAPIPath = ""
		humaCfg.DocsPath = ""
		humaAPI = humachi.New(r, humaCfg)

		// ── api.State + RegisterRoutes per subdomain ───────────────────
		clientapi.Register(humaAPI, &clientapi.State{
			Repo: clientRepo,
			UoW:  uow,
		})

		roleapi.Register(humaAPI, &roleapi.State{
			Repo: roleRepo,
			UoW:  uow,
		})

		applicationapi.Register(humaAPI, &applicationapi.State{
			Repo:             applicationRepo,
			ClientConfigRepo: applicationClientConfigRepo,
			ClientRepo:       clientRepo,
			Principals:       principalRepo,
			UoW:              uow,
		})

		principalapi.Register(humaAPI, &principalapi.State{
			Repo:         principalRepo,
			GrantRepo:    principalGrantRepo,
			Roles:        roleRepo,
			Applications: applicationRepo,
			Clients:      clientRepo,
			UoW:          uow,
		})

		serviceaccountapi.Register(humaAPI, &serviceaccountapi.State{
			Repo: serviceAccountRepo,
			UoW:  uow,
		})

		authapi.Register(humaAPI, &authapi.State{
			Repo: authRepo,
			UoW:  uow,
		})

		// OAuth provider routes (token, authorize, revoke, introspect, .well-known/*)
		provider.NewTokenEndpoint(authProvider).RegisterRoutes(r)
		provider.NewAuthorizeEndpoint(authProvider).RegisterRoutes(r)
		provider.NewRevokeEndpoint(authProvider).RegisterRoutes(r)
		provider.NewIntrospectEndpoint(authProvider).RegisterRoutes(r)
		if disc, err := provider.NewDiscoveryEndpoint(provider.Config{
			Issuer:       cfg.JWTIssuer,
			SigningKey:   signingKey,
			SigningKeyID: cfg.JWTSigningKeyID,
		}, cfg.JWTIssuer); err == nil {
			disc.RegisterRoutes(r)
		}

		// OIDC bridge — POST /oauth/check-domain, GET /oauth/oidc/login,
		// GET /oauth/oidc/callback. The bridge resolves the external IDP
		// for an email's domain, drives the redirect dance, and on
		// callback either uses the existing FlowCatalyst Principal or
		// auto-provisions one via the EmailDomainMapping that drove the
		// login. The SessionWriter is left at its default (200 + JSON
		// {principalId}) — the frontend's OIDC bridge handler swaps it
		// for a session-cookie write at startup.
		bridgeClient := bridge.NewBridge(authRepo)
		loginStateRepo := bridge.NewLoginStateRepo(pool)
		bridge.NewLoginEndpoint(bridgeClient, loginStateRepo, principalRepo, edmRepo, uow).RegisterRoutes(r)

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
			Repo: edmRepo,
			UoW:  uow,
		})

		platformconfigapi.Register(humaAPI, &platformconfigapi.State{
			Repo: platformConfigRepo,
			UoW:  uow,
		})

		processapi.Register(humaAPI, &processapi.State{
			Repo: processRepo,
			UoW:  uow,
		})

		scheduledjobapi.Register(humaAPI, &scheduledjobapi.State{
			Repo: scheduledJobRepo,
			UoW:  uow,
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
		sdkapi.RegisterRoutes(r, &sdkapi.DispatchJobsBatchState{Repo: dispatchJobRepo})
	})

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

	return nil
}
