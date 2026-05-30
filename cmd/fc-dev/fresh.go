package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/spf13/cobra"

	"github.com/flowcatalyst/flowcatalyst-go/internal/config"
	"github.com/flowcatalyst/flowcatalyst-go/internal/migrate"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/seed"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/database"
)

// freshTables is the explicit list of FlowCatalyst tables `fresh`
// truncates. Preserves the schema + the _fc_migrations tracker so the
// next boot doesn't reapply migrations. Order is leaf-tables-first so
// FK cascades don't trip.
var freshTables = []string{
	// Audit + event read tables (high-volume, leaf).
	"aud_logs",
	"msg_events_read",
	"msg_events",
	"msg_dispatch_jobs",
	"msg_dispatch_job_attempts",
	"msg_scheduled_job_instances",
	"msg_subscription_event_types",
	"msg_event_type_spec_versions",
	"msg_subscriptions",
	"msg_event_types",
	"msg_connections",
	"msg_dispatch_pools",
	"msg_scheduled_jobs",
	// OAuth + login state.
	"oauth_oidc_payloads",
	"oauth_oidc_login_states",
	"oauth_client_grant_types",
	"oauth_client_allowed_origins",
	"oauth_client_redirect_uris",
	"oauth_client_application_ids",
	"oauth_clients",
	"oauth_idp_role_mappings",
	"oauth_identity_provider_allowed_domains",
	"oauth_identity_providers",
	// Webauthn.
	"webauthn_credentials",
	// Auth tracking.
	"iam_login_attempts",
	"iam_password_reset_tokens",
	// IAM + tenancy relations. additional_client_ids and
	// granted_client_ids are JSONB columns on tnt_client_auth_configs,
	// not separate junction tables.
	"tnt_client_auth_configs",
	"tnt_anchor_domains",
	"iam_principal_application_access",
	"iam_client_access_grants",
	"iam_principal_roles",
	"iam_role_permissions",
	"iam_principals",
	"iam_roles",
	"oauth_idp_role_mappings",
	"oauth_clients",
	"tnt_cors_allowed_origins",
	"iam_service_accounts",
	"app_client_configs",
	"app_applications",
	"tnt_clients",
	// Platform config.
	"app_platform_config_access",
	"app_platform_configs",
}

// newFreshCmd truncates every FlowCatalyst table (preserving schema +
// migration tracker). Refuses to run without --yes. Idempotent.
//
// When --database-url / FC_DATABASE_URL is set, fresh connects to that
// URL and skips the embedded Postgres. Otherwise it starts the
// embedded Postgres (using the same data dir as `fc-dev start`), runs
// the truncate + reseed, then stops it. That way `bin/fc-dev fresh` is
// self-contained — no need to leave `fc-dev start` running in another
// terminal just to wipe state.
func newFreshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fresh",
		Short: "Truncate every FlowCatalyst table (preserves schema)",
		Long: `Removes every row from every FlowCatalyst table, returning the database
to an immediately-post-migration state. The _fc_migrations tracker is
preserved so the next boot skips re-migrating. Automatically starts the
embedded Postgres if no external --database-url is supplied.

Refuses to run without --yes to prevent accidental data loss.`,
		RunE: runFresh,
	}
	cmd.Flags().String("database-url", envStrDefault("FC_DATABASE_URL", ""), "Postgres URL (defaults to local embedded)")
	cmd.Flags().Int("embedded-db-port", envIntDefault("FC_EMBEDDED_DB_PORT", 15432), "embedded Postgres port (when --database-url is unset)")
	cmd.Flags().String("embedded-db-path", envStrDefault("FC_EMBEDDED_DB_PATH", defaultEmbeddedPath()), "embedded Postgres data directory")
	cmd.Flags().Bool("yes", false, "confirm truncation (required)")
	return cmd
}

func runFresh(cmd *cobra.Command, _ []string) error {
	getStr := func(k string) string { v, _ := cmd.Flags().GetString(k); return v }
	getInt := func(k string) int { v, _ := cmd.Flags().GetInt(k); return v }
	getBool := func(k string) bool { v, _ := cmd.Flags().GetBool(k); return v }
	if !getBool("yes") {
		return errors.New("refusing to truncate without --yes")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Resolve the target URL: explicit --database-url wins; otherwise
	// boot the embedded Postgres ourselves so the command is self-
	// contained when fc-dev start isn't running.
	url := getStr("database-url")
	var pg *embeddedpostgres.EmbeddedPostgres
	if url == "" {
		port := getInt("embedded-db-port")
		dataPath := getStr("embedded-db-path")
		if err := os.MkdirAll(dataPath, 0o755); err != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
		cacheDir := embeddedPGCacheDir()
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return fmt.Errorf("create cache dir: %w", err)
		}
		pg = embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
			Port(uint32(port)).
			DataPath(filepath.Join(dataPath, "data")).
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
		url = fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/flowcatalyst?sslmode=disable", port)
		slog.Info("embedded postgres started for fresh", "port", port, "path", dataPath)
	}

	pool, err := database.NewPool(ctx, config.DBConfig{URL: url})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	// Run migrations before truncating — when fresh is the very first
	// command against an empty embedded data dir, the tables don't
	// exist yet and TRUNCATE would 42P01.
	if err := migrate.Run(ctx, pool); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	// Single TRUNCATE with CASCADE so FK ordering doesn't matter at the
	// SQL level. The explicit table list is still the source of truth
	// for which tables are "FlowCatalyst's" — anything not listed
	// belongs to a consumer app and is intentionally left alone.
	stmt := "TRUNCATE TABLE "
	for i, t := range freshTables {
		if i > 0 {
			stmt += ", "
		}
		stmt += t
	}
	stmt += " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	slog.Info("FlowCatalyst tables truncated", "table_count", len(freshTables))

	// Re-seed so the next sign-in works without restarting. Match
	// fc-dev start's bootstrap defaults so the post-truncate admin
	// matches what a fresh `fc-dev start` would create.
	setEnvDefault(seed.EnvBootstrapEmail, "admin@flowcatalyst.local")
	setEnvDefault(seed.EnvBootstrapPassword, "DevPassword123!")
	setEnvDefault(seed.EnvBootstrapName, "Local Admin")
	if err := seed.NewSeeder(pool).Run(ctx); err != nil {
		return fmt.Errorf("reseed: %w", err)
	}
	slog.Info("FlowCatalyst reseeded — sign in with the bootstrap admin",
		"email", "admin@flowcatalyst.local")
	return nil
}
