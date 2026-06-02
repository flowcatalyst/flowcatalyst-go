// Package api wire DTOs. Public types are exported so huma can pick
// them up in the generated OpenAPI document; field-level JSON tags
// dictate the wire shape.
//
// Casing posture matches the Rust API:
//   - `/monitoring` (overview) + `/monitoring/pools` + `/warnings`     → snake_case (Rust serde default)
//   - `/monitoring/health` + dashboard endpoints (pool-stats/queue-stats/circuit-breakers/in-flight) → camelCase (explicit Rust renames)
//   - `/api/config` + reload / publish responses                        → snake_case
package api

import (
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
)

// ── Probe + simple health ────────────────────────────────────────────────

// ProbeResponse matches Rust ProbeResponse: `{"status": "LIVE"|"READY"|"NOT_READY"}`.
type ProbeResponse struct {
	Status string `json:"status"`
}

// SimpleHealthResponse is the legacy /health summary.
type SimpleHealthResponse struct {
	Status           string `json:"status"`
	Version          string `json:"version"`
	ActiveWarnings   uint32 `json:"active_warnings"`
	CriticalWarnings uint32 `json:"critical_warnings"`
}

// ── Monitoring overview ──────────────────────────────────────────────────

// MonitoringResponse mirrors Rust MonitoringResponse — snake_case.
type MonitoringResponse struct {
	Status           string           `json:"status"`
	Version          string           `json:"version"`
	HealthReport     WireHealthReport `json:"health_report"`
	PoolStats        []WirePoolStats  `json:"pool_stats"`
	ActiveWarnings   uint32           `json:"active_warnings"`
	CriticalWarnings uint32           `json:"critical_warnings"`
}

// WireHealthReport mirrors Rust HealthReport — snake_case.
type WireHealthReport struct {
	Status             router.HealthStatus `json:"status"`
	PoolsHealthy       uint32              `json:"pools_healthy"`
	PoolsUnhealthy     uint32              `json:"pools_unhealthy"`
	ConsumersHealthy   uint32              `json:"consumers_healthy"`
	ConsumersUnhealthy uint32              `json:"consumers_unhealthy"`
	ActiveWarnings     uint32              `json:"active_warnings"`
	CriticalWarnings   uint32              `json:"critical_warnings"`
	Issues             []string            `json:"issues"`
}

func fromHealthReport(r router.HealthReport) WireHealthReport {
	if r.Issues == nil {
		r.Issues = []string{}
	}
	return WireHealthReport{
		Status:             r.Status,
		PoolsHealthy:       r.PoolsHealthy,
		PoolsUnhealthy:     r.PoolsUnhealthy,
		ConsumersHealthy:   r.ConsumersHealthy,
		ConsumersUnhealthy: r.ConsumersUnhealthy,
		ActiveWarnings:     r.ActiveWarnings,
		CriticalWarnings:   r.CriticalWarnings,
		Issues:             r.Issues,
	}
}

// WirePoolStats mirrors Rust PoolStats — outer snake_case, inner
// EnhancedPoolMetrics camelCase (its serde rename).
type WirePoolStats struct {
	PoolCode           string                      `json:"pool_code"`
	Concurrency        uint32                      `json:"concurrency"`
	ActiveWorkers      uint32                      `json:"active_workers"`
	QueueSize          uint32                      `json:"queue_size"`
	QueueCapacity      uint32                      `json:"queue_capacity"`
	MessageGroupCount  uint32                      `json:"message_group_count"`
	RateLimitPerMinute *uint32                     `json:"rate_limit_per_minute,omitempty"`
	IsRateLimited      bool                        `json:"is_rate_limited"`
	Metrics            *common.EnhancedPoolMetrics `json:"metrics,omitempty"`
}

func fromPoolStats(s []router.PoolStats) []WirePoolStats {
	out := make([]WirePoolStats, len(s))
	for i, p := range s {
		out[i] = WirePoolStats{
			PoolCode:           p.PoolCode,
			Concurrency:        p.Concurrency,
			ActiveWorkers:      p.ActiveWorkers,
			QueueSize:          p.QueueSize,
			QueueCapacity:      p.QueueCapacity,
			MessageGroupCount:  p.MessageGroupCount,
			RateLimitPerMinute: p.RateLimitPerMinute,
			IsRateLimited:      p.IsRateLimited,
			Metrics:            p.Metrics,
		}
	}
	return out
}

// ── Dashboard health (/monitoring/health) ────────────────────────────────

// DashboardHealthResponse mirrors Rust DashboardHealthResponse. Top
// level snake_case (status / timestamp) with the details object using
// explicit camelCase renames.
type DashboardHealthResponse struct {
	Status       string                  `json:"status"`
	Timestamp    string                  `json:"timestamp"`
	UptimeMillis int64                   `json:"uptimeMillis"`
	Details      *DashboardHealthDetails `json:"details,omitempty"`
}

