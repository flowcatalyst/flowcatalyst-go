package auth

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

// Repository bundles per-entity repositories. Each *Repo type implements
// usecasepgx.Persist[T] for its aggregate, so they can be passed
// directly into usecasepgx.Commit.
type Repository struct {
	OAuthClients      *OAuthClientRepo
	AnchorDomains     *AnchorDomainRepo
	ClientAuthConfigs *ClientAuthConfigRepo
	IdpRoleMappings   *IdpRoleMappingRepo
}

// NewRepository wires the bundle.
func NewRepository(pool *pgxpool.Pool) *Repository {
	q := dbq.New(pool)
	return &Repository{
		OAuthClients:      &OAuthClientRepo{q: q, pool: pool},
		AnchorDomains:     &AnchorDomainRepo{q: q},
		ClientAuthConfigs: &ClientAuthConfigRepo{q: q},
		IdpRoleMappings:   &IdpRoleMappingRepo{q: q},
	}
}

// ── OAuthClient repo ──────────────────────────────────────────────────────
//
// Schema: oauth_clients + oauth_client_redirect_uris +
// oauth_client_grant_types + oauth_client_post_logout_redirect_uris (the
// OIDC RP-Initiated Logout whitelist consulted by /auth/oidc/session/end) +
// oauth_client_allowed_origins + oauth_client_application_ids. The
// post-logout, allowed-origins, and application-ids junctions are
// loaded/persisted via raw pgx (they aren't wired through sqlc).
// client_secret_ref holds the reversibly-encrypted client secret
// (AES-256-GCM under FLOWCATALYST_APP_KEY, "encrypted:"-prefixed),
// matching Rust; it is verified at /oauth/token by decrypt-and-compare,
// NOT by hashing. See internal/platform/shared/encryption.

type OAuthClientRepo struct {
	q    *dbq.Queries
	pool *pgxpool.Pool
}

func (r *OAuthClientRepo) FindByID(ctx context.Context, id string) (*OAuthClient, error) {
	row, err := r.q.OAuthClientFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("oauth_client repo: %w", err)
	}
	return r.hydrate(ctx, rowToOAuthClient(row))
}

func (r *OAuthClientRepo) FindByClientID(ctx context.Context, clientID string) (*OAuthClient, error) {
	row, err := r.q.OAuthClientFindByClientID(ctx, clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("oauth_client repo: %w", err)
	}
	return r.hydrate(ctx, rowToOAuthClient(row))
}

func (r *OAuthClientRepo) FindAll(ctx context.Context) ([]OAuthClient, error) {
	rows, err := r.q.OAuthClientFindAll(ctx)
	if err != nil {
		return nil, err
	}
	bare := make([]OAuthClient, 0, len(rows))
	for _, row := range rows {
		bare = append(bare, *rowToOAuthClient(row))
	}
	return r.hydrateAll(ctx, bare)
}

