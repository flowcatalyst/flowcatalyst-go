// Package outbox implements the outbox processor: it polls the
// consumer application's outbox table, batches by message group, and
// forwards to the FlowCatalyst platform API. Mirrors fc-outbox/src/*.
//
// Multi-backend: Postgres, SQLite, MySQL, MongoDB. The Repository
// interface abstracts the storage; each backend lives in its own
// subpackage and registers a factory at init time.
package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// Item is one outbox row, mapped from the SDK customer outbox_messages
// table (clients/*/migrations/.../001_create_outbox_messages.sql). The DB
// columns are: type, message_group, payload (TEXT), status, retry_count,
// error_message, created_at, updated_at — plus SDK-owned columns
// (client_id, payload_size, headers) the processor reads-around and never
// writes. AttemptCount maps to retry_count; StatusMessage to error_message.
type Item struct {
	ID            string                `json:"id"`
	ItemType      common.OutboxItemType `json:"itemType"`
	MessageGroup  *string               `json:"messageGroup,omitempty"`
	Payload       json.RawMessage       `json:"payload"`
	Status        common.OutboxStatus
	StatusMessage string    `json:"statusMessage,omitempty"`
	AttemptCount  int       `json:"attemptCount"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Repository is the per-backend storage interface.
type Repository interface {
	// ClaimPending claims up to batchSize PENDING items, marks them IN_PROGRESS,
	// and returns them. Each backend implements this with a backend-appropriate
	// claim semantic (FOR UPDATE SKIP LOCKED for SQL, findAndUpdate for Mongo).
	ClaimPending(ctx context.Context, batchSize int) ([]Item, error)
	// MarkSuccess removes the items: the upstream model DELETEs successfully
	// dispatched rows (matches Rust/Java) to keep the customer table bounded.
	MarkSuccess(ctx context.Context, ids []string) error
	// MarkFailed records the failure: it bumps retry_count, stores the
	// error_message, and sets the status. When requeue is true the row is
	// returned to PENDING so the next poll re-claims it; when false it keeps
	// the failure status code so it is NOT re-claimed (a terminal failure or
	// an exhausted-retries item). The caller (processor) decides requeue from
	// the status' retryability AND the max-retries cap. There is no
	// next-retry-at backoff column upstream.
	MarkFailed(ctx context.Context, ids []string, status common.OutboxStatus, msg string, requeue bool) error
	// Release returns the given claimed rows to PENDING WITHOUT a failure
	// penalty (no retry_count bump, no error_message), for the next poll to
	// re-claim. Used by block-on-error: when a group's item fails, the rest of
	// that group's already-claimed items are released un-dispatched so they
	// re-run in order behind the failed item rather than being delivered ahead
	// of it. Only affects rows still IN_PROGRESS.
	Release(ctx context.Context, ids []string) error
	// Requeue resets the given rows to PENDING regardless of their current
	// status, clearing retry_count + error, for a fresh attempt. Used by the
	// operational state machine's Unblock control to retry a poison item that
	// had blocked its message group.
	Requeue(ctx context.Context, ids []string) error
	// RecoverStuck resets rows stuck in IN_PROGRESS (claimed but never
	// resolved — e.g. the processor crashed mid-dispatch) whose updated_at is
	// older than olderThan, returning them to PENDING for re-claim. Returns
	// the number recovered. Mirrors the Rust recovery loop.
	RecoverStuck(ctx context.Context, olderThan time.Duration) (int, error)
	// Healthy reports whether the backend can be reached.
	Healthy(ctx context.Context) bool
	// InitSchema ensures the outbox table/collection exists.
	InitSchema(ctx context.Context) error
}
