package stream

import (
	"sync"
	"sync/atomic"
	"time"
)

// StreamStatus is the coarse running-state enum.
type StreamStatus string

const (
	StatusRunning StreamStatus = "RUNNING"
	StatusStopped StreamStatus = "STOPPED"
)

// Health tracks one projection's runtime state. Thread-safe via atomics
// — every field except Name is updated lock-free. Mirrors
// crates/fc-stream/src/health.rs::StreamHealth.
type Health struct {
	name string

	running        atomic.Bool
	processedCount atomic.Uint64
	errorCount     atomic.Uint64
	// lastPollMs is set on every successful AddProcessed call.
	lastPollMs atomic.Int64
}

// NewHealth builds a stopped health tracker with the supplied name.
func NewHealth(name string) *Health {
	return &Health{name: name}
}

// Name returns the projection identifier.
func (h *Health) Name() string { return h.name }

// SetRunning toggles the running flag. Called from Projector.Run entry
// + deferred at exit.
func (h *Health) SetRunning(running bool) { h.running.Store(running) }

// IsRunning reports the current running flag.
func (h *Health) IsRunning() bool { return h.running.Load() }

// AddProcessed bumps the processed counter and stamps the last-poll
// time. Called on each non-empty Step.
func (h *Health) AddProcessed(n uint64) {
	h.processedCount.Add(n)
	h.lastPollMs.Store(time.Now().UnixMilli())
}

// RecordError bumps the error counter. Called when Step returns an error.
func (h *Health) RecordError() { h.errorCount.Add(1) }

// IsHealthy is currently equivalent to IsRunning — a projection that's
// up is healthy. Matches Rust's `is_healthy = is_running`.
func (h *Health) IsHealthy() bool { return h.IsRunning() }

// Snapshot is the API-facing point-in-time view of one projection.
type Snapshot struct {
	Name           string       `json:"name"`
	Status         StreamStatus `json:"status"`
	Running        bool         `json:"running"`
	Healthy        bool         `json:"healthy"`
	BatchSequence  uint64       `json:"batchSequence"`
	ErrorCount     uint64       `json:"errorCount"`
	LastPollTimeMs int64        `json:"lastPollTimeMs"`
}

// Status returns the snapshot for this projection.
func (h *Health) Status() Snapshot {
	running := h.IsRunning()
	status := StatusStopped
	if running {
		status = StatusRunning
	}
	return Snapshot{
		Name:           h.name,
		Status:         status,
		Running:        running,
		Healthy:        running,
		BatchSequence:  h.processedCount.Load(),
		ErrorCount:     h.errorCount.Load(),
		LastPollTimeMs: h.lastPollMs.Load(),
	}
}

// HealthService aggregates per-projection Health trackers. Used by the
// stream processor's HTTP surface and (when co-tenanted with the
// router) by the router's stream-health endpoints.
//
// Mirrors crates/fc-stream/src/health.rs::StreamHealthService.
type HealthService struct {
	mu      sync.RWMutex
	healths []*Health
}

// NewHealthService builds an empty service.
func NewHealthService() *HealthService { return &HealthService{} }

// Register adds a projection health to the aggregator. Safe to call
// concurrently with reads.
func (s *HealthService) Register(h *Health) {
	s.mu.Lock()
	s.healths = append(s.healths, h)
	s.mu.Unlock()
}

// IsLive reports liveness — true when at least one projection is
// running. Returns false when no projections have been registered to
// match Rust semantics ("not configured" → not live).
func (s *HealthService) IsLive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.healths) == 0 {
		return false
	}
	for _, h := range s.healths {
		if h.IsRunning() {
			return true
		}
	}
	return false
}

// IsReady reports readiness — true when every registered projection is
// running. Empty registry → not ready.
func (s *HealthService) IsReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.healths) == 0 {
		return false
	}
	for _, h := range s.healths {
		if !h.IsHealthy() {
			return false
		}
	}
	return true
}

// Aggregate is the API-facing aggregate snapshot.
type Aggregate struct {
	Healthy          bool       `json:"healthy"`
	TotalStreams     int        `json:"totalStreams"`
	HealthyStreams   int        `json:"healthyStreams"`
	UnhealthyStreams int        `json:"unhealthyStreams"`
	Streams          []Snapshot `json:"streams"`
}

// Aggregate returns the aggregated snapshot for /monitoring/stream-health.
func (s *HealthService) Aggregate() Aggregate {
	s.mu.RLock()
	defer s.mu.RUnlock()

	streams := make([]Snapshot, 0, len(s.healths))
	healthy := 0
	for _, h := range s.healths {
		snap := h.Status()
		streams = append(streams, snap)
		if snap.Healthy {
			healthy++
		}
	}
	total := len(streams)
	return Aggregate{
		Healthy:          total > 0 && healthy == total,
		TotalStreams:     total,
		HealthyStreams:   healthy,
		UnhealthyStreams: total - healthy,
		Streams:          streams,
	}
}

// Count returns the number of registered projections.
func (s *HealthService) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.healths)
}