func (r *OAuthClientRepo) Persist(ctx context.Context, c *OAuthClient, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	now := time.Now().UTC()
	var scopes *string
	if joined := strings.Join(c.Scopes, ","); joined != "" {
		scopes = &joined
	}
	if err := q.OAuthClientUpsert(ctx, dbq.OAuthClientUpsertParams{
		ID:                        c.ID,
		ClientID:                  c.ClientID,
		ClientName:                c.ClientName,
		ClientType:                string(c.ClientType),
		ClientSecretRef:           c.SecretRef,
		DefaultScopes:             scopes,
		PkceRequired:              c.PKCERequired,
		ServiceAccountPrincipalID: c.PrincipalID,
		Active:                    c.Active,
		CreatedAt:                 c.CreatedAt,
		UpdatedAt:                 now,
	}); err != nil {
		return fmt.Errorf("oauth_client persist: %w", err)
	}
	if err := q.OAuthClientRedirectURIsClear(ctx, c.ID); err != nil {
		return err
	}
	for _, u := range c.RedirectURIs {
		if err := q.OAuthClientRedirectURIInsert(ctx, dbq.OAuthClientRedirectURIInsertParams{
			OauthClientID: c.ID, RedirectUri: u,
		}); err != nil {
			return err
		}
	}
	if err := q.OAuthClientGrantTypesClear(ctx, c.ID); err != nil {
		return err
	}
	for _, g := range c.GrantTypes {
		if err := q.OAuthClientGrantTypeInsert(ctx, dbq.OAuthClientGrantTypeInsertParams{
			OauthClientID: c.ID, GrantType: g,
		}); err != nil {
			return err
		}
	}
	// Post-logout redirect URIs — raw pgx (this junction isn't wired
	// through sqlc). Clear-then-reinsert mirrors the redirect_uris path.
	if _, err := tx.Inner().Exec(ctx,
		`DELETE FROM oauth_client_post_logout_redirect_uris WHERE oauth_client_id = $1`, c.ID); err != nil {
		return fmt.Errorf("post_logout uris clear: %w", err)
	}
	for _, u := range c.PostLogoutRedirectURIs {
		if _, err := tx.Inner().Exec(ctx,
			`INSERT INTO oauth_client_post_logout_redirect_uris (oauth_client_id, post_logout_redirect_uri)
			 VALUES ($1, $2) ON CONFLICT DO NOTHING`, c.ID, u); err != nil {
			return fmt.Errorf("post_logout uri insert: %w", err)
		}
	}
	// Allowed origins + application ids — raw pgx (not wired through sqlc),
	// clear-then-reinsert like the redirect_uris path.
	if _, err := tx.Inner().Exec(ctx,
		`DELETE FROM oauth_client_allowed_origins WHERE oauth_client_id = $1`, c.ID); err != nil {
		return fmt.Errorf("allowed_origins clear: %w", err)
	}
	for _, o := range c.AllowedOrigins {
		if _, err := tx.Inner().Exec(ctx,
			`INSERT INTO oauth_client_allowed_origins (oauth_client_id, allowed_origin)
			 VALUES ($1, $2) ON CONFLICT DO NOTHING`, c.ID, o); err != nil {
			return fmt.Errorf("allowed_origin insert: %w", err)
		}
	}
	if _, err := tx.Inner().Exec(ctx,
		`DELETE FROM oauth_client_application_ids WHERE oauth_client_id = $1`, c.ID); err != nil {
		return fmt.Errorf("application_ids clear: %w", err)
	}
	for _, a := range c.ApplicationIDs {
		if _, err := tx.Inner().Exec(ctx,
			`INSERT INTO oauth_client_application_ids (oauth_client_id, application_id)
			 VALUES ($1, $2) ON CONFLICT DO NOTHING`, c.ID, a); err != nil {
			return fmt.Errorf("application_id insert: %w", err)
		}
	}
	return nil
}

func (r *OAuthClientRepo) Delete(ctx context.Context, c *OAuthClient, tx *usecasepgx.DbTx) error {
	q := r.q.WithTx(tx.Inner())
	// FK ON DELETE CASCADE clears the junctions, but clear explicitly
	// to be tx-consistent and to match Rust's behaviour.
	if err := q.OAuthClientRedirectURIsClear(ctx, c.ID); err != nil {
		return err
	}
	if err := q.OAuthClientGrantTypesClear(ctx, c.ID); err != nil {
		return err
	}
	if _, err := tx.Inner().Exec(ctx,
		`DELETE FROM oauth_client_post_logout_redirect_uris WHERE oauth_client_id = $1`, c.ID); err != nil {
		return err
	}
	if _, err := tx.Inner().Exec(ctx,
		`DELETE FROM oauth_client_allowed_origins WHERE oauth_client_id = $1`, c.ID); err != nil {
		return err
	}
	if _, err := tx.Inner().Exec(ctx,
		`DELETE FROM oauth_client_application_ids WHERE oauth_client_id = $1`, c.ID); err != nil {
		return err
	}
	return q.OAuthClientDelete(ctx, c.ID)
}

func (r *OAuthClientRepo) hydrate(ctx context.Context, c *OAuthClient) (*OAuthClient, error) {
	out, err := r.hydrateAll(ctx, []OAuthClient{*c})
	if err != nil {
		return nil, err
	}
	return &out[0], nil
}

