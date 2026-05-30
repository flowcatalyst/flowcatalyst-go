package scheduledjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/sqlc/dbq"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// Repository is the Postgres-backed repo. Table: msg_scheduled_jobs.
type Repository struct {
	pool *pgxpool.Pool // retained for FindWithFilters
	q    *dbq.Queries
}

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, q: dbq.New(pool)}
}

// FindByID loads by id.
func (r *Repository) FindByID(ctx context.Context, id string) (*ScheduledJob, error) {
	row, err := r.q.ScheduledJobFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scheduled_job repo: %w", err)
	}
	return rowToScheduledJob(row), nil
}

// FindByCode loads by (code, client_id). clientID may be nil for platform-scoped.
func (r *Repository) FindByCode(ctx context.Context, code string, clientID *string) (*ScheduledJob, error) {
	var (
		row dbq.MsgScheduledJob
		err error
	)
	if clientID != nil {
		row, err = r.q.ScheduledJobFindByCodeClient(ctx, dbq.ScheduledJobFindByCodeClientParams{
			Code: code, ClientID: clientID,
		})
	} else {
		row, err = r.q.ScheduledJobFindByCodePlatform(ctx, code)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scheduled_job repo: %w", err)
	}
	return rowToScheduledJob(row), nil
}

// ListFilters drives FindWithFilters / CountWithFilters. AND semantics
// across non-nil fields.
//
//   - ClientID: a non-nil pointer with non-empty *string matches that
//     client; a non-nil pointer to "" matches platform-scoped jobs
//     (client_id IS NULL). A nil pointer means "don't filter on
//     client_id".
//   - Search: case-insensitive prefix match against code OR name.
//   - AccessibleClientIDs: a non-nil pointer scopes results to
//     platform-scoped jobs (client_id IS NULL) plus jobs whose client_id is
//     in the set; a nil pointer means "no access scoping" (anchor/all-access).
//   - Limit / Offset: applied only by List; Count ignores them.
type ListFilters struct {
	ClientID            *string
	Status              *string
	Search              *string
	AccessibleClientIDs *[]string
	Limit               *int64
	Offset              *int64
}

// FindWithFilters returns jobs matching non-nil filters, ordered by
// code, with optional pagination. Hand-rolled dynamic query (mirrors
// the application repo pattern).
func (r *Repository) FindWithFilters(ctx context.Context, f ListFilters) ([]ScheduledJob, error) {
	q, args := buildJobQuery(`SELECT id, client_id, code, name, description, status, crons, timezone,
		payload, concurrent, tracks_completion, timeout_seconds,
		delivery_max_attempts, target_url, last_fired_at, created_at, updated_at,
		created_by, updated_by, version FROM msg_scheduled_jobs`, f, true)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledJob
	for rows.Next() {
		var row dbq.MsgScheduledJob
		if err := rows.Scan(
			&row.ID, &row.ClientID, &row.Code, &row.Name, &row.Description, &row.Status,
			&row.Crons, &row.Timezone, &row.Payload, &row.Concurrent, &row.TracksCompletion,
			&row.TimeoutSeconds, &row.DeliveryMaxAttempts, &row.TargetUrl, &row.LastFiredAt,
			&row.CreatedAt, &row.UpdatedAt, &row.CreatedBy, &row.UpdatedBy, &row.Version,
		); err != nil {
			return nil, err
		}
		out = append(out, *rowToScheduledJob(row))
	}
	return out, rows.Err()
}

// CountWithFilters returns the total job count for the filters,
// ignoring Limit/Offset so callers can render pagination totals.
func (r *Repository) CountWithFilters(ctx context.Context, f ListFilters) (int64, error) {
	q, args := buildJobQuery(`SELECT COUNT(*) FROM msg_scheduled_jobs`, f, false)
	var count int64
	if err := r.pool.QueryRow(ctx, q, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("scheduled_job count: %w", err)
	}
	return count, nil
}

