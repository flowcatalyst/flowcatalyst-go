package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/standby"
)

// ServerConfig is the runtime config for an in-process router server.
// Mirrors the env-driven knobs that cmd/fc-router/main.go reads — the
// standalone binary and fc-server's StartRouter both go through here.
type ServerConfig struct {
	// DevMode swaps in the dev mediator (relaxed TLS, longer timeouts).
	DevMode bool

	// ConfigURL is the FLOWCATALYST_CONFIG_URL the router polls for
	// pool definitions. Empty disables config sync — no pools will run.
	ConfigURL string

	// ConfigPollInterval governs how often ConfigURL is re-fetched.
	// Zero falls back to 30s (matches cmd/fc-router).
	ConfigPollInterval time.Duration

	// NotifyWebhookURL receives stall + backlog warnings. Empty → log-only.
	NotifyWebhookURL string

	// DrainTimeout is the upper bound for graceful drain on shutdown.
	// Zero falls back to 60s.
	DrainTimeout time.Duration

	// InFlightReapMaxAge bounds the in-flight tracker against backend
	// bugs that fail to remove completed messages. Zero falls back to
	// 15m. Reaper ticks every 5m regardless.
	InFlightReapMaxAge time.Duration

	// BreakerIdleMaxAge bounds the per-endpoint circuit breaker
	// registry against unbounded growth from short-lived target URLs.
	// Zero falls back to 1h, matching Rust
	// circuit_breaker_registry.rs::evict_idle. The same reaper goroutine
	// that handles InFlightReapMaxAge calls BreakerRegistry.Evict on
	// each 5m tick.
	BreakerIdleMaxAge time.Duration

	// Standby (Redis leader election). When enabled the pool config
	// watcher only runs while this instance holds the lock.
	StandbyEnabled  bool
	StandbyRedisURL string
	StandbyLockKey  string

	// Traffic management. When enabled, this instance is
	// registered/deregistered with the ALB target group as it
	// gains/loses leadership. Disabled by default.
	Traffic TrafficConfig
}

// Server is the reusable router wiring used by both cmd/fc-router (with
// its own HTTP listener + signal handler) and fc-server's StartRouter
// subsystem (run in-process alongside the platform API).
//
// Fields are public so callers can wire a /health, /ready, /metrics
// surface without depending on private state.
type Server struct {
	Cfg          ServerConfig
	Notifier     *Notifier
	Mediator     Mediator
	Breakers     *BreakerRegistry
	Tracker      *InFlightTracker
	Manager      *Manager
	Warnings     *WarningService
	Health       *HealthService
	Lifecycle    *LifecycleManager
	BrokerStats  *CachedBrokerStats
	ConfigSource *ConfigSource
	Traffic      *TrafficStrategy

	election *standby.Election
}

// NewServer assembles the long-lived components. Nothing starts running
// until Run is called. Returns an error only when standby is enabled
// but the Redis client cannot be constructed.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = 60 * time.Second
	}
	if cfg.ConfigPollInterval == 0 {
		cfg.ConfigPollInterval = 300 * time.Second // 5m, matching the Rust/Java default
	}
	if cfg.InFlightReapMaxAge == 0 {
		cfg.InFlightReapMaxAge = 15 * time.Minute
	}
	if cfg.BreakerIdleMaxAge == 0 {
		cfg.BreakerIdleMaxAge = time.Hour
	}

	breakers := NewBreakerRegistry(DefaultBreakerConfig())
	s := &Server{
		Cfg:      cfg,
		Notifier: NewNotifier(cfg.NotifyWebhookURL, 20, 10*time.Second),
		Mediator: pickMediator(cfg.DevMode, breakers),
		Breakers: breakers,
		Tracker:  NewInFlightTracker(),
	}
	s.Manager = NewManager(s.Mediator, s.Tracker)
	s.BrokerStats = NewCachedBrokerStats(s.Manager)
	if cfg.ConfigURL != "" {
		s.ConfigSource = NewConfigSource(cfg.ConfigURL)
	}

	// Warning + health services back the deferred /monitoring/* and
	// /warnings/* HTTP surface; for now they're observable via slog
	// only. WarningService forwards added warnings to the existing
	// Notifier so the webhook path stays consistent.
	s.Warnings = NewWarningService(DefaultWarningServiceConfig())
	s.Warnings.SetNotifier(s.Notifier)
	// Surface mediator config-error warnings (400/401/403/404, 501→Critical) on
	// /warnings and into health. Opt-in setter avoids a constructor dependency.
	if hm, ok := s.Mediator.(*HTTPMediator); ok {
		hm.SetWarnings(s.Warnings)
	}
	// Surface manager routing/capacity warnings (unknown pool_code, all-pools-full).
	s.Manager.SetWarnings(s.Warnings)
	s.Health = NewHealthService(DefaultHealthServiceConfig(), s.Warnings)
	s.Lifecycle = NewLifecycleManager(DefaultLifecycleConfig(), s.Warnings, s.Health)
	// The Manager owns the consumer poll loops, so it is the consumer-restart
	// source; the lifecycle consumer-health tick restarts any stalled loop.
	s.Lifecycle.SetConsumerRestarter(s.Manager)
	s.Lifecycle.SetPoolStatsProvider(s.Manager)

	if cfg.StandbyEnabled {
		ecfg := common.NewLeaderElectionConfig(cfg.StandbyRedisURL)
		if cfg.StandbyLockKey != "" {
			ecfg.LockKey = cfg.StandbyLockKey
		}
		el, err := standby.New(ecfg)
		if err != nil {
			return nil, err
		}
		s.election = el
	}
	// Traffic strategy is constructed eagerly so /monitoring/traffic-status
	// has something to report even when disabled. NewTrafficStrategy is
	// a no-op when cfg.Traffic.Enabled=false.
	ts, err := NewTrafficStrategy(context.Background(), cfg.Traffic)
	if err != nil {
		return nil, err
	}
	s.Traffic = ts
	return s, nil
}

