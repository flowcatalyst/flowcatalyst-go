package server

import (
	"net/url"
	"os"
	"strconv"
	"strings"
)

// EnvCfg captures every env-driven knob fc-server reads. Mirrors the
// Rust fc-server's FC_*_ENABLED + DB resolution surface, plus the
// aliased TS variable names for compatibility with existing ECS task
// definitions.
type EnvCfg struct {
	APIPort     int
	MetricsPort int

	DatabaseURL string
	JWTIssuer   string

	// Subsystem toggles.
	PlatformEnabled     bool
	RouterEnabled       bool
	SchedulerEnabled    bool // dispatch-job scheduler (internal/platform/scheduler)
	ScheduledJobEnabled bool // scheduled-job cron + dispatch engine
	StreamEnabled       bool
	OutboxEnabled       bool
	MCPEnabled          bool

	// Router HTTP mount prefix on the unified API listener. Default
	// /router. fc-router (when it exists) ignores this and mounts at root.
	RouterHTTPPrefix string

	// DefaultBroker picks the fallback queue backend when no
	// FLOWCATALYST_CONFIG_URL is configured. "postgres" synthesises a
	// single 'default' pool against the shared Postgres pool. Empty
	// means "no pools start" — the historical behaviour. fc-dev sets
	// this to "postgres"; fc-server leaves it empty in prod.
	DefaultBroker string

	// MCPPort is the listener for the MCP subsystem. Default 8090.
	MCPPort int

	// MCPBind is the bind host for the MCP listener. Default 127.0.0.1
	// (localhost-only): the MCP server is an agent-facing surface, not a
	// public API. Set FC_MCP_BIND=0.0.0.0 to expose it (e.g. in a container).
	MCPBind string

	// Stream processor — per-projection sub-toggles (default true when
	// StreamEnabled is on, so the top-level flag is enough on its own).
	StreamEventsEnabled       bool
	StreamDispatchJobsEnabled bool
	StreamFanOutEnabled       bool
	StreamPartitionsEnabled   bool
	StreamBatchSize           int
	// Partition manager tuning (months forward, retention, tick cadence).
	// 0 = use the package default (3 / 90 / 24h).
	StreamPartitionMonthsForward int
	StreamPartitionRetentionDays int
	StreamPartitionTickHours     int

	// Outbox processor — only Postgres is supported in the unified
	// binary; the standalone cmd/fc-outbox-processor remains the home
	// for the (future) sqlite/mysql/mongo backends.
	OutboxPlatformURL       string
	OutboxPlatformAuthToken string
	OutboxBatchSize         int
	OutboxMaxInFlight       int
	OutboxPollIntervalMS    int
	// OB7 max concurrent message groups (0 = use default 10). OB4 block-on-error
	// (default true): stop a group on a failing item, releasing the rest to
	// re-run in order behind it.
	OutboxMaxConcurrentGroups int
	OutboxBlockOnError        bool
	// Backend selection: "postgres" (default, shared pool) or "mongo".
	OutboxBackend  string
	OutboxMongoURI string
	OutboxMongoDB  string

	// Router — used when FC_ROUTER_ENABLED=true. Mirrors the env vars
	// the standalone cmd/fc-router binary reads.
	RouterConfigURL        string
	RouterDevMode          bool
	RouterNotifyWebhookURL string
	RouterDrainTimeoutSec  int

	// ALB self-registration (router). When ALBEnabled, the router registers
	// this instance's IP with the target group on leader-gain (or non-standby
	// start) and deregisters on leader-loss / shutdown. Mirrors Rust FC_ALB_*.
	ALBEnabled        bool
	ALBTargetGroupARN string
	ALBInstanceIP     string // FC_ALB_TARGET_ID / FC_ALB_INSTANCE_IP — the target id (IP) for RegisterTargets
	ALBPort           int
	ALBRegion         string
	ALBDeregDelaySec  int

	// Standby / HA.
	StandbyEnabled  bool
	StandbyRedisURL string
	StandbyLockKey  string

	// JWT signing.
	JWTSigningKeyPath string

	// MCP — the read-only MCP server proxies into the platform. URL is
	// where it dials the platform itself; for fc-dev it's the local
	// listener (http://localhost:<APIPort>).
	MCPPlatformURL  string
	MCPClientID     string
	MCPClientSecret string

	// AuthAllowTestHeaders enables the X-FC-Test-Principal dev fallback
	// in the platform Authenticator middleware. Defaults to false in
	// production. fc-dev flips it on for the local embedded-PG flow.
	AuthAllowTestHeaders bool
}