// DashboardHealthDetails has explicit camelCase per-field renames in Rust.
type DashboardHealthDetails struct {
	TotalQueues         uint32  `json:"totalQueues"`
	HealthyQueues       uint32  `json:"healthyQueues"`
	TotalPools          uint32  `json:"totalPools"`
	HealthyPools        uint32  `json:"healthyPools"`
	ActiveWarnings      uint32  `json:"activeWarnings"`
	CriticalWarnings    uint32  `json:"criticalWarnings"`
	CircuitBreakersOpen uint32  `json:"circuitBreakersOpen"`
	DegradationReason   *string `json:"degradationReason,omitempty"`
}

// ── Dashboard pool / queue / circuit-breaker / in-flight stats ───────────

// DashboardPoolStats mirrors Rust DashboardPoolStats.
type DashboardPoolStats struct {
	PoolCode                string  `json:"poolCode"`
	TotalProcessed          uint64  `json:"totalProcessed"`
	TotalSucceeded          uint64  `json:"totalSucceeded"`
	TotalFailed             uint64  `json:"totalFailed"`
	TotalRateLimited        uint64  `json:"totalRateLimited"`
	SuccessRate             float64 `json:"successRate"`
	ActiveWorkers           uint32  `json:"activeWorkers"`
	AvailablePermits        uint32  `json:"availablePermits"`
	MaxConcurrency          uint32  `json:"maxConcurrency"`
	QueueSize               uint32  `json:"queueSize"`
	MaxQueueCapacity        uint32  `json:"maxQueueCapacity"`
	AverageProcessingTimeMs float64 `json:"averageProcessingTimeMs"`
}

// DashboardQueueStats mirrors Rust DashboardQueueStats.
type DashboardQueueStats struct {
	Name               string  `json:"name"`
	TotalMessages      uint64  `json:"totalMessages"`
	TotalConsumed      uint64  `json:"totalConsumed"`
	TotalFailed        uint64  `json:"totalFailed"`
	TotalDeferred      uint64  `json:"totalDeferred"`
	SuccessRate        float64 `json:"successRate"`
	CurrentSize        uint64  `json:"currentSize"`
	Throughput         float64 `json:"throughput"`
	PendingMessages    uint64  `json:"pendingMessages"`
	MessagesNotVisible uint64  `json:"messagesNotVisible"`
}

// DashboardCircuitBreaker mirrors Rust DashboardCircuitBreakerStats.
type DashboardCircuitBreaker struct {
	Name            string  `json:"name"`
	State           string  `json:"state"`
	SuccessfulCalls uint64  `json:"successfulCalls"`
	FailedCalls     uint64  `json:"failedCalls"`
	RejectedCalls   uint64  `json:"rejectedCalls"`
	FailureRate     float64 `json:"failureRate"`
	BufferedCalls   uint32  `json:"bufferedCalls"`
	BufferSize      uint32  `json:"bufferSize"`
}

// CircuitBreakerStateResponse mirrors Rust CircuitBreakerStateResponse.
type CircuitBreakerStateResponse struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	Successes      uint64 `json:"successes"`
	Failures       uint64 `json:"failures"`
	RecentFailures uint32 `json:"recentFailures"`
}

// InFlightMessageInfo mirrors Rust InFlightMessageInfo. Every field has
// an explicit camelCase rename in Rust; the JSON tags here match.
type InFlightMessageInfo struct {
	MessageID           string    `json:"messageId"`
	BrokerMessageID     *string   `json:"brokerMessageId"`
	QueueID             string    `json:"queueId"`
	PoolCode            string    `json:"poolCode"`
	ElapsedTimeMs       uint64    `json:"elapsedTimeMs"`
	AddedToInPipelineAt time.Time `json:"addedToInPipelineAt"`
}

// InFlightCheckResponse is the response for the single-message check
// endpoint. inPipeline=false → safe to resend.
type InFlightCheckResponse struct {
	MessageID  string `json:"messageId"`
	InPipeline bool   `json:"inPipeline"`
	PoolCode   string `json:"poolCode,omitempty"`
	QueueID    string `json:"queueId,omitempty"`
}

// InFlightCheckBatchRequest is the body for the batch in-flight check.
type InFlightCheckBatchRequest struct {
	MessageIDs []string `json:"messageIds"`
}

// QueueMetricsView mirrors Rust QueueMetricsResponse.
type QueueMetricsView struct {
	QueueIdentifier  string `json:"queue_identifier"`
	PendingMessages  uint64 `json:"pending_messages"`
	InFlightMessages uint64 `json:"in_flight_messages"`
}

// ── Consumer health (/monitoring/consumer-health) ────────────────────────

