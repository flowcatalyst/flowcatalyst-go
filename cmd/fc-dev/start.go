package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"

	"github.com/flowcatalyst/flowcatalyst-go/frontend"
	"github.com/flowcatalyst/flowcatalyst-go/internal/config"
	"github.com/flowcatalyst/flowcatalyst-go/internal/migrate"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/seed"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/database"
	"github.com/flowcatalyst/flowcatalyst-go/internal/server"
)

// startOpts captures the flag set for `fc-dev start`. Defaults match
// the Rust fc-dev so existing dev workflows transfer 1:1.
type startOpts struct {
	APIPort          int
	MetricsPort      int
	EmbeddedDB       bool
	EmbeddedDBPort   int
	EmbeddedDBPath   string
	EmbeddedDBReset  bool
	DatabaseURL      string
	SchedulerEnabled bool
	StreamEnabled    bool
	OutboxEnabled    bool
	RouterEnabled    bool
	MCPEnabled       bool
}

func newStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Run the dev monolith (identical to invoking fc-dev with no subcommand)",
		RunE:  runStart,
	}
	addStartFlags(cmd)
	return cmd
}

func addStartFlags(cmd *cobra.Command) {
	cmd.Flags().Int("api-port", envIntDefault("FC_API_PORT", 8080), "API server port")
	cmd.Flags().Int("metrics-port", envIntDefault("FC_METRICS_PORT", 9090), "metrics server port")
	cmd.Flags().Bool("embedded-db", envBoolDefault("FC_EMBEDDED_DB", true), "start an embedded Postgres")
	cmd.Flags().Int("embedded-db-port", envIntDefault("FC_EMBEDDED_DB_PORT", 15432), "embedded Postgres port")
	cmd.Flags().String("embedded-db-path", envStrDefault("FC_EMBEDDED_DB_PATH", defaultEmbeddedPath()), "embedded Postgres data directory")
	cmd.Flags().Bool("embedded-db-reset", false, "wipe the embedded Postgres data directory before starting")
	cmd.Flags().String("database-url", envStrDefault("FC_DATABASE_URL", ""), "Postgres URL (overrides --embedded-db)")
	cmd.Flags().Bool("scheduler", envBoolDefault("FC_SCHEDULER_ENABLED", true), "run the dispatch scheduler")
	cmd.Flags().Bool("stream", envBoolDefault("FC_STREAM_PROCESSOR_ENABLED", true), "run the stream processor")
	cmd.Flags().Bool("outbox", envBoolDefault("FC_OUTBOX_ENABLED", false), "run the outbox processor")
	cmd.Flags().Bool("router", envBoolDefault("FC_ROUTER_ENABLED", true), "run the message router (uses the embedded Postgres broker by default)")
	cmd.Flags().Bool("mcp", envBoolDefault("FC_MCP_ENABLED", false), "run the MCP HTTP server")
}

func optsFromFlags(cmd *cobra.Command) startOpts {
	getInt := func(k string) int { v, _ := cmd.Flags().GetInt(k); return v }
	getStr := func(k string) string { v, _ := cmd.Flags().GetString(k); return v }
	getBool := func(k string) bool { v, _ := cmd.Flags().GetBool(k); return v }
	return startOpts{
		APIPort:          getInt("api-port"),
		MetricsPort:      getInt("metrics-port"),
		EmbeddedDB:       getBool("embedded-db"),
		EmbeddedDBPort:   getInt("embedded-db-port"),
		EmbeddedDBPath:   getStr("embedded-db-path"),
		EmbeddedDBReset:  getBool("embedded-db-reset"),
		DatabaseURL:      getStr("database-url"),
		SchedulerEnabled: getBool("scheduler"),
		StreamEnabled:    getBool("stream"),
		OutboxEnabled:    getBool("outbox"),
		RouterEnabled:    getBool("router"),
		MCPEnabled:       getBool("mcp"),
	}
}

