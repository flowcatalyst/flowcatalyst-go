// Package mongo is the MongoDB-backed outbox repository. It is
// schema-compatible with the SDK customer outbox (a single shared
// `outbox_messages` collection discriminated by a `type` field) and with the
// Rust fc-outbox MongoOutboxRepository, so the Go processor can be pointed at
// a real customer MongoDB created by the SDK.
//
// Document shape (matches the SDK / Java / Rust representation EXACTLY — these
// are the wire types a drop-in must read/write):
//
//	{ id, type, message_group, payload (STRING json), status (INT code),
//	  retry_count (INT), error_message, created_at (RFC3339 STRING),
//	  updated_at (RFC3339 STRING), client_id, payload_size, headers (STRING) }
//
// status codes: 0 PENDING, 9 IN_PROGRESS (claimed), 1 SUCCESS, 2 BAD_REQUEST,
// 3 INTERNAL_ERROR, 4 UNAUTHORIZED, 5 FORBIDDEN, 6 GATEWAY_ERROR. The
// processor claims PENDING (0), DELETEs on success, and on failure bumps
// retry_count + records error_message (retryable -> back to PENDING).
//
// Unlike the SQL backends there is no FOR UPDATE SKIP LOCKED; the claim is a
// find-then-update (mirroring Rust). Run a single active instance (the
// fc-server outbox subsystem is leader-gated) to avoid double-claims.
package mongo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/outbox"
)

const collectionName = "outbox_messages"

// Repository is the MongoDB outbox repository.
type Repository struct {
	client *mongo.Client
	coll   *mongo.Collection
}

// New wires a repository against an existing client + database name.
func New(client *mongo.Client, dbName string) *Repository {
	return &Repository{
		client: client,
		coll:   client.Database(dbName).Collection(collectionName),
	}
}

// Connect dials the supplied URI and returns a repository. The caller owns
// the returned client's lifetime via Close.
func Connect(ctx context.Context, uri, dbName string) (*Repository, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	return New(client, dbName), nil
}

// Close disconnects the underlying client.
func (r *Repository) Close(ctx context.Context) error { return r.client.Disconnect(ctx) }

// doc is the stored representation. Timestamps + payload + headers are
// STRINGS and status is an INT code — matching the SDK/Rust schema.
type doc struct {
	ID           string  `bson:"id"`
	Type         string  `bson:"type"`
	MessageGroup *string `bson:"message_group,omitempty"`
	Payload      string  `bson:"payload"`
	Status       int32   `bson:"status"`
	RetryCount   int32   `bson:"retry_count"`
	ErrorMessage *string `bson:"error_message,omitempty"`
	CreatedAt    string  `bson:"created_at"`
	UpdatedAt    string  `bson:"updated_at"`
}

func (d doc) toItem() outbox.Item {
	created, _ := time.Parse(time.RFC3339, d.CreatedAt)
	updated, _ := time.Parse(time.RFC3339, d.UpdatedAt)
	item := outbox.Item{
		ID:           d.ID,
		ItemType:     common.OutboxItemType(d.Type),
		MessageGroup: d.MessageGroup,
		Payload:      json.RawMessage(d.Payload),
		Status:       common.FromOutboxCode(int(d.Status)),
		AttemptCount: int(d.RetryCount),
		CreatedAt:    created,
		UpdatedAt:    updated,
	}
	if d.ErrorMessage != nil {
		item.StatusMessage = *d.ErrorMessage
	}
	return item
}

// InitSchema creates the indexes (idempotent). Mirrors the Rust init_schema.
func (r *Repository) InitSchema(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "status", Value: 1}, {Key: "type", Value: 1}, {Key: "message_group", Value: 1}, {Key: "created_at", Value: 1}},
			Options: options.Index().SetName("idx_pending"),
		},
		{
			Keys:    bson.D{{Key: "status", Value: 1}, {Key: "type", Value: 1}, {Key: "created_at", Value: 1}},
			Options: options.Index().SetName("idx_stuck"),
		},
		{
			Keys:    bson.D{{Key: "client_id", Value: 1}, {Key: "status", Value: 1}, {Key: "created_at", Value: 1}},
			Options: options.Index().SetName("idx_client_pending"),
		},
	})
	if err != nil {
		return fmt.Errorf("mongo create indexes: %w", err)
	}
	return nil
}

