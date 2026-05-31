package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
	routerapi "github.com/flowcatalyst/flowcatalyst-go/internal/router/api"
	"github.com/flowcatalyst/flowcatalyst-go/internal/stream"
)

// RunOptions lets the caller (fc-server / fc-dev) extend the unified
// HTTP server without forking it. ExtraAPIRoutes runs after platform +
// router are mounted; Fallback runs as the NotFound handler (used by
// fc-dev to mount the embedded Vue SPA).
type RunOptions struct {
	ExtraAPIRoutes func(r chi.Router)
	Fallback       http.Handler
}

// Run is the single orchestrator that fc-server and fc-dev both call.
// Responsibilities:
//   - build the API chi mux + huma API
//   - wire the platform aggregates (when cfg.PlatformEnabled)
//   - mount the router HTTP surface under cfg.RouterHTTPPrefix (when cfg.RouterEnabled)
//   - spawn background subsystems (scheduler, stream, outbox, router engine, mcp, purger)
//   - bridge stream.HealthService → router StreamHealthProvider so the
//     dashboard reflects live projection state when co-tenanted
//   - bind the API + metrics + (optional) MCP listeners
//   - block until ctx is cancelled, then drain and return
//
// Run never panics; it returns the first listener error or nil on
// graceful shutdown.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg EnvCfg, opts RunOptions) error {
	// Derive a runCtx so a listener failure can cut subsystems off
	// before the caller's defer pool.Close() / pg.Stop() fires. Without
	// this the subsystem goroutines outlive the pool and spew
	// "closed pool" errors during shutdown.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Always build a stream HealthService — empty when stream is off so
	// the router's StreamHealthProvider reports zero streams gracefully.
	streamHealth := stream.NewHealthService()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Get("/health", healthHandler)

	var routerSrv *router.Server
	var routerErr error

	if cfg.PlatformEnabled {
		if err := WirePlatform(r, pool, cfg); err != nil {
			return fmt.Errorf("platform wiring: %w", err)
		}
		slog.Info("platform API wired")
	}

	if cfg.RouterEnabled {
		routerSrv, routerErr = newRouterServer(cfg, pool)
		if routerErr != nil {
			return fmt.Errorf("router init: %w", routerErr)
		}
		prefix := cfg.RouterHTTPPrefix
		if prefix == "" {
			prefix = "/router"
		}
		MountRouterHTTP(r, prefix, routerSrv, streamHealth, cfg)
		slog.Info("router HTTP mounted", "prefix", prefix)
	}

	if opts.ExtraAPIRoutes != nil {
		opts.ExtraAPIRoutes(r)
	}
	if opts.Fallback != nil {
		r.NotFound(opts.Fallback.ServeHTTP)
	}

	// ── Background subsystems ─────────────────────────────────────────────
	var wg sync.WaitGroup
	if cfg.PlatformEnabled {
		go StartPurger(ctx, pool)
	}
	if cfg.SchedulerEnabled {
		wg.Add(1)
		go func() { defer wg.Done(); StartScheduler(ctx, pool, cfg) }()
		slog.Info("scheduler started")
	}
	if cfg.ScheduledJobEnabled {
		wg.Add(1)
		go func() { defer wg.Done(); StartScheduledJobScheduler(ctx, pool, cfg) }()
		slog.Info("scheduled-job scheduler started")
	}
	if cfg.StreamEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartStreamProcessorWithHealth(ctx, pool, cfg, streamHealth)
		}()
		slog.Info("stream processor started")
	}
	if cfg.OutboxEnabled {
		wg.Add(1)
		go func() { defer wg.Done(); StartOutboxProcessor(ctx, pool, cfg) }()
		slog.Info("outbox processor started")
	}
	if cfg.RouterEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := routerSrv.Run(ctx); err != nil {
				slog.Warn("router run failed", "err", err)
			}
		}()
		slog.Info("router engine started")
	}
	if cfg.MCPEnabled {
		wg.Add(1)
		go func() { defer wg.Done(); StartMCP(ctx, cfg) }()
		slog.Info("mcp started")
	}

	// ── Listeners ─────────────────────────────────────────────────────────
	apiSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.APIPort),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	metricsSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler:           metricsRouter(cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listenErr := make(chan error, 2)
	go func() {
		slog.Info("api server listening", "addr", apiSrv.Addr)
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- fmt.Errorf("api server: %w", err)
		}
	}()
	go func() {
		slog.Info("metrics server listening", "addr", metricsSrv.Addr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- fmt.Errorf("metrics server: %w", err)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-listenErr:
		slog.Error("listener exited", "err", err)
		runErr = err
	}

	// Cancel every subsystem ctx BEFORE pool.Close() / pg.Stop() can fire
	// in the parent's defer chain — otherwise projectors keep polling a
	// closed pool and we spew "closed pool" errors during shutdown.
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	_ = apiSrv.Shutdown(shutdownCtx)
	_ = metricsSrv.Shutdown(shutdownCtx)
	wg.Wait()
	slog.Info("server stopped")
	return runErr
}

