package principal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/sqlc/dbq"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// Repository is the Postgres-backed principal repo.
//
// Phase 3c scope: the base Persist writes the iam_principals row and
// nothing else. The junction tables (iam_principal_roles,
// iam_client_access_grants, iam_principal_application_access) are
// deliberately left alone by Persist so unrelated writers (login
// last-login bumps, create-user) can't clobber a junction they never
// loaded. The ops that OWN a junction opt into writing it in the SAME
// transaction as the domain event via a wrapper persister:
//   - RolesPersister → iam_principal_roles
//     (assign_roles, sync_idp_roles, sync_principals)
//   - AppAccessPersister → iam_principal_application_access
//     (assign_application_access)
// Client-access grants are their own aggregate (ClientAccessGrantRepo).
// Delete still cleans the non-cascade junctions to avoid orphans (only
// iam_principal_roles has FK ON DELETE CASCADE; the other two don't).
//
// User-identity fields are stored as flat columns on iam_principals
// (email, idp_type, external_idp_id, password_hash, last_login_at) —
// not as JSONB. The entity exposes UserIdentity{} as a struct for API
// shape; fields with no backing column (email_verified, first_name,
// last_name, picture_url, phone) are zero-valued on read and dropped
// on write. Mirrors the Rust impl.
type Repository struct {
	q    *dbq.Queries
	pool *pgxpool.Pool
}

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{q: dbq.New(pool), pool: pool}
}