func LoadEnv() EnvCfg {
	c := EnvCfg{
		APIPort:     envIntAlias("FC_API_PORT", "PORT", 8080),
		MetricsPort: envInt("FC_METRICS_PORT", 9090),

		DatabaseURL: ResolveDatabaseURL(),
		JWTIssuer:   envFirst("FC_JWT_ISSUER", "FC_EXTERNAL_BASE_URL", "EXTERNAL_BASE_URL", "http://localhost:8080"),

		PlatformEnabled:     envBoolAlias("FC_PLATFORM_ENABLED", "PLATFORM_ENABLED", true),
		RouterEnabled:       envBoolAlias("FC_ROUTER_ENABLED", "MESSAGE_ROUTER_ENABLED", false),
		SchedulerEnabled:    envBoolAlias("FC_SCHEDULER_ENABLED", "DISPATCH_SCHEDULER_ENABLED", false),
		ScheduledJobEnabled: envBoolAlias("FC_SCHEDULED_JOB_ENABLED", "SCHEDULED_JOB_SCHEDULER_ENABLED", false),
		StreamEnabled:       envBoolAlias("FC_STREAM_PROCESSOR_ENABLED", "STREAM_PROCESSOR_ENABLED", false),
		OutboxEnabled:       envBoolAlias("FC_OUTBOX_ENABLED", "OUTBOX_PROCESSOR_ENABLED", false),
		MCPEnabled:          envBool("FC_MCP_ENABLED", false),

		RouterHTTPPrefix: envOr("FC_ROUTER_HTTP_PREFIX", "/router"),
		DefaultBroker:    envOr("FC_DEFAULT_BROKER", ""),
		MCPPort:          envInt("FC_MCP_PORT", 8090),
		MCPBind:          envOr("FC_MCP_BIND", "127.0.0.1"),

		// Stream sub-toggles default ON so FC_STREAM_PROCESSOR_ENABLED=true
		// is sufficient to bring up the whole stream pipeline.
		StreamEventsEnabled:       envBool("FC_STREAM_EVENTS_ENABLED", true),
		StreamDispatchJobsEnabled: envBool("FC_STREAM_DISPATCH_JOBS_ENABLED", true),
		StreamFanOutEnabled:       envBool("FC_STREAM_FAN_OUT_ENABLED", true),
		// Toggle renamed to FC_STREAM_PARTITION_MANAGER_ENABLED; the old
		// FC_STREAM_PARTITIONS_ENABLED stays as a back-compat alias.
		StreamPartitionsEnabled:      envBoolAlias("FC_STREAM_PARTITION_MANAGER_ENABLED", "FC_STREAM_PARTITIONS_ENABLED", true),
		StreamBatchSize:              envInt("FC_STREAM_BATCH_SIZE", 0),
		StreamPartitionMonthsForward: envInt("FC_STREAM_PARTITION_MONTHS_FORWARD", 0),
		StreamPartitionRetentionDays: envInt("FC_STREAM_PARTITION_RETENTION_DAYS", 0),
		StreamPartitionTickHours:     envInt("FC_STREAM_PARTITION_TICK_HOURS", 0),

		// FC_OUTBOX_API_URL / FC_OUTBOX_TOKEN align with the standalone Rust
		// outbox CLI; FC_API_BASE_URL / FC_API_TOKEN align with the Rust
		// fc-outbox-processor binary; FC_OUTBOX_PLATFORM_* + FLOWCATALYST_URL
		// kept as aliases.
		OutboxPlatformURL:         envFirst("FC_OUTBOX_PLATFORM_URL", "FC_OUTBOX_API_URL", "FC_API_BASE_URL", "FLOWCATALYST_URL", "", ""),
		OutboxPlatformAuthToken:   envFirst("FC_OUTBOX_PLATFORM_AUTH_TOKEN", "FC_OUTBOX_TOKEN", "FC_API_TOKEN", "", ""),
		OutboxBatchSize:           envInt("FC_OUTBOX_BATCH_SIZE", 0),
		OutboxMaxInFlight:         envInt("FC_OUTBOX_MAX_IN_FLIGHT", 0),
		OutboxPollIntervalMS:      envInt("FC_OUTBOX_POLL_INTERVAL_MS", 0),
		OutboxMaxConcurrentGroups: envIntAlias("FC_OUTBOX_MAX_CONCURRENT_GROUPS", "FC_MAX_CONCURRENT_GROUPS", 0),
		OutboxBlockOnError:        envBool("FC_OUTBOX_BLOCK_ON_ERROR", true),
		OutboxBackend:             envOr("FC_OUTBOX_BACKEND", "postgres"),
		OutboxMongoURI:            envFirst("FC_OUTBOX_MONGO_URI", "FC_OUTBOX_DB_URL", "", ""),
		OutboxMongoDB:             envOr("FC_OUTBOX_MONGO_DB", "flowcatalyst"),

		RouterConfigURL:        os.Getenv("FLOWCATALYST_CONFIG_URL"),
		RouterDevMode:          envBool("FLOWCATALYST_DEV_MODE", false),
		RouterNotifyWebhookURL: os.Getenv("FC_NOTIFY_WEBHOOK_URL"),
		RouterDrainTimeoutSec:  envInt("FC_DRAIN_TIMEOUT_SECONDS", 60),

		ALBEnabled:        envBool("FC_ALB_ENABLED", false),
		ALBTargetGroupARN: os.Getenv("FC_ALB_TARGET_GROUP_ARN"),
		ALBInstanceIP:     envFirst("FC_ALB_TARGET_ID", "FC_ALB_INSTANCE_IP", "", ""),
		ALBPort:           envInt("FC_ALB_TARGET_PORT", 8080),
		ALBRegion:         os.Getenv("FC_ALB_REGION"), // empty → AWS SDK default region chain
		ALBDeregDelaySec:  envInt("FC_ALB_DEREGISTRATION_DELAY_SECONDS", 0),

		StandbyEnabled:  envBoolAlias("FC_STANDBY_ENABLED", "STANDBY_ENABLED", false),
		StandbyRedisURL: envFirst("FC_STANDBY_REDIS_URL", "REDIS_URL", "", "redis://127.0.0.1:6379"),
		StandbyLockKey:  envOr("FC_STANDBY_LOCK_KEY", "fc:server:leader"),

		JWTSigningKeyPath:    os.Getenv("FC_JWT_SIGNING_KEY_PATH"),
		AuthAllowTestHeaders: envBool("FC_AUTH_ALLOW_TEST_HEADERS", false),

		MCPPlatformURL:  envFirst("FLOWCATALYST_URL", "FC_MCP_PLATFORM_URL", "", ""),
		MCPClientID:     os.Getenv("FLOWCATALYST_CLIENT_ID"),
		MCPClientSecret: os.Getenv("FLOWCATALYST_CLIENT_SECRET"),
	}
	return c
}

