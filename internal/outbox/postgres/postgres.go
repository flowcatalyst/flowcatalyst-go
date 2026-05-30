// Package postgres is the Postgres-backed outbox repository. It is
// schema-compatible with the SDK customer outbox migration
// (clients/*/migrations/postgresql/001_create_outbox_messages.sql) and the
// Rust/Java outbox-processor, so the Go processor can be pointed at a real
// customer database created by the SDK.
//
// Schema (created by InitSchema; matches the SDK migration exactly):
//
//	CREATE TABLE outbox_messages (
//	    id            VARCHAR(26) PRIMARY KEY,
//	    type          VARCHAR(20) NOT NULL,
//	    message_group VARCHAR(255),
//	    payload       TEXT NOT NULL,
//	    status        SMALLINT NOT NULL DEFAULT 0,
//	    retry_count   SMALLINT NOT NULL DEFAULT 0,
//	    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    error_message TEXT,
//	    client_id     VARCHAR(26),     -- SDK-owned; processor reads around it
//	    payload_size  INTEGER,         -- SDK-owned
//	    headers       JSONB            -- SDK-owned
//	);
//
// status 0 = PENDING, 9 = IN_PROGRESS (claimed). The processor claims
// PENDING rows (status=0), DELETEs them on success, and on failure bumps
// retry_count + records error_message (retryable -> back to PENDING).
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/outbox"
)

// Repository is the Postgres outbox repository.
type Repository struct {
	pool *pgxpool.Pool
}

// New wires a repository against an existing pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// InitSchema creates the outbox table and indexes if missing.
func (r *Repository) InitSchema(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS outbox_messages (
    id            VARCHAR(26) PRIMARY KEY,
    type          VARCHAR(20) NOT NULL,
    message_group VARCHAR(255),
    payload       TEXT NOT NULL,
    status        SMALLINT NOT NULL DEFAULT 0,
    retry_count   SMALLINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    error_message TEXT,
    client_id     VARCHAR(26),
    payload_size  INTEGER,
    headers       JSONB
);
CREATE INDEX IF NOT EXISTS idx_outbox_messages_pending
    ON outbox_messages (status, message_group, created_at) WHERE status = 0;
CREATE INDEX IF NOT EXISTS idx_outbox_messages_stuck
    ON outbox_messages (status, created_at) WHERE status = 9;
CREATE INDEX IF NOT EXISTS idx_outbox_client_pending
    ON outbox_messages (client_id, status, created_at);
`
	_, err := r.pool.Exec(ctx, ddl)
	return err
}

// ClaimPending claims a batch of pending items via FOR UPDATE SKIP LOCKED.
func (r *Repository) ClaimPending(ctx context.Context, batchSize int) ([]outbox.Item, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
WITH claimed AS (
  SELECT id FROM outbox_messages
   WHERE status = 0
   ORDER BY message_group, created_at
   LIMIT $1
   FOR UPDATE SKIP LOCKED
)
UPDATE outbox_messages m
   SET status = 9, updated_at = NOW()
  FROM claimed
 WHERE m.id = claimed.id
 RETURNING m.id, m.type, m.message_group, m.payload, m.status, m.retry_count,
           m.error_message, m.created_at, m.updated_at
`, batchSize)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	defer rows.Close()

	var out []outbox.Item
	for rows.Next() {
		var item outbox.Item
		var itemType string
		var msgGroup *string
		var payload []byte
		var statusInt int
		var errMsg *string
		if err := rows.Scan(&item.ID, &itemType, &msgGroup, &payload, &statusInt, &item.AttemptCount,
			&errMsg, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.ItemType = common.OutboxItemType(itemType)
		item.MessageGroup = msgGroup
		item.Payload = json.RawMessage(payload)
		item.Status = common.FromOutboxCode(statusInt)
		if errMsg != nil {
			item.StatusMessage = *errMsg
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// MarkSuccess deletes successfully dispatched rows (the upstream Java/Rust
// model DELETEs on success to keep the customer outbox table bounded).
func (r *Repository) MarkSuccess(ctx context.Context, ids []string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM outbox_messages WHERE id = ANY($1)`,
		ids)
	return err
}

// MarkFailed bumps retry_count, records error_message, and sets the status.
// Retryable statuses are returned to PENDING (0) so the next poll re-claims
// them (matching Rust increment_retry_count); terminal statuses keep their
// code so they are not re-claimed. There is no next_retry_at column upstream.
func (r *Repository) MarkFailed(ctx context.Context, ids []string, status common.OutboxStatus, msg string, requeue bool) error {
	newStatus := status.Code()
	if requeue {
		newStatus = int(common.OutboxPending)
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE outbox_messages
		    SET status = $1, error_message = $2, retry_count = retry_count + 1, updated_at = NOW()
		  WHERE id = ANY($3)`,
		newStatus, msg, ids)
	return err
}

// RecoverStuck resets IN_PROGRESS (9) rows older than olderThan back to
// PENDING (0) so a crash that left rows claimed-but-unresolved self-heals.
// Release returns claimed (IN_PROGRESS) rows to PENDING without a failure
// penalty (no retry bump / error). Used by block-on-error to re-run a group's
// undispatched items in order behind a failed one.
func (r *Repository) Release(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE outbox_messages SET status = 0, updated_at = NOW()
		  WHERE id = ANY($1) AND status = 9`, ids)
	return err
}

// Requeue resets rows to PENDING from ANY status, clearing retry_count + error
// for a fresh attempt (the state machine's Unblock-retry of a poison item).
func (r *Repository) Requeue(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE outbox_messages SET status = 0, retry_count = 0, error_message = NULL, updated_at = NOW()
		  WHERE id = ANY($1)`, ids)
	return err
}

func (r *Repository) RecoverStuck(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	tag, err := r.pool.Exec(ctx,
		`UPDATE outbox_messages SET status = 0, updated_at = NOW()
		  WHERE status = 9 AND updated_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Healthy pings the pool.
func (r *Repository) Healthy(ctx context.Context) bool {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return r.pool.Ping(c) == nil
}
