-- Queries for iam_service_accounts. Webhook credentials are stored as
-- separate columns (wh_auth_type, wh_auth_token_ref, wh_signing_secret_ref,
-- wh_signing_algorithm) matching Rust. The repository maps the flat
-- columns into a single WebhookCredentials struct in the aggregate.

-- name: ServiceAccountFindByID :one
SELECT id, code, name, description, application_id, active,
       wh_auth_type, wh_auth_token_ref, wh_signing_secret_ref,
       wh_signing_algorithm, wh_credentials_created_at,
       wh_credentials_regenerated_at, last_used_at, created_at, updated_at,
       scope, client_ids
FROM iam_service_accounts
WHERE id = $1;

-- name: ServiceAccountFindByCode :one
SELECT id, code, name, description, application_id, active,
       wh_auth_type, wh_auth_token_ref, wh_signing_secret_ref,
       wh_signing_algorithm, wh_credentials_created_at,
       wh_credentials_regenerated_at, last_used_at, created_at, updated_at,
       scope, client_ids
FROM iam_service_accounts
WHERE code = $1;

-- name: ServiceAccountFindAll :many
SELECT id, code, name, description, application_id, active,
       wh_auth_type, wh_auth_token_ref, wh_signing_secret_ref,
       wh_signing_algorithm, wh_credentials_created_at,
       wh_credentials_regenerated_at, last_used_at, created_at, updated_at,
       scope, client_ids
FROM iam_service_accounts
ORDER BY code;

-- name: ServiceAccountUpsert :exec
INSERT INTO iam_service_accounts
    (id, code, name, description, application_id, scope, client_ids, active,
     wh_auth_type, wh_auth_token_ref, wh_signing_secret_ref,
     wh_signing_algorithm, wh_credentials_created_at,
     wh_credentials_regenerated_at, last_used_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    application_id = EXCLUDED.application_id,
    scope = EXCLUDED.scope,
    client_ids = EXCLUDED.client_ids,
    active = EXCLUDED.active,
    wh_auth_type = EXCLUDED.wh_auth_type,
    wh_auth_token_ref = EXCLUDED.wh_auth_token_ref,
    wh_signing_secret_ref = EXCLUDED.wh_signing_secret_ref,
    wh_signing_algorithm = EXCLUDED.wh_signing_algorithm,
    wh_credentials_created_at = EXCLUDED.wh_credentials_created_at,
    wh_credentials_regenerated_at = EXCLUDED.wh_credentials_regenerated_at,
    last_used_at = EXCLUDED.last_used_at,
    updated_at = EXCLUDED.updated_at;

-- name: ServiceAccountDelete :exec
DELETE FROM iam_service_accounts WHERE id = $1;
