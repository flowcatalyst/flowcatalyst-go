-- Queries for the auth subdomain: OAuthClient + AnchorDomain +
-- ClientAuthConfig + IdpRoleMapping aggregates.

-- ── OAuthClient (oauth_clients + 2 junctions wired today) ────────────
-- The schema also has 3 more junctions (post_logout_redirect_uris,
-- allowed_origins, application_ids) that the Go entity doesn't carry
-- yet — they're a follow-up alongside the entity extension.
-- The Go entity stores Argon2-hashed secrets in client_secret_ref
-- (Rust uses it as a secrets-manager reference; Go uses it as the
-- hash). See HANDOFF.md §4 for the planned PHC-salt fix.

-- name: OAuthClientFindByID :one
SELECT id, client_id, client_name, client_type, client_secret_ref,
       default_scopes, pkce_required, service_account_principal_id,
       active, created_at, updated_at
FROM oauth_clients
WHERE id = $1;

-- name: OAuthClientFindByClientID :one
SELECT id, client_id, client_name, client_type, client_secret_ref,
       default_scopes, pkce_required, service_account_principal_id,
       active, created_at, updated_at
FROM oauth_clients
WHERE client_id = $1;

-- name: OAuthClientFindAll :many
SELECT id, client_id, client_name, client_type, client_secret_ref,
       default_scopes, pkce_required, service_account_principal_id,
       active, created_at, updated_at
FROM oauth_clients
ORDER BY client_name;

