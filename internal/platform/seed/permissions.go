package seed

// Permission identifiers — exact 1:1 port of
// fc-platform/src/role/entity.rs::permissions::*. Keep the string values
// byte-identical to what the Rust impl emits: existing rows in
// iam_role_permissions reference these strings, and the SDK pins its
// permission checks against them.
//
// Naming follows the Rust module path (admin/iam/auth/applicationService/developer)
// so the cross-reference stays obvious.

// Admin context — clients, applications, config.
const (
	// Client
	permAdminClientRead       = "platform:admin:client:view"
	permAdminClientCreate     = "platform:admin:client:create"
	permAdminClientUpdate     = "platform:admin:client:update"
	permAdminClientDelete     = "platform:admin:client:delete"
	permAdminClientManage     = "platform:admin:client:manage"
	permAdminClientActivate   = "platform:admin:client:activate"
	permAdminClientSuspend    = "platform:admin:client:suspend"
	permAdminClientDeactivate = "platform:admin:client:deactivate"

	// Anchor domain
	permAdminAnchorDomainRead   = "platform:admin:anchor-domain:view"
	permAdminAnchorDomainCreate = "platform:admin:anchor-domain:create"
	permAdminAnchorDomainUpdate = "platform:admin:anchor-domain:update"
	permAdminAnchorDomainDelete = "platform:admin:anchor-domain:delete"
	permAdminAnchorDomainManage = "platform:admin:anchor-domain:manage"

	// Application
	permAdminApplicationRead          = "platform:admin:application:view"
	permAdminApplicationCreate        = "platform:admin:application:create"
	permAdminApplicationUpdate        = "platform:admin:application:update"
	permAdminApplicationDelete        = "platform:admin:application:delete"
	permAdminApplicationManage        = "platform:admin:application:manage"
	permAdminApplicationActivate      = "platform:admin:application:activate"
	permAdminApplicationDeactivate    = "platform:admin:application:deactivate"
	permAdminApplicationEnableClient  = "platform:admin:application:enable-client"
	permAdminApplicationDisableClient = "platform:admin:application:disable-client"

	// Event type (stored under messaging context)
	permAdminEventTypeRead         = "platform:messaging:event-type:view"
	permAdminEventTypeCreate       = "platform:messaging:event-type:create"
	permAdminEventTypeUpdate       = "platform:messaging:event-type:update"
	permAdminEventTypeDelete       = "platform:messaging:event-type:delete"
	permAdminEventTypeManage       = "platform:messaging:event-type:manage"
	permAdminEventTypeArchive      = "platform:messaging:event-type:archive"
	permAdminEventTypeManageSchema = "platform:messaging:event-type:manage-schema"
	permAdminEventTypeSync         = "platform:messaging:event-type:sync"

	// Process
	permAdminProcessRead    = "platform:messaging:process:view"
	permAdminProcessCreate  = "platform:messaging:process:create"
	permAdminProcessUpdate  = "platform:messaging:process:update"
	permAdminProcessDelete  = "platform:messaging:process:delete"
	permAdminProcessManage  = "platform:messaging:process:manage"
	permAdminProcessArchive = "platform:messaging:process:archive"
	permAdminProcessSync    = "platform:messaging:process:sync"

	// Dispatch pool
	permAdminDispatchPoolRead   = "platform:messaging:dispatch-pool:view"
	permAdminDispatchPoolCreate = "platform:messaging:dispatch-pool:create"
	permAdminDispatchPoolUpdate = "platform:messaging:dispatch-pool:update"
	permAdminDispatchPoolDelete = "platform:messaging:dispatch-pool:delete"
	permAdminDispatchPoolManage = "platform:messaging:dispatch-pool:manage"
	permAdminDispatchPoolSync   = "platform:messaging:dispatch-pool:sync"

	// Connection
	permAdminConnectionRead   = "platform:messaging:connection:view"
	permAdminConnectionCreate = "platform:messaging:connection:create"
	permAdminConnectionUpdate = "platform:messaging:connection:update"
	permAdminConnectionDelete = "platform:messaging:connection:delete"
	permAdminConnectionManage = "platform:messaging:connection:manage"

	// Subscription
	permAdminSubscriptionRead   = "platform:messaging:subscription:view"
	permAdminSubscriptionCreate = "platform:messaging:subscription:create"
	permAdminSubscriptionUpdate = "platform:messaging:subscription:update"
	permAdminSubscriptionDelete = "platform:messaging:subscription:delete"
	permAdminSubscriptionManage = "platform:messaging:subscription:manage"
	permAdminSubscriptionSync   = "platform:messaging:subscription:sync"

	// Event
	permAdminEventRead    = "platform:messaging:event:view"
	permAdminEventViewRaw = "platform:messaging:event:view-raw"

	// Dispatch job
	permAdminDispatchJobRead    = "platform:messaging:dispatch-job:view"
	permAdminDispatchJobViewRaw = "platform:messaging:dispatch-job:view-raw"

	// Scheduled job
	permAdminScheduledJobRead         = "platform:messaging:scheduled-job:view"
	permAdminScheduledJobCreate       = "platform:messaging:scheduled-job:create"
	permAdminScheduledJobUpdate       = "platform:messaging:scheduled-job:update"
	permAdminScheduledJobDelete       = "platform:messaging:scheduled-job:delete"
	permAdminScheduledJobPause        = "platform:messaging:scheduled-job:pause"
	permAdminScheduledJobFire         = "platform:messaging:scheduled-job:fire"
	permAdminScheduledJobManage       = "platform:messaging:scheduled-job:manage"
	permAdminScheduledJobSync         = "platform:messaging:scheduled-job:sync"
	permAdminScheduledJobInstanceRead = "platform:messaging:scheduled-job-instance:view"

	// Identity provider
	permAdminIdentityProviderRead   = "platform:iam:idp:view"
	permAdminIdentityProviderCreate = "platform:iam:idp:create"
	permAdminIdentityProviderUpdate = "platform:iam:idp:update"
	permAdminIdentityProviderDelete = "platform:iam:idp:delete"
	permAdminIdentityProviderManage = "platform:iam:idp:manage"

	// Email-domain mapping
	permAdminEmailDomainMappingRead   = "platform:iam:email-domain-mapping:view"
	permAdminEmailDomainMappingCreate = "platform:iam:email-domain-mapping:create"
	permAdminEmailDomainMappingUpdate = "platform:iam:email-domain-mapping:update"
	permAdminEmailDomainMappingDelete = "platform:iam:email-domain-mapping:delete"
	permAdminEmailDomainMappingManage = "platform:iam:email-domain-mapping:manage"

	// Service account
	permAdminServiceAccountRead   = "platform:iam:service-account:view"
	permAdminServiceAccountCreate = "platform:iam:service-account:create"
	permAdminServiceAccountUpdate = "platform:iam:service-account:update"
	permAdminServiceAccountDelete = "platform:iam:service-account:delete"
	permAdminServiceAccountManage = "platform:iam:service-account:manage"

	// CORS
	permAdminCorsOriginRead   = "platform:admin:cors-origin:view"
	permAdminCorsOriginCreate = "platform:admin:cors-origin:create"
	permAdminCorsOriginDelete = "platform:admin:cors-origin:delete"
	permAdminCorsOriginManage = "platform:admin:cors-origin:manage"

	// Login + audit
	permAdminLoginAttemptRead = "platform:admin:login-attempt:view"
	permAdminAuditLogRead     = "platform:admin:audit-log:view"
	permAdminAuditLogExport   = "platform:admin:audit-log:export"

	// Config
	permAdminConfigRead   = "platform:admin:config:view"
	permAdminConfigUpdate = "platform:admin:config:update"

	// Batch
	permAdminBatchEventsWrite       = "platform:messaging:batch:events-write"
	permAdminBatchDispatchJobsWrite = "platform:messaging:batch:dispatch-jobs-write"
	permAdminBatchAuditLogsWrite    = "platform:admin:batch:audit-logs-write"
)

