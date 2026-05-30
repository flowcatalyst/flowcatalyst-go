package stream

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PartitionManager maintains the monthly RANGE partitions on the partitioned
// messaging tables. Ports fc-stream/src/partition_manager.rs. Each pass:
//
//   - ensures monthly partitions exist for the current month + the next
//     MonthsForward months on every partitioned parent;
//   - drops partitions whose entire date range ends before the retention
//     cutoff (now - RetentionDays).
//
// Partition naming (migrations 019/022): `<parent>_YYYY_MM`. The drop pass
// parses the `YYYY_MM` suffix to compute each partition's age.
//
// Deliberately depends on no Postgres extension (pg_partman/pg_cron) — RDS
// extension allowlists move on AWS's schedule, not ours. Pure-Go maintenance
// gives identical behaviour in dev and prod.
//
// Drop-in safety: every parent is guarded by isPartitioned — if a populated
// upstream DB never applied the partitioning migration, the parent is a plain
// table and is silently skipped (CREATE … PARTITION OF would otherwise error
// on every tick).
type PartitionManager struct {
	pool   *pgxpool.Pool
	Health *Health

	// Config is applied with DefaultPartitionManagerConfig() filling any
	// zero fields at Run time, so the back-compat NewPartitionManager(pool)
	// path keeps working.
	Config PartitionManagerConfig
	// IsLeader gates each pass; nil means always-leader (single instance).
	// The DDL (CREATE/DROP) is idempotent, but gating avoids needless
	// concurrent churn across replicas.
	IsLeader func() bool
}

// PartitionManagerConfig tunes the manager. Mirrors the Rust
// PartitionManagerConfig (months_forward / retention_days / tick_interval).
type PartitionManagerConfig struct {
	MonthsForward int           // forward monthly partitions to keep ahead (default 3)
	RetentionDays int           // drop partitions whose range ends before now-this (default 90)
	TickInterval  time.Duration // re-check cadence after the startup pass (default 24h)
}

// DefaultPartitionManagerConfig returns the Rust defaults.
func DefaultPartitionManagerConfig() PartitionManagerConfig {
	return PartitionManagerConfig{MonthsForward: 3, RetentionDays: 90, TickInterval: 24 * time.Hour}
}

// PartitionedTables is the canonical list. Mirrors the Rust
// PARTITIONED_PARENTS + migrations 019/020/022.
var PartitionedTables = []string{
	"msg_events",
	"msg_events_read",
	"msg_dispatch_jobs",
	"msg_dispatch_jobs_read",
	"msg_dispatch_job_attempts",
	"msg_scheduled_job_instances",
	"msg_scheduled_job_instance_logs",
}

// NewPartitionManager wires a manager with default config + always-leader.
func NewPartitionManager(pool *pgxpool.Pool) *PartitionManager {
	return &PartitionManager{pool: pool}
}

func (m *PartitionManager) cfg() PartitionManagerConfig {
	c := m.Config
	d := DefaultPartitionManagerConfig()
	if c.MonthsForward <= 0 {
		c.MonthsForward = d.MonthsForward
	}
	if c.RetentionDays <= 0 {
		c.RetentionDays = d.RetentionDays
	}
	if c.TickInterval <= 0 {
		c.TickInterval = d.TickInterval
	}
	return c
}

func (m *PartitionManager) leader() bool {
	if m.IsLeader == nil {
		return true
	}
	return m.IsLeader()
}

// Run ticks once on startup, then every Config.TickInterval, until ctx is
// cancelled.
func (m *PartitionManager) Run(ctx context.Context) {
	cfg := m.cfg()
	if m.Health != nil {
		m.Health.SetRunning(true)
		defer m.Health.SetRunning(false)
	}
	m.runPass(ctx)

	tick := time.NewTicker(cfg.TickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("partition manager stopped")
			return
		case <-tick.C:
			m.runPass(ctx)
		}
	}
}

func (m *PartitionManager) runPass(ctx context.Context) {
	if !m.leader() {
		return
	}
	created, dropped, err := m.pass(ctx)
	if err != nil {
		slog.Warn("partition manager pass failed", "err", err)
		if m.Health != nil {
			m.Health.RecordError()
		}
		return
	}
	if m.Health != nil {
		m.Health.AddProcessed(0) // stamp last-poll
	}
	if created > 0 || dropped > 0 {
		slog.Info("partition manager pass", "created", created, "dropped", dropped)
	}
}

