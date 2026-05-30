// Package auth provides the authenticated-request context and the
// permission-check helpers used by every write handler. Mirrors the
// Rust shared::authorization_service::checks namespace.
//
// Conventions (see docs/conventions.md §1):
//   - CanRead<Resource>(ctx)   for GET
//   - CanCreate/Update/Delete  for the specific verbs
//   - CanWrite<Resource>       for any of create/update/delete
//   - RequireAnchor(ctx)       for anchor-only endpoints
//   - IsAdmin(ctx)             anchor OR the super-admin wildcard
//
// The check functions return a usecase.Error (Kind=Authorization) on
// failure so handlers can httperror.Write(err) without branching.
//
// Permission strings are the 4-segment `platform:<context>:<resource>:<action>`
// identifiers that are actually stored in iam_role_permissions and pinned by
// the SDK — byte-identical to seed/permissions.go and Rust role/entity.rs.
// HasPermission matches them with `*` segment wildcards, so a role holding
// `platform:messaging:*:*` (or the super-admin `platform:*:*:*`) satisfies a
// concrete check. (The earlier short logical names like "READ_EVENT_TYPES"
// never matched any stored permission, locking out every non-anchor principal.)
package auth

import (
	"context"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

// Scope is the principal's top-level scope. Matches Rust UserScope.
type Scope string

const (
	ScopeAnchor  Scope = "ANCHOR"
	ScopePartner Scope = "PARTNER"
	ScopeClient  Scope = "CLIENT"
)

// Permission identifiers used by the typed checks below. These MUST match
// seed/permissions.go (which mirrors Rust role/entity.rs::permissions) — the
// stored iam_role_permissions rows reference exactly these strings.
const (
	// EventType (messaging context)
	permEventTypeView   = "platform:messaging:event-type:view"
	permEventTypeCreate = "platform:messaging:event-type:create"
	permEventTypeUpdate = "platform:messaging:event-type:update"
	permEventTypeDelete = "platform:messaging:event-type:delete"
	permEventTypeSync   = "platform:messaging:event-type:sync"
	permEventTypeManage = "platform:messaging:event-type:manage"
	// Connection (messaging)
	permConnectionView   = "platform:messaging:connection:view"
	permConnectionCreate = "platform:messaging:connection:create"
	permConnectionUpdate = "platform:messaging:connection:update"
	permConnectionDelete = "platform:messaging:connection:delete"
	// Subscription (messaging)
	permSubscriptionView   = "platform:messaging:subscription:view"
	permSubscriptionCreate = "platform:messaging:subscription:create"
	permSubscriptionUpdate = "platform:messaging:subscription:update"
	permSubscriptionDelete = "platform:messaging:subscription:delete"
	permSubscriptionSync   = "platform:messaging:subscription:sync"
	permSubscriptionManage = "platform:messaging:subscription:manage"
	// DispatchPool (messaging)
	permDispatchPoolView   = "platform:messaging:dispatch-pool:view"
	permDispatchPoolCreate = "platform:messaging:dispatch-pool:create"
	permDispatchPoolUpdate = "platform:messaging:dispatch-pool:update"
	permDispatchPoolDelete = "platform:messaging:dispatch-pool:delete"
	permDispatchPoolSync   = "platform:messaging:dispatch-pool:sync"
	permDispatchPoolManage = "platform:messaging:dispatch-pool:manage"
	// Process (messaging)
	permProcessView   = "platform:messaging:process:view"
	permProcessCreate = "platform:messaging:process:create"
	permProcessUpdate = "platform:messaging:process:update"
	permProcessDelete = "platform:messaging:process:delete"
	permProcessSync   = "platform:messaging:process:sync"
	// Application (admin)
	permApplicationView   = "platform:admin:application:view"
	permApplicationCreate = "platform:admin:application:create"
	permApplicationUpdate = "platform:admin:application:update"
	permApplicationDelete = "platform:admin:application:delete"
	// Role (iam)
	permRoleView   = "platform:iam:role:view"
	permRoleCreate = "platform:iam:role:create"
	permRoleUpdate = "platform:iam:role:update"
	permRoleDelete = "platform:iam:role:delete"
	permRoleManage = "platform:iam:role:manage"
	// Application-service permissions. These are held by SDK service
	// accounts so an application can self-register its own resources via
	// the /api/applications/{appCode}/{resource}/sync endpoints. They sit
	// in the dedicated application-service context (not messaging/iam).
	permAppSvcEventTypeCreate    = "platform:application-service:event-type:create"
	permAppSvcEventTypeUpdate    = "platform:application-service:event-type:update"
	permAppSvcEventTypeDelete    = "platform:application-service:event-type:delete"
	permAppSvcRoleCreate         = "platform:application-service:role:create"
	permAppSvcRoleUpdate         = "platform:application-service:role:update"
	permAppSvcRoleDelete         = "platform:application-service:role:delete"
	permAppSvcProcessSync        = "platform:application-service:process:sync"
	permAppSvcSubscriptionCreate = "platform:application-service:subscription:create"
	permAppSvcSubscriptionUpdate = "platform:application-service:subscription:update"
	permAppSvcSubscriptionDelete = "platform:application-service:subscription:delete"
	permAppSvcScheduledJobSync   = "platform:application-service:scheduled-job:sync"
	// Developer (application OpenAPI documents)
	permAppOpenApiSync   = "platform:developer:application-openapi:sync"
	permAppOpenApiManage = "platform:developer:application-openapi:manage"
	// ServiceAccount (iam)
	permServiceAccountView   = "platform:iam:service-account:view"
	permServiceAccountCreate = "platform:iam:service-account:create"
	permServiceAccountUpdate = "platform:iam:service-account:update"
	permServiceAccountDelete = "platform:iam:service-account:delete"
	// User / Principal (iam)
	permUserView        = "platform:iam:user:view"
	permUserCreate      = "platform:iam:user:create"
	permUserUpdate      = "platform:iam:user:update"
	permUserDelete      = "platform:iam:user:delete"
	permUserManage      = "platform:iam:user:manage"
	permUserAssignRoles = "platform:iam:user:assign-roles"
	// ScheduledJob (messaging)
	permScheduledJobView   = "platform:messaging:scheduled-job:view"
	permScheduledJobCreate = "platform:messaging:scheduled-job:create"
	permScheduledJobUpdate = "platform:messaging:scheduled-job:update"
	permScheduledJobDelete = "platform:messaging:scheduled-job:delete"
	permScheduledJobFire   = "platform:messaging:scheduled-job:fire"
	permScheduledJobSync   = "platform:messaging:scheduled-job:sync"
	permScheduledJobManage = "platform:messaging:scheduled-job:manage"
	// Super-admin wildcard.
	permSuperAdmin = "platform:*:*:*"
)

// AuthContext carries the authenticated principal across the request.
// Attached to the request context by the auth middleware; handlers and
// use cases retrieve it via FromContext.
type AuthContext struct {
	PrincipalID string
	Email       string
	Scope       Scope
	// Clients is the set of tenant IDs this principal can access.
	Clients []string
	// Roles is the set of role codes assigned to this principal.
	Roles []string
	// Applications is the set of application IDs in scope.
	Applications []string
	// Permissions is the flattened set of permission codes from all roles.
	Permissions []string
}

// IsAnchor reports whether the principal has anchor scope.
func (a *AuthContext) IsAnchor() bool { return a.Scope == ScopeAnchor }

// IsSuperAdmin reports whether the principal holds the super-admin wildcard
// permission (platform:*:*:*). Mirrors Rust has_permission(ADMIN_ALL) — used
// by handlers (e.g. SDK openapi sync) that gate on the admin-all grant.
func (a *AuthContext) IsSuperAdmin() bool { return a.HasPermission(permSuperAdmin) }

// CanAccessClient reports whether the principal has access to a specific tenant.
func (a *AuthContext) CanAccessClient(clientID string) bool {
	if a.IsAnchor() {
		return true
	}
	for _, c := range a.Clients {
		if c == clientID {
			return true
		}
	}
	return false
}

// HasPermission reports whether the principal carries a permission that
// satisfies code. A held permission may contain `*` segment wildcards
// (e.g. a role granting `platform:messaging:*:*`, or super-admin
// `platform:*:*:*`); such a permission matches any concrete code with the
// same segment count whose non-wildcard segments are equal.
func (a *AuthContext) HasPermission(code string) bool {
	for _, p := range a.Permissions {
		if permissionMatches(p, code) {
			return true
		}
	}
	return false
}

// permissionMatches reports whether a held permission pattern satisfies the
// required permission. Segment counts must match; each held segment must be
// "*" or equal the required segment. 1:1 with Rust matches_pattern.
func permissionMatches(held, required string) bool {
	if held == required {
		return true
	}
	h := strings.Split(held, ":")
	r := strings.Split(required, ":")
	if len(h) != len(r) {
		return false
	}
	for i := range h {
		if h[i] != "*" && h[i] != r[i] {
			return false
		}
	}
	return true
}

// ctxKey is the private context key for AuthContext.
type ctxKey struct{}

// WithContext attaches an AuthContext to ctx. The auth middleware calls this.
func WithContext(ctx context.Context, a *AuthContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// FromContext retrieves the AuthContext, or nil if none is attached.
func FromContext(ctx context.Context) *AuthContext {
	v, _ := ctx.Value(ctxKey{}).(*AuthContext)
	return v
}

// ── Check helpers ──────────────────────────────────────────────────────────

// RequireAnchor errors if the principal is not anchor-scoped.
func RequireAnchor(a *AuthContext) error {
	if a == nil {
		return usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	if !a.IsAnchor() {
		return usecase.Authorization("ANCHOR_REQUIRED", "anchor scope required")
	}
	return nil
}

// IsAdmin returns nil if the principal is anchor-scoped or holds the
// super-admin wildcard.
func IsAdmin(a *AuthContext) error {
	if a == nil {
		return usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	if a.IsAnchor() || a.HasPermission(permSuperAdmin) {
		return nil
	}
	return usecase.Authorization("ADMIN_REQUIRED", "admin permission required")
}

// CanAccessScope reports whether the caller may access a resource owned by the
// given client (nil clientID = platform-level → anchor/super-admin only). The
// boolean form of CheckScopeAccess, for filtering list results.
func CanAccessScope(a *AuthContext, clientID *string) bool {
	if a == nil {
		return false
	}
	if clientID != nil {
		return a.CanAccessClient(*clientID)
	}
	return a.IsAnchor() || a.IsSuperAdmin()
}

// CheckScopeAccess enforces per-resource scope on a by-id operation, on top of
// the coarse Can* permission check: a client/tenant-scoped resource (clientID
// non-nil) requires the caller can access that client; a platform-level
// resource (clientID nil) requires anchor or super-admin. Without this a
// non-anchor principal holding e.g. "update subscriptions" could mutate another
// tenant's resource by guessing its id. 1:1 with Rust check_scope_access.
func CheckScopeAccess(a *AuthContext, clientID *string) error {
	if a == nil {
		return usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	if CanAccessScope(a, clientID) {
		return nil
	}
	if clientID != nil {
		return usecase.Authorization("SCOPE_FORBIDDEN", "no access to this resource's client")
	}
	return usecase.Authorization("SCOPE_FORBIDDEN", "anchor scope required for this resource")
}

// requirePermission is the generic helper.
func requirePermission(a *AuthContext, perm string) error {
	if a == nil {
		return usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	if a.IsAnchor() || a.HasPermission(perm) {
		return nil
	}
	return usecase.Authorization("PERMISSION_REQUIRED", "permission required: "+perm)
}

// CanWritePermission is the public alias used by ad-hoc handlers (e.g.
// SDK batch ingest) where the permission name isn't covered by a typed
// Can* helper. Callers pass the full 4-segment permission string.
func CanWritePermission(a *AuthContext, perm string) error { return requirePermission(a, perm) }

// requireAny returns nil if the principal has ANY of perms.
func requireAny(a *AuthContext, perms ...string) error {
	if a == nil {
		return usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	if a.IsAnchor() {
		return nil
	}
	for _, p := range perms {
		if a.HasPermission(p) {
			return nil
		}
	}
	return usecase.Authorization("PERMISSION_REQUIRED", "one of: "+joinPerms(perms))
}

func joinPerms(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// ── EventType permission checks ───────────────────────────────────────────
func CanReadEventTypes(a *AuthContext) error   { return requirePermission(a, permEventTypeView) }
func CanCreateEventTypes(a *AuthContext) error { return requirePermission(a, permEventTypeCreate) }
func CanUpdateEventTypes(a *AuthContext) error { return requirePermission(a, permEventTypeUpdate) }
func CanDeleteEventTypes(a *AuthContext) error { return requirePermission(a, permEventTypeDelete) }

// CanSyncEventTypes guards POST /api/applications/{appCode}/event-types/sync.
// Mirrors Rust can_sync_event_types: admits admin sync/manage plus the
// application-service create/update/delete permissions an SDK service
// account holds. Per-application scope is enforced inside the use case.
func CanSyncEventTypes(a *AuthContext) error {
	return requireAny(a,
		permEventTypeSync, permEventTypeManage,
		permAppSvcEventTypeCreate, permAppSvcEventTypeUpdate, permAppSvcEventTypeDelete)
}
func CanWriteEventTypes(a *AuthContext) error {
	return requireAny(a, permEventTypeCreate, permEventTypeUpdate, permEventTypeDelete)
}

// ── Connection permissions ───────────────────────────────────────────────
func CanReadConnections(a *AuthContext) error   { return requirePermission(a, permConnectionView) }
func CanCreateConnections(a *AuthContext) error { return requirePermission(a, permConnectionCreate) }
func CanUpdateConnections(a *AuthContext) error { return requirePermission(a, permConnectionUpdate) }
func CanDeleteConnections(a *AuthContext) error { return requirePermission(a, permConnectionDelete) }
func CanWriteConnections(a *AuthContext) error {
	return requireAny(a, permConnectionCreate, permConnectionUpdate, permConnectionDelete)
}

// ── Subscription permissions ─────────────────────────────────────────────
func CanReadSubscriptions(a *AuthContext) error { return requirePermission(a, permSubscriptionView) }
func CanCreateSubscriptions(a *AuthContext) error {
	return requirePermission(a, permSubscriptionCreate)
}
func CanUpdateSubscriptions(a *AuthContext) error {
	return requirePermission(a, permSubscriptionUpdate)
}
func CanDeleteSubscriptions(a *AuthContext) error {
	return requirePermission(a, permSubscriptionDelete)
}
func CanWriteSubscriptions(a *AuthContext) error {
	return requireAny(a, permSubscriptionCreate, permSubscriptionUpdate, permSubscriptionDelete)
}

// ── Dispatch pool permissions ────────────────────────────────────────────
func CanReadDispatchPools(a *AuthContext) error { return requirePermission(a, permDispatchPoolView) }
func CanCreateDispatchPools(a *AuthContext) error {
	return requirePermission(a, permDispatchPoolCreate)
}
func CanUpdateDispatchPools(a *AuthContext) error {
	return requirePermission(a, permDispatchPoolUpdate)
}
func CanDeleteDispatchPools(a *AuthContext) error {
	return requirePermission(a, permDispatchPoolDelete)
}
func CanWriteDispatchPools(a *AuthContext) error {
	return requireAny(a, permDispatchPoolCreate, permDispatchPoolUpdate, permDispatchPoolDelete)
}

// ── Process permissions ──────────────────────────────────────────────────
func CanReadProcesses(a *AuthContext) error   { return requirePermission(a, permProcessView) }
func CanCreateProcesses(a *AuthContext) error { return requirePermission(a, permProcessCreate) }
func CanUpdateProcesses(a *AuthContext) error { return requirePermission(a, permProcessUpdate) }
func CanDeleteProcesses(a *AuthContext) error { return requirePermission(a, permProcessDelete) }
func CanWriteProcesses(a *AuthContext) error {
	return requireAny(a, permProcessCreate, permProcessUpdate, permProcessDelete)
}

// ── Application permissions ──────────────────────────────────────────────
func CanReadApplications(a *AuthContext) error   { return requirePermission(a, permApplicationView) }
func CanCreateApplications(a *AuthContext) error { return requirePermission(a, permApplicationCreate) }
func CanUpdateApplications(a *AuthContext) error { return requirePermission(a, permApplicationUpdate) }
func CanDeleteApplications(a *AuthContext) error { return requirePermission(a, permApplicationDelete) }
func CanWriteApplications(a *AuthContext) error {
	return requireAny(a, permApplicationCreate, permApplicationUpdate, permApplicationDelete)
}

// ── Role permissions ─────────────────────────────────────────────────────
func CanReadRoles(a *AuthContext) error   { return requirePermission(a, permRoleView) }
func CanCreateRoles(a *AuthContext) error { return requirePermission(a, permRoleCreate) }
func CanUpdateRoles(a *AuthContext) error { return requirePermission(a, permRoleUpdate) }
func CanDeleteRoles(a *AuthContext) error { return requirePermission(a, permRoleDelete) }
func CanWriteRoles(a *AuthContext) error {
	return requireAny(a, permRoleCreate, permRoleUpdate, permRoleDelete)
}

// CanSyncRoles guards POST /api/applications/{appCode}/roles/sync. Mirrors
// Rust can_sync_roles: admits the iam manage/create/update/delete tier plus
// the application-service create/update/delete permissions an SDK service
// account holds. Per-application scope is enforced inside the use case.
func CanSyncRoles(a *AuthContext) error {
	return requireAny(a,
		permRoleManage, permRoleCreate, permRoleUpdate, permRoleDelete,
		permAppSvcRoleCreate, permAppSvcRoleUpdate, permAppSvcRoleDelete)
}

// CanSyncSubscriptions guards POST /api/applications/{appCode}/subscriptions/sync.
// Mirrors Rust can_sync_subscriptions: admin sync/manage plus the
// application-service create/update/delete permissions an SDK service account
// holds. Per-application scope is enforced inside the use case.
func CanSyncSubscriptions(a *AuthContext) error {
	return requireAny(a,
		permSubscriptionSync, permSubscriptionManage,
		permAppSvcSubscriptionCreate, permAppSvcSubscriptionUpdate, permAppSvcSubscriptionDelete)
}

// CanSyncPrincipals guards POST /api/applications/{appCode}/principals/sync.
// Mirrors Rust can_sync_principals: admin-tier IAM user manage/create/update/
// delete or assign-roles (no application-service grant — users are global).
func CanSyncPrincipals(a *AuthContext) error {
	return requireAny(a,
		permUserManage, permUserCreate, permUserUpdate, permUserDelete, permUserAssignRoles)
}

// CanSyncScheduledJobs guards POST /api/applications/{appCode}/scheduled-jobs/sync.
// Mirrors Rust can_sync_scheduled_jobs_app: the application-service
// scheduled-job sync permission plus admin sync/manage. The handler
// additionally enforces target-client access (or anchor for platform-scoped).
func CanSyncScheduledJobs(a *AuthContext) error {
	return requireAny(a,
		permAppSvcScheduledJobSync, permScheduledJobSync, permScheduledJobManage)
}

// CanSyncProcesses guards POST /api/applications/{appCode}/processes/sync.
// Mirrors Rust can_sync_processes: admin process:sync or the
// application-service process:sync permission an SDK service account holds.
func CanSyncProcesses(a *AuthContext) error {
	return requireAny(a, permProcessSync, permAppSvcProcessSync)
}

// CanSyncDispatchPools guards POST /api/applications/{appCode}/dispatch-pools/sync.
// Mirrors Rust can_sync_dispatch_pools: admin-tier only (dispatch-pool
// sync/manage) — pools are global, so there is no application-service grant.
func CanSyncDispatchPools(a *AuthContext) error {
	return requireAny(a, permDispatchPoolSync, permDispatchPoolManage)
}

// CanSyncApplicationOpenAPI guards POST /api/applications/{appCode}/openapi/sync.
// Mirrors Rust can_sync_application_openapi (developer sync/manage). The
// handler additionally enforces a resource-level guard (anchor, super-admin,
// or the application's own bound service account).
func CanSyncApplicationOpenAPI(a *AuthContext) error {
	return requireAny(a, permAppOpenApiSync, permAppOpenApiManage)
}

// ── Service account permissions ──────────────────────────────────────────
func CanReadServiceAccounts(a *AuthContext) error {
	return requirePermission(a, permServiceAccountView)
}
func CanCreateServiceAccounts(a *AuthContext) error {
	return requirePermission(a, permServiceAccountCreate)
}
func CanUpdateServiceAccounts(a *AuthContext) error {
	return requirePermission(a, permServiceAccountUpdate)
}
func CanDeleteServiceAccounts(a *AuthContext) error {
	return requirePermission(a, permServiceAccountDelete)
}
func CanWriteServiceAccounts(a *AuthContext) error {
	return requireAny(a, permServiceAccountCreate, permServiceAccountUpdate, permServiceAccountDelete)
}

// ── Client (tenant) permissions ──────────────────────────────────────────
// Anchor-only by convention — tenant management is platform-owner work.
func CanReadClients(a *AuthContext) error   { return RequireAnchor(a) }
func CanCreateClients(a *AuthContext) error { return RequireAnchor(a) }
func CanUpdateClients(a *AuthContext) error { return RequireAnchor(a) }
func CanDeleteClients(a *AuthContext) error { return RequireAnchor(a) }
func CanWriteClients(a *AuthContext) error  { return RequireAnchor(a) }

// ── Principal (user) permissions ─────────────────────────────────────────
func CanReadPrincipals(a *AuthContext) error   { return requirePermission(a, permUserView) }
func CanCreatePrincipals(a *AuthContext) error { return requirePermission(a, permUserCreate) }
func CanUpdatePrincipals(a *AuthContext) error { return requirePermission(a, permUserUpdate) }
func CanDeletePrincipals(a *AuthContext) error { return requirePermission(a, permUserDelete) }
func CanWritePrincipals(a *AuthContext) error {
	return requireAny(a, permUserCreate, permUserUpdate, permUserDelete)
}

// ── Identity provider permissions ────────────────────────────────────────
// Anchor-only — IDPs are platform-level config.
func CanReadIdentityProviders(a *AuthContext) error  { return RequireAnchor(a) }
func CanWriteIdentityProviders(a *AuthContext) error { return RequireAnchor(a) }

// ── Scheduled job permissions ────────────────────────────────────────────
func CanReadScheduledJobs(a *AuthContext) error { return requirePermission(a, permScheduledJobView) }
func CanCreateScheduledJobs(a *AuthContext) error {
	return requirePermission(a, permScheduledJobCreate)
}
func CanUpdateScheduledJobs(a *AuthContext) error {
	return requirePermission(a, permScheduledJobUpdate)
}
func CanDeleteScheduledJobs(a *AuthContext) error {
	return requirePermission(a, permScheduledJobDelete)
}
func CanWriteScheduledJobs(a *AuthContext) error {
	return requireAny(a, permScheduledJobCreate, permScheduledJobUpdate, permScheduledJobDelete)
}
func CanFireScheduledJobs(a *AuthContext) error { return requirePermission(a, permScheduledJobFire) }
