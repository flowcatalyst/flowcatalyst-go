package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/jackc/pgx/v5"
)

// dbSecret is the subset of an RDS-style AWS Secrets Manager secret we read.
// Mirrors the Rust AwsSecretProvider: only username/password/port come from the
// secret JSON — host + database name come from DB_HOST / DB_NAME env vars.
type dbSecret struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Port     *int   `json:"port,omitempty"`
}

// ResolveDBSecretURL builds a Postgres connection string from an AWS Secrets
// Manager secret when DB_SECRET_ARN is configured. Returns (url, true, nil)
// when Secrets-Manager mode applies, ("", false, nil) when it does not (so the
// caller keeps the env-resolved URL), or an error on a genuine fetch/parse
// failure.
//
// SM mode applies only when DB_HOST and DB_SECRET_ARN are both set and no
// explicit FC_DATABASE_URL/DATABASE_URL is present — matching the Rust
// fc-server precedence (full URL > Secrets Manager > explicit DB_* creds).
// DB_SECRET_PROVIDER must be "aws" (the default). Credentials are resolved via
// the standard AWS chain (env, instance profile, ECS task role, …).
//
// Note: this reads the secret once at startup; the Rust DB_SECRET_REFRESH_*
// rotation poller is a tracked follow-up, not yet ported.
func ResolveDBSecretURL(ctx context.Context) (string, bool, error) {
	// An explicit connection string always wins — SM is never consulted.
	if envFirst("FC_DATABASE_URL", "DATABASE_URL", "", "") != "" {
		return "", false, nil
	}
	arn := os.Getenv("DB_SECRET_ARN")
	host := os.Getenv("DB_HOST")
	if arn == "" || host == "" {
		return "", false, nil
	}
	if provider := envOr("DB_SECRET_PROVIDER", "aws"); !strings.EqualFold(provider, "aws") {
		return "", false, fmt.Errorf("DB_SECRET_PROVIDER %q not supported (only \"aws\")", provider)
	}

	sm, err := newSMClient(ctx)
	if err != nil {
		return "", false, err
	}
	sec, err := fetchDBSecret(ctx, sm, arn)
	if err != nil {
		return "", false, err
	}
	return buildDBSecretDSN(host, envOr("DB_NAME", "flowcatalyst"), os.Getenv("DB_PORT"), sec), true, nil
}

// newSMClient builds an AWS Secrets Manager client from the default credential
// chain (env, instance profile, ECS task role, …).
func newSMClient(ctx context.Context) (*secretsmanager.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return secretsmanager.NewFromConfig(awsCfg), nil
}

// fetchDBSecret loads + parses the RDS-style secret (username/password/port).
func fetchDBSecret(ctx context.Context, sm *secretsmanager.Client, arn string) (dbSecret, error) {
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &arn})
	if err != nil {
		return dbSecret{}, fmt.Errorf("get secret %s: %w", arn, err)
	}
	if out.SecretString == nil {
		return dbSecret{}, fmt.Errorf("secret %s has no string value", arn)
	}
	var sec dbSecret
	if err := json.Unmarshal([]byte(*out.SecretString), &sec); err != nil {
		return dbSecret{}, fmt.Errorf("parse secret %s JSON: %w", arn, err)
	}
	if sec.Username == "" || sec.Password == "" {
		return dbSecret{}, fmt.Errorf("secret %s is missing username/password", arn)
	}
	return sec, nil
}

// buildDBSecretDSN assembles the Postgres DSN from the secret + env-supplied
// host/name/port. Pure (no env/network reads) so the parity-critical bits —
// port precedence (secret JSON > DB_PORT > 5432), password URL-escaping, and
// host-already-has-port — are unit-testable. 1:1 with Rust's connection-string
// builder.
func buildDBSecretDSN(host, name, envPort string, sec dbSecret) string {
	port := envPort
	if port == "" {
		port = "5432"
	}
	if sec.Port != nil && *sec.Port > 0 {
		port = strconv.Itoa(*sec.Port)
	}
	hostPort := host
	if !strings.Contains(host, ":") {
		hostPort = host + ":" + port
	}
	return "postgresql://" + sec.Username + ":" + url.QueryEscape(sec.Password) + "@" + hostPort + "/" + name
}

// defaultSecretRefreshIntervalMS is the rotation poll cadence when SM mode is
// active and DB_SECRET_REFRESH_INTERVAL_MS is unset. 5 min, matching Rust.
const defaultSecretRefreshIntervalMS = 300000

// DBSecretRefresher polls AWS Secrets Manager and injects the current DB
// credentials into every new pool connection via BeforeConnect, so a rotated
// RDS password is picked up without a restart (existing connections roll over
// as the pool recycles them by MaxConnLifetime). Mirrors Rust start_secret_refresh.
type DBSecretRefresher struct {
	sm       *secretsmanager.Client
	arn      string
	interval time.Duration

	mu       sync.RWMutex
	user     string
	password string
}

// NewDBSecretRefresher builds a refresher with an initial fetch, but ONLY when
// Secrets-Manager DB mode applies (DB_SECRET_ARN + DB_HOST, no explicit URL)
// AND rotation is enabled (DB_SECRET_REFRESH_INTERVAL_MS != 0). Returns
// (nil, nil) when not applicable — the caller then builds a plain pool.
func NewDBSecretRefresher(ctx context.Context) (*DBSecretRefresher, error) {
	if envFirst("FC_DATABASE_URL", "DATABASE_URL", "", "") != "" {
		return nil, nil
	}
	arn := os.Getenv("DB_SECRET_ARN")
	if arn == "" || os.Getenv("DB_HOST") == "" {
		return nil, nil
	}
	intervalMS := envInt("DB_SECRET_REFRESH_INTERVAL_MS", defaultSecretRefreshIntervalMS)
	if intervalMS <= 0 {
		return nil, nil // rotation disabled
	}
	sm, err := newSMClient(ctx)
	if err != nil {
		return nil, err
	}
	sec, err := fetchDBSecret(ctx, sm, arn)
	if err != nil {
		return nil, err
	}
	return &DBSecretRefresher{
		sm:       sm,
		arn:      arn,
		interval: time.Duration(intervalMS) * time.Millisecond,
		user:     sec.Username,
		password: sec.Password,
	}, nil
}

// BeforeConnect injects the current credentials into a new connection. Wire it
// into database.NewPoolWithBeforeConnect.
func (r *DBSecretRefresher) BeforeConnect(_ context.Context, cc *pgx.ConnConfig) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cc.User = r.user
	cc.Password = r.password
	return nil
}

// Run polls Secrets Manager on the configured interval and swaps the cached
// credentials when they change. Blocks until ctx is cancelled. A fetch error is
// logged and retried on the next tick (the cached creds keep working meanwhile).
func (r *DBSecretRefresher) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sec, err := fetchDBSecret(ctx, r.sm, r.arn)
			if err != nil {
				slog.Warn("DB secret refresh failed; keeping current credentials", "err", err)
				continue
			}
			r.mu.Lock()
			changed := sec.Username != r.user || sec.Password != r.password
			r.user, r.password = sec.Username, sec.Password
			r.mu.Unlock()
			if changed {
				slog.Info("rotated DB credentials from Secrets Manager (new connections will use them)")
			}
		}
	}
}
