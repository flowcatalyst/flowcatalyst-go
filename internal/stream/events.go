package stream

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EventProjection denormalizes msg_events into msg_events_read.
//
// Unprojected events have `projected_at IS NULL`; on success we INSERT
// into the read model and stamp `projected_at = NOW()` atomically.
// Multiple replicas can run safely because the claim uses FOR UPDATE
// SKIP LOCKED. Mirrors crates/fc-stream/src/event_projection.rs.
type EventProjection struct {
	pool *pgxpool.Pool
}

// NewEventProjection wires the projection.
func NewEventProjection(pool *pgxpool.Pool) *EventProjection {
	return &EventProjection{pool: pool}
}

// Projector returns the configured Projector ready to Run.
func (p *EventProjection) Projector(cfg ProjectorConfig) *Projector {
	return &Projector{
		Name: "event_projection",
		Pool: p.pool,
		Cfg:  cfg,
		Step: p.step,
	}
}

func (p *EventProjection) step(ctx context.Context, batchSize int) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1) Claim a batch of unprojected events. `msg_events` is partitioned
	//    on (id, created_at) so the claim carries both columns.
	rows, err := tx.Query(ctx,
		`SELECT id, created_at FROM msg_events
		 WHERE projected_at IS NULL
		 ORDER BY created_at
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	type claim struct{ id string }
	var ids []string
	for rows.Next() {
		var c claim
		var createdAt any // we only need ids; created_at goes back via the JOIN
		if err := rows.Scan(&c.id, &createdAt); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, c.id)
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}

	// 2) Insert into msg_events_read. Application/subdomain/aggregate
	//    are derived from the event type ("application:subdomain:aggregate:verb").
	if _, err := tx.Exec(ctx,
		`INSERT INTO msg_events_read
		     (id, spec_version, type, source, subject, time, data,
		      correlation_id, causation_id, deduplication_id, message_group,
		      client_id, application, subdomain, aggregate, created_at, projected_at)
		 SELECT e.id, e.spec_version, e.type, e.source, e.subject, e.time, e.data::text,
		        e.correlation_id, e.causation_id, e.deduplication_id, e.message_group,
		        e.client_id,
		        split_part(e.type, ':', 1),
		        NULLIF(split_part(e.type, ':', 2), ''),
		        NULLIF(split_part(e.type, ':', 3), ''),
		        -- Preserve the SOURCE created_at (the (id, created_at) partition
		        -- key) so read rows land in the same time partition as their
		        -- source events and age out with them. Was defaulting to the
		        -- projection time (NOW()). Mirrors the Rust event projection.
		        e.created_at,
		        NOW()
		   FROM msg_events e
		  WHERE e.id = ANY($1)
		 ON CONFLICT (id, created_at) DO NOTHING`, ids); err != nil {
		return 0, fmt.Errorf("insert read: %w", err)
	}

	// 3) Stamp projected_at on the source rows.
	if _, err := tx.Exec(ctx,
		`UPDATE msg_events SET projected_at = NOW() WHERE id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("update projected_at: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(ids), nil
}