// pass runs one full create+drop sweep across all partitioned parents.
func (m *PartitionManager) pass(ctx context.Context) (created, dropped int, err error) {
	cfg := m.cfg()
	now := time.Now().UTC()
	for _, parent := range PartitionedTables {
		ok, perr := m.isPartitioned(ctx, parent)
		if perr != nil {
			return created, dropped, fmt.Errorf("is_partitioned(%s): %w", parent, perr)
		}
		if !ok {
			// Not a partitioned table in this DB (drop-in over a
			// non-partitioned upstream schema) — skip silently.
			continue
		}
		c, cerr := m.ensureForward(ctx, parent, now, cfg.MonthsForward)
		if cerr != nil {
			slog.Warn("partition manager: ensure forward failed", "parent", parent, "err", cerr)
		}
		created += c
		d, derr := m.dropOld(ctx, parent, now, cfg.RetentionDays)
		if derr != nil {
			slog.Warn("partition manager: drop old failed", "parent", parent, "err", derr)
		}
		dropped += d
	}
	return created, dropped, nil
}

// isPartitioned reports whether parent is a partitioned table (relkind 'p').
func (m *PartitionManager) isPartitioned(ctx context.Context, parent string) (bool, error) {
	var relkind string
	err := m.pool.QueryRow(ctx,
		`SELECT relkind::text FROM pg_class WHERE relname = $1`, parent).Scan(&relkind)
	if err != nil {
		// Not found → not partitioned (table may not exist in this DB).
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, err
	}
	return relkind == "p", nil
}

// ensureForward creates the current + next MonthsForward monthly partitions.
// Uses CREATE TABLE IF NOT EXISTS … PARTITION OF (valid since PG 10), so it
// is idempotent without a pre-check.
func (m *PartitionManager) ensureForward(ctx context.Context, parent string, now time.Time, monthsForward int) (int, error) {
	created := 0
	for offset := 0; offset <= monthsForward; offset++ {
		start := monthStart(now, offset)
		end := monthStart(now, offset+1)
		name := fmt.Sprintf("%s_%s", parent, start.Format("2006_01"))
		_, err := m.pool.Exec(ctx, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
			pgQuoteIdent(name), pgQuoteIdent(parent),
			start.Format("2006-01-02"), end.Format("2006-01-02")))
		if err != nil {
			return created, fmt.Errorf("create partition %s: %w", name, err)
		}
		created++
	}
	return created, nil
}

// dropOld drops partitions of parent whose entire range ends on or before the
// retention cutoff (now - retentionDays). Mirrors the Rust drop_old_partitions.
func (m *PartitionManager) dropOld(ctx context.Context, parent string, now time.Time, retentionDays int) (int, error) {
	cutoff := now.AddDate(0, 0, -retentionDays)
	rows, err := m.pool.Query(ctx, `
		SELECT child.relname
		FROM pg_inherits i
		JOIN pg_class parent ON i.inhparent = parent.oid
		JOIN pg_class child ON i.inhrelid = child.oid
		WHERE parent.relname = $1`, parent)
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return 0, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	dropped := 0
	for _, name := range names {
		end, ok := parsePartitionEnd(name, parent)
		if !ok {
			continue // not a YYYY_MM monthly partition (e.g. a default) — leave it
		}
		if !end.After(cutoff) { // end <= cutoff
			if _, err := m.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, pgQuoteIdent(name))); err != nil {
				slog.Warn("partition manager: drop failed", "partition", name, "err", err)
				continue
			}
			slog.Info("dropped expired partition", "partition", name)
			dropped++
		}
	}
	return dropped, nil
}

// monthStart returns the first instant of the month `offset` months from now.
func monthStart(now time.Time, offset int) time.Time {
	return time.Date(now.Year(), now.Month()+time.Month(offset), 1, 0, 0, 0, 0, time.UTC)
}

// parsePartitionEnd extracts the exclusive end (start of the following month)
// from a `<parent>_YYYY_MM` partition name. Returns ok=false for names that
// don't match the monthly convention.
func parsePartitionEnd(name, parent string) (time.Time, bool) {
	suffix := strings.TrimPrefix(name, parent+"_")
	if suffix == name {
		return time.Time{}, false
	}
	parts := strings.Split(suffix, "_")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	year, err1 := strconv.Atoi(parts[0])
	month, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || month < 1 || month > 12 {
		return time.Time{}, false
	}
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	return start.AddDate(0, 1, 0), true // exclusive end = first of next month
}

// pgQuoteIdent double-quotes a Postgres identifier (the partition/parent names
// are internal constants + derived suffixes, but quote them defensively).
func pgQuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
