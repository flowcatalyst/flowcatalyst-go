// Package seed is the port of fc-platform/src/shared/database.rs's
// seed_builtin_roles / seed_platform_application and the platform
// event-types catalog under fc-platform/src/seed. Startup-time
// hydration: bootstrap-only, runs before HTTP serving begins.
//
// Per docs/conventions.md §3, this is infrastructure-processing (no
// UoW, no executing principal). All writes are direct INSERTs that
// upsert idempotently on every boot.
package seed

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// Seeder applies code-defined definitions to the database.
type Seeder struct {
	pool *pgxpool.Pool
}

// NewSeeder wires a seeder.
func NewSeeder(pool *pgxpool.Pool) *Seeder { return &Seeder{pool: pool} }

// Run executes the full seed pass. Called from cmd/fc-platform-server
// during startup, after migrations have run.
func (s *Seeder) Run(ctx context.Context) error {
	if err := s.seedPlatformApplication(ctx); err != nil {
		return fmt.Errorf("seed platform application: %w", err)
	}
	if err := s.seedRoles(ctx); err != nil {
		return fmt.Errorf("seed roles: %w", err)
	}
	if err := s.seedEventTypes(ctx); err != nil {
		return fmt.Errorf("seed event types: %w", err)
	}
	if err := s.seedEventSchemas(ctx); err != nil {
		return fmt.Errorf("seed event schemas: %w", err)
	}
	if err := s.seedDefaultProcesses(ctx); err != nil {
		return fmt.Errorf("seed default processes: %w", err)
	}
	if err := s.seedBootstrapAdmin(ctx); err != nil {
		return fmt.Errorf("seed bootstrap admin: %w", err)
	}
	return nil
}

// seedPlatformApplication inserts the single "platform" application row
// if it doesn't already exist. Idempotent; leaves any existing row
// alone (matching Rust's seed_platform_application).
func (s *Seeder) seedPlatformApplication(ctx context.Context) error {
	var existingID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM app_applications WHERE code = $1`, "platform").Scan(&existingID)
	if err == nil {
		return nil // already seeded
	}
	app := application.New("platform", "FlowCatalyst Platform")
	desc := "Core platform — its own OpenAPI document is published here as one of the applications"
	app.Description = &desc
	now := time.Now().UTC()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO app_applications
		     (id, type, code, name, description, icon_url, website, logo, logo_mime_type,
		      default_base_url, service_account_id, active, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 ON CONFLICT (code) DO NOTHING`,
		app.ID, string(app.Type), app.Code, app.Name, app.Description,
		app.IconURL, app.Website, app.Logo, app.LogoMimeType,
		app.DefaultBaseURL, app.ServiceAccountID, app.Active, now, now); err != nil {
		return err
	}
	slog.Info("seeded built-in platform application")
	return nil
}

// seedRoles upserts the 12 built-in roles. Mirrors Rust's
// seed_builtin_roles: skip-if-name-exists (preserves any local edits to
// permissions, matching Rust's behaviour exactly).
func (s *Seeder) seedRoles(ctx context.Context) error {
	roles := PlatformRoles()
	var inserted int
	for _, r := range roles {
		var existingID string
		err := s.pool.QueryRow(ctx,
			`SELECT id FROM iam_roles WHERE name = $1`, r.Name).Scan(&existingID)
		if err == nil {
			continue // role already exists, leave it alone
		}
		now := time.Now().UTC()
		id := tsid.Generate(tsid.Role)
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO iam_roles
			     (id, application_id, name, display_name, description, application_code,
			      source, client_managed, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT (name) DO NOTHING`,
			id, nil, r.Name, r.DisplayName, r.Description, r.ApplicationCode,
			string(r.Source), false, now, now); err != nil {
			return fmt.Errorf("insert role %s: %w", r.Name, err)
		}
		// We may have raced with another seeder; re-lookup to get the
		// authoritative id before writing permissions.
		if err := s.pool.QueryRow(ctx,
			`SELECT id FROM iam_roles WHERE name = $1`, r.Name).Scan(&id); err != nil {
			return fmt.Errorf("lookup id for role %s: %w", r.Name, err)
		}
		for _, p := range r.Permissions {
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO iam_role_permissions (role_id, permission)
				 VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				id, p); err != nil {
				return fmt.Errorf("insert permission %s for %s: %w", p, r.Name, err)
			}
		}
		slog.Info("seeded built-in role", "role", r.Name)
		inserted++
	}
	if inserted > 0 {
		slog.Info("built-in role seeding complete", "count", inserted)
	}
	return nil
}

// seedEventTypes upserts the platform's built-in event type catalog.
// Body lives in event_types.go.
func (s *Seeder) seedEventTypes(ctx context.Context) error {
	return s.seedPlatformEventTypes(ctx)
}

// seedEventSchemas upserts the JSON schemas for platform event types.
// Body lives in event_schemas.go.
func (s *Seeder) seedEventSchemas(ctx context.Context) error {
	return s.seedPlatformEventSchemas(ctx)
}