// ResolveDatabaseURL mirrors the Rust fc-server's three-mode resolution.
// 1. FC_DATABASE_URL / DATABASE_URL — full connection string (preferred).
// 2. DB_HOST + DB_NAME + DB_USERNAME + DB_PASSWORD — explicit credentials.
// 3. (TODO) AWS Secrets Manager via DB_SECRET_ARN — deferred.
func ResolveDatabaseURL() string {
	if v := envFirst("FC_DATABASE_URL", "DATABASE_URL", "", ""); v != "" {
		return v
	}
	host := os.Getenv("DB_HOST")
	if host == "" {
		// No DB config found. Default to local dev for ergonomics —
		// fc-server logs + dies in main() if the connection fails.
		return "postgresql://postgres@localhost:5432/flowcatalyst"
	}
	name := envOr("DB_NAME", "flowcatalyst")
	port := envOr("DB_PORT", "5432")
	username := envOr("DB_USERNAME", "postgres")
	password := os.Getenv("DB_PASSWORD")
	hostPort := host
	if !strings.Contains(host, ":") {
		hostPort = host + ":" + port
	}
	if password == "" {
		return "postgresql://" + username + "@" + hostPort + "/" + name
	}
	return "postgresql://" + username + ":" + url.QueryEscape(password) + "@" + hostPort + "/" + name
}

// ── helpers ──────────────────────────────────────────────────────────────

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envFirst(keys ...string) string {
	// Last argument is the default; everything else is a key in priority order.
	def := keys[len(keys)-1]
	for _, k := range keys[:len(keys)-1] {
		if k == "" {
			continue
		}
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envIntAlias(key, alias string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	if v := os.Getenv(alias); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func envBoolAlias(key, alias string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return envBool(key, def)
	}
	return envBool(alias, def)
}
