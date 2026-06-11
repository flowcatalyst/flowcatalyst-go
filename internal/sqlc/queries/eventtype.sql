-- Queries for msg_event_types + msg_event_type_spec_versions.
-- Two upsert variants: ON CONFLICT (id) for the canonical path, and
-- ON CONFLICT (code) for the sync use case which keys by code.

-- name: EventTypeFindByID :one
SELECT id, code, name, description, status, source, client_scoped,
       application, subdomain, aggregate, created_at, updated_at, created_by
FROM msg_event_types
WHERE id = $1;

-- name: EventTypeFindByCode :one
SELECT id, code, name, description, status, source, client_scoped,
       application, subdomain, aggregate, created_at, updated_at, created_by
FROM msg_event_types
WHERE code = $1;

-- name: EventTypeFindByApplication :many
SELECT id, code, name, description, status, source, client_scoped,
       application, subdomain, aggregate, created_at, updated_at, created_by
FROM msg_event_types
WHERE application = $1
ORDER BY code;

-- name: EventTypeUpsertByID :exec
INSERT INTO msg_event_types
    (id, code, name, description, status, source, client_scoped,
     application, subdomain, aggregate, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (id) DO UPDATE SET
    code = EXCLUDED.code,
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    status = EXCLUDED.status,
    source = EXCLUDED.source,
    client_scoped = EXCLUDED.client_scoped,
    application = EXCLUDED.application,
    subdomain = EXCLUDED.subdomain,
    aggregate = EXCLUDED.aggregate,
    updated_at = EXCLUDED.updated_at;

-- name: EventTypeUpsertByCode :exec
INSERT INTO msg_event_types
    (id, code, name, description, status, source, client_scoped,
     application, subdomain, aggregate, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (code) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    status = EXCLUDED.status,
    source = EXCLUDED.source,
    client_scoped = EXCLUDED.client_scoped,
    updated_at = EXCLUDED.updated_at;

-- name: EventTypeDelete :exec
DELETE FROM msg_event_types WHERE id = $1;

-- name: SpecVersionsClear :exec
DELETE FROM msg_event_type_spec_versions WHERE event_type_id = $1;

-- name: SpecVersionUpsert :exec
INSERT INTO msg_event_type_spec_versions
    (id, event_type_id, version, mime_type, schema_content, schema_type,
     status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    schema_content = EXCLUDED.schema_content,
    schema_type = EXCLUDED.schema_type,
    status = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at;

-- name: SpecVersionsForEventTypes :many
SELECT id, event_type_id, version, mime_type, schema_content, schema_type,
       status, created_at, updated_at
FROM msg_event_type_spec_versions
WHERE event_type_id = ANY(@event_type_ids::text[])
ORDER BY event_type_id, version;
