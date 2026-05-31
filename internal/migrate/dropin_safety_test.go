package migrate

// Drop-in migration safety — proves that booting the Go schema runner against
// an already-migrated database does NOT re-run the destructive partition
// migrations (019/022, which DROP and recreate the messaging tables). Uses a
// real embedded Postgres, so it is gated behind FC_MIGRATE_PG_TEST=1 to stay
// out of the DB-free unit suite (and out of CI unless explicitly enabled). The
// embedded Postgres binary downloads once per machine on first run.
//
//	FC_MIGRATE_PG_TEST=1 go test ./internal/migrate/ -run TestDropInSafety -v

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDropInSafety(t *testing.T) {
	if os.Getenv("FC_MIGRATE_PG_TEST") == "" {
		t.Skip("set FC_MIGRATE_PG_TEST=1 to run the embedded-Postgres drop-in safety test")
	}
	ctx := context.Background()

	const port = 15433
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).
		DataPath(filepath.Join(t.TempDir(), "data")).
		RuntimePath(filepath.Join(t.TempDir(), "runtime")).
		Username("postgres").Password("postgres").Database("flowcatalyst").
		StartTimeout(90 * time.Second))
	if err := pg.Start(); err != nil {
		t.Fatalf("start embedded pg: %v", err)
	}
	defer func() { _ = pg.Stop() }()

	pool, err := pgxpool.New(ctx, fmt.Sprintf(
		"postgresql://postgres:postgres@localhost:%d/flowcatalyst?sslmode=disable", port))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	sentinelCount := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM msg_events WHERE id = 'SENTINEL'`).Scan(&n); err != nil {
			t.Fatalf("count sentinel: %v", err)
		}
		return n
	}

	// Fresh migrate → full schema + goose ledger. Then drop a sentinel into a
	// table migration 019 would DROP CASCADE if it ever re-ran.
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	mustExec(`INSERT INTO msg_events (id, type, source, time) VALUES ('SENTINEL','test','test',NOW())`)
	if sentinelCount() != 1 {
		t.Fatal("sentinel not inserted")
	}

	// ── Scenario A: a Rust-migrated DB — populated schema + the Rust
	//    _schema_migrations tracker, but no goose ledger. ───────────────────
	mustExec(`DROP TABLE goose_db_version`)
	mustExec(`CREATE TABLE _schema_migrations (
		migration_id VARCHAR(100) PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		duration_ms INTEGER, checksum TEXT)`)
	for _, v := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20,
		21, 22, 24, 25, 26, 27, 28, 29, 30} {
		mustExec(`INSERT INTO _schema_migrations (migration_id) VALUES ($1)`,
			fmt.Sprintf("%03d_rust_migration", v))
	}

	if err := Run(ctx, pool); err != nil {
		t.Fatalf("scenario A re-migrate: %v", err)
	}
	if got := sentinelCount(); got != 1 {
		t.Fatalf("scenario A: msg_events was recreated — DATA LOSS (sentinel count=%d)", got)
	}
	var v19, hasRust int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM goose_db_version WHERE version_id = 19 AND is_applied`).Scan(&v19); err != nil {
		t.Fatalf("check goose v19: %v", err)
	}
	if v19 != 1 {
		t.Fatalf("scenario A: goose version 19 not seeded as applied (got %d)", v19)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name='_schema_migrations'`).Scan(&hasRust); err != nil {
		t.Fatalf("check _schema_migrations: %v", err)
	}
	if hasRust != 1 {
		t.Fatal("scenario A: _schema_migrations was dropped (must be preserved for Rust rollback)")
	}
	t.Log("scenario A (Rust _schema_migrations): sentinel survived, goose seeded, Rust tracker preserved")

	// ── Scenario B: populated schema, NO tracker, NO goose ledger. The
	//    tnt_clients fallback must baseline so nothing re-runs. ─────────────
	mustExec(`DROP TABLE _schema_migrations`)
	mustExec(`DROP TABLE goose_db_version`)
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("scenario B re-migrate: %v", err)
	}
	if got := sentinelCount(); got != 1 {
		t.Fatalf("scenario B: msg_events was recreated — DATA LOSS (sentinel count=%d)", got)
	}
	t.Log("scenario B (no tracker, populated schema): sentinel survived via tnt_clients baseline")
}
