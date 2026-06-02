// Package api wires the router's HTTP surface using huma — the same
// OpenAPI library the platform uses, so the swagger surface is shared.
//
// Sibling of crates/fc-router/src/api/mod.rs. Endpoint paths and JSON
// shapes mirror the Rust router where it makes sense; the few deferred
// endpoints are documented inline.
//
// Mount pattern (cmd/fc-router):
//
//	r := chi.NewRouter()
//	api := humachi.New(r, huma.DefaultConfig("FlowCatalyst Router API", "dev"))
//	routerapi.Register(api, routerapi.FromServer(srv))
//	routerapi.MountDashboard(r) // HTML — not a huma operation
package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
)

// Version is the FlowCatalyst release reported by /monitoring. Override
// at link time with `-ldflags "-X .../api.Version=0.x.y"`.
var Version = "dev"

// startTime is captured at first call to Register so /monitoring/health's
// uptimeMillis is process-relative.
var startTime time.Time

// ─────────────────────────────────────────────────────────────────────
// Provider interfaces (kept tiny so tests can substitute small fakes)
// ─────────────────────────────────────────────────────────────────────

// PoolStatsProvider feeds the pool-stats / monitoring endpoints.
type PoolStatsProvider interface {
	PoolStats() []router.PoolStats
}

// CircuitBreakerOpenCounter reports the count of currently-open breakers.
type CircuitBreakerOpenCounter interface {
	OpenCount() int
}

// BreakerSnapshotProvider exposes the full breaker registry plus reset
// operations. Optional.
type BreakerSnapshotProvider interface {
	Snapshot() map[string]router.BreakerStats
	Reset(name string) bool
	ResetAll() int
}

// InFlightSnapshotProvider exposes the in-flight tracker entries.
type InFlightSnapshotProvider interface {
	Snapshot() []common.InFlightMessage
}

// BrokerStatsProvider serves cached + windowed queue metrics.
type BrokerStatsProvider interface {
	GetWindowed(window time.Duration) []queue.Metrics
	Refresh()
	AgeSeconds() int64
}

// PoolUpdater applies runtime config changes.
type PoolUpdater interface {
	UpdatePool(code string, concurrency uint32, rateLimitPerMinute *uint32, setRateLimit bool) bool
}

// PublisherProvider returns the publisher bound to a pool's queue.
// Used by POST /messages.
type PublisherProvider interface {
	Publisher(ctx context.Context, poolCode string) (queue.Publisher, error)
}

// LeaderInfo reports leadership / standby state.
type LeaderInfo interface {
	IsLeader() bool
	StandbyEnabled() bool
	InstanceID() string
}

// ConfigReloader triggers an immediate config refresh.
// Optional — when nil POST /config/reload returns 501.
type ConfigReloader interface {
	Reload(ctx context.Context) error
}

// TrafficStatusProvider exposes the live ALB target-group status.
// Optional — when nil the /monitoring/traffic-status endpoint reports
// `enabled: false`.
type TrafficStatusProvider interface {
	Status() router.TrafficStatus
}

// StreamHealth is the projection-level snapshot consumed by the stream
// health endpoints. Kept package-local so api callers don't need to
// import internal/stream — fc-server adapts its stream.HealthService
// into this interface.
type StreamHealth struct {
	Name           string
	Status         string
	Running        bool
	Healthy        bool
	BatchSequence  uint64
	ErrorCount     uint64
	LastPollTimeMs int64
}

// StreamHealthAggregate is the aggregated snapshot.
type StreamHealthAggregate struct {
	Healthy          bool
	TotalStreams     int
	HealthyStreams   int
	UnhealthyStreams int
	Streams          []StreamHealth
}

// StreamHealthProvider exposes live stream-processor health. Optional —
// when nil the /monitoring/stream-health* endpoints report a stub.
type StreamHealthProvider interface {
	Aggregate() StreamHealthAggregate
	IsLive() bool
	IsReady() bool
}

// ─────────────────────────────────────────────────────────────────────
// State — bundles every dependency the handlers need.
// ─────────────────────────────────────────────────────────────────────

// State is the dependency bundle passed to Register. Every field except
// Warnings/Health is optional; handlers gracefully degrade when a
// provider is nil (return 503 or an empty payload, matching Rust).
type State struct {
	Warnings     *router.WarningService
	Health       *router.HealthService
	PoolStats    PoolStatsProvider
	OpenCount    CircuitBreakerOpenCounter
	Breakers     BreakerSnapshotProvider
	InFlight     InFlightSnapshotProvider
	BrokerStats  BrokerStatsProvider
	PoolUpdater  PoolUpdater
	Publisher    PublisherProvider
	Leader       LeaderInfo
	Reloader     ConfigReloader
	Traffic      TrafficStatusProvider
	StreamHealth StreamHealthProvider

	// Mocks is the counter set for /api/test/*. Created automatically by
	// FromServer; tests can substitute their own.
	Mocks *MockState
}

// FromServer builds a fully-populated State from a *router.Server.
func FromServer(s *router.Server) *State {
	return &State{
		Warnings:    s.Warnings,
		Health:      s.Health,
		PoolStats:   managerPoolStatsAdapter{m: s.Manager},
		OpenCount:   breakersAdapter{breakers: s.Breakers},
		Breakers:    breakerSnapshotAdapter{breakers: s.Breakers},
		InFlight:    inFlightAdapter{tracker: s.Tracker},
		BrokerStats: brokerStatsAdapter{cache: s.BrokerStats},
		PoolUpdater: poolUpdaterAdapter{m: s.Manager},
		Publisher:   publisherAdapter{m: s.Manager},
		Leader:      leaderAdapter{s: s},
		Reloader:    reloaderAdapter{s: s},
		Traffic:     trafficAdapter{traffic: s.Traffic},
		Mocks:       NewMockState(),
	}
}