func buildJobQuery(base string, f ListFilters, withPagination bool) (string, []any) {
	q := base
	args := []any{}
	conds := []string{}
	if f.ClientID != nil {
		if *f.ClientID == "" {
			conds = append(conds, "client_id IS NULL")
		} else {
			args = append(args, *f.ClientID)
			conds = append(conds, fmt.Sprintf("client_id = $%d", len(args)))
		}
	}
	if f.Status != nil {
		args = append(args, *f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.Search != nil && *f.Search != "" {
		args = append(args, "%"+*f.Search+"%")
		conds = append(conds, fmt.Sprintf("(code ILIKE $%d OR name ILIKE $%d)", len(args), len(args)))
	}
	if f.AccessibleClientIDs != nil {
		args = append(args, *f.AccessibleClientIDs)
		conds = append(conds, fmt.Sprintf("(client_id IS NULL OR client_id = ANY($%d))", len(args)))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	if withPagination {
		q += " ORDER BY code"
		if f.Limit != nil {
			args = append(args, *f.Limit)
			q += fmt.Sprintf(" LIMIT $%d", len(args))
		}
		if f.Offset != nil {
			args = append(args, *f.Offset)
			q += fmt.Sprintf(" OFFSET $%d", len(args))
		}
	}
	return q, args
}

// FindActive lists ACTIVE jobs; used by the scheduler poller.
func (r *Repository) FindActive(ctx context.Context) ([]ScheduledJob, error) {
	rows, err := r.q.ScheduledJobFindActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ScheduledJob, 0, len(rows))
	for _, row := range rows {
		out = append(out, *rowToScheduledJob(row))
	}
	return out, nil
}

// MarkFired advances last_fired_at to slot, monotonically — it never moves
// the timestamp backwards (GREATEST), so a slow/duplicate poll can't cause a
// re-fire of an already-fired window. Mirrors the Rust mark_fired
// (GREATEST(last_fired_at, $2)); last_fired_at is bookkeeping, so version is
// intentionally not bumped.
func (r *Repository) MarkFired(ctx context.Context, id string, slot time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE msg_scheduled_jobs SET last_fired_at = GREATEST(last_fired_at, $2) WHERE id = $1`,
		id, slot)
	if err != nil {
		return fmt.Errorf("scheduled_job mark_fired: %w", err)
	}
	return nil
}

// Persist implements usecasepgx.Persist[ScheduledJob].
func (r *Repository) Persist(ctx context.Context, j *ScheduledJob, tx *usecasepgx.DbTx) error {
	var payloadBytes []byte
	if len(j.Payload) > 0 {
		payloadBytes = []byte(j.Payload)
	}
	return r.q.WithTx(tx.Inner()).ScheduledJobUpsert(ctx, dbq.ScheduledJobUpsertParams{
		ID:                  j.ID,
		ClientID:            j.ClientID,
		Code:                j.Code,
		Name:                j.Name,
		Description:         j.Description,
		Status:              string(j.Status),
		Crons:               j.Crons,
		Timezone:            j.Timezone,
		Payload:             payloadBytes,
		Concurrent:          j.Concurrent,
		TracksCompletion:    j.TracksCompletion,
		TimeoutSeconds:      j.TimeoutSeconds,
		DeliveryMaxAttempts: j.DeliveryMaxAttempts,
		TargetUrl:           j.TargetURL,
		LastFiredAt:         j.LastFiredAt,
		CreatedAt:           j.CreatedAt,
		UpdatedAt:           time.Now().UTC(),
		CreatedBy:           j.CreatedBy,
		UpdatedBy:           j.UpdatedBy,
		Version:             j.Version,
	})
}

// Delete removes the row.
func (r *Repository) Delete(ctx context.Context, j *ScheduledJob, tx *usecasepgx.DbTx) error {
	return r.q.WithTx(tx.Inner()).ScheduledJobDelete(ctx, j.ID)
}

func rowToScheduledJob(row dbq.MsgScheduledJob) *ScheduledJob {
	j := ScheduledJob{
		ID:                  row.ID,
		ClientID:            row.ClientID,
		Code:                row.Code,
		Name:                row.Name,
		Description:         row.Description,
		Status:              ParseStatus(row.Status),
		Crons:               row.Crons,
		Timezone:            row.Timezone,
		Concurrent:          row.Concurrent,
		TracksCompletion:    row.TracksCompletion,
		TimeoutSeconds:      row.TimeoutSeconds,
		DeliveryMaxAttempts: row.DeliveryMaxAttempts,
		TargetURL:           row.TargetUrl,
		LastFiredAt:         row.LastFiredAt,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		CreatedBy:           row.CreatedBy,
		UpdatedBy:           row.UpdatedBy,
		Version:             row.Version,
	}
	if j.Crons == nil {
		j.Crons = []string{}
	}
	if len(row.Payload) > 0 {
		j.Payload = json.RawMessage(row.Payload)
	}
	return &j
}
