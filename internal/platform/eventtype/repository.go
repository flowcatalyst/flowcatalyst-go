package eventtype

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/repocommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/sqlc/dbq"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// Repository is the Postgres-backed event type repo. SQL ported
// verbatim from fc-platform/src/event_type/repository.rs.
type Repository struct {
	pool *pgxpool.Pool // retained for FindWithFilters
	q    *dbq.Queries
}

// NewRepository wires a repository against an existing pgx pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, q: dbq.New(pool)}
}

// FindByID loads an event type with its schema versions.
func (r *Repository) FindByID(ctx context.Context, id string) (*EventType, error) {
	res, err := r.q.EventTypeFindByID(ctx, id)
	row, err := repocommon.One(res, err, "event_types FindByID")
	if row == nil || err != nil {
		return nil, err
	}
	return r.hydrateOne(ctx, rowToEventType(*row))
}

// FindByCode loads an event type by its unique code.
func (r *Repository) FindByCode(ctx context.Context, code string) (*EventType, error) {
	res, err := r.q.EventTypeFindByCode(ctx, code)
	row, err := repocommon.One(res, err, "event_types FindByCode")
	if row == nil || err != nil {
		return nil, err
	}
	return r.hydrateOne(ctx, rowToEventType(*row))
}

// FindByApplication lists every event type whose first code segment
// matches the supplied application code.
func (r *Repository) FindByApplication(ctx context.Context, applicationCode string) ([]EventType, error) {
	rows, err := r.q.EventTypeFindByApplication(ctx, applicationCode)
	if err != nil {
		return nil, fmt.Errorf("event_types FindByApplication: %w", err)
	}
	bare := make([]EventType, 0, len(rows))
	for _, row := range rows {
		bare = append(bare, *rowToEventType(row))
	}
	return r.hydrateAll(ctx, bare)
}

// FindWithFilters returns event types matching the supplied filters.
// Hand-rolled dynamic query — see docs/sqlc.md.
func (r *Repository) FindWithFilters(
	ctx context.Context,
	application, clientID, status, subdomain, aggregate *string,
) ([]EventType, error) {
	var f repocommon.Filter
	f.EqPtr("application", application)
	f.EqPtr("status", status)
	f.EqPtr("subdomain", subdomain)
	f.EqPtr("aggregate", aggregate)
	_ = clientID // not a column on msg_event_types

	q := `SELECT id, code, name, description, status, source, client_scoped,
		         application, subdomain, aggregate, created_by, created_at, updated_at
		  FROM msg_event_types` + f.Where() + " ORDER BY code ASC"

	rows, err := r.pool.Query(ctx, q, f.Args()...)
	if err != nil {
		return nil, fmt.Errorf("event_types FindWithFilters: %w", err)
	}
	collected, err := pgx.CollectRows(rows, pgx.RowToStructByName[dbq.MsgEventType])
	if err != nil {
		return nil, err
	}
	var bare []EventType
	for _, row := range collected {
		bare = append(bare, *rowToEventType(row))
	}
	return r.hydrateAll(ctx, bare)
}

// Pool exposes the underlying pgxpool so use cases that need an
// orchestrated transaction (e.g. sync) can run multiple writes atomically.
func (r *Repository) Pool() *pgxpool.Pool { return r.pool }

// PersistTx persists into a caller-supplied pgx.Tx. Used by sync to
// roll the per-row writes into a single transaction. Conflict target is
// `code` so sync can match against the canonical-by-code identity.
func (r *Repository) PersistTx(ctx context.Context, et *EventType, tx pgx.Tx) error {
	p := eventTypeUpsertParams(et)
	return r.q.WithTx(tx).EventTypeUpsertByCode(ctx, dbq.EventTypeUpsertByCodeParams(p))
}

// DeleteTx deletes a row via the supplied tx.
func (r *Repository) DeleteTx(ctx context.Context, et *EventType, tx pgx.Tx) error {
	return r.q.WithTx(tx).EventTypeDelete(ctx, et.ID)
}