// ClaimPending finds up to batchSize PENDING docs (ordered by message_group +
// created_at, like the SQL backends) and flips them to IN_PROGRESS. Mongo has
// no atomic batch claim, so this is a find-then-update (mirrors Rust); the
// fc-server outbox subsystem is leader-gated to keep it single-active.
func (r *Repository) ClaimPending(ctx context.Context, batchSize int) ([]outbox.Item, error) {
	cur, err := r.coll.Find(ctx,
		bson.M{"status": int(common.OutboxPending)},
		options.Find().
			SetSort(bson.D{{Key: "message_group", Value: 1}, {Key: "created_at", Value: 1}}).
			SetLimit(int64(batchSize)))
	if err != nil {
		return nil, fmt.Errorf("mongo find pending: %w", err)
	}
	defer cur.Close(ctx)

	var items []outbox.Item
	var ids []string
	for cur.Next(ctx) {
		var d doc
		if err := cur.Decode(&d); err != nil {
			return nil, fmt.Errorf("mongo decode: %w", err)
		}
		items = append(items, d.toItem())
		ids = append(ids, d.ID)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := r.coll.UpdateMany(ctx,
		bson.M{"id": bson.M{"$in": ids}},
		bson.M{"$set": bson.M{"status": int(common.OutboxInProgress), "updated_at": nowISO()}}); err != nil {
		return nil, fmt.Errorf("mongo mark in_progress: %w", err)
	}
	return items, nil
}

// MarkSuccess deletes successfully dispatched docs (SUCCESS is terminal; the
// platform now owns the message, so the customer collection stays bounded).
func (r *Repository) MarkSuccess(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.coll.DeleteMany(ctx, bson.M{"id": bson.M{"$in": ids}})
	return err
}

// MarkFailed bumps retry_count, records error_message, and sets the status.
// Retryable statuses are returned to PENDING (0) so the next poll re-claims
// them; terminal statuses keep their code. Mirrors the SQL backends.
func (r *Repository) MarkFailed(ctx context.Context, ids []string, status common.OutboxStatus, msg string, requeue bool) error {
	if len(ids) == 0 {
		return nil
	}
	newStatus := status.Code()
	if requeue {
		newStatus = int(common.OutboxPending)
	}
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"id": bson.M{"$in": ids}},
		bson.M{
			"$set": bson.M{"status": newStatus, "error_message": msg, "updated_at": nowISO()},
			"$inc": bson.M{"retry_count": 1},
		})
	return err
}

// RecoverStuck resets IN_PROGRESS docs older than olderThan back to PENDING.
// updated_at is an RFC3339 string, so the cutoff is compared lexically (RFC3339
// is lexicographically ordered for a fixed offset — the SDK writes UTC "Z").
// Release returns claimed (IN_PROGRESS) docs to PENDING without a failure
// penalty. Used by block-on-error to re-run a group's undispatched items in
// order behind a failed one.
func (r *Repository) Release(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"id": bson.M{"$in": ids}, "status": int(common.OutboxInProgress)},
		bson.M{"$set": bson.M{"status": int(common.OutboxPending), "updated_at": nowISO()}})
	return err
}

func (r *Repository) RecoverStuck(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)
	res, err := r.coll.UpdateMany(ctx,
		bson.M{"status": int(common.OutboxInProgress), "updated_at": bson.M{"$lt": cutoff}},
		bson.M{"$set": bson.M{"status": int(common.OutboxPending), "updated_at": nowISO()}})
	if err != nil {
		return 0, err
	}
	return int(res.ModifiedCount), nil
}

// Healthy pings the server.
func (r *Repository) Healthy(ctx context.Context) bool {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return r.client.Ping(c, nil) == nil
}

// nowISO is the RFC3339 string form the SDK/Rust write for created_at /
// updated_at, kept consistent so cross-runtime reads parse cleanly.
func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

var _ outbox.Repository = (*Repository)(nil)
