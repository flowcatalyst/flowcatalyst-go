package subscription

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/repocommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/sqlc/dbq"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// Repository is the Postgres-backed repository. Tables: msg_subscriptions
// + msg_subscription_event_types + msg_subscription_custom_configs.
// EventTypeBinding.Filter is in-memory only — there's no column for it.
type Repository struct {
	pool *pgxpool.Pool // retained for FindWithFilters
	q    *dbq.Queries
}

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, q: dbq.New(pool)}
}

// FindByID loads a subscription with hydrated junction tables.
func (r *Repository) FindByID(ctx context.Context, id string) (*Subscription, error) {
	res, err := r.q.SubscriptionFindByID(ctx, id)
	row, err := repocommon.One(res, err, "subscription repo")
	if row == nil || err != nil {
		return nil, err
	}
	return r.hydrateOne(ctx, rowToSubscription(*row))
}

// FindByCode loads by (code, client_id).
func (r *Repository) FindByCode(ctx context.Context, code string, clientID *string) (*Subscription, error) {
	var (
		res dbq.MsgSubscription
		err error
	)
	if clientID != nil {
		res, err = r.q.SubscriptionFindByCodeClient(ctx, dbq.SubscriptionFindByCodeClientParams{
			Code: code, ClientID: clientID,
		})
	} else {
		res, err = r.q.SubscriptionFindByCodeAnchor(ctx, code)
	}
	row, err := repocommon.One(res, err, "subscription repo")
	if row == nil || err != nil {
		return nil, err
	}
	return r.hydrateOne(ctx, rowToSubscription(*row))
}

// FindAll returns every subscription, hydrated.
func (r *Repository) FindAll(ctx context.Context) ([]Subscription, error) {
	rows, err := r.q.SubscriptionFindAll(ctx)
	if err != nil {
		return nil, err
	}
	bare := make([]Subscription, 0, len(rows))
	for _, row := range rows {
		bare = append(bare, *rowToSubscription(row))
	}
	return r.hydrateAll(ctx, bare)
}

// FindWithFilters returns subscriptions matching non-nil filters.
func (r *Repository) FindWithFilters(ctx context.Context, status, clientID *string) ([]Subscription, error) {
	var f repocommon.Filter
	f.EqPtr("status", status)
	f.EqPtr("client_id", clientID)

	q := `SELECT id, code, application_code, name, description, client_id,
		client_identifier, client_scoped, target, queue, source, status,
		max_age_seconds, dispatch_pool_id, dispatch_pool_code, delay_seconds, sequence,
		mode, timeout_seconds, max_retries, service_account_id, data_only,
		created_by, created_at, updated_at, connection_id FROM msg_subscriptions` + f.Where() + ` ORDER BY code`

	rows, err := r.pool.Query(ctx, q, f.Args()...)
	if err != nil {
		return nil, err
	}
	collected, err := pgx.CollectRows(rows, pgx.RowToStructByName[dbq.MsgSubscription])
	if err != nil {
		return nil, err
	}
	var bare []Subscription
	for _, row := range collected {
		bare = append(bare, *rowToSubscription(row))
	}
	return r.hydrateAll(ctx, bare)
}

// FindByApplicationCode returns the subscriptions whose application_code
// matches, hydrated with their event-type bindings and custom config. Used by
// the SDK sync to scope an application's API/CODE-sourced subscriptions.
// Mirrors the Rust SubscriptionRepository::find_by_application_code.
func (r *Repository) FindByApplicationCode(ctx context.Context, appCode string) ([]Subscription, error) {
	const baseSelect = `SELECT id, code, application_code, name, description, client_id,
		client_identifier, client_scoped, target, queue, source, status,
		max_age_seconds, dispatch_pool_id, dispatch_pool_code, delay_seconds, sequence,
		mode, timeout_seconds, max_retries, service_account_id, data_only,
		created_by, created_at, updated_at, connection_id FROM msg_subscriptions
		WHERE application_code = $1 ORDER BY code`
	rows, err := r.pool.Query(ctx, baseSelect, appCode)
	if err != nil {
		return nil, err
	}
	collected, err := pgx.CollectRows(rows, pgx.RowToStructByName[dbq.MsgSubscription])
	if err != nil {
		return nil, err
	}
	var bare []Subscription
	for _, row := range collected {
		bare = append(bare, *rowToSubscription(row))
	}
	return r.hydrateAll(ctx, bare)
}

