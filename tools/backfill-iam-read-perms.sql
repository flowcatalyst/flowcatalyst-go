-- backfill-iam-read-perms.sql — OPTIONAL drop-in helper (Go↔Rust IAM parity).
--
-- WHAT THIS IS FOR
-- The Go platform gates the IAM principal-read endpoints (GET /api/principals,
-- /api/principals/{id}, …/roles, …/application-access, …) on the permission
-- string `platform:iam:user:view`, then membership-filters the rows so a caller
-- only ever sees principals in clients they can access. The Rust platform gated
-- the same reads purely on client membership (is_anchor || can_access_client),
-- with no permission check.
--
-- For ANCHOR principals the two are identical (anchors bypass the permission
-- gate). For holders of any BUILT-IN role the two are identical too — the
-- built-in roles (iam-admin, iam-readonly, viewer, admin, admin-readonly) carry
-- `platform:iam:user:view`, and Go re-syncs those roles from its code catalogue
-- on every boot, so they self-heal when Go is dropped in over a Rust DB.
--
-- The ONLY residual gap: a NON-anchor, client-scoped principal who on Rust could
-- read principals *within their own client* via membership while holding only a
-- USER-CREATED role that lacks `platform:iam:user:view`. On Go that principal
-- gets 403 PERMISSION_REQUIRED. This script closes that gap WITHOUT changing
-- Go's authz model, by granting `platform:iam:user:view` to user-created roles.
--
-- WHY ONLY `platform:iam:user:view` (and not role/service-account/application
-- view): those list endpoints are permission-gated but NOT membership-filtered
-- in Go — a holder sees ALL roles/service-accounts/applications globally. They
-- are platform-administration reads (anchor/admin only on both Go and Rust), so
-- granting them to client-scoped roles would over-expose cross-tenant data.
-- Principal reads are the only membership-FILTERED IAM read, hence the only one
-- safe to backfill: the filter guarantees no cross-tenant leak (matches Rust).
--
-- WHY DATABASE-only: CODE (built-in) roles are reconciled from PlatformRoles()
-- on every boot and SDK roles from the SDK's declared catalogue on every /sync —
-- a grant to either would be reverted. DATABASE (user-created) roles are not
-- reconciled, so the grant persists.
--
-- SAFE + IDEMPOTENT: additive only (no DROP/ALTER), re-runnable (ON CONFLICT on
-- the (role_id, permission) primary key). OPTIONAL: run once against an existing
-- DB only if you have custom client-admin roles whose holders read their own
-- client's principals. Review the affected roles with the SELECT at the bottom
-- before running.
--
-- NOTE (pre-existing Go behaviour, unchanged by this script): Go's principal
-- list also surfaces platform-level principals (client_id IS NULL) to any
-- view-perm holder, whereas Rust shows those only to anchors. If that matters,
-- align the filter in internal/platform/principal/api/api.go (separate change).

INSERT INTO iam_role_permissions (role_id, permission)
SELECT r.id, 'platform:iam:user:view'
FROM iam_roles r
WHERE r.source = 'DATABASE'
ON CONFLICT (role_id, permission) DO NOTHING;

-- Preview the user-created roles this touches (run before/after):
--   SELECT id, name, source FROM iam_roles WHERE source = 'DATABASE' ORDER BY name;