// ConsumerHealthResponse is the top-level shape consumed by the
// dashboard. Always camelCase.
type ConsumerHealthResponse struct {
	CurrentTimeMs int64                           `json:"currentTimeMs"`
	CurrentTime   string                          `json:"currentTime"`
	Consumers     map[string]ConsumerHealthDetail `json:"consumers"`
}

// ConsumerHealthDetail mirrors the Java per-consumer entry.
type ConsumerHealthDetail struct {
	MapKey                   string `json:"mapKey"`
	QueueIdentifier          string `json:"queueIdentifier"`
	ConsumerQueueIdentifier  string `json:"consumerQueueIdentifier"`
	IsHealthy                bool   `json:"isHealthy"`
	LastPollTimeMs           int64  `json:"lastPollTimeMs"`
	LastPollTime             string `json:"lastPollTime"`
	TimeSinceLastPollMs      int64  `json:"timeSinceLastPollMs"`
	TimeSinceLastPollSeconds int64  `json:"timeSinceLastPollSeconds"`
	IsRunning                bool   `json:"isRunning"`
}

// ── Warnings (/warnings, /monitoring/warnings, /warnings/{id}/...) ───────

// WireWarning mirrors Rust Warning — snake_case JSON tags.
type WireWarning struct {
	ID             string     `json:"id"`
	Category       string     `json:"category"`
	Severity       string     `json:"severity"`
	Message        string     `json:"message"`
	Source         string     `json:"source"`
	CreatedAt      time.Time  `json:"created_at"`
	Acknowledged   bool       `json:"acknowledged"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

func fromWarning(w router.Warning) WireWarning {
	return WireWarning{
		ID:             w.ID,
		Category:       string(w.Category),
		Severity:       string(w.Severity),
		Message:        w.Message,
		Source:         w.Source,
		CreatedAt:      w.CreatedAt,
		Acknowledged:   w.Acknowledged,
		AcknowledgedAt: w.AcknowledgedAt,
	}
}

func fromWarnings(ws []router.Warning) []WireWarning {
	out := make([]WireWarning, len(ws))
	for i, w := range ws {
		out[i] = fromWarning(w)
	}
	return out
}

// CountResponse is the cleared / deleted count wrapper.
type CountResponse struct {
	Cleared uint64 `json:"cleared"`
}

// AcknowledgedResponse is the body for single-warning acknowledgement.
type AcknowledgedResponse struct {
	Acknowledged bool `json:"acknowledged"`
}

// AcknowledgedCountResponse is the body for bulk acknowledgement.
type AcknowledgedCountResponse struct {
	Acknowledged uint64 `json:"acknowledged"`
}

// ── Mutations: PUT pool, broker refresh, breaker reset ───────────────────

// PoolConfigUpdateRequest is the body for PUT /monitoring/pools/{poolCode}.
// Both fields optional; omitting a field leaves the knob unchanged.
type PoolConfigUpdateRequest struct {
	Concurrency        *uint32 `json:"concurrency,omitempty"`
	RateLimitPerMinute *uint32 `json:"rate_limit_per_minute,omitempty"`
}

// PoolConfigUpdateResponse describes the applied update.
type PoolConfigUpdateResponse struct {
	Success   bool                      `json:"success"`
	PoolCode  string                    `json:"pool_code"`
	NewConfig PoolConfigUpdateNewConfig `json:"new_config"`
}

// PoolConfigUpdateNewConfig echoes the values that were applied. Nil
// fields mean "left unchanged".
type PoolConfigUpdateNewConfig struct {
	Concurrency        *uint32 `json:"concurrency,omitempty"`
	RateLimitPerMinute *uint32 `json:"rate_limit_per_minute,omitempty"`
}

// BrokerStatsRefreshResponse is the body for POST /monitoring/broker-stats/refresh.
type BrokerStatsRefreshResponse struct {
	Refreshed  bool  `json:"refreshed"`
	AgeSeconds int64 `json:"ageSeconds"`
}

// BreakerResetResponse confirms a single breaker reset.
type BreakerResetResponse struct {
	Reset bool   `json:"reset"`
	Name  string `json:"name"`
}

// BreakerResetAllResponse counts breakers reset via reset-all.
type BreakerResetAllResponse struct {
	Reset uint64 `json:"reset"`
}

// ── Publish + seed ───────────────────────────────────────────────────────

// PublishMessageRequest is the body for POST /messages.
type PublishMessageRequest struct {
	ID              string `json:"id,omitempty" doc:"Message ID; auto-generated when empty"`
	PoolCode        string `json:"pool_code" doc:"Target pool (must match a registered pool)"`
	MediationType   string `json:"mediation_type,omitempty" doc:"Mediation type; defaults to HTTP"`
	MediationTarget string `json:"mediation_target" doc:"Target URL"`
	MessageGroupID  string `json:"message_group_id,omitempty" doc:"Optional FIFO group ID"`
	HighPriority    bool   `json:"high_priority,omitempty" doc:"Queue-level priority hint; does NOT reorder within a message group (groups are strict FIFO)"`
	DispatchMode    string `json:"dispatch_mode,omitempty" doc:"IMMEDIATE | NEXT_ON_ERROR | BLOCK_ON_ERROR"`
	AuthToken       string `json:"auth_token,omitempty"`
	SigningSecret   string `json:"signing_secret,omitempty"`
}

// PublishMessageResponse echoes the resulting broker IDs.
type PublishMessageResponse struct {
	MessageID       string `json:"message_id"`
	BrokerMessageID string `json:"broker_message_id"`
	PoolCode        string `json:"pool_code"`
	QueueIdentifier string `json:"queue_identifier"`
}

// SeedMessagesRequest is the body for /api/seed/messages.
type SeedMessagesRequest struct {
	PoolCode        string `json:"pool_code"`
	MediationTarget string `json:"mediation_target,omitempty"`
	Count           int    `json:"count" doc:"Number of messages to enqueue (1-10000)"`
}

// SeedMessagesResponse counts the seeded messages.
type SeedMessagesResponse struct {
	PoolCode        string `json:"pool_code"`
	QueueIdentifier string `json:"queue_identifier"`
	Published       int    `json:"published"`
}

// ── Mock counters ────────────────────────────────────────────────────────

// MockOKResponse is the success body for /api/test/* endpoints.
type MockOKResponse struct {
	OK       bool   `json:"ok"`
	Endpoint string `json:"endpoint"`
}

// MockStatsResponse is the body for GET /api/test/stats.
type MockStatsResponse struct {
	Fast          uint64 `json:"fast"`
	Slow          uint64 `json:"slow"`
	Faulty        uint64 `json:"faulty"`
	FaultySuccess uint64 `json:"faulty_success"`
	FaultyFail    uint64 `json:"faulty_fail"`
	Fail          uint64 `json:"fail"`
	Success       uint64 `json:"success"`
	Pending       uint64 `json:"pending"`
	ClientError   uint64 `json:"client_error"`
	ServerError   uint64 `json:"server_error"`
}

// ResetResponse is the reset/clear acknowledgement.
type ResetResponse struct {
	Reset bool `json:"reset"`
}

// ── Config / standby / traffic ───────────────────────────────────────────

// LocalConfigResponse mirrors Rust's get_local_config: never expose secrets.
type LocalConfigResponse struct {
	Version          string `json:"version"`
	WarningsTotal    uint64 `json:"warnings_total"`
	WarningsCritical uint64 `json:"warnings_critical"`
}

// ConfigReloadResponse is the body for POST /config/reload.
type ConfigReloadResponse struct {
	Success bool   `json:"success"`
	Note    string `json:"note,omitempty"`
}

// StandbyStatusResponse mirrors Rust StandbyStatusResponse.
type StandbyStatusResponse struct {
	Enabled    bool   `json:"enabled"`
	IsLeader   bool   `json:"is_leader"`
	InstanceID string `json:"instance_id"`
}

// TrafficStatusResponse mirrors Rust TrafficStatusResponse.
type TrafficStatusResponse struct {
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode"`
	TargetGroup   string `json:"targetGroupArn,omitempty"`
	Registered    bool   `json:"registered"`
	LastChangedAt string `json:"lastChangedAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

// StreamHealthResponse is the body for /monitoring/stream-health. When
// no StreamHealthProvider is wired the response is `enabled: false`
// and `status: NOT_CONFIGURED`; otherwise the live aggregate from the
// stream subsystem is reported.
type StreamHealthResponse struct {
	Enabled          bool                     `json:"enabled"`
	Status           string                   `json:"status"`
	Detail           string                   `json:"detail,omitempty"`
	TotalStreams     int                      `json:"totalStreams,omitempty"`
	HealthyStreams   int                      `json:"healthyStreams,omitempty"`
	UnhealthyStreams int                      `json:"unhealthyStreams,omitempty"`
	Streams          []StreamProjectionHealth `json:"streams,omitempty"`
}

// StreamProjectionHealth is one row in StreamHealthResponse.Streams.
type StreamProjectionHealth struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	Running        bool   `json:"running"`
	Healthy        bool   `json:"healthy"`
	BatchSequence  uint64 `json:"batchSequence"`
	ErrorCount     uint64 `json:"errorCount"`
	LastPollTimeMs int64  `json:"lastPollTimeMs"`
}

// StreamProbeResponse is the body for /monitoring/stream-health/{live,ready}.
type StreamProbeResponse struct {
	Status string `json:"status"`
}
