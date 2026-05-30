// Package postgres is the Postgres-backed queue backend. It is wire- and
// schema-compatible with the Rust fc-queue Postgres backend so a Go router
// can be dropped into an existing deployment and drain the SAME
// queue_messages table that the existing producers write to (and vice
// versa).
//
// Schema (created by InitSchema; matches crates/fc-queue/src/postgres.rs):
//
//	CREATE TABLE queue_messages (
//	    id               TEXT NOT NULL,
//	    queue_name       TEXT NOT NULL,
//	    message_group_id TEXT,
//	    receipt_handle   TEXT,
//	    visible_at       BIGINT NOT NULL,   -- unix epoch seconds
//	    payload          TEXT NOT NULL,     -- JSON-encoded common.Message
//	    created_at       BIGINT NOT NULL,   -- unix epoch seconds
//	    receive_count    INTEGER DEFAULT 0,
//	    PRIMARY KEY (queue_name, id)
//	);
//	CREATE INDEX idx_queue_visible
//	    ON queue_messages (queue_name, visible_at, message_group_id);
//
// Semantics (mirrors Rust):
//   - Claim is keyed on visible_at, NOT on receipt_handle being NULL. A
//     claimed message becomes eligible again once its visibility window
//     lapses, so a crashed consumer's messages are redelivered (at-least-once)
//     without an explicit NACK.
//   - FIFO per message group: only the earliest visible message of each
//     group (COALESCE(message_group_id, id)) is eligible at a time, so a
//     NULL group behaves as a singleton keyed by the message id.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

func init() {
	queue.RegisterConsumer("postgres", consumerFactory)
	queue.RegisterPublisher("postgres", publisherFactory)
}

func consumerFactory(ctx context.Context, cfg common.QueueConfig) (queue.Consumer, error) {
	pool, err := pgxpool.New(ctx, cfg.URI)
	if err != nil {
		return nil, fmt.Errorf("pgxpool: %w", err)
	}
	return &Queue{pool: pool, cfg: cfg}, nil
}

func publisherFactory(ctx context.Context, cfg common.QueueConfig) (queue.Publisher, error) {
	pool, err := pgxpool.New(ctx, cfg.URI)
	if err != nil {
		return nil, fmt.Errorf("pgxpool: %w", err)
	}
	return &Queue{pool: pool, cfg: cfg}, nil
}

// Queue is the Postgres-backed queue (both consumer + publisher).
type Queue struct {
	pool *pgxpool.Pool
	cfg  common.QueueConfig

	polled   atomic.Uint64
	acked    atomic.Uint64
	nacked   atomic.Uint64
	deferred atomic.Uint64
}

// Identifier returns the queue name.
func (q *Queue) Identifier() string { return q.cfg.Name }

