package router

import "github.com/flowcatalyst/flowcatalyst-go/internal/common"

// Shared types referenced by HealthService + the /monitoring/* HTTP
// surface. Mirrors `fc_common::{HealthStatus, HealthReport,
// ConsumerHealth, PoolStats}`. Field names use Rust-camelCase JSON tags
// so the API surface lands without translation.

// HealthStatus is the coarse system-health verdict.
type HealthStatus string

const (
	HealthHealthy  HealthStatus = "Healthy"
	HealthWarning  HealthStatus = "Warning"
	HealthDegraded HealthStatus = "Degraded"
)

// HealthReport is the JSON shape returned by /monitoring/health.
type HealthReport struct {
	Status             HealthStatus `json:"status"`
	PoolsHealthy       uint32       `json:"poolsHealthy"`
	PoolsUnhealthy     uint32       `json:"poolsUnhealthy"`
	ConsumersHealthy   uint32       `json:"consumersHealthy"`
	ConsumersUnhealthy uint32       `json:"consumersUnhealthy"`
	ActiveWarnings     uint32       `json:"activeWarnings"`
	CriticalWarnings   uint32       `json:"criticalWarnings"`
	Issues             []string     `json:"issues"`
}

// ConsumerHealth is the per-consumer snapshot returned by
// /monitoring/consumers/{id}.
type ConsumerHealth struct {
	QueueIdentifier     string `json:"queueIdentifier"`
	IsHealthy           bool   `json:"isHealthy"`
	LastPollTimeMs      *int64 `json:"lastPollTimeMs,omitempty"`
	TimeSinceLastPollMs *int64 `json:"timeSinceLastPollMs,omitempty"`
	IsRunning           bool   `json:"isRunning"`
}

// PoolStats is the per-pool snapshot returned by /monitoring/pools.
type PoolStats struct {
	PoolCode           string                      `json:"poolCode"`
	Concurrency        uint32                      `json:"concurrency"`
	ActiveWorkers      uint32                      `json:"activeWorkers"`
	QueueSize          uint32                      `json:"queueSize"`
	QueueCapacity      uint32                      `json:"queueCapacity"`
	MessageGroupCount  uint32                      `json:"messageGroupCount"`
	RateLimitPerMinute *uint32                     `json:"rateLimitPerMinute,omitempty"`
	IsRateLimited      bool                        `json:"isRateLimited"`
	Metrics            *common.EnhancedPoolMetrics `json:"metrics,omitempty"`
	// Histogram is the cumulative mediation-latency histogram, emitted by the
	// Prometheus collector as fc_mediation_duration_seconds. Not serialized to
	// the dashboard JSON (the dashboard uses Metrics.ProcessingTime instead).
	Histogram MediationHistogram `json:"-"`
}