// Reload triggers an immediate config refetch + reconfigure. Returns
// nil when the source reports ErrUnchanged — the config is still
// "applied" in the sense that what's running matches the source.
// Used by POST /config/reload to force-sync ahead of the watcher tick.
func (s *Server) Reload(ctx context.Context) error {
	if s.ConfigSource == nil {
		return errors.New("router has no config source configured")
	}
	cfg, err := s.ConfigSource.Fetch(ctx)
	if err != nil {
		if errors.Is(err, ErrUnchanged) {
			return nil
		}
		return err
	}
	return s.Manager.Reconfigure(ctx, *cfg)
}

// IsLeader reports whether this instance currently holds the standby
// lock. Always true when standby is disabled.
func (s *Server) IsLeader() bool {
	if s.election == nil {
		return true
	}
	return s.election.IsLeader()
}

// Run starts every subsystem and blocks until ctx is cancelled. On
// cancellation it performs a graceful drain (up to DrainTimeout) and
// then a full Manager + Notifier + Election shutdown.
func (s *Server) Run(ctx context.Context) error {
	go s.Notifier.Run(ctx)
	go NewStallDetector(DefaultStallConfig(), s.Tracker, s.Notifier).Watch(ctx)
	go NewQueueHealthMonitor(DefaultQueueHealthConfig(), s.Notifier).Watch(ctx, s.Manager.Consumers)
	go s.reapInFlight(ctx)
	SpawnBrokerStatsRefresh(ctx, s.BrokerStats)
	s.Lifecycle.Start(ctx)

	startPools := func(c context.Context) {
		if s.ConfigSource == nil {
			// Pools may already be running because the caller bootstrapped
			// them via a default broker (e.g. server.newRouterServer when
			// FC_DEFAULT_BROKER=postgres). Don't warn in that case — the
			// router is correctly configured, there's just no remote URL
			// to poll for hot reloads.
			if s.Manager.PoolCount() > 0 {
				slog.Info("router config URL not set; using bootstrapped pools (no hot reload)",
					"pool_count", s.Manager.PoolCount())
				return
			}
			slog.Warn("router config URL not set; no pools will start")
			return
		}
		go Watch(c, s.ConfigSource, s.Manager, s.Cfg.ConfigPollInterval)
	}

	if s.election != nil {
		if err := s.election.Start(ctx); err != nil {
			return err
		}
		go gateOnLeadership(ctx, s.election, s.Manager, s.Traffic, startPools)
	} else {
		startPools(ctx)
		// Non-standby mode: still register with the ALB if traffic
		// management is enabled (single-instance deployment).
		if err := s.Traffic.Register(ctx); err != nil {
			slog.Warn("traffic register failed", "err", err)
		}
	}

	<-ctx.Done()

	slog.Info("router shutdown initiated",
		"in_flight", s.Tracker.Count(), "drain_timeout", s.Cfg.DrainTimeout)

	drainCtx, drainCancel := context.WithTimeout(context.Background(), s.Cfg.DrainTimeout)
	defer drainCancel()
	if err := drain(drainCtx, s.Tracker); err != nil {
		slog.Warn("router drain incomplete",
			"err", err, "remaining_in_flight", s.Tracker.Count())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	// Deregister early in shutdown so the ALB stops routing new traffic
	// before the drain finishes.
	if err := s.Traffic.Deregister(shutdownCtx); err != nil {
		slog.Warn("traffic deregister on shutdown failed", "err", err)
	}
	if err := s.Lifecycle.Shutdown(shutdownCtx); err != nil {
		slog.Warn("router lifecycle shutdown error", "err", err)
	}
	if err := s.Manager.Shutdown(shutdownCtx); err != nil {
		slog.Warn("router manager shutdown error", "err", err)
	}
	if s.election != nil {
		if err := s.election.Stop(shutdownCtx); err != nil {
			slog.Warn("router standby stop error", "err", err)
		}
	}
	s.Notifier.Stop()

	slog.Info("router stopped")
	return nil
}

// reapInFlight is the periodic janitor: it prunes the in-flight tracker
// (entries older than InFlightReapMaxAge) and the circuit-breaker
// registry (idle entries older than BreakerIdleMaxAge). Mirrors the
// Rust stale-entry reaper in lifecycle.rs (5 min cadence).
// inFlightMemoryWarnThreshold mirrors the Rust memory-health monitor: warn when
// the in-flight tracker grows past this, signalling a possible callback leak.
const inFlightMemoryWarnThreshold = 10000

func (s *Server) reapInFlight(ctx context.Context) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if n := s.Tracker.Reap(s.Cfg.InFlightReapMaxAge); n > 0 {
				slog.Warn("router reaped stale in-flight entries", "count", n)
			}
			if n := s.Breakers.Evict(s.Cfg.BreakerIdleMaxAge); n > 0 {
				slog.Info("router evicted idle circuit breakers", "count", n)
			}
			// Memory-health: warn when the in-flight tracker grows past the
			// threshold — a possible callback leak. Mirrors the Rust memory
			// monitor (lifecycle.rs); piggybacks on this reaper's tick.
			if n := s.Tracker.Count(); n > inFlightMemoryWarnThreshold {
				s.Warnings.Add(WarningCategoryResource, WarningError,
					fmt.Sprintf("in-flight tracker is large (%d entries) - possible leak", n), "router")
			}
		}
	}
}

