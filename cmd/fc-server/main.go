// Command fc-server is the FlowCatalyst unified production server.
//
// Single binary; every subsystem is independently togglable via
// FC_*_ENABLED env vars so the same image can be deployed as the API
// tier, a worker tier, or both. Mirrors the Rust fc-server's env
// contract. fc-dev wraps the same `server.Run` orchestrator with
// embedded-Postgres + dev defaults for local work.
//
// See internal/server/envcfg.go for the full env-var list.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/flowcatalyst/flowcatalyst-go/frontend"
	"github.com/flowcatalyst/flowcatalyst-go/internal/config"
	"github.com/flowcatalyst/flowcatalyst-go/internal/logging"
	"github.com/flowcatalyst/flowcatalyst-go/internal/migrate"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/seed"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/database"
	"github.com/flowcatalyst/flowcatalyst-go/internal/server"
)

func main() {
	logging.Init()
	cfg := server.LoadEnv()

	slog.Info("starting fc-server",
		"platform", cfg.PlatformEnabled,
		"router", cfg.RouterEnabled,
		"scheduler", cfg.SchedulerEnabled,
		"stream", cfg.StreamEnabled,
		"outbox", cfg.OutboxEnabled,
		"mcp", cfg.MCPEnabled,
		"standby", cfg.StandbyEnabled,
		"api_port", cfg.APIPort,
		"metrics_port", cfg.MetricsPort,
	)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// AWS Secrets Manager DB mode: when DB_SECRET_ARN + DB_HOST are set (and no
	// explicit FC_DATABASE_URL/DATABASE_URL), resolve the connection from the
	// secret. Mirrors the Rust fc-server DB-secret resolution.
	if dbURL, ok, err := server.ResolveDBSecretURL(rootCtx); err != nil {
		slog.Error("resolve DB secret failed", "err", err)
		os.Exit(1)
	} else if ok {
		cfg.DatabaseURL = dbURL
		slog.Info("resolved database URL from AWS Secrets Manager")
	}

	// DB-secret rotation: when SM mode + DB_SECRET_REFRESH_INTERVAL_MS != 0,
	// poll for rotated creds and inject them into new connections (no restart).
	dbCfg := config.DBConfig{URL: cfg.DatabaseURL}
	var beforeConnect func(context.Context, *pgx.ConnConfig) error
	if refresher, err := server.NewDBSecretRefresher(rootCtx); err != nil {
		slog.Error("DB secret refresher init failed", "err", err)
		os.Exit(1)
	} else if refresher != nil {
		beforeConnect = refresher.BeforeConnect
		go refresher.Run(rootCtx)
		slog.Info("DB secret rotation enabled")
	}

	pool, err := database.NewPoolWithBeforeConnect(rootCtx, dbCfg, beforeConnect)
	if err != nil {
		slog.Error("postgres connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("postgres connected")

	if err := migrate.Run(rootCtx, pool); err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	if err := seed.NewSeeder(pool).Run(rootCtx); err != nil {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
	slog.Info("seed complete")

	// SIGTERM / SIGINT → cancel rootCtx → server.Run drains.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("shutdown signal received")
		cancel()
	}()

	// Serve the embedded Vue SPA when it was built into the binary (parity
	// with the Rust fc-server, which hosts the dashboard). No-op when dist
	// wasn't embedded — fc-server then runs API-only.
	runOpts := server.RunOptions{}
	if frontend.IsAvailable() {
		runOpts.Fallback = frontend.Handler()
		slog.Info("embedded Vue SPA available")
	}

	if err := server.Run(rootCtx, pool, cfg, runOpts); err != nil {
		slog.Error("fc-server exited with error", "err", err)
		os.Exit(1)
	}
}
