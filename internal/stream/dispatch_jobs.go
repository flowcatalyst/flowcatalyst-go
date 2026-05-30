package stream

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DispatchJobProjection denormalizes msg_dispatch_jobs into
// msg_dispatch_jobs_read. Mirrors
// crates/fc-stream/src/dispatch_job_projection.rs.
//
// Picks up jobs that are new (`projected_at IS NULL`) or updated since
// the last projection (`updated_at > projected_at`), upserts into the
// read model, and stamps `projected_at`. Application/subdomain/aggregate
// fields are derived from the dispatch job `code` (same
// `application:subdomain:aggregate:verb` shape as event types).
type DispatchJobProjection struct {
	pool *pgxpool.Pool
}

// NewDispatchJobProjection wires the projection.
func NewDispatchJobProjection(pool *pgxpool.Pool) *DispatchJobProjection {
	return &DispatchJobProjection{pool: pool}
}

// Projector returns the configured Projector ready to Run.
func (p *DispatchJobProjection) Projector(cfg ProjectorConfig) *Projector {
	return &Projector{
		Name: "dispatch_job_projection",
		Pool: p.pool,
		Cfg:  cfg,
		Step: p.step,
	}
}

func (p *DispatchJobProjection) step(ctx context.Context, batchSize int) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id FROM msg_dispatch_jobs
		 WHERE projected_at IS NULL OR updated_at > projected_at
		 ORDER BY created_at
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO msg_dispatch_jobs_read (
		     id, external_id, source, kind, code, subject, event_id, correlation_id,
		     target_url, protocol, service_account_id, client_id, subscription_id,
		     mode, dispatch_pool_id, message_group, sequence, timeout_seconds,
		     status, max_retries, retry_strategy, scheduled_for, expires_at,
		     attempt_count, last_attempt_at, completed_at, duration_millis, last_error,
		     idempotency_key, is_completed, is_terminal,
		     application, subdomain, aggregate,
		     created_at, updated_at, projected_at)
		 SELECT j.id, j.external_id, j.source, j.kind, j.code, j.subject,
		        j.event_id, j.correlation_id, j.target_url, j.protocol,
		        j.service_account_id, j.client_id, j.subscription_id,
		        j.mode, j.dispatch_pool_id, j.message_group,
		        j.sequence, j.timeout_seconds, j.status,
		        j.max_retries, j.retry_strategy,
		        j.scheduled_for, j.expires_at,
		        j.attempt_count, j.last_attempt_at, j.completed_at,
		        j.duration_millis, j.last_error, j.idempotency_key,
		        -- is_completed: the single success terminal (status == COMPLETED).
		        -- is_terminal: any non-retryable end state. Mirrors the Rust
		        -- DispatchStatus is_completed / is_terminal() split — the prior
		        -- code conflated them and used SUCCESS/IGNORED names that Go
		        -- never writes (Go uses COMPLETED; there is no IGNORED).
		        j.status = 'COMPLETED',
		        j.status IN ('COMPLETED', 'FAILED', 'CANCELLED', 'EXPIRED'),
		        split_part(j.code, ':', 1),
		        NULLIF(split_part(j.code, ':', 2), ''),
		        NULLIF(split_part(j.code, ':', 3), ''),
		        j.created_at, j.updated_at, NOW()
		   FROM msg_dispatch_jobs j
		  WHERE j.id = ANY($1)
		 ON CONFLICT (id, created_at) DO UPDATE SET
		     status = EXCLUDED.status,
		     attempt_count = EXCLUDED.attempt_count,
		     last_attempt_at = EXCLUDED.last_attempt_at,
		     completed_at = EXCLUDED.completed_at,
		     duration_millis = EXCLUDED.duration_millis,
		     last_error = EXCLUDED.last_error,
		     is_completed = EXCLUDED.is_completed,
		     is_terminal = EXCLUDED.is_terminal,
		     updated_at = EXCLUDED.updated_at,
		     projected_at = NOW()`, ids); err != nil {
		return 0, fmt.Errorf("insert read: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE msg_dispatch_jobs SET projected_at = NOW() WHERE id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("update projected_at: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(ids), nil
}
