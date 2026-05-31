// Package migrate applies the embedded schema migrations to a Postgres
// database using github.com/pressly/goose/v3 as the runner.
//
// Each migration is a numbered .sql file under internal/migrate/sql/
// prefixed with `-- +goose Up`. Forward-only by design — we don't write
// down migrations. New migrations: `internal/migrate/sql/NNN_subject.sql`
// where NNN is the next zero-padded sequence.
//
// Run is idempotent. An already-migrated database is detected and its goose
// ledger is seeded so nothing re-runs (which matters because migrations
// 019/022 DROP and recreate the messaging tables). Go's own legacy
// `_fc_migrations` tracker, the Rust platform's `_schema_migrations` tracker,
// and — as a final safety net — a populated-but-untracked schema are all
// recognised. See bootstrap for the precedence.
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed all:sql
var migrationsFS embed.FS

// Run applies every pending migration to pool's database.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	goose.SetBaseFS(migrationsFS)

	if err := bootstrap(ctx, db); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	return goose.UpContext(ctx, db, "sql")
}

// bootstrap seeds goose_db_version so an existing, already-migrated database
// is never re-migrated on the cutover — which matters because migrations
// 019/022 DROP and recreate the messaging tables. It recognises, in priority
// order:
//
//  1. `_fc_migrations` — Go's own pre-goose tracker (a `name` column). Seed
//     goose from it, then drop it: it's ours and goose supersedes it.
//  2. `_schema_migrations` — the Rust platform's tracker (a `migration_id`
//     column). Seed goose from it but leave it in place; the Rust system may
//     still own it during a side-by-side cutover or a rollback.
//  3. No recognised tracker and goose has never run here, but the schema is
//     already populated (the canonical `tnt_clients` table exists). Baseline
//     goose to the full shipped migration set so none re-run against live
//     data. Mirrors tools/baseline-goose-ledger.sql.
//
// A genuinely fresh database matches none of these and is left untouched, so
// goose applies every migration normally. Safe + idempotent on every boot.
func bootstrap(ctx context.Context, db *sql.DB) error {
	// (1) Go's own pre-goose tracker.
	if ok, err := tableExists(ctx, db, "_fc_migrations"); err != nil {
		return err
	} else if ok {
		versions, err := trackerVersions(ctx, db, "_fc_migrations", "name")
		if err != nil {
			return err
		}
		slog.Info("migrate: seeding goose ledger from legacy _fc_migrations",
			"migrations", len(versions))
		return seedGoose(ctx, db, versions, "_fc_migrations")
	}

	// (2) The Rust platform's tracker — seed but never drop it.
	if ok, err := tableExists(ctx, db, "_schema_migrations"); err != nil {
		return err
	} else if ok {
		versions, err := trackerVersions(ctx, db, "_schema_migrations", "migration_id")
		if err != nil {
			return err
		}
		if len(versions) > 0 {
			slog.Info("migrate: seeding goose ledger from Rust _schema_migrations",
				"migrations", len(versions))
			return seedGoose(ctx, db, versions, "")
		}
		// Exists but empty — fall through to the populated-schema baseline.
	}

	// (3) Populated schema, no tracker goose recognises, and goose itself has
	// never run here → baseline to the shipped set so destructive migrations
	// don't re-run. Gated on an empty goose ledger so a normal restart (or a
	// freshly-added local migration) is never marked applied behind our back.
	if hasGoose, err := gooseHasMigrations(ctx, db); err != nil {
		return err
	} else if !hasGoose {
		if ok, err := tableExists(ctx, db, "tnt_clients"); err != nil {
			return err
		} else if ok {
			versions, err := shippedVersions()
			if err != nil {
				return err
			}
			slog.Warn("migrate: populated schema with no recognised migration tracker; "+
				"baselining goose ledger to the shipped set (no migrations will re-run)",
				"migrations", len(versions))
			return seedGoose(ctx, db, versions, "")
		}
	}

	// (4) Fresh database (or already goose-tracked) — nothing to seed.
	return nil
}

// tableExists reports whether a table of the given name is visible in the
// connected database (any schema, matching the original probe).
func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var ok bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM information_schema.tables WHERE table_name = $1
		 )`, name).Scan(&ok); err != nil {
		return false, fmt.Errorf("probe %s: %w", name, err)
	}
	return ok, nil
}

// gooseHasMigrations reports whether goose has already recorded at least one
// real migration (version_id > 0) for this database.
func gooseHasMigrations(ctx context.Context, db *sql.DB) (bool, error) {
	ok, err := tableExists(ctx, db, "goose_db_version")
	if err != nil || !ok {
		return false, err
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM goose_db_version WHERE version_id > 0`).Scan(&n); err != nil {
		return false, fmt.Errorf("count goose_db_version: %w", err)
	}
	return n > 0, nil
}

// trackerVersions reads a tracker table's id column and returns the numeric
// version prefix of each entry. table/column are trusted in-package
// constants, not user input.
func trackerVersions(ctx context.Context, db *sql.DB, table, column string) ([]int64, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+column+" FROM "+table)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if v, ok := versionPrefix(id); ok {
			versions = append(versions, v)
		}
	}
	return versions, rows.Err()
}

// shippedVersions returns the numeric prefix of every migration file embedded
// under sql/, so a tracker-less but populated database can be baselined to the
// exact set this binary ships.
func shippedVersions() ([]int64, error) {
	entries, err := migrationsFS.ReadDir("sql")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var versions []int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if v, ok := versionPrefix(e.Name()); ok {
			versions = append(versions, v)
		}
	}
	return versions, nil
}

// seedGoose ensures goose_db_version exists (with goose's zero row) and marks
// each version applied, skipping any already present. When dropTable is
// non-empty it is dropped in the same transaction (used to retire Go's own
// _fc_migrations after seeding). dropTable is a trusted in-package constant.
func seedGoose(ctx context.Context, db *sql.DB, versions []int64, dropTable string) error {
	if _, err := db.ExecContext(ctx, gooseSchemaDDL); err != nil {
		return fmt.Errorf("ensure goose_db_version: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, v := range versions {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO goose_db_version (version_id, is_applied)
			 SELECT $1, TRUE
			 WHERE NOT EXISTS (
			     SELECT 1 FROM goose_db_version WHERE version_id = $1
			 )`, v); err != nil {
			return fmt.Errorf("seed goose version %d: %w", v, err)
		}
	}
	if dropTable != "" {
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+dropTable); err != nil {
			return fmt.Errorf("drop %s: %w", dropTable, err)
		}
	}
	return tx.Commit()
}

// versionPrefix extracts the leading NNN_ numeric version from a migration id
// or filename (e.g. "026_processes" or "026_processes.sql" → 26).
func versionPrefix(name string) (int64, bool) {
	m := versionRe.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

var versionRe = regexp.MustCompile(`^(\d+)_`)

// gooseSchemaDDL matches the table goose creates lazily on first use.
// Declaring it here lets us populate the table during bootstrap without
// having to call goose.Up first (which would attempt to apply all
// migrations against a database that already has the schema).
const gooseSchemaDDL = `
CREATE TABLE IF NOT EXISTS goose_db_version (
    id serial NOT NULL,
    version_id bigint NOT NULL,
    is_applied boolean NOT NULL,
    tstamp timestamp NULL default now(),
    PRIMARY KEY(id)
);

INSERT INTO goose_db_version (version_id, is_applied)
SELECT 0, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 0);
`