// IAM context — users, roles, access control.
const (
	permIAMUserRead           = "platform:iam:user:view"
	permIAMUserCreate         = "platform:iam:user:create"
	permIAMUserUpdate         = "platform:iam:user:update"
	permIAMUserDelete         = "platform:iam:user:delete"
	permIAMUserManage         = "platform:iam:user:manage"
	permIAMUserActivate       = "platform:iam:user:activate"
	permIAMUserDeactivate     = "platform:iam:user:deactivate"
	permIAMUserAssignRoles    = "platform:iam:user:assign-roles"
	permIAMRoleRead           = "platform:iam:role:view"
	permIAMRoleCreate         = "platform:iam:role:create"
	permIAMRoleUpdate         = "platform:iam:role:update"
	permIAMRoleDelete         = "platform:iam:role:delete"
	permIAMRoleManage         = "platform:iam:role:manage"
	permIAMClientAccessGrant  = "platform:iam:client-access:grant"
	permIAMClientAccessRevoke = "platform:iam:client-access:revoke"
	permIAMClientAccessRead   = "platform:iam:client-access:view"
	permIAMPermissionRead     = "platform:iam:permission:view"
)

// Auth context — OAuth clients + per-tenant auth configs.
const (
	permAuthClientAuthConfigRead        = "platform:auth:client-auth-config:view"
	permAuthClientAuthConfigCreate      = "platform:auth:client-auth-config:create"
	permAuthClientAuthConfigUpdate      = "platform:auth:client-auth-config:update"
	permAuthClientAuthConfigDelete      = "platform:auth:client-auth-config:delete"
	permAuthClientAuthConfigManage      = "platform:auth:client-auth-config:manage"
	permAuthOAuthClientRead             = "platform:auth:oauth-client:view"
	permAuthOAuthClientCreate           = "platform:auth:oauth-client:create"
	permAuthOAuthClientUpdate           = "platform:auth:oauth-client:update"
	permAuthOAuthClientDelete           = "platform:auth:oauth-client:delete"
	permAuthOAuthClientManage           = "platform:auth:oauth-client:manage"
	permAuthOAuthClientRegenerateSecret = "platform:auth:oauth-client:regenerate-secret"
)

// Developer portal.
const (
	permDeveloperApplicationOpenAPIView   = "platform:developer:application-openapi:view"
	permDeveloperApplicationOpenAPISync   = "platform:developer:application-openapi:sync"
	permDeveloperApplicationOpenAPIManage = "platform:developer:application-openapi:manage"
)

// Application-service permissions — scoped to a single application via SDK.
var permsApplicationService = []string{
	"platform:application-service:event:create",
	"platform:application-service:event-type:view",
	"platform:application-service:event-type:create",
	"platform:application-service:event-type:update",
	"platform:application-service:event-type:delete",
	"platform:application-service:subscription:view",
	"platform:application-service:subscription:create",
	"platform:application-service:subscription:update",
	"platform:application-service:subscription:delete",
	"platform:application-service:role:view",
	"platform:application-service:role:create",
	"platform:application-service:role:update",
	"platform:application-service:role:delete",
	"platform:application-service:permission:view",
	"platform:application-service:permission:sync",
	"platform:application-service:scheduled-job-instance:write",
	"platform:application-service:scheduled-job:sync",
	"platform:application-service:process:view",
	"platform:application-service:process:sync",
}

// Wildcard.
const permAdminAll = "platform:*:*:*"