// MountRouterHTTP nests the router API + dashboard + Prometheus under
// the supplied prefix. Authentication is BasicAuth (env-driven). The
// router engine itself must be started separately — this only wires
// the HTTP surface that reads its state.
func MountRouterHTTP(r chi.Router, prefix string, srv *router.Server, streamHealth *stream.HealthService, cfg EnvCfg) {
	state := routerapi.FromServer(srv)
	if streamHealth != nil {
		state.StreamHealth = streamHealthBridge{svc: streamHealth}
	}
	r.Route(prefix, func(sub chi.Router) {
		// BasicAuth on the router prefix. Disabled when no creds set.
		sub.Use(routerapi.BasicAuthMiddleware(resolveRouterAuth()))
		humaCfg := huma.DefaultConfig("FlowCatalyst Router API", routerapi.Version)
		// Nest the spec under the prefix so external tooling can grab
		// the OpenAPI doc at <prefix>/openapi.json.
		// Drop huma's $schema link injection (Rust never emits it), matching
		// the platform API config in wire.go.
		humaCfg.SchemasPath = ""
		api := humachi.New(sub, humaCfg)
		routerapi.Register(api, state)
		routerapi.MountDashboard(sub)
		sub.Mount("/metrics", routerapi.PrometheusHandler(state))
	})
}

// resolveRouterAuth reads the router HTTP BasicAuth config, accepting the Rust
// AUTH_BASIC_USERNAME / AUTH_BASIC_PASSWORD names as aliases for
// FC_ROUTER_AUTH_USER / FC_ROUTER_AUTH_PASS. AUTH_MODE=NONE (case-insensitive)
// forces auth off regardless of creds; any other value (incl. BASIC or unset)
// uses the resolved creds — an empty username disables auth, mirroring the
// Rust router's AuthMode::None. (The router HTTP surface supports basic/none
// only; OIDC modes are not honoured here.)
func resolveRouterAuth() routerapi.BasicAuthConfig {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("AUTH_MODE")), "NONE") {
		return routerapi.BasicAuthConfig{}
	}
	return routerapi.BasicAuthConfig{
		Username: envFirst("FC_ROUTER_AUTH_USER", "AUTH_BASIC_USERNAME", ""),
		Password: envFirst("FC_ROUTER_AUTH_PASS", "AUTH_BASIC_PASSWORD", ""),
	}
}

// streamHealthBridge adapts the in-process stream.HealthService into
// the routerapi.StreamHealthProvider surface. Conversion is per-call so
// the router always sees fresh counters.
type streamHealthBridge struct{ svc *stream.HealthService }

func (b streamHealthBridge) Aggregate() routerapi.StreamHealthAggregate {
	agg := b.svc.Aggregate()
	streams := make([]routerapi.StreamHealth, 0, len(agg.Streams))
	for _, s := range agg.Streams {
		streams = append(streams, routerapi.StreamHealth{
			Name:           s.Name,
			Status:         string(s.Status),
			Running:        s.Running,
			Healthy:        s.Healthy,
			BatchSequence:  s.BatchSequence,
			ErrorCount:     s.ErrorCount,
			LastPollTimeMs: s.LastPollTimeMs,
		})
	}
	return routerapi.StreamHealthAggregate{
		Healthy:          agg.Healthy,
		TotalStreams:     agg.TotalStreams,
		HealthyStreams:   agg.HealthyStreams,
		UnhealthyStreams: agg.UnhealthyStreams,
		Streams:          streams,
	}
}

func (b streamHealthBridge) IsLive() bool  { return b.svc.IsLive() }
func (b streamHealthBridge) IsReady() bool { return b.svc.IsReady() }

