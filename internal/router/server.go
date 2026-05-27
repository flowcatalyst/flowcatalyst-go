package router

import (
	"context"
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
}

// Server is the reusable router wiring used by both cmd/fc-router (with
// its own HTTP listener + signal handler) and fc-server's StartRouter
// subsystem (run in-process alongside the platform API).
//
// Fields are public so callers can wire a /health, /ready, /metrics
// surface without depending on private state.
type Server struct {
	Cfg       ServerConfig
	Notifier  *Notifier
	Mediator  Mediator
	Breakers  *BreakerRegistry
	Tracker   *InFlightTracker
	Manager   *Manager
	Warnings  *WarningService
	Health    *HealthService
	Lifecycle *LifecycleManager

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
		cfg.ConfigPollInterval = 30 * time.Second
	}
	if cfg.InFlightReapMaxAge == 0 {
		cfg.InFlightReapMaxAge = 15 * time.Minute
	}
	if cfg.BreakerIdleMaxAge == 0 {
		cfg.BreakerIdleMaxAge = time.Hour
	}

	s := &Server{
		Cfg:      cfg,
		Notifier: NewNotifier(cfg.NotifyWebhookURL, 20, 10*time.Second),
		Mediator: pickMediator(cfg.DevMode),
		Breakers: NewBreakerRegistry(DefaultBreakerConfig()),
		Tracker:  NewInFlightTracker(),
	}
	s.Manager = NewManager(s.Mediator, s.Breakers, s.Tracker)

	// Warning + health services back the deferred /monitoring/* and
	// /warnings/* HTTP surface; for now they're observable via slog
	// only. WarningService forwards added warnings to the existing
	// Notifier so the webhook path stays consistent.
	s.Warnings = NewWarningService(DefaultWarningServiceConfig())
	s.Warnings.SetNotifier(s.Notifier)
	s.Health = NewHealthService(DefaultHealthServiceConfig(), s.Warnings)
	s.Lifecycle = NewLifecycleManager(DefaultLifecycleConfig(), s.Warnings, s.Health)

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
	return s, nil
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
	s.Lifecycle.Start(ctx)

	startPools := func(c context.Context) {
		if s.Cfg.ConfigURL == "" {
			slog.Warn("router config URL not set; no pools will start")
			return
		}
		cs := NewConfigSource(s.Cfg.ConfigURL)
		go Watch(c, cs, s.Manager, s.Cfg.ConfigPollInterval)
	}

	if s.election != nil {
		if err := s.election.Start(ctx); err != nil {
			return err
		}
		go gateOnLeadership(ctx, s.election, s.Manager, startPools)
	} else {
		startPools(ctx)
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
		}
	}
}

func pickMediator(devMode bool) Mediator {
	if devMode {
		return NewHTTPMediator(DevMediatorConfig())
	}
	return NewHTTPMediator(DefaultMediatorConfig())
}

// gateOnLeadership starts the pool config watcher only when this
// instance is the leader. On loss of leadership it cancels the
// per-leadership context so pools wind down.
func gateOnLeadership(ctx context.Context, election *standby.Election, manager *Manager, startPools func(context.Context)) {
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
			slog.Info("router assumed leadership; pools started")
			return
		}
		if poolCancel != nil {
			poolCancel()
			poolCtx, poolCancel = nil, nil
			drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
			defer drainCancel()
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