type trafficAdapter struct{ traffic *router.TrafficStrategy }

func (a trafficAdapter) Status() router.TrafficStatus {
	if a.traffic == nil {
		return router.TrafficStatus{Enabled: false, Mode: "disabled"}
	}
	return a.traffic.Status()
}

// Register mounts every router endpoint on the supplied huma API.
// Call MountDashboard separately on the underlying chi router to serve
// the embedded HTML.
func Register(api huma.API, s *State) {
	if startTime.IsZero() {
		startTime = time.Now()
	}
	registerHealth(api, s)
	registerMonitoring(api, s)
	registerDashboardReads(api, s)
	registerWarnings(api, s)
	registerMutations(api, s)
	registerMessages(api, s)
	registerMocks(api, s)
	registerMisc(api, s)
}

// MountDashboard registers the embedded HTML dashboard on the chi
// router. Mounted separately from huma because huma operations are
// JSON-shaped. Mounts at /monitoring/dashboard and /dashboard.html.
func MountDashboard(r chi.Router) {
	r.Get("/monitoring/dashboard", handleDashboardHTML())
	r.Get("/dashboard.html", handleDashboardHTML())
}

// ─────────────────────────────────────────────────────────────────────
// Adapters that connect router.Server to the small State interfaces.
// ─────────────────────────────────────────────────────────────────────

type managerPoolStatsAdapter struct{ m *router.Manager }

func (a managerPoolStatsAdapter) PoolStats() []router.PoolStats {
	if a.m == nil {
		return nil
	}
	return a.m.PoolStats()
}

type breakersAdapter struct{ breakers *router.BreakerRegistry }

func (a breakersAdapter) OpenCount() int {
	if a.breakers == nil {
		return 0
	}
	n := 0
	for _, s := range a.breakers.Snapshot() {
		if s.State == router.CircuitOpen {
			n++
		}
	}
	return n
}

type breakerSnapshotAdapter struct{ breakers *router.BreakerRegistry }

func (a breakerSnapshotAdapter) Snapshot() map[string]router.BreakerStats {
	if a.breakers == nil {
		return nil
	}
	return a.breakers.Snapshot()
}

func (a breakerSnapshotAdapter) Reset(name string) bool {
	if a.breakers == nil {
		return false
	}
	return a.breakers.Reset(name)
}

func (a breakerSnapshotAdapter) ResetAll() int {
	if a.breakers == nil {
		return 0
	}
	return a.breakers.ResetAll()
}

type inFlightAdapter struct{ tracker *router.InFlightTracker }

func (a inFlightAdapter) Snapshot() []common.InFlightMessage {
	if a.tracker == nil {
		return nil
	}
	return a.tracker.Snapshot()
}

type brokerStatsAdapter struct{ cache *router.CachedBrokerStats }

func (a brokerStatsAdapter) GetWindowed(window time.Duration) []queue.Metrics {
	if a.cache == nil {
		return nil
	}
	return a.cache.GetWindowed(window)
}

func (a brokerStatsAdapter) Refresh() {
	if a.cache == nil {
		return
	}
	a.cache.Refresh(context.Background())
}

func (a brokerStatsAdapter) AgeSeconds() int64 {
	if a.cache == nil {
		return -1
	}
	return a.cache.AgeSeconds()
}

type poolUpdaterAdapter struct{ m *router.Manager }

func (a poolUpdaterAdapter) UpdatePool(code string, concurrency uint32, rate *uint32, setRate bool) bool {
	if a.m == nil {
		return false
	}
	return a.m.UpdatePool(code, concurrency, rate, setRate)
}

type publisherAdapter struct{ m *router.Manager }

func (a publisherAdapter) Publisher(ctx context.Context, code string) (queue.Publisher, error) {
	if a.m == nil {
		return nil, notConfigured("publisher")
	}
	return a.m.Publisher(ctx, code)
}

type reloaderAdapter struct{ s *router.Server }

func (a reloaderAdapter) Reload(ctx context.Context) error {
	if a.s == nil {
		return notConfigured("reloader")
	}
	return a.s.Reload(ctx)
}

type leaderAdapter struct{ s *router.Server }

func (a leaderAdapter) IsLeader() bool {
	if a.s == nil {
		return false
	}
	return a.s.IsLeader()
}

func (a leaderAdapter) StandbyEnabled() bool {
	if a.s == nil {
		return false
	}
	return a.s.Cfg.StandbyEnabled
}

func (a leaderAdapter) InstanceID() string {
	if a.s == nil || a.s.Cfg.StandbyLockKey == "" {
		return "default"
	}
	return a.s.Cfg.StandbyLockKey
}

// ─────────────────────────────────────────────────────────────────────
// Common types / helpers
// ─────────────────────────────────────────────────────────────────────

// emptyInput is the placeholder for handlers with no input.
type emptyInput struct{}

// emptyOutput is the placeholder for handlers that return only a status.
type emptyOutput struct{}

// notConfigured returns the 503 error used when an optional provider
// is nil on the State.
func notConfigured(name string) error {
	return huma.Error503ServiceUnavailable(name + " not configured")
}