// newRouterServer wraps router.NewServer with the env-driven router
// config. When cfg.RouterConfigURL is empty we honour cfg.DefaultBroker
// to synthesize an in-process Postgres pool config so fc-dev "just works".
func newRouterServer(cfg EnvCfg, pool *pgxpool.Pool) (*router.Server, error) {
	rcfg := router.ServerConfig{
		DevMode:          cfg.RouterDevMode,
		ConfigURL:        cfg.RouterConfigURL,
		NotifyWebhookURL: cfg.RouterNotifyWebhookURL,
		DrainTimeout:     time.Duration(cfg.RouterDrainTimeoutSec) * time.Second,
		StandbyEnabled:   cfg.StandbyEnabled,
		StandbyRedisURL:  cfg.StandbyRedisURL,
		StandbyLockKey:   cfg.StandbyLockKey,
		// ALB self-registration: register on leader-gain / non-standby start,
		// deregister on leader-loss / drain. No-op unless FC_ALB_ENABLED + the
		// target group ARN + instance IP are set.
		Traffic: router.TrafficConfig{
			Enabled:                    cfg.ALBEnabled,
			TargetGroupARN:             cfg.ALBTargetGroupARN,
			InstanceIP:                 cfg.ALBInstanceIP,
			Port:                       int32(cfg.ALBPort),
			Region:                     cfg.ALBRegion,
			DeregistrationDelaySeconds: int64(cfg.ALBDeregDelaySec),
		},
	}
	srv, err := router.NewServer(rcfg)
	if err != nil {
		return nil, err
	}

	// If no remote config URL was provided, honour the default-broker
	// switch so dev / single-tenant deployments don't need an HTTP
	// config service just to spin up one pool.
	if cfg.RouterConfigURL == "" && cfg.DefaultBroker == "postgres" {
		bootCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		dbURL := cfg.DatabaseURL
		if dbURL == "" {
			dbURL = "postgresql://postgres@localhost:5432/flowcatalyst"
		}
		def := defaultPostgresRouterConfig(dbURL)

		// Init the queue's tables before Reconfigure spins up consumers
		// that will try to SELECT from them. The postgres queue backend
		// exposes InitSchema via the Embedded interface — the factory
		// returns the same *Queue for Consumer and Publisher, so we
		// build a transient one here, init, then drop it. The Manager
		// will then build its own consumer per pool.
		for _, qc := range def.Queues {
			if err := initQueueSchema(bootCtx, qc); err != nil {
				return nil, fmt.Errorf("init queue schema for %q: %w", qc.Name, err)
			}
		}

		if err := srv.Manager.Reconfigure(bootCtx, def); err != nil {
			return nil, fmt.Errorf("default broker reconfigure: %w", err)
		}
		slog.Info("router: built-in postgres broker active", "pool", "default")
		_ = pool // reserved for future co-tenanted backends
	}
	return srv, nil
}

// initQueueSchema bootstraps the backend's tables when the underlying
// queue.Consumer also implements queue.Embedded (the in-process backends
// — Postgres, SQLite — do). External backends like SQS no-op cleanly
// because their factory doesn't implement Embedded.
func initQueueSchema(ctx context.Context, qc common.QueueConfig) error {
	c, err := queue.NewConsumer(ctx, qc)
	if err != nil {
		return err
	}
	if e, ok := c.(queue.Embedded); ok {
		if err := e.InitSchema(ctx); err != nil {
			return err
		}
	}
	// We don't keep this consumer; Manager.Reconfigure builds its own.
	c.Stop()
	return nil
}

// defaultPostgresRouterConfig synthesizes a single-pool config pointing
// at the shared Postgres pool. Used by fc-dev (and any single-tenant
// deployment) so the router has somewhere to poll without an external
// config service.
func defaultPostgresRouterConfig(databaseURL string) common.RouterConfig {
	return common.RouterConfig{
		ProcessingPools: []common.PoolConfig{
			{Code: "default", Concurrency: 4},
		},
		Queues: []common.QueueConfig{
			{Name: "default", URI: postgresQueueURI(databaseURL), VisibilityTimeout: 30},
		},
	}
}

// postgresQueueURI takes a database URL like
//
//	postgresql://user:pass@host:5432/db
//
// and produces the queue URI the postgres queue backend expects, which
// it recognises by the `postgres://` scheme.
func postgresQueueURI(databaseURL string) string {
	// The postgres queue backend accepts the standard pg URL; only the
	// scheme matters to the queue registry. Caller's URL already uses
	// postgresql:// or postgres://; normalise to postgres://.
	if len(databaseURL) > 13 && databaseURL[:13] == "postgresql://" {
		return "postgres://" + databaseURL[13:]
	}
	return databaseURL
}
