-- Queries for tnt_email_domain_mappings + its junction tables
-- (additional_clients, granted_clients, allowed_roles, 2fa_methods).
-- None of the junctions declare FK ON DELETE CASCADE, so Delete must
-- clean them explicitly. Mirrors the Rust impl (2fa_methods is Go-only).

-- name: EmailDomainMappingFindByID :one
SELECT id, email_domain, identity_provider_id, scope_type, primary_client_id,
       required_oidc_tenant_id, sync_roles_from_idp, created_at, updated_at,
       require_2fa, remember_device_enabled, remember_device_days
FROM tnt_email_domain_mappings
WHERE id = $1;

-- name: EmailDomainMappingFindByDomain :one
SELECT id, email_domain, identity_provider_id, scope_type, primary_client_id,
       required_oidc_tenant_id, sync_roles_from_idp, created_at, updated_at,
       require_2fa, remember_device_enabled, remember_device_days
FROM tnt_email_domain_mappings
WHERE email_domain = $1;

-- name: EmailDomainMappingFindAll :many
SELECT id, email_domain, identity_provider_id, scope_type, primary_client_id,
       required_oidc_tenant_id, sync_roles_from_idp, created_at, updated_at,
       require_2fa, remember_device_enabled, remember_device_days
FROM tnt_email_domain_mappings
ORDER BY email_domain;

-- name: EmailDomainMappingUpsert :exec
INSERT INTO tnt_email_domain_mappings
    (id, email_domain, identity_provider_id, scope_type, primary_client_id,
     required_oidc_tenant_id, sync_roles_from_idp, require_2fa,
     remember_device_enabled, remember_device_days, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO UPDATE SET
    email_domain = EXCLUDED.email_domain,
    identity_provider_id = EXCLUDED.identity_provider_id,
    scope_type = EXCLUDED.scope_type,
    primary_client_id = EXCLUDED.primary_client_id,
    required_oidc_tenant_id = EXCLUDED.required_oidc_tenant_id,
    sync_roles_from_idp = EXCLUDED.sync_roles_from_idp,
    require_2fa = EXCLUDED.require_2fa,
    remember_device_enabled = EXCLUDED.remember_device_enabled,
    remember_device_days = EXCLUDED.remember_device_days,
    updated_at = EXCLUDED.updated_at;

-- name: EmailDomainMappingDelete :exec
DELETE FROM tnt_email_domain_mappings WHERE id = $1;

-- ── junctions: clear + insert ─────────────────────────────────────────

-- name: EmailDomainMappingAdditionalClientsClear :exec
DELETE FROM tnt_email_domain_mapping_additional_clients
WHERE email_domain_mapping_id = $1;

-- name: EmailDomainMappingGrantedClientsClear :exec
DELETE FROM tnt_email_domain_mapping_granted_clients
WHERE email_domain_mapping_id = $1;

-- name: EmailDomainMappingAllowedRolesClear :exec
DELETE FROM tnt_email_domain_mapping_allowed_roles
WHERE email_domain_mapping_id = $1;

-- name: EmailDomainMapping2FAMethodsClear :exec
DELETE FROM tnt_email_domain_mapping_2fa_methods
WHERE email_domain_mapping_id = $1;

-- name: EmailDomainMappingAdditionalClientInsert :exec
INSERT INTO tnt_email_domain_mapping_additional_clients
    (email_domain_mapping_id, client_id)
VALUES ($1, $2);

-- name: EmailDomainMappingGrantedClientInsert :exec
INSERT INTO tnt_email_domain_mapping_granted_clients
    (email_domain_mapping_id, client_id)
VALUES ($1, $2);

-- name: EmailDomainMappingAllowedRoleInsert :exec
INSERT INTO tnt_email_domain_mapping_allowed_roles
    (email_domain_mapping_id, role_id)
VALUES ($1, $2);

-- name: EmailDomainMapping2FAMethodInsert :exec
INSERT INTO tnt_email_domain_mapping_2fa_methods
    (email_domain_mapping_id, method)
VALUES ($1, $2);

-- ── junctions: batch hydrate via ANY($1) ─────────────────────────────

-- name: EmailDomainMappingAdditionalClientsForMappings :many
SELECT email_domain_mapping_id, client_id
FROM tnt_email_domain_mapping_additional_clients
WHERE email_domain_mapping_id = ANY($1::varchar[]);

-- name: EmailDomainMappingGrantedClientsForMappings :many
SELECT email_domain_mapping_id, client_id
FROM tnt_email_domain_mapping_granted_clients
WHERE email_domain_mapping_id = ANY($1::varchar[]);

-- name: EmailDomainMappingAllowedRolesForMappings :many
SELECT email_domain_mapping_id, role_id
FROM tnt_email_domain_mapping_allowed_roles
WHERE email_domain_mapping_id = ANY($1::varchar[]);

-- name: EmailDomainMapping2FAMethodsForMappings :many
SELECT email_domain_mapping_id, method
FROM tnt_email_domain_mapping_2fa_methods
WHERE email_domain_mapping_id = ANY($1::varchar[]);
