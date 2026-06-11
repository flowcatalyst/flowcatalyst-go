-- Queries for msg_subscriptions + msg_subscription_event_types +
-- msg_subscription_custom_configs.
--
-- Schema columns differ from the previous Go port in several places:
--   - target (not endpoint)
--   - msg_subscription_event_types has no filter column
--   - msg_subscription_custom_configs uses config_key/config_value (not key/value)
-- All of these were silent runtime bugs in the pre-sqlc repo.
-- created_by was added Go-side in migration 035 (Rust never had it; its
-- rows read back NULL).

-- name: SubscriptionFindByID :one
SELECT id, code, application_code, name, description, client_id,
       client_identifier, client_scoped, target, queue,
       source, status, max_age_seconds, dispatch_pool_id, dispatch_pool_code,
       delay_seconds, sequence, mode, timeout_seconds, max_retries,
       service_account_id, data_only, created_at, updated_at, connection_id, created_by
FROM msg_subscriptions
WHERE id = $1;

-- name: SubscriptionFindByCodeClient :one
SELECT id, code, application_code, name, description, client_id,
       client_identifier, client_scoped, target, queue,
       source, status, max_age_seconds, dispatch_pool_id, dispatch_pool_code,
       delay_seconds, sequence, mode, timeout_seconds, max_retries,
       service_account_id, data_only, created_at, updated_at, connection_id, created_by
FROM msg_subscriptions
WHERE code = $1 AND client_id = $2;

-- name: SubscriptionFindByCodeAnchor :one
SELECT id, code, application_code, name, description, client_id,
       client_identifier, client_scoped, target, queue,
       source, status, max_age_seconds, dispatch_pool_id, dispatch_pool_code,
       delay_seconds, sequence, mode, timeout_seconds, max_retries,
       service_account_id, data_only, created_at, updated_at, connection_id, created_by
FROM msg_subscriptions
WHERE code = $1 AND client_id IS NULL;

-- name: SubscriptionFindAll :many
SELECT id, code, application_code, name, description, client_id,
       client_identifier, client_scoped, target, queue,
       source, status, max_age_seconds, dispatch_pool_id, dispatch_pool_code,
       delay_seconds, sequence, mode, timeout_seconds, max_retries,
       service_account_id, data_only, created_at, updated_at, connection_id, created_by
FROM msg_subscriptions
ORDER BY code;

-- name: SubscriptionUpsert :exec
INSERT INTO msg_subscriptions
    (id, code, application_code, name, description, client_id, client_identifier,
     client_scoped, connection_id, target, queue, source, status, max_age_seconds,
     dispatch_pool_id, dispatch_pool_code, delay_seconds, sequence, mode,
     timeout_seconds, max_retries, service_account_id, data_only,
     created_by, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    target = EXCLUDED.target,
    status = EXCLUDED.status,
    max_age_seconds = EXCLUDED.max_age_seconds,
    dispatch_pool_id = EXCLUDED.dispatch_pool_id,
    dispatch_pool_code = EXCLUDED.dispatch_pool_code,
    delay_seconds = EXCLUDED.delay_seconds,
    sequence = EXCLUDED.sequence,
    mode = EXCLUDED.mode,
    timeout_seconds = EXCLUDED.timeout_seconds,
    max_retries = EXCLUDED.max_retries,
    service_account_id = EXCLUDED.service_account_id,
    data_only = EXCLUDED.data_only,
    updated_at = EXCLUDED.updated_at;

-- name: SubscriptionDelete :exec
DELETE FROM msg_subscriptions WHERE id = $1;

-- name: SubscriptionEventTypesClear :exec
DELETE FROM msg_subscription_event_types WHERE subscription_id = $1;

-- name: SubscriptionEventTypeInsert :exec
INSERT INTO msg_subscription_event_types
    (subscription_id, event_type_id, event_type_code, spec_version)
VALUES (@subscription_id, @event_type_id, @event_type_code, @spec_version);

-- name: SubscriptionEventTypesForSubs :many
SELECT subscription_id, event_type_id, event_type_code, spec_version
FROM msg_subscription_event_types
WHERE subscription_id = ANY(@subscription_ids::text[]);

-- name: SubscriptionConfigsClear :exec
DELETE FROM msg_subscription_custom_configs WHERE subscription_id = $1;

-- name: SubscriptionConfigInsert :exec
INSERT INTO msg_subscription_custom_configs
    (subscription_id, config_key, config_value)
VALUES (@subscription_id, @config_key, @config_value);

-- name: SubscriptionConfigsForSubs :many
SELECT subscription_id, config_key, config_value
FROM msg_subscription_custom_configs
WHERE subscription_id = ANY(@subscription_ids::text[]);
