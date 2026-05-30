-- Baseline the goose migration ledger for a drop-in deployment.
--
-- WHEN TO RUN
--   On demand, against an EXISTING FlowCatalyst database whose schema was
--   already applied by another implementation (e.g. the Rust platform, which
--   tracks its own history in `_sqlx_migrations`), BEFORE starting the Go
--   fc-server / fc-dev against that database for the first time.
--
-- WHY
--   The Go server applies its schema with pressly/goose, which records
--   applied versions in `goose_db_version`. On a database goose has never
--   seen, that ledger is empty, so goose would try to (re-)apply every
--   migration 001..030 — including 019/022, which DROP and recreate the
--   messaging tables. (Go's own internal/migrate.bootstrap() auto-seeds the
--   ledger only when the legacy `_fc_migrations` tracker is present; it does
--   NOT recognise Rust's `_sqlx_migrations`, hence this manual script.)
--
--   Running this first marks migrations 001..030 as already applied, so
--   goose.Up runs none of them. Any NEW migration added later (031+) is
--   unaffected and still applies normally.
--
-- USAGE
--   psql "$FC_DATABASE_URL" -f tools/baseline-goose-ledger.sql
--
-- SAFE TO RE-RUN: each version is inserted only if absent (no duplicates).
--
-- KEEP IN SYNC: the version list below is the numeric prefix of every file in
-- internal/migrate/sql/ (001..030, with 023 intentionally absent). When you
-- add a migration you do NOT need to update this script unless you want it to
-- baseline that new version too — leaving it out simply lets goose apply the
-- new migration on first boot, which is usually what you want.

BEGIN;

-- Identical to the table pressly/goose creates lazily, and to
-- internal/migrate/migrate.go:gooseSchemaDDL.
CREATE TABLE IF NOT EXISTS goose_db_version (
    id         serial    NOT NULL,
    version_id bigint    NOT NULL,
    is_applied boolean   NOT NULL,
    tstamp     timestamp NULL DEFAULT now(),
    PRIMARY KEY (id)
);

-- goose's zero row + every shipped migration (001..030, no 023), marked
-- applied. The NOT EXISTS guard makes re-runs no-ops.
INSERT INTO goose_db_version (version_id, is_applied)
SELECT v, TRUE
FROM (VALUES
    (0),
    (1),  (2),  (3),  (4),  (5),  (6),  (7),  (8),  (9),  (10),
    (11), (12), (13), (14), (15), (16), (17), (18), (19), (20),
    (21), (22), (24), (25), (26), (27), (28), (29), (30)
) AS t(v)
WHERE NOT EXISTS (
    SELECT 1 FROM goose_db_version g WHERE g.version_id = t.v
);

COMMIT;

-- Show the resulting ledger so you can eyeball it.
SELECT version_id, is_applied, tstamp
FROM goose_db_version
ORDER BY version_id;
