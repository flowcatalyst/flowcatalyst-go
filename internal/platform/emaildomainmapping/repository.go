package emaildomainmapping

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/sqlc/dbq"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// Repository is the Postgres-backed EDM repo. Three junction tables
// hang off the main aggregate (additional_clients, granted_clients,
// allowed_roles); none have FK ON DELETE CASCADE so Delete clears
// them explicitly. Mirrors the Rust impl.
type Repository struct{ q *dbq.Queries }

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{q: dbq.New(pool)}
}

// FindByID loads a mapping with hydrated junction tables.
func (r *Repository) FindByID(ctx context.Context, id string) (*EmailDomainMapping, error) {
	row, err := r.q.EmailDomainMappingFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("edm repo: %w", err)
	}
	return r.hydrateOne(ctx, rowToEDM(row))
}

// FindByEmailDomain loads by the unique email domain.
func (r *Repository) FindByEmailDomain(ctx context.Context, domain string) (*EmailDomainMapping, error) {
	row, err := r.q.EmailDomainMappingFindByDomain(ctx, domain)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("edm repo: %w", err)
	}
	return r.hydrateOne(ctx, rowToEDM(row))
}

// FindAll loads every mapping, hydrated.
func (r *Repository) FindAll(ctx context.Context) ([]EmailDomainMapping, error) {
	rows, err := r.q.EmailDomainMappingFindAll(ctx)
	if err != nil {
		return nil, err
	}
	bare := make([]EmailDomainMapping, 0, len(rows))
	for _, row := range rows {
		bare = append(bare, *rowToEDM(row))
	}
	return r.hydrateAll(ctx, bare)
}

