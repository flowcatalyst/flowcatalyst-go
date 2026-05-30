package common

import (
	"encoding/json"

	"github.com/google/uuid"
)

// PoolConfig is the per-pool routing configuration.
type PoolConfig struct {
	Code               string  `json:"code"`
	Concurrency        uint32  `json:"concurrency"`
	RateLimitPerMinute *uint32 `json:"rateLimitPerMinute,omitempty"`
}

// QueueConfig is the per-queue connection configuration.
//
// The wire contract is the camelCase shape emitted by the central config
// service (and the Rust/Java router): {queueName, queueUri, connections,
// visibilityTimeout}. This MUST stay interoperable — a Go router dropped
// into an existing deployment polls the same config service, and a Go
// platform serving config must be readable by an existing Rust router.
// UnmarshalJSON below additionally accepts the legacy {name, uri} keys so
// configs produced by older Go builds still load.
type QueueConfig struct {
	Name              string `json:"queueName"`
	URI               string `json:"queueUri"`
	Connections       uint32 `json:"connections"`
	VisibilityTimeout uint32 `json:"visibilityTimeout"`
}

// UnmarshalJSON accepts both the canonical camelCase keys (queueName,
// queueUri) and the legacy aliases (name, uri), and applies the same
// defaults as the Rust QueueConfigResponse -> QueueConfig conversion:
// name falls back to uri, connections defaults to 1, visibilityTimeout
// defaults to 120.
func (q *QueueConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		QueueName         *string `json:"queueName"`
		QueueURI          *string `json:"queueUri"`
		Name              *string `json:"name"`
		URI               *string `json:"uri"`
		Connections       *uint32 `json:"connections"`
		VisibilityTimeout *uint32 `json:"visibilityTimeout"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch {
	case raw.QueueURI != nil:
		q.URI = *raw.QueueURI
	case raw.URI != nil:
		q.URI = *raw.URI
	}

	switch {
	case raw.QueueName != nil:
		q.Name = *raw.QueueName
	case raw.Name != nil:
		q.Name = *raw.Name
	default:
		q.Name = q.URI
	}

	if raw.Connections != nil {
		q.Connections = *raw.Connections
	} else {
		q.Connections = 1
	}

	if raw.VisibilityTimeout != nil {
		q.VisibilityTimeout = *raw.VisibilityTimeout
	} else {
		q.VisibilityTimeout = 120
	}
	return nil
}

// RouterConfig is what the router fetches from its config source.
type RouterConfig struct {
	ProcessingPools []PoolConfig  `json:"processingPools"`
	Queues          []QueueConfig `json:"queues"`
}

// LeaderElectionConfig is the unified leader-election configuration
// shared by fc-outbox and fc-standby in Rust.
type LeaderElectionConfig struct {
	Enabled                  bool
	RedisURL                 string
	LockKey                  string
	LockTTLSeconds           uint64
	HeartbeatIntervalSeconds uint64
	InstanceID               string
}

// NewLeaderElectionConfig creates a config with sane defaults.
func NewLeaderElectionConfig(redisURL string) LeaderElectionConfig {
	return LeaderElectionConfig{
		Enabled:                  true,
		RedisURL:                 redisURL,
		LockKey:                  "fc:leader",
		LockTTLSeconds:           30,
		HeartbeatIntervalSeconds: 10,
		InstanceID:               uuid.NewString(),
	}
}

// StallConfig controls stall detection in the router.
type StallConfig struct {
	Enabled               bool   `json:"enabled"`
	StallThresholdSeconds uint64 `json:"stallThresholdSeconds"`
	ForceNackStalled      bool   `json:"forceNackStalled"`
	ForceNackAfterSeconds uint64 `json:"forceNackAfterSeconds"`
	NackDelaySeconds      uint32 `json:"nackDelaySeconds"`
}

// DefaultStallConfig matches the Rust defaults.
func DefaultStallConfig() StallConfig {
	return StallConfig{
		Enabled:               true,
		StallThresholdSeconds: 300,
		ForceNackStalled:      false,
		ForceNackAfterSeconds: 600,
		NackDelaySeconds:      30,
	}
}