// Persist implements usecase.Persist[EventType] via UnitOfWork. Uses
// ON CONFLICT (id) — id-stable identity for the canonical write path.
func (r *Repository) Persist(ctx context.Context, et *EventType, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	if err := q.EventTypeUpsertByID(ctx, eventTypeUpsertParams(et)); err != nil {
		return fmt.Errorf("event_types persist: %w", err)
	}
	now := time.Now().UTC()
	for _, sv := range et.SpecVersions {
		if err := q.SpecVersionUpsert(ctx, dbq.SpecVersionUpsertParams{
			ID:            sv.ID,
			EventTypeID:   sv.EventTypeID,
			Version:       sv.Version,
			MimeType:      sv.MimeType,
			SchemaContent: specContentBytes(sv.SchemaContent),
			SchemaType:    string(sv.SchemaType),
			Status:        string(sv.Status),
			CreatedAt:     sv.CreatedAt,
			UpdatedAt:     now,
		}); err != nil {
			return fmt.Errorf("spec_version persist: %w", err)
		}
	}
	return nil
}

// Delete removes the event type and its spec versions.
func (r *Repository) Delete(ctx context.Context, et *EventType, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	if err := q.SpecVersionsClear(ctx, et.ID); err != nil {
		return fmt.Errorf("delete spec_versions: %w", err)
	}
	if err := q.EventTypeDelete(ctx, et.ID); err != nil {
		return fmt.Errorf("delete event_type: %w", err)
	}
	return nil
}

// ── private helpers ───────────────────────────────────────────────────────

func (r *Repository) hydrateOne(ctx context.Context, et *EventType) (*EventType, error) {
	byID, err := r.loadSpecVersions(ctx, []string{et.ID})
	if err != nil {
		return nil, err
	}
	et.SpecVersions = byID[et.ID]
	return et, nil
}

func (r *Repository) hydrateAll(ctx context.Context, ets []EventType) ([]EventType, error) {
	if len(ets) == 0 {
		return ets, nil
	}
	ids := make([]string, len(ets))
	for i, e := range ets {
		ids[i] = e.ID
	}
	byID, err := r.loadSpecVersions(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range ets {
		ets[i].SpecVersions = byID[ets[i].ID]
	}
	return ets, nil
}

func (r *Repository) loadSpecVersions(ctx context.Context, eventTypeIDs []string) (map[string][]SpecVersion, error) {
	rows, err := r.q.SpecVersionsForEventTypes(ctx, eventTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("load_spec_versions: %w", err)
	}
	out := make(map[string][]SpecVersion)
	for _, row := range rows {
		sv := SpecVersion{
			ID:          row.ID,
			EventTypeID: row.EventTypeID,
			Version:     row.Version,
			MimeType:    row.MimeType,
			SchemaType:  ParseSchemaType(row.SchemaType),
			Status:      ParseSpecVersionStatus(row.Status),
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		}
		if len(row.SchemaContent) > 0 {
			sv.SchemaContent = json.RawMessage(row.SchemaContent)
		}
		out[sv.EventTypeID] = append(out[sv.EventTypeID], sv)
	}
	return out, nil
}

func rowToEventType(row dbq.MsgEventType) *EventType {
	et := &EventType{
		ID:           row.ID,
		Code:         row.Code,
		Name:         row.Name,
		Description:  row.Description,
		Status:       ParseStatus(row.Status),
		Source:       ParseSource(row.Source),
		ClientScoped: row.ClientScoped,
		Application:  row.Application,
		Subdomain:    row.Subdomain,
		Aggregate:    row.Aggregate,
		CreatedBy:    row.CreatedBy,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
	parts := strings.Split(et.Code, ":")
	if len(parts) == 4 {
		et.EventName = parts[3]
	}
	return et
}

// eventTypeUpsertParams is shared between the two upsert paths so they
// stay in lockstep with the entity shape.
func eventTypeUpsertParams(et *EventType) dbq.EventTypeUpsertByIDParams {
	return dbq.EventTypeUpsertByIDParams{
		ID:           et.ID,
		Code:         et.Code,
		Name:         et.Name,
		Description:  et.Description,
		Status:       string(et.Status),
		Source:       string(et.Source),
		ClientScoped: et.ClientScoped,
		Application:  et.Application,
		Subdomain:    et.Subdomain,
		Aggregate:    et.Aggregate,
		CreatedBy:    et.CreatedBy,
		CreatedAt:    et.CreatedAt,
		UpdatedAt:    time.Now().UTC(),
	}
}

func specContentBytes(rm json.RawMessage) []byte {
	if len(rm) == 0 {
		return nil
	}
	return []byte(rm)
}