// Persist implements usecasepgx.Persist[Subscription]. Replaces the
// junction-table rows (event_types, custom_config) wholesale.
func (r *Repository) Persist(ctx context.Context, s *Subscription, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	if err := q.SubscriptionUpsert(ctx, dbq.SubscriptionUpsertParams{
		ID:               s.ID,
		Code:             s.Code,
		ApplicationCode:  s.ApplicationCode,
		Name:             s.Name,
		Description:      s.Description,
		ClientID:         s.ClientID,
		ClientIdentifier: s.ClientIdentifier,
		ClientScoped:     s.ClientScoped,
		ConnectionID:     s.ConnectionID,
		Target:           s.Endpoint,
		Queue:            s.Queue,
		Source:           string(s.Source),
		Status:           string(s.Status),
		MaxAgeSeconds:    s.MaxAgeSeconds,
		DispatchPoolID:   s.DispatchPoolID,
		DispatchPoolCode: s.DispatchPoolCode,
		DelaySeconds:     s.DelaySeconds,
		Sequence:         s.Sequence,
		Mode:             string(s.Mode),
		TimeoutSeconds:   s.TimeoutSeconds,
		MaxRetries:       s.MaxRetries,
		ServiceAccountID: s.ServiceAccountID,
		DataOnly:         s.DataOnly,
		CreatedBy:        s.CreatedBy,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("subscription persist: %w", err)
	}
	if err := q.SubscriptionEventTypesClear(ctx, s.ID); err != nil {
		return err
	}
	if err := q.SubscriptionConfigsClear(ctx, s.ID); err != nil {
		return err
	}
	for _, b := range s.EventTypes {
		if err := q.SubscriptionEventTypeInsert(ctx, dbq.SubscriptionEventTypeInsertParams{
			SubscriptionID: s.ID,
			EventTypeID:    b.EventTypeID,
			EventTypeCode:  b.EventTypeCode,
			SpecVersion:    b.SpecVersion,
		}); err != nil {
			return err
		}
	}
	for _, c := range s.CustomConfig {
		if err := q.SubscriptionConfigInsert(ctx, dbq.SubscriptionConfigInsertParams{
			SubscriptionID: s.ID,
			ConfigKey:      c.Key,
			ConfigValue:    c.Value,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the subscription.
func (r *Repository) Delete(ctx context.Context, s *Subscription, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	_ = q.SubscriptionEventTypesClear(ctx, s.ID)
	_ = q.SubscriptionConfigsClear(ctx, s.ID)
	return q.SubscriptionDelete(ctx, s.ID)
}

// ── private helpers ───────────────────────────────────────────────────────

func (r *Repository) hydrateOne(ctx context.Context, s *Subscription) (*Subscription, error) {
	out, err := r.hydrateAll(ctx, []Subscription{*s})
	if err != nil {
		return nil, err
	}
	return &out[0], nil
}

func (r *Repository) hydrateAll(ctx context.Context, subs []Subscription) ([]Subscription, error) {
	if len(subs) == 0 {
		return subs, nil
	}
	ids := make([]string, len(subs))
	for i, s := range subs {
		ids[i] = s.ID
	}

	bindingRows, err := r.q.SubscriptionEventTypesForSubs(ctx, ids)
	if err != nil {
		return nil, err
	}
	configRows, err := r.q.SubscriptionConfigsForSubs(ctx, ids)
	if err != nil {
		return nil, err
	}

	bindingsByID := make(map[string][]EventTypeBinding)
	for _, b := range bindingRows {
		bindingsByID[b.SubscriptionID] = append(bindingsByID[b.SubscriptionID], EventTypeBinding{
			EventTypeID:   b.EventTypeID,
			EventTypeCode: b.EventTypeCode,
			SpecVersion:   b.SpecVersion,
		})
	}
	configsByID := make(map[string][]ConfigEntry)
	for _, c := range configRows {
		configsByID[c.SubscriptionID] = append(configsByID[c.SubscriptionID], ConfigEntry{
			Key: c.ConfigKey, Value: c.ConfigValue,
		})
	}
	for i := range subs {
		subs[i].EventTypes = bindingsByID[subs[i].ID]
		subs[i].CustomConfig = configsByID[subs[i].ID]
		if subs[i].EventTypes == nil {
			subs[i].EventTypes = []EventTypeBinding{}
		}
		if subs[i].CustomConfig == nil {
			subs[i].CustomConfig = []ConfigEntry{}
		}
	}
	return subs, nil
}

func rowToSubscription(row dbq.MsgSubscription) *Subscription {
	return &Subscription{
		ID:               row.ID,
		Code:             row.Code,
		ApplicationCode:  row.ApplicationCode,
		Name:             row.Name,
		Description:      row.Description,
		ClientID:         row.ClientID,
		ClientIdentifier: row.ClientIdentifier,
		ClientScoped:     row.ClientScoped,
		ConnectionID:     row.ConnectionID,
		Endpoint:         row.Target,
		Queue:            row.Queue,
		Source:           ParseSource(row.Source),
		Status:           ParseStatus(row.Status),
		MaxAgeSeconds:    row.MaxAgeSeconds,
		DispatchPoolID:   row.DispatchPoolID,
		DispatchPoolCode: row.DispatchPoolCode,
		DelaySeconds:     row.DelaySeconds,
		Sequence:         row.Sequence,
		Mode:             common.ParseDispatchMode(row.Mode),
		TimeoutSeconds:   row.TimeoutSeconds,
		MaxRetries:       row.MaxRetries,
		ServiceAccountID: row.ServiceAccountID,
		DataOnly:         row.DataOnly,
		CreatedBy:        row.CreatedBy,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		EventTypes:       []EventTypeBinding{},
		CustomConfig:     []ConfigEntry{},
	}
}