// FindByID loads a principal by id, with role assignments hydrated
// from iam_principal_roles.
func (r *Repository) FindByID(ctx context.Context, id string) (*Principal, error) {
	row, err := r.q.PrincipalFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("principal repo: %w", err)
	}
	p := rowToPrincipal(row)
	if err := r.hydrateRoles(ctx, p); err != nil {
		return nil, err
	}
	if err := r.hydrateClientAccess(ctx, p); err != nil {
		return nil, err
	}
	if err := r.hydrateAppAccess(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// FindByEmail loads a user-type principal by email, with role
// assignments hydrated from iam_principal_roles.
//
// Matching is case-insensitive: the input is lower-cased + trimmed here and the
// query compares `LOWER(email) = $1`. Callers receive the email verbatim from
// external sources whose casing we don't control — e.g. an OIDC IdP like Entra
// returns "John.Doe@contoso.com", and the internal login form passes whatever
// the user typed. Without this, a case-only difference made the old `email = $1`
// exact-match miss the existing (lower-cased) row, which broke federated login
// (auto-provision then hit the EMAIL_EXISTS uniqueness check) and password
// login. LOWER(email) also matches any legacy mixed-case row so the login
// self-heal (LowercaseEmail) can normalise it in place.
func (r *Repository) FindByEmail(ctx context.Context, email string) (*Principal, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	row, err := r.q.PrincipalFindByEmail(ctx, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("principal repo: %w", err)
	}
	p := rowToPrincipal(row)
	if err := r.hydrateRoles(ctx, p); err != nil {
		return nil, err
	}
	if err := r.hydrateClientAccess(ctx, p); err != nil {
		return nil, err
	}
	if err := r.hydrateAppAccess(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// hydrateClientAccess populates p.AssignedClients from iam_client_access_grants
// and p.ClientIdentifierMap (client id → identifier) from tnt_clients for the
// home + granted clients. 1:1 with Rust repository.rs::find_by_id.
//
// The identifier map is what makes the JWT `clients` claim carry "id:identifier"
// pairs (e.g. "clt_abc:spar"). SDK consumers (the Laravel FlowCatalystUser) split
// on ":" and match the identifier against their tenant code — without it every
// login fails closed with "No access to this tenant". Inline SQL, matching the
// hydrateRoles pattern, to avoid a sqlc regen for two trivial reads.
func (r *Repository) hydrateClientAccess(ctx context.Context, p *Principal) error {
	if r.pool == nil || p == nil {
		return nil
	}
	// Granted clients (the access-grants junction).
	rows, err := r.pool.Query(ctx,
		`SELECT client_id FROM iam_client_access_grants WHERE principal_id = $1 ORDER BY client_id`, p.ID)
	if err != nil {
		return fmt.Errorf("principal client grants: %w", err)
	}
	granted := make([]string, 0)
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return fmt.Errorf("principal client grants scan: %w", err)
		}
		granted = append(granted, cid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("principal client grants: %w", err)
	}
	p.AssignedClients = granted

	// Identifier lookup for the home + granted client ids.
	idSet := make(map[string]struct{}, len(granted)+1)
	for _, c := range granted {
		idSet[c] = struct{}{}
	}
	if p.ClientID != nil && *p.ClientID != "" {
		idSet[*p.ClientID] = struct{}{}
	}
	if len(idSet) == 0 {
		return nil
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	idRows, err := r.pool.Query(ctx,
		`SELECT id, identifier FROM tnt_clients WHERE id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("client identifiers: %w", err)
	}
	defer idRows.Close()
	idMap := make(map[string]string, len(ids))
	for idRows.Next() {
		var id, identifier string
		if err := idRows.Scan(&id, &identifier); err != nil {
			return fmt.Errorf("client identifiers scan: %w", err)
		}
		idMap[id] = identifier
	}
	if err := idRows.Err(); err != nil {
		return fmt.Errorf("client identifiers: %w", err)
	}
	p.ClientIdentifierMap = idMap
	return nil
}

// hydrateRoles populates p.Roles from iam_principal_roles. Returns nil
// for a principal with no role assignments (Roles stays an empty slice
// from rowToPrincipal). Inline SQL rather than a sqlc-generated query
// to avoid a regen step — the row shape (principal_id, role_name,
// assignment_source, assigned_at) is stable and the query is trivial.
func (r *Repository) hydrateRoles(ctx context.Context, p *Principal) error {
	if r.pool == nil || p == nil {
		return nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT role_name, assignment_source, assigned_at
		 FROM iam_principal_roles
		 WHERE principal_id = $1
		 ORDER BY assigned_at`, p.ID)
	if err != nil {
		return fmt.Errorf("principal roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ra serviceaccount.RoleAssignment
		var src *string
		if err := rows.Scan(&ra.Role, &src, &ra.AssignedAt); err != nil {
			return fmt.Errorf("principal roles scan: %w", err)
		}
		ra.AssignmentSource = src
		p.Roles = append(p.Roles, ra)
	}
	return rows.Err()
}

// hydrateAppAccess populates p.AccessibleApplicationIDs from
// iam_principal_application_access. Mirrors hydrateRoles /
// hydrateClientAccess: inline SQL, an empty slice when the user has no
// grants. Without this the application-access list endpoint and the JWT
// `applications` claim (see auth/provider.BuildClaims) always read empty,
// even after assign_application_access has written the junction.
func (r *Repository) hydrateAppAccess(ctx context.Context, p *Principal) error {
	if r.pool == nil || p == nil {
		return nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT application_id
		 FROM iam_principal_application_access
		 WHERE principal_id = $1
		 ORDER BY application_id`, p.ID)
	if err != nil {
		return fmt.Errorf("principal application access: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("principal application access scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("principal application access: %w", err)
	}
	p.AccessibleApplicationIDs = ids
	return nil
}

// FindByServiceAccount loads the SERVICE-type principal linked to the
// given service-account row. Used by callers that need to translate a
// SA id into the principal id its FKs reference (e.g.
// `app_applications.service_account_id`, which has a FK to
// `iam_principals.id` per migration 028).
func (r *Repository) FindByServiceAccount(ctx context.Context, serviceAccountID string) (*Principal, error) {
	row, err := r.q.PrincipalFindByServiceAccount(ctx, &serviceAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("principal repo: %w", err)
	}
	return rowToPrincipal(row), nil
}

// FindAll lists every principal.
func (r *Repository) FindAll(ctx context.Context) ([]Principal, error) {
	rows, err := r.q.PrincipalFindAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Principal, 0, len(rows))
	for _, row := range rows {
		out = append(out, *rowToPrincipal(row))
	}
	return out, nil
}

// Persist implements usecasepgx.Persist[Principal].
func (r *Repository) Persist(ctx context.Context, p *Principal, tx *usecasepgx.DbTx) error {
	now := time.Now().UTC()

	var email, emailDomain, idpType, externalIdpID, passwordHash *string
	var lastLoginAt *time.Time

	if p.UserIdentity != nil {
		// Normalise on the way to the DB: every write to the email column goes
		// through Persist, so lower-casing here guarantees the stored value (and
		// the derived email_domain) is always lower-case, regardless of which
		// operation built the entity. Pairs with the lower-cased lookup in
		// FindByEmail. Trim too, so stray whitespace never lands in a key column.
		em := strings.ToLower(strings.TrimSpace(p.UserIdentity.Email))
		email = &em
		if domain := domainOf(em); domain != "" {
			emailDomain = &domain
		}
		if p.UserIdentity.Provider != nil {
			idpType = p.UserIdentity.Provider
		}
		if p.UserIdentity.ExternalID != nil {
			externalIdpID = p.UserIdentity.ExternalID
		}
		if p.UserIdentity.PasswordHash != nil {
			passwordHash = p.UserIdentity.PasswordHash
		}
		if p.UserIdentity.LastLoginAt != nil {
			lastLoginAt = p.UserIdentity.LastLoginAt
		}
	}
	// USER without an explicit provider defaults to INTERNAL (matches Rust).
	if idpType == nil && p.Type == TypeUser {
		internal := "INTERNAL"
		idpType = &internal
	}
	// ExternalIdentity, when present, wins for the IDP columns.
	if p.ExternalIdentity != nil {
		provider := p.ExternalIdentity.ProviderID
		if provider != "" {
			idpType = &provider
		}
		ext := p.ExternalIdentity.ExternalID
		externalIdpID = &ext
	}

	scope := string(p.Scope)
	return r.q.WithTx(tx.Inner()).PrincipalUpsert(ctx, dbq.PrincipalUpsertParams{
		ID:               p.ID,
		Type:             string(p.Type),
		Scope:            &scope,
		ClientID:         p.ClientID,
		ApplicationID:    p.ApplicationID,
		Name:             p.Name,
		Active:           p.Active,
		Email:            email,
		EmailDomain:      emailDomain,
		IdpType:          idpType,
		ExternalIdpID:    externalIdpID,
		PasswordHash:     passwordHash,
		LastLoginAt:      lastLoginAt,
		ServiceAccountID: p.ServiceAccountID,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        now,
	})
}

// replaceRolesTx rewrites iam_principal_roles for p from p.Roles within
// tx (clear-then-insert, so the junction exactly mirrors the entity's
// hydrated role set). Run by RolesPersister AFTER the base Persist, so
// the iam_principals row exists for the FK on a freshly-created user.
// ON CONFLICT makes a duplicate role name in the input a no-op rather
// than a primary-key error.
func (r *Repository) replaceRolesTx(ctx context.Context, p *Principal, tx *usecasepgx.DbTx) error {
	q := tx.Inner()
	if _, err := q.Exec(ctx,
		`DELETE FROM iam_principal_roles WHERE principal_id = $1`, p.ID); err != nil {
		return fmt.Errorf("clear principal roles: %w", err)
	}
	for _, ra := range p.Roles {
		if _, err := q.Exec(ctx,
			`INSERT INTO iam_principal_roles
			     (principal_id, role_name, assignment_source, assigned_at)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (principal_id, role_name) DO UPDATE
			     SET assignment_source = EXCLUDED.assignment_source,
			         assigned_at       = EXCLUDED.assigned_at`,
			p.ID, ra.Role, ra.AssignmentSource, ra.AssignedAt); err != nil {
			return fmt.Errorf("insert principal role %q: %w", ra.Role, err)
		}
	}
	return nil
}

// replaceAppAccessTx rewrites iam_principal_application_access for p from
// p.AccessibleApplicationIDs within tx. The app-access analogue of
// replaceRolesTx; see RolesPersister.
func (r *Repository) replaceAppAccessTx(ctx context.Context, p *Principal, tx *usecasepgx.DbTx) error {
	q := tx.Inner()
	if _, err := q.Exec(ctx,
		`DELETE FROM iam_principal_application_access WHERE principal_id = $1`, p.ID); err != nil {
		return fmt.Errorf("clear principal application access: %w", err)
	}
	for _, appID := range p.AccessibleApplicationIDs {
		if _, err := q.Exec(ctx,
			`INSERT INTO iam_principal_application_access (principal_id, application_id)
			 VALUES ($1, $2)
			 ON CONFLICT (principal_id, application_id) DO NOTHING`,
			p.ID, appID); err != nil {
			return fmt.Errorf("insert principal application access %q: %w", appID, err)
		}
	}
	return nil
}

// RolesPersister adapts the principal repo so commit.Save / commit.Sync
// also rewrite iam_principal_roles from the principal's (hydrated) Roles
// slice, in the same transaction as the domain event. The ops that own
// the role set (assign_roles, sync_idp_roles, sync_principals) use it;
// every other writer uses the base repo, whose Persist leaves the
// junction untouched. Delete is promoted from the embedded *Repository.
type RolesPersister struct{ *Repository }

// Persist upserts the principal row, then replaces its role junction.
func (rp RolesPersister) Persist(ctx context.Context, p *Principal, tx *usecasepgx.DbTx) error {
	if err := rp.Repository.Persist(ctx, p, tx); err != nil {
		return err
	}
	return rp.Repository.replaceRolesTx(ctx, p, tx)
}

// AppAccessPersister is the application-access analogue of RolesPersister:
// Persist also rewrites iam_principal_application_access from
// p.AccessibleApplicationIDs. Used by assign_application_access.
type AppAccessPersister struct{ *Repository }

// Persist upserts the principal row, then replaces its app-access junction.
func (ap AppAccessPersister) Persist(ctx context.Context, p *Principal, tx *usecasepgx.DbTx) error {
	if err := ap.Repository.Persist(ctx, p, tx); err != nil {
		return err
	}
	return ap.Repository.replaceAppAccessTx(ctx, p, tx)
}

// UpdatePasswordHash overwrites only the password_hash for a principal. Used by
// the login flow to transparently re-encode a legacy hash (e.g. an upstream
// Laravel argon2i hash) to the native scheme after a successful verify. A
// direct UPDATE — not a domain event — because it's an internal migration, not
// a user-initiated password change.
func (r *Repository) UpdatePasswordHash(ctx context.Context, principalID, hash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE iam_principals SET password_hash = $1, updated_at = NOW() WHERE id = $2`,
		hash, principalID)
	return err
}

// LowercaseEmail normalises a principal's stored email (and the derived
// email_domain) to lower-case in place, but only when it isn't already
// normalised — an already-lower-case row triggers no write. Like
// UpdatePasswordHash, this is a transparent migration of a legacy row run after
// a successful login, not a user-initiated change, so it's a direct UPDATE
// rather than a domain event. It also rewrites p.UserIdentity.Email in memory so
// the caller's principal reflects the stored value. Callers should treat any
// error as non-fatal: the user is already authenticated; healing can wait for
// the next login.
func (r *Repository) LowercaseEmail(ctx context.Context, p *Principal) error {
	if p == nil || p.UserIdentity == nil {
		return nil
	}
	lowered := strings.ToLower(strings.TrimSpace(p.UserIdentity.Email))
	if lowered == p.UserIdentity.Email {
		return nil // already normalised — no write
	}
	var domainPtr *string
	if d := domainOf(lowered); d != "" {
		domainPtr = &d
	}
	if _, err := r.pool.Exec(ctx,
		`UPDATE iam_principals SET email = $1, email_domain = $2, updated_at = NOW() WHERE id = $3`,
		lowered, domainPtr, p.ID); err != nil {
		return fmt.Errorf("principal repo: lowercase email: %w", err)
	}
	p.UserIdentity.Email = lowered
	return nil
}

// Delete removes the principal and the two non-FK-cascade junctions.
// iam_principal_roles has FK ON DELETE CASCADE so it goes via the main row.
func (r *Repository) Delete(ctx context.Context, p *Principal, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	if err := q.PrincipalApplicationAccessClear(ctx, p.ID); err != nil {
		return err
	}
	if err := q.PrincipalClientAccessGrantsClear(ctx, p.ID); err != nil {
		return err
	}
	return q.PrincipalDelete(ctx, p.ID)
}

// rowToPrincipal projects the flat schema row onto the Principal aggregate.
// Mirrors the Rust From<PrincipalRow> for Principal mapping.
func rowToPrincipal(row dbq.IamPrincipal) *Principal {
	p := Principal{
		ID:                       row.ID,
		Type:                     ParseType(row.Type),
		ClientID:                 row.ClientID,
		ApplicationID:            row.ApplicationID,
		Name:                     row.Name,
		Active:                   row.Active,
		ServiceAccountID:         row.ServiceAccountID,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
		Roles:                    []serviceaccount.RoleAssignment{},
		AssignedClients:          []string{},
		AccessibleApplicationIDs: []string{},
	}
	if row.Scope != nil {
		p.Scope = ParseScope(*row.Scope)
	} else {
		p.Scope = ScopeClient
	}
	if p.Type == TypeUser && row.Email != nil {
		p.UserIdentity = &UserIdentity{
			Email:        *row.Email,
			ExternalID:   row.ExternalIdpID,
			Provider:     row.IdpType,
			PasswordHash: row.PasswordHash,
			LastLoginAt:  row.LastLoginAt,
		}
	}
	if row.ExternalIdpID != nil {
		providerID := ""
		if row.IdpType != nil {
			providerID = *row.IdpType
		}
		p.ExternalIdentity = &ExternalIdentity{
			ProviderID: providerID,
			ExternalID: *row.ExternalIdpID,
		}
	}
	return &p
}

func domainOf(email string) string {
	at := strings.IndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return email[at+1:]
}
