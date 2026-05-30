// Package config loads the FlowCatalyst configuration from TOML files
// with environment variable overrides. Mirrors the Rust fc-config crate.
//
// Precedence: env > file > default.
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// AppConfig is the root configuration. Each subsystem owns a sub-struct.
type AppConfig struct {
	HTTP    HTTPConfig    `toml:"http"`
	DB      DBConfig      `toml:"db"`
	Redis   RedisConfig   `toml:"redis"`
	Router  RouterFile    `toml:"router"`
	Stream  StreamConfig  `toml:"stream"`
	Outbox  OutboxConfig  `toml:"outbox"`
	Secrets SecretsConfig `toml:"secrets"`
	Auth    AuthConfig    `toml:"auth"`
}

// HTTPConfig is the HTTP listener config for a binary.
type HTTPConfig struct {
	APIPort     uint16 `toml:"api_port"`
	MetricsPort uint16 `toml:"metrics_port"`
	BindAddr    string `toml:"bind_addr"`
}

// DBConfig is the Postgres connection config.
type DBConfig struct {
	URL                string `toml:"url"`
	MaxConnections     int    `toml:"max_connections"`
	MinConnections     int    `toml:"min_connections"`
	MaxLifetimeSeconds int    `toml:"max_lifetime_seconds"`
}

// RedisConfig is the Redis connection config.
type RedisConfig struct {
	URL string `toml:"url"`
}

// RouterFile is the static router config (dynamic config arrives via
// HTTP from FLOWCATALYST_CONFIG_URL).
type RouterFile struct {
	ConfigURL      string `toml:"config_url"`
	StandbyEnabled bool   `toml:"standby_enabled"`
}

// StreamConfig governs the projection / fan-out / partition processor.
type StreamConfig struct {
	EventsEnabled       bool `toml:"events_enabled"`
	DispatchJobsEnabled bool `toml:"dispatch_jobs_enabled"`
	FanOutEnabled       bool `toml:"fan_out_enabled"`
	PartitionsEnabled   bool `toml:"partitions_enabled"`
	BatchSize           int  `toml:"batch_size"`
}

// OutboxConfig governs the outbox processor.
type OutboxConfig struct {
	DBType            string `toml:"db_type"` // postgres, sqlite, mysql, mongo
	TableName         string `toml:"table_name"`
	PollIntervalMS    int    `toml:"poll_interval_ms"`
	BatchSize         int    `toml:"batch_size"`
	MaxInFlight       int    `toml:"max_in_flight"`
	PlatformURL       string `toml:"platform_url"`
	PlatformAuthToken string `toml:"platform_auth_token"`
}

// SecretsConfig configures the secret provider registry.
type SecretsConfig struct {
	DefaultProvider string `toml:"default_provider"`
	EncryptedFile   string `toml:"encrypted_file"`
	EncryptionKey   string `toml:"encryption_key"`
	VaultAddr       string `toml:"vault_addr"`
	VaultToken      string `toml:"vault_token"`
}

// AuthConfig holds JWT keys and OIDC bridge settings.
type AuthConfig struct {
	JWTPrivateKey string `toml:"jwt_private_key"`
	JWTPublicKey  string `toml:"jwt_public_key"`
	JWTIssuer     string `toml:"jwt_issuer"`
	JWKSURL       string `toml:"jwks_url"`
}

// Load reads a TOML file and applies env overrides.
// path may be empty — in that case only defaults + env apply.
func Load(path string) (*AppConfig, error) {
	cfg := defaults()
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, cfg); err != nil {
				return nil, fmt.Errorf("decode %s: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
	}
	applyEnv(cfg)
	return cfg, nil
}

func defaults() *AppConfig {
	return &AppConfig{
		HTTP: HTTPConfig{
			// 8080 is the single canonical default (matches internal/server
			// envcfg + cmd/fc-dev + Docker EXPOSE). We do not chase Rust's
			// 3000; FC_API_PORT/PORT overrides it.
			APIPort:     8080,
			MetricsPort: 9090,
			BindAddr:    "0.0.0.0",
		},
		DB: DBConfig{
			MaxConnections:     20,
			MinConnections:     2,
			MaxLifetimeSeconds: 3600,
		},
		Stream: StreamConfig{
			EventsEnabled:       true,
			DispatchJobsEnabled: true,
			FanOutEnabled:       true,
			PartitionsEnabled:   true,
			BatchSize:           500,
		},
		Outbox: OutboxConfig{
			DBType:         "postgres",
			TableName:      "outbox_messages",
			PollIntervalMS: 1000,
			BatchSize:      100,
			MaxInFlight:    1000,
		},
		Secrets: SecretsConfig{
			DefaultProvider: "env",
		},
	}
}

func applyEnv(c *AppConfig) {
	if v := os.Getenv("FC_API_PORT"); v != "" {
		var p uint16
		_, _ = fmt.Sscanf(v, "%d", &p)
		c.HTTP.APIPort = p
	}
	if v := os.Getenv("FC_METRICS_PORT"); v != "" {
		var p uint16
		_, _ = fmt.Sscanf(v, "%d", &p)
		c.HTTP.MetricsPort = p
	}
	if v := os.Getenv("FC_DATABASE_URL"); v != "" {
		c.DB.URL = v
	}
	if v := os.Getenv("FC_REDIS_URL"); v != "" {
		c.Redis.URL = v
	}
	if v := os.Getenv("FLOWCATALYST_CONFIG_URL"); v != "" {
		c.Router.ConfigURL = v
	}
	if v := os.Getenv("FC_STANDBY_ENABLED"); v == "true" {
		c.Router.StandbyEnabled = true
	}
	if v := os.Getenv("FC_JWT_ISSUER"); v != "" {
		c.Auth.JWTIssuer = v
	}
	if v := os.Getenv("FC_OUTBOX_DB_TYPE"); v != "" {
		c.Outbox.DBType = v
	}
	if v := os.Getenv("FC_OUTBOX_PLATFORM_URL"); v != "" {
		c.Outbox.PlatformURL = v
	}
}