func pickMediator(devMode bool, breakers *BreakerRegistry) Mediator {
	if devMode {
		return NewHTTPMediator(DevMediatorConfig(), breakers)
	}
	return NewHTTPMediator(DefaultMediatorConfig(), breakers)
}

// gateOnLeadership starts the pool config watcher only when this
// instance is the leader. On loss of leadership it cancels the
// per-leadership context so pools wind down. Also drives the traffic
// strategy: register on leader-gain, deregister on leader-loss so an
// ALB stops routing requests to standing-by replicas.
func gateOnLeadership(ctx context.Context, election *standby.Election, manager *Manager, traffic *TrafficStrategy, startPools func(context.Context)) {
	sub := election.Subscribe()
	var poolCtx context.Context
	var poolCancel context.CancelFunc

	apply := func(isLeader bool) {
		if isLeader {
			if poolCtx != nil {
				return
			}
			poolCtx, poolCancel = context.WithCancel(ctx)
			startPools(poolCtx)
			if err := traffic.Register(ctx); err != nil {
				slog.Warn("traffic register on leader-gain failed", "err", err)
			}
			slog.Info("router assumed leadership; pools started")
			return
		}
		if poolCancel != nil {
			poolCancel()
			poolCtx, poolCancel = nil, nil
			// Deregister BEFORE draining so the ALB stops routing new
			// requests; in-flight work then finishes naturally.
			drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
			defer drainCancel()
			if err := traffic.Deregister(drainCtx); err != nil {
				slog.Warn("traffic deregister on leader-loss failed", "err", err)
			}
			_ = manager.Shutdown(drainCtx)
			slog.Info("router lost leadership; pools drained")
		}
	}

	apply(election.IsLeader())
	for {
		select {
		case <-ctx.Done():
			if poolCancel != nil {
				poolCancel()
			}
			return
		case ch := <-sub:
			apply(ch.IsLeader)
		}
	}
}

// drain waits for the in-flight tracker count to reach zero, or ctx to expire.
func drain(ctx context.Context, tracker *InFlightTracker) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		if tracker.Count() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