// Persist implements usecasepgx.Persist[EmailDomainMapping].
// Replaces the junction-table rows wholesale within the open transaction.
func (r *Repository) Persist(ctx context.Context, e *EmailDomainMapping, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	if err := q.EmailDomainMappingUpsert(ctx, dbq.EmailDomainMappingUpsertParams{
		ID:                    e.ID,
		EmailDomain:           e.EmailDomain,
		IdentityProviderID:    e.IdentityProviderID,
		ScopeType:             string(e.ScopeType),
		PrimaryClientID:       e.PrimaryClientID,
		RequiredOidcTenantID:  e.RequiredOIDCTenantID,
		SyncRolesFromIdp:      e.SyncRolesFromIDP,
		Require2fa:            e.Require2FA,
		RememberDeviceEnabled: e.RememberDeviceEnabled,
		RememberDeviceDays:    int32(e.RememberDeviceDays),
		CreatedAt:             e.CreatedAt,
		UpdatedAt:             time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("edm persist: %w", err)
	}
	if err := q.EmailDomainMappingAdditionalClientsClear(ctx, e.ID); err != nil {
		return err
	}
	if err := q.EmailDomainMappingGrantedClientsClear(ctx, e.ID); err != nil {
		return err
	}
	if err := q.EmailDomainMappingAllowedRolesClear(ctx, e.ID); err != nil {
		return err
	}
	if err := q.EmailDomainMapping2FAMethodsClear(ctx, e.ID); err != nil {
		return err
	}
	for _, c := range e.AdditionalClientIDs {
		if err := q.EmailDomainMappingAdditionalClientInsert(ctx, dbq.EmailDomainMappingAdditionalClientInsertParams{
			EmailDomainMappingID: e.ID, ClientID: c,
		}); err != nil {
			return err
		}
	}
	for _, c := range e.GrantedClientIDs {
		if err := q.EmailDomainMappingGrantedClientInsert(ctx, dbq.EmailDomainMappingGrantedClientInsertParams{
			EmailDomainMappingID: e.ID, ClientID: c,
		}); err != nil {
			return err
		}
	}
	for _, ro := range e.AllowedRoleIDs {
		if err := q.EmailDomainMappingAllowedRoleInsert(ctx, dbq.EmailDomainMappingAllowedRoleInsertParams{
			EmailDomainMappingID: e.ID, RoleID: ro,
		}); err != nil {
			return err
		}
	}
	for _, m := range e.Allowed2FAMethods {
		if err := q.EmailDomainMapping2FAMethodInsert(ctx, dbq.EmailDomainMapping2FAMethodInsertParams{
			EmailDomainMappingID: e.ID, Method: m,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the mapping and explicitly clears the three junctions
// (no FK ON DELETE CASCADE in the schema).
func (r *Repository) Delete(ctx context.Context, e *EmailDomainMapping, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	if err := q.EmailDomainMappingAdditionalClientsClear(ctx, e.ID); err != nil {
		return err
	}
	if err := q.EmailDomainMappingGrantedClientsClear(ctx, e.ID); err != nil {
		return err
	}
	if err := q.EmailDomainMappingAllowedRolesClear(ctx, e.ID); err != nil {
		return err
	}
	if err := q.EmailDomainMapping2FAMethodsClear(ctx, e.ID); err != nil {
		return err
	}
	return q.EmailDomainMappingDelete(ctx, e.ID)
}

// ── private helpers ───────────────────────────────────────────────────────

func (r *Repository) hydrateOne(ctx context.Context, e *EmailDomainMapping) (*EmailDomainMapping, error) {
	out, err := r.hydrateAll(ctx, []EmailDomainMapping{*e})
	if err != nil {
		return nil, err
	}
	return &out[0], nil
}

func (r *Repository) hydrateAll(ctx context.Context, edms []EmailDomainMapping) ([]EmailDomainMapping, error) {
	if len(edms) == 0 {
		return edms, nil
	}
	ids := make([]string, len(edms))
	for i, e := range edms {
		ids[i] = e.ID
	}

	addRows, err := r.q.EmailDomainMappingAdditionalClientsForMappings(ctx, ids)
	if err != nil {
		return nil, err
	}
	grantRows, err := r.q.EmailDomainMappingGrantedClientsForMappings(ctx, ids)
	if err != nil {
		return nil, err
	}
	roleRows, err := r.q.EmailDomainMappingAllowedRolesForMappings(ctx, ids)
	if err != nil {
		return nil, err
	}
	methodRows, err := r.q.EmailDomainMapping2FAMethodsForMappings(ctx, ids)
	if err != nil {
		return nil, err
	}

	additionalByID := map[string][]string{}
	for _, a := range addRows {
		additionalByID[a.EmailDomainMappingID] = append(additionalByID[a.EmailDomainMappingID], a.ClientID)
	}
	grantedByID := map[string][]string{}
	for _, g := range grantRows {
		grantedByID[g.EmailDomainMappingID] = append(grantedByID[g.EmailDomainMappingID], g.ClientID)
	}
	rolesByID := map[string][]string{}
	for _, ro := range roleRows {
		rolesByID[ro.EmailDomainMappingID] = append(rolesByID[ro.EmailDomainMappingID], ro.RoleID)
	}
	methodsByID := map[string][]string{}
	for _, m := range methodRows {
		methodsByID[m.EmailDomainMappingID] = append(methodsByID[m.EmailDomainMappingID], m.Method)
	}
	for i := range edms {
		edms[i].AdditionalClientIDs = additionalByID[edms[i].ID]
		edms[i].GrantedClientIDs = grantedByID[edms[i].ID]
		edms[i].AllowedRoleIDs = rolesByID[edms[i].ID]
		edms[i].Allowed2FAMethods = methodsByID[edms[i].ID]
		if edms[i].AdditionalClientIDs == nil {
			edms[i].AdditionalClientIDs = []string{}
		}
		if edms[i].GrantedClientIDs == nil {
			edms[i].GrantedClientIDs = []string{}
		}
		if edms[i].AllowedRoleIDs == nil {
			edms[i].AllowedRoleIDs = []string{}
		}
		if edms[i].Allowed2FAMethods == nil {
			edms[i].Allowed2FAMethods = []string{}
		}
	}
	return edms, nil
}

func rowToEDM(row dbq.TntEmailDomainMapping) *EmailDomainMapping {
	return &EmailDomainMapping{
		ID:                    row.ID,
		EmailDomain:           row.EmailDomain,
		IdentityProviderID:    row.IdentityProviderID,
		ScopeType:             ParseScopeType(row.ScopeType),
		PrimaryClientID:       row.PrimaryClientID,
		RequiredOIDCTenantID:  row.RequiredOidcTenantID,
		SyncRolesFromIDP:      row.SyncRolesFromIdp,
		Require2FA:            row.Require2fa,
		RememberDeviceEnabled: row.RememberDeviceEnabled,
		RememberDeviceDays:    int(row.RememberDeviceDays),
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
		AdditionalClientIDs:   []string{},
		GrantedClientIDs:      []string{},
		AllowedRoleIDs:        []string{},
		Allowed2FAMethods:     []string{},
	}
}