// InitSchema creates the queue table and index (idempotent). The DDL
// matches the Rust backend exactly so it is a no-op when the table was
// already provisioned by the existing system.
func (q *Queue) InitSchema(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS queue_messages (
    id               TEXT NOT NULL,
    queue_name       TEXT NOT NULL,
    message_group_id TEXT,
    receipt_handle   TEXT,
    visible_at       BIGINT NOT NULL,
    payload          TEXT NOT NULL,
    created_at       BIGINT NOT NULL,
    receive_count    INTEGER DEFAULT 0,
    PRIMARY KEY (queue_name, id)
);
CREATE INDEX IF NOT EXISTS idx_queue_visible
    ON queue_messages (queue_name, visible_at, message_group_id);
`
	_, err := q.pool.Exec(ctx, ddl)
	return err
}

// Poll claims up to maxMessages eligible messages from this queue.
//
// Eligibility = visible_at <= now AND the message is the earliest visible
// message in its group (COALESCE(message_group_id, id)). Each claimed row
// gets a unique receipt handle (<pollUUID>:<id>) and its visibility window
// is pushed out by the configured timeout. We use a correlated NOT EXISTS
// rather than a windowed CTE because FOR UPDATE SKIP LOCKED cannot be
// applied over a ROW_NUMBER() result under Postgres CTE inlining; the
// resulting claim set is equivalent.
func (q *Queue) Poll(ctx context.Context, maxMessages uint32) ([]common.QueuedMessage, error) {
	visibility := time.Duration(q.cfg.VisibilityTimeout) * time.Second
	if visibility <= 0 {
		visibility = 30 * time.Second
	}
	now := time.Now().Unix()
	newVisibleAt := now + int64(visibility.Seconds())
	receipt := uuid.NewString()

	const sql = `
WITH claimed AS (
  SELECT m.id
    FROM queue_messages m
   WHERE m.queue_name = $1
     AND m.visible_at <= $2
     AND NOT EXISTS (
           SELECT 1 FROM queue_messages e
            WHERE e.queue_name = m.queue_name
              AND COALESCE(e.message_group_id, e.id) = COALESCE(m.message_group_id, m.id)
              AND e.visible_at <= $2
              AND (e.created_at < m.created_at
                   OR (e.created_at = m.created_at AND e.id < m.id))
         )
   ORDER BY m.created_at, m.id
   LIMIT $3
   FOR UPDATE SKIP LOCKED
)
UPDATE queue_messages t
   SET receipt_handle = $4 || ':' || t.id,
       visible_at     = $5,
       receive_count  = t.receive_count + 1
  FROM claimed
 WHERE t.queue_name = $1
   AND t.id = claimed.id
 RETURNING t.id, t.payload
`
	rows, err := q.pool.Query(ctx, sql, q.cfg.Name, now, int64(maxMessages), receipt, newVisibleAt)
	if err != nil {
		return nil, fmt.Errorf("postgres queue poll: %w", err)
	}
	defer rows.Close()

	var msgs []common.QueuedMessage
	for rows.Next() {
		var id string
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, err
		}
		var m common.Message
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			return nil, fmt.Errorf("unmarshal message %s: %w", id, err)
		}
		msgs = append(msgs, common.QueuedMessage{
			Message:         m,
			ReceiptHandle:   receipt + ":" + id,
			BrokerMessageID: id,
			QueueIdentifier: q.cfg.Name,
		})
	}
	q.polled.Add(uint64(len(msgs)))
	return msgs, rows.Err()
}

// Ack deletes the message permanently.
func (q *Queue) Ack(ctx context.Context, receipt string) error {
	tag, err := q.pool.Exec(ctx,
		`DELETE FROM queue_messages WHERE receipt_handle = $1 AND queue_name = $2`,
		receipt, q.cfg.Name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ack: %w", errNotFound(receipt))
	}
	q.acked.Add(1)
	return nil
}

// Nack restores visibility after delay; counted as a failure.
func (q *Queue) Nack(ctx context.Context, receipt string, delaySeconds *uint32) error {
	if err := q.makeVisible(ctx, receipt, delaySeconds); err != nil {
		return err
	}
	q.nacked.Add(1)
	return nil
}

// Defer restores visibility after delay; not counted as a failure.
func (q *Queue) Defer(ctx context.Context, receipt string, delaySeconds *uint32) error {
	if err := q.makeVisible(ctx, receipt, delaySeconds); err != nil {
		return err
	}
	q.deferred.Add(1)
	return nil
}

// ExtendVisibility prolongs the visibility window without releasing.
func (q *Queue) ExtendVisibility(ctx context.Context, receipt string, seconds uint32) error {
	newVisibleAt := time.Now().Unix() + int64(seconds)
	_, err := q.pool.Exec(ctx,
		`UPDATE queue_messages SET visible_at = $1 WHERE receipt_handle = $2 AND queue_name = $3`,
		newVisibleAt, receipt, q.cfg.Name)
	return err
}

// Publish writes a single message. Uses ON CONFLICT DO NOTHING so a
// duplicate id is a no-op (matches Rust at-least-once publish semantics).
func (q *Queue) Publish(ctx context.Context, m common.Message) (string, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	_, err = q.pool.Exec(ctx,
		`INSERT INTO queue_messages
		     (id, queue_name, message_group_id, visible_at, payload, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (queue_name, id) DO NOTHING`,
		m.ID, q.cfg.Name, m.MessageGroupID, now, string(payload), now)
	return m.ID, err
}

// PublishBatch writes a batch of messages (loops Publish, matching Rust).
func (q *Queue) PublishBatch(ctx context.Context, msgs []common.Message) ([]string, error) {
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		id, err := q.Publish(ctx, m)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Healthy reports whether we can talk to Postgres.
func (q *Queue) Healthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return q.pool.Ping(ctx) == nil
}

// Stop closes the connection pool.
func (q *Queue) Stop() { q.pool.Close() }

// Metrics returns broker-side metrics. Reads counts from the table.
func (q *Queue) Metrics(ctx context.Context) (*queue.Metrics, error) {
	now := time.Now().Unix()
	var pending, inflight uint64
	err := q.pool.QueryRow(ctx,
		`SELECT
		   COUNT(*) FILTER (WHERE receipt_handle IS NULL AND visible_at <= $2),
		   COUNT(*) FILTER (WHERE receipt_handle IS NOT NULL)
		 FROM queue_messages WHERE queue_name = $1`,
		q.cfg.Name, now,
	).Scan(&pending, &inflight)
	if err != nil {
		return nil, err
	}
	return &queue.Metrics{
		QueueIdentifier:  q.cfg.Name,
		PendingMessages:  pending,
		InFlightMessages: inflight,
		TotalPolled:      q.polled.Load(),
		TotalAcked:       q.acked.Load(),
		TotalNacked:      q.nacked.Load(),
		TotalDeferred:    q.deferred.Load(),
	}, nil
}

// Counters returns process-local counters only.
func (q *Queue) Counters() *queue.Metrics {
	return &queue.Metrics{
		QueueIdentifier: q.cfg.Name,
		TotalPolled:     q.polled.Load(),
		TotalAcked:      q.acked.Load(),
		TotalNacked:     q.nacked.Load(),
		TotalDeferred:   q.deferred.Load(),
	}
}

func (q *Queue) makeVisible(ctx context.Context, receipt string, delaySeconds *uint32) error {
	delay := int64(0)
	if delaySeconds != nil {
		delay = int64(*delaySeconds)
	}
	newVisibleAt := time.Now().Unix() + delay
	_, err := q.pool.Exec(ctx,
		`UPDATE queue_messages
		    SET receipt_handle = NULL,
		        visible_at = $1
		  WHERE receipt_handle = $2 AND queue_name = $3`,
		newVisibleAt, receipt, q.cfg.Name)
	return err
}

func errNotFound(receipt string) error {
	return fmt.Errorf("receipt handle not found: %s", receipt)
}