-- name: OAuthClientUpsert :exec
INSERT INTO oauth_clients
    (id, client_id, client_name, client_type, client_secret_ref,
     default_scopes, pkce_required, service_account_principal_id,
     active, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO UPDATE SET
    client_id = EXCLUDED.client_id,
    client_name = EXCLUDED.client_name,
    client_type = EXCLUDED.client_type,
    client_secret_ref = EXCLUDED.client_secret_ref,
    default_scopes = EXCLUDED.default_scopes,
    pkce_required = EXCLUDED.pkce_required,
    service_account_principal_id = EXCLUDED.service_account_principal_id,
    active = EXCLUDED.active,
    updated_at = EXCLUDED.updated_at;

-- name: OAuthClientDelete :exec
DELETE FROM oauth_clients WHERE id = $1;

-- name: OAuthClientRedirectURIsClear :exec
DELETE FROM oauth_client_redirect_uris WHERE oauth_client_id = $1;

-- name: OAuthClientRedirectURIInsert :exec
INSERT INTO oauth_client_redirect_uris (oauth_client_id, redirect_uri)
VALUES ($1, $2);

-- name: OAuthClientRedirectURIsForClient :many
SELECT redirect_uri FROM oauth_client_redirect_uris
WHERE oauth_client_id = $1;

-- name: OAuthClientRedirectURIsForClients :many
SELECT oauth_client_id, redirect_uri
FROM oauth_client_redirect_uris
WHERE oauth_client_id = ANY($1::varchar[]);

-- name: OAuthClientGrantTypesClear :exec
DELETE FROM oauth_client_grant_types WHERE oauth_client_id = $1;

-- name: OAuthClientGrantTypeInsert :exec
INSERT INTO oauth_client_grant_types (oauth_client_id, grant_type)
VALUES ($1, $2);

-- name: OAuthClientGrantTypesForClient :many
SELECT grant_type FROM oauth_client_grant_types
WHERE oauth_client_id = $1;

-- name: OAuthClientGrantTypesForClients :many
SELECT oauth_client_id, grant_type
FROM oauth_client_grant_types
WHERE oauth_client_id = ANY($1::varchar[]);

-- ── AnchorDomain (tnt_anchor_domains) ────────────────────────────────

-- name: AnchorDomainFindByID :one
SELECT id, domain, created_at, updated_at
FROM tnt_anchor_domains
WHERE id = $1;

-- name: AnchorDomainFindByDomain :one
SELECT id, domain, created_at, updated_at
FROM tnt_anchor_domains
WHERE domain = $1;

-- name: AnchorDomainFindAll :many
SELECT id, domain, created_at, updated_at
FROM tnt_anchor_domains
ORDER BY domain;

-- name: AnchorDomainUpsert :exec
INSERT INTO tnt_anchor_domains (id, domain, created_at, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET
    domain = EXCLUDED.domain,
    updated_at = EXCLUDED.updated_at;

-- name: AnchorDomainDelete :exec
DELETE FROM tnt_anchor_domains WHERE id = $1;

-- ── ClientAuthConfig (tnt_client_auth_configs) ───────────────────────

-- name: ClientAuthConfigFindByID :one
SELECT id, email_domain, config_type, primary_client_id,
       additional_client_ids, granted_client_ids,
       auth_provider, oidc_issuer_url, oidc_client_id, oidc_multi_tenant,
       oidc_issuer_pattern, oidc_client_secret_ref, created_at, updated_at
FROM tnt_client_auth_configs
WHERE id = $1;

-- name: ClientAuthConfigFindByEmailDomain :one
SELECT id, email_domain, config_type, primary_client_id,
       additional_client_ids, granted_client_ids,
       auth_provider, oidc_issuer_url, oidc_client_id, oidc_multi_tenant,
       oidc_issuer_pattern, oidc_client_secret_ref, created_at, updated_at
FROM tnt_client_auth_configs
WHERE email_domain = $1;

-- name: ClientAuthConfigFindAll :many
SELECT id, email_domain, config_type, primary_client_id,
       additional_client_ids, granted_client_ids,
       auth_provider, oidc_issuer_url, oidc_client_id, oidc_multi_tenant,
       oidc_issuer_pattern, oidc_client_secret_ref, created_at, updated_at
FROM tnt_client_auth_configs
ORDER BY email_domain;

-- name: ClientAuthConfigUpsert :exec
INSERT INTO tnt_client_auth_configs
    (id, email_domain, config_type, primary_client_id,
     additional_client_ids, granted_client_ids,
     auth_provider, oidc_issuer_url, oidc_client_id, oidc_multi_tenant,
     oidc_issuer_pattern, oidc_client_secret_ref, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (id) DO UPDATE SET
    email_domain = EXCLUDED.email_domain,
    config_type = EXCLUDED.config_type,
    primary_client_id = EXCLUDED.primary_client_id,
    additional_client_ids = EXCLUDED.additional_client_ids,
    granted_client_ids = EXCLUDED.granted_client_ids,
    auth_provider = EXCLUDED.auth_provider,
    oidc_issuer_url = EXCLUDED.oidc_issuer_url,
    oidc_client_id = EXCLUDED.oidc_client_id,
    oidc_multi_tenant = EXCLUDED.oidc_multi_tenant,
    oidc_issuer_pattern = EXCLUDED.oidc_issuer_pattern,
    oidc_client_secret_ref = EXCLUDED.oidc_client_secret_ref,
    updated_at = EXCLUDED.updated_at;

-- name: ClientAuthConfigDelete :exec
DELETE FROM tnt_client_auth_configs WHERE id = $1;

-- ── IdpRoleMapping (oauth_idp_role_mappings) ─────────────────────────
-- idp_type was added Go-side in migration 035 (Rust had dropped the
-- column, so its rows read back NULL). It is persisted and echoed, but
-- FindByIdpRole deliberately does NOT filter on it — pre-035 rows have
-- NULL idp_type and live mappings must keep matching.

-- name: IdpRoleMappingFindByID :one
SELECT id, idp_role_name, internal_role_name, created_at, updated_at, idp_type
FROM oauth_idp_role_mappings
WHERE id = $1;

-- name: IdpRoleMappingFindByIdpRole :many
SELECT id, idp_role_name, internal_role_name, created_at, updated_at, idp_type
FROM oauth_idp_role_mappings
WHERE idp_role_name = $1
ORDER BY internal_role_name;

-- name: IdpRoleMappingFindAll :many
SELECT id, idp_role_name, internal_role_name, created_at, updated_at, idp_type
FROM oauth_idp_role_mappings
ORDER BY idp_role_name;

-- name: IdpRoleMappingUpsert :exec
INSERT INTO oauth_idp_role_mappings
    (id, idp_role_name, internal_role_name, idp_type, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    internal_role_name = EXCLUDED.internal_role_name,
    idp_type = EXCLUDED.idp_type,
    updated_at = EXCLUDED.updated_at;

-- name: IdpRoleMappingDelete :exec
DELETE FROM oauth_idp_role_mappings WHERE id = $1;