func runStart(cmd *cobra.Command, _ []string) error {
	opts := optsFromFlags(cmd)
	banner(opts)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Embedded Postgres ──────────────────────────────────────────────
	databaseURL := opts.DatabaseURL
	var pg *embeddedpostgres.EmbeddedPostgres
	if databaseURL == "" {
		if !opts.EmbeddedDB {
			return errors.New("no --database-url given and --embedded-db=false; nothing to connect to")
		}
		if opts.EmbeddedDBReset {
			slog.Warn("wiping embedded Postgres data directory", "path", opts.EmbeddedDBPath)
			_ = os.RemoveAll(opts.EmbeddedDBPath)
		}
		if err := os.MkdirAll(opts.EmbeddedDBPath, 0o755); err != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
		cacheDir := embeddedPGCacheDir()
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return fmt.Errorf("create cache dir: %w", err)
		}
		pg = embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
			Port(uint32(opts.EmbeddedDBPort)).
			DataPath(filepath.Join(opts.EmbeddedDBPath, "data")).
			RuntimePath(filepath.Join(cacheDir, "runtime")).
			BinariesPath(filepath.Join(cacheDir, "bin")).
			Username("postgres").
			Password("postgres").
			Database("flowcatalyst").
			StartTimeout(60 * time.Second))
		if err := pg.Start(); err != nil {
			return fmt.Errorf("embedded postgres start: %w", err)
		}
		defer func() {
			slog.Info("stopping embedded postgres")
			_ = pg.Stop()
		}()
		databaseURL = fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/flowcatalyst?sslmode=disable", opts.EmbeddedDBPort)
		slog.Info("embedded postgres started", "port", opts.EmbeddedDBPort, "path", opts.EmbeddedDBPath)
	}

	// ── Connect + migrate + seed ───────────────────────────────────────
	pool, err := database.NewPool(rootCtx, config.DBConfig{URL: databaseURL})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	if err := migrate.Run(rootCtx, pool); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	// Pre-set bootstrap-admin defaults so the dev workflow yields a
	// usable login on first run. fc-server requires operators to set
	// these explicitly. Existing env values win.
	setEnvDefault(seed.EnvBootstrapEmail, "admin@flowcatalyst.local")
	setEnvDefault(seed.EnvBootstrapPassword, "DevPassword123!")
	setEnvDefault(seed.EnvBootstrapName, "Local Admin")

	// Ensure a persistent JWT signing key exists so tokens survive a
	// restart. fc-dev stores it under ~/.flowcatalyst/jwt-signing-key.pem
	// (0600). fc-server requires operators to supply one via env.
	if os.Getenv("FC_JWT_SIGNING_KEY_PATH") == "" {
		defaultKey := filepath.Join(filepath.Dir(opts.EmbeddedDBPath), "jwt-signing-key.pem")
		if resolved, err := server.EnsureSigningKeyFile(defaultKey); err == nil {
			_ = os.Setenv("FC_JWT_SIGNING_KEY_PATH", resolved)
		} else {
			slog.Warn("unable to persist JWT signing key — falling back to ephemeral", "err", err)
		}
	}

	// Persist the field-encryption key so OAuth client secrets (incl. the
	// bootstrapped MCP client) stay decryptable across restarts. fc-server
	// requires operators to supply FLOWCATALYST_APP_KEY via env.
	if os.Getenv("FLOWCATALYST_APP_KEY") == "" {
		keyPath := filepath.Join(filepath.Dir(opts.EmbeddedDBPath), "app-key")
		if key, err := ensureAppKeyFile(keyPath); err == nil {
			_ = os.Setenv("FLOWCATALYST_APP_KEY", key)
		} else {
			slog.Warn("unable to persist app encryption key — OAuth client secrets won't survive restart", "err", err)
		}
	}

	if err := seed.NewSeeder(pool).Run(rootCtx); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	// Bootstrap local MCP credentials (idempotent, dev-only) so `fc-dev mcp`
	// and `--mcp` just work. Non-fatal — a failure must not block the dev boot.
	mcpBaseURL := fmt.Sprintf("http://localhost:%d", opts.APIPort)
	if err := bootstrapMCPCredentials(rootCtx, pool, mcpBaseURL); err != nil {
		slog.Warn("MCP credential bootstrap failed (continuing)", "err", err)
	}

	// SIGTERM / SIGINT → cancel rootCtx so server.Run drains.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("shutdown signal received")
		cancel()
	}()

	// ── Delegate to the shared run-loop ────────────────────────────────
	cfg := devEnvCfg(opts, databaseURL)
	runOpts := server.RunOptions{}
	if frontend.IsAvailable() {
		runOpts.Fallback = frontend.Handler()
		slog.Info("embedded Vue SPA available")
	} else {
		slog.Warn("frontend not embedded — run `make frontend` and rebuild to ship the SPA")
	}
	runOpts.ExtraAPIRoutes = func(_ chi.Router) {} // no extras today
	return server.Run(rootCtx, pool, cfg, runOpts)
}

// banner prints the startup summary the way Rust fc-dev does.
func banner(opts startOpts) {
	slog.Info("=== FlowCatalyst Dev Monolith ===")
	slog.Info("subsystem configuration",
		"api_port", opts.APIPort,
		"embedded_db", opts.EmbeddedDB,
		"embedded_db_port", opts.EmbeddedDBPort,
		"scheduler", opts.SchedulerEnabled,
		"stream", opts.StreamEnabled,
		"outbox", opts.OutboxEnabled,
		"router", opts.RouterEnabled,
		"mcp", opts.MCPEnabled,
	)
}

// defaultEmbeddedPath is the persistent per-user base for the dev Postgres data
// CLUSTER. The re-creatable binaries + runtime live separately under the cache
// dir (embeddedPGCacheDir), so they survive a data wipe and aren't mixed with
// the data. Never /tmp: that may be mounted noexec (can't exec postgres) and is
// cleared on reboot. Override the data base with FC_EMBEDDED_DB_PATH (handled by
// the flag default).
func defaultEmbeddedPath() string {
	return filepath.Join(userDataDir(), "flowcatalyst", "embedded-pg")
}

// userDataDir returns the OS-idiomatic persistent data directory:
// $XDG_DATA_HOME or the OS app-data dir (~/Library/Application Support on macOS,
// %AppData% on Windows, ~/.config on Linux), with a ~/.local/share fallback.
// Never returns /tmp.
func userDataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x
	}
	if d, err := os.UserConfigDir(); err == nil {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share")
	}
	return "."
}

// embeddedPGCacheDir is the OS cache dir for the extracted/downloaded Postgres
// binaries + runtime — a re-creatable cache, so it belongs in the cache dir
// (which, unlike /tmp, persists across reboots and isn't mounted noexec).
func embeddedPGCacheDir() string {
	if c, err := os.UserCacheDir(); err == nil {
		return filepath.Join(c, "flowcatalyst", "embedded-pg")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "flowcatalyst", "embedded-pg")
	}
	return filepath.Join(".", ".flowcatalyst-cache", "embedded-pg")
}