func (r *OAuthClientRepo) hydrateAll(ctx context.Context, clients []OAuthClient) ([]OAuthClient, error) {
	if len(clients) == 0 {
		return clients, nil
	}
	ids := make([]string, len(clients))
	for i, c := range clients {
		ids[i] = c.ID
	}
	uriRows, err := r.q.OAuthClientRedirectURIsForClients(ctx, ids)
	if err != nil {
		return nil, err
	}
	grantRows, err := r.q.OAuthClientGrantTypesForClients(ctx, ids)
	if err != nil {
		return nil, err
	}
	// Post-logout redirect URIs — raw pgx (this junction isn't wired
	// through sqlc). Needed so /auth/oidc/session/end can validate a
	// supplied post_logout_redirect_uri against the client's whitelist.
	plByID := map[string][]string{}
	plRows, err := r.pool.Query(ctx,
		`SELECT oauth_client_id, post_logout_redirect_uri
		   FROM oauth_client_post_logout_redirect_uris
		  WHERE oauth_client_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("post_logout uris load: %w", err)
	}
	for plRows.Next() {
		var cid, uri string
		if err := plRows.Scan(&cid, &uri); err != nil {
			plRows.Close()
			return nil, err
		}
		plByID[cid] = append(plByID[cid], uri)
	}
	plRows.Close()
	if err := plRows.Err(); err != nil {
		return nil, err
	}
	// Allowed origins + application ids — raw pgx (not wired through sqlc).
	originsByID, err := r.loadClientStringJunction(ctx,
		`SELECT oauth_client_id, allowed_origin FROM oauth_client_allowed_origins WHERE oauth_client_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("allowed_origins load: %w", err)
	}
	appsByID, err := r.loadClientStringJunction(ctx,
		`SELECT oauth_client_id, application_id FROM oauth_client_application_ids WHERE oauth_client_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("application_ids load: %w", err)
	}
	urisByID := map[string][]string{}
	for _, u := range uriRows {
		urisByID[u.OauthClientID] = append(urisByID[u.OauthClientID], u.RedirectUri)
	}
	grantsByID := map[string][]string{}
	for _, g := range grantRows {
		grantsByID[g.OauthClientID] = append(grantsByID[g.OauthClientID], g.GrantType)
	}
	for i := range clients {
		clients[i].RedirectURIs = urisByID[clients[i].ID]
		clients[i].GrantTypes = grantsByID[clients[i].ID]
		clients[i].PostLogoutRedirectURIs = plByID[clients[i].ID]
		clients[i].AllowedOrigins = originsByID[clients[i].ID]
		clients[i].ApplicationIDs = appsByID[clients[i].ID]
		if clients[i].RedirectURIs == nil {
			clients[i].RedirectURIs = []string{}
		}
		if clients[i].GrantTypes == nil {
			clients[i].GrantTypes = []string{}
		}
		if clients[i].PostLogoutRedirectURIs == nil {
			clients[i].PostLogoutRedirectURIs = []string{}
		}
		if clients[i].AllowedOrigins == nil {
			clients[i].AllowedOrigins = []string{}
		}
		if clients[i].ApplicationIDs == nil {
			clients[i].ApplicationIDs = []string{}
		}
	}
	return clients, nil
}

// loadClientStringJunction runs a (oauth_client_id, value) query against a
// raw-pgx junction table and returns the values grouped by client id.
// Shared by the allowed-origins and application-ids hydration paths.
func (r *OAuthClientRepo) loadClientStringJunction(ctx context.Context, query string, ids []string) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var cid, val string
		if err := rows.Scan(&cid, &val); err != nil {
			return nil, err
		}
		out[cid] = append(out[cid], val)
	}
	return out, rows.Err()
}

func rowToOAuthClient(row dbq.OauthClient) *OAuthClient {
	c := OAuthClient{
		ID:                     row.ID,
		ClientID:               row.ClientID,
		ClientName:             row.ClientName,
		ClientType:             ParseOAuthClientType(row.ClientType),
		SecretRef:              row.ClientSecretRef,
		PKCERequired:           row.PkceRequired,
		Active:                 row.Active,
		PrincipalID:            row.ServiceAccountPrincipalID,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
		RedirectURIs:           []string{},
		PostLogoutRedirectURIs: []string{},
		GrantTypes:             []string{},
		Scopes:                 []string{},
		AllowedOrigins:         []string{},
		ApplicationIDs:         []string{},
	}
	if row.DefaultScopes != nil && *row.DefaultScopes != "" {
		for _, s := range strings.Split(*row.DefaultScopes, ",") {
			if s != "" {
				c.Scopes = append(c.Scopes, s)
			}
		}
	}
	return &c
}

// ── AnchorDomain repo ─────────────────────────────────────────────────────

type AnchorDomainRepo struct{ q *dbq.Queries }

func (r *AnchorDomainRepo) FindByID(ctx context.Context, id string) (*AnchorDomain, error) {
	row, err := r.q.AnchorDomainFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("anchor_domain repo: %w", err)
	}
	return rowToAnchorDomain(row), nil
}

func (r *AnchorDomainRepo) FindByDomain(ctx context.Context, domain string) (*AnchorDomain, error) {
	row, err := r.q.AnchorDomainFindByDomain(ctx, strings.ToLower(domain))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("anchor_domain repo: %w", err)
	}
	return rowToAnchorDomain(row), nil
}

func (r *AnchorDomainRepo) FindAll(ctx context.Context) ([]AnchorDomain, error) {
	rows, err := r.q.AnchorDomainFindAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AnchorDomain, 0, len(rows))
	for _, row := range rows {
		out = append(out, *rowToAnchorDomain(row))
	}
	return out, nil
}

func (r *AnchorDomainRepo) Persist(ctx context.Context, a *AnchorDomain, tx *usecasepgx.DbTx) error {
	return r.q.WithTx(tx.Inner()).AnchorDomainUpsert(ctx, dbq.AnchorDomainUpsertParams{
		ID:        a.ID,
		Domain:    a.Domain,
		CreatedAt: a.CreatedAt,
		UpdatedAt: time.Now().UTC(),
	})
}

func (r *AnchorDomainRepo) Delete(ctx context.Context, a *AnchorDomain, tx *usecasepgx.DbTx) error {
	return r.q.WithTx(tx.Inner()).AnchorDomainDelete(ctx, a.ID)
}

func rowToAnchorDomain(row dbq.TntAnchorDomain) *AnchorDomain {
	return &AnchorDomain{
		ID:        row.ID,
		Domain:    row.Domain,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

// ── ClientAuthConfig repo ─────────────────────────────────────────────────
//
// additional_client_ids and granted_client_ids are JSONB arrays on the
// tnt_client_auth_configs row itself — matches the Rust schema. The Go
// port previously used fictitious junction tables; that was a bug.

type ClientAuthConfigRepo struct{ q *dbq.Queries }

func (r *ClientAuthConfigRepo) FindByID(ctx context.Context, id string) (*ClientAuthConfig, error) {
	row, err := r.q.ClientAuthConfigFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("client_auth_config repo: %w", err)
	}
	return rowToClientAuthConfig(row)
}

func (r *ClientAuthConfigRepo) FindByEmailDomain(ctx context.Context, domain string) (*ClientAuthConfig, error) {
	row, err := r.q.ClientAuthConfigFindByEmailDomain(ctx, strings.ToLower(domain))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("client_auth_config repo: %w", err)
	}
	return rowToClientAuthConfig(row)
}

func (r *ClientAuthConfigRepo) FindAll(ctx context.Context) ([]ClientAuthConfig, error) {
	rows, err := r.q.ClientAuthConfigFindAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ClientAuthConfig, 0, len(rows))
	for _, row := range rows {
		c, err := rowToClientAuthConfig(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (r *ClientAuthConfigRepo) Persist(ctx context.Context, c *ClientAuthConfig, tx *usecasepgx.DbTx) error {
	additional, err := json.Marshal(stringSliceOrEmpty(c.AdditionalClientIDs))
	if err != nil {
		return fmt.Errorf("marshal additional_client_ids: %w", err)
	}
	granted, err := json.Marshal(stringSliceOrEmpty(c.GrantedClientIDs))
	if err != nil {
		return fmt.Errorf("marshal granted_client_ids: %w", err)
	}
	return r.q.WithTx(tx.Inner()).ClientAuthConfigUpsert(ctx, dbq.ClientAuthConfigUpsertParams{
		ID:                  c.ID,
		EmailDomain:         c.EmailDomain,
		ConfigType:          string(c.ConfigType),
		PrimaryClientID:     c.PrimaryClientID,
		AdditionalClientIds: additional,
		GrantedClientIds:    granted,
		AuthProvider:        string(c.AuthProvider),
		OidcIssuerUrl:       c.OIDCIssuerURL,
		OidcClientID:        c.OIDCClientID,
		OidcMultiTenant:     c.OIDCMultiTenant,
		OidcIssuerPattern:   c.OIDCIssuerPattern,
		OidcClientSecretRef: c.OIDCClientSecretRef,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           time.Now().UTC(),
	})
}

func (r *ClientAuthConfigRepo) Delete(ctx context.Context, c *ClientAuthConfig, tx *usecasepgx.DbTx) error {
	return r.q.WithTx(tx.Inner()).ClientAuthConfigDelete(ctx, c.ID)
}

func rowToClientAuthConfig(row dbq.TntClientAuthConfig) (*ClientAuthConfig, error) {
	c := ClientAuthConfig{
		ID:                  row.ID,
		EmailDomain:         row.EmailDomain,
		ConfigType:          ParseAuthConfigType(row.ConfigType),
		PrimaryClientID:     row.PrimaryClientID,
		AuthProvider:        ParseAuthProvider(row.AuthProvider),
		OIDCIssuerURL:       row.OidcIssuerUrl,
		OIDCClientID:        row.OidcClientID,
		OIDCMultiTenant:     row.OidcMultiTenant,
		OIDCIssuerPattern:   row.OidcIssuerPattern,
		OIDCClientSecretRef: row.OidcClientSecretRef,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		AdditionalClientIDs: []string{},
		GrantedClientIDs:    []string{},
	}
	if len(row.AdditionalClientIds) > 0 {
		if err := json.Unmarshal(row.AdditionalClientIds, &c.AdditionalClientIDs); err != nil {
			return nil, fmt.Errorf("decode additional_client_ids: %w", err)
		}
	}
	if len(row.GrantedClientIds) > 0 {
		if err := json.Unmarshal(row.GrantedClientIds, &c.GrantedClientIDs); err != nil {
			return nil, fmt.Errorf("decode granted_client_ids: %w", err)
		}
	}
	return &c, nil
}

// stringSliceOrEmpty ensures nil slices marshal as `[]` (not `null`)
// so the NOT NULL JSONB column accepts an empty value.
func stringSliceOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ── IdpRoleMapping repo ───────────────────────────────────────────────────
//
// idp_type was added Go-side in migration 035 (Rust had dropped the
// column); it round-trips on persist/read, but FindByIdpRole deliberately
// does not filter on it — pre-035 rows have NULL idp_type and live
// mappings must keep matching.

type IdpRoleMappingRepo struct{ q *dbq.Queries }

func (r *IdpRoleMappingRepo) FindByID(ctx context.Context, id string) (*IdpRoleMapping, error) {
	row, err := r.q.IdpRoleMappingFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("idp_role_mapping repo: %w", err)
	}
	return rowToIdpMapping(row), nil
}

// FindByIdpRole filters by idp_role_name only — the idp_type argument is
// accepted for API compat but deliberately not filtered on: rows created
// before migration 035 carry NULL idp_type and must keep matching.
func (r *IdpRoleMappingRepo) FindByIdpRole(ctx context.Context, _idpType, idpRoleName string) ([]IdpRoleMapping, error) {
	rows, err := r.q.IdpRoleMappingFindByIdpRole(ctx, idpRoleName)
	if err != nil {
		return nil, err
	}
	out := make([]IdpRoleMapping, 0, len(rows))
	for _, row := range rows {
		out = append(out, *rowToIdpMapping(row))
	}
	return out, nil
}

func (r *IdpRoleMappingRepo) FindAll(ctx context.Context) ([]IdpRoleMapping, error) {
	rows, err := r.q.IdpRoleMappingFindAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IdpRoleMapping, 0, len(rows))
	for _, row := range rows {
		out = append(out, *rowToIdpMapping(row))
	}
	return out, nil
}

func (r *IdpRoleMappingRepo) Persist(ctx context.Context, m *IdpRoleMapping, tx *usecasepgx.DbTx) error {
	var idpType *string
	if m.IdpType != "" {
		idpType = &m.IdpType
	}
	return r.q.WithTx(tx.Inner()).IdpRoleMappingUpsert(ctx, dbq.IdpRoleMappingUpsertParams{
		ID:               m.ID,
		IdpRoleName:      m.IdpRoleName,
		InternalRoleName: m.PlatformRoleName,
		IdpType:          idpType,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        time.Now().UTC(),
	})
}

func (r *IdpRoleMappingRepo) Delete(ctx context.Context, m *IdpRoleMapping, tx *usecasepgx.DbTx) error {
	return r.q.WithTx(tx.Inner()).IdpRoleMappingDelete(ctx, m.ID)
}

func rowToIdpMapping(row dbq.OauthIdpRoleMapping) *IdpRoleMapping {
	idpType := ""
	if row.IdpType != nil {
		idpType = *row.IdpType
	}
	return &IdpRoleMapping{
		ID:               row.ID,
		IdpType:          idpType,
		IdpRoleName:      row.IdpRoleName,
		PlatformRoleName: row.InternalRoleName,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}
