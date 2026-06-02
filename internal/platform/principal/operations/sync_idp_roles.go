package operations

import (
	"context"
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// IdpSyncSource is the AssignmentSource value used to tag role
// assignments that came from an IDP claim. Distinguishes them from
// ADMIN_ASSIGNED rows so the next sync only touches its own set.
const IdpSyncSource = "IDP_SYNC"

// SyncIdpRolesCommand is the input DTO. PlatformRoles is the set of
// internal role names the IDP claim resolved to AFTER applying the
// EmailDomainMapping.AllowedRoleIDs filter. Empty slice = remove all
// IDP-sourced roles (the user lost every group upstream).
type SyncIdpRolesCommand struct {
	UserID        string   `json:"userId"`
	PlatformRoles []string `json:"platformRoles"`
}

// SyncIdpRoles replaces the principal's IDP_SYNC-sourced role
// assignments with the supplied set. Non-IDP role assignments
// (ADMIN_ASSIGNED, SYSTEM, etc.) are preserved untouched. Mirrors
// Rust's OidcSyncService::sync_idp_roles_filtered minus the IDP-
// mapping lookup, which the caller does upstream.
//
// Validates every supplied role name exists. Refuses to run on
// non-USER principals (service accounts get their roles via a
// separate flow).
func SyncIdpRoles(
	ctx context.Context,
	principals *principal.Repository,
	roles *role.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd SyncIdpRolesCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[RolesAssigned], error) {
	var zero commit.Committed[RolesAssigned]

	if strings.TrimSpace(cmd.UserID) == "" {
		return zero, usecase.Validation("USER_ID_REQUIRED", "User ID is required")
	}
	p, err := principals.FindByID(ctx, cmd.UserID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return zero, httperror.NotFound("User", cmd.UserID)
	}
	if p.Type != principal.TypeUser {
		return zero, usecase.BusinessRule("NOT_A_USER",
			"IDP role sync only applies to USER principals")
	}

	// Validate every incoming role exists.
	for _, name := range cmd.PlatformRoles {
		r, err := roles.FindByName(ctx, name)
		if err != nil {
			return zero, usecase.Internal("REPO", "validate role failed", err)
		}
		if r == nil {
			return zero, usecase.Validation("ROLE_NOT_FOUND", "Role not found: "+name)
		}
	}

	// Snapshot the prior set for the event payload.
	previous := make([]string, 0, len(p.Roles))
	for _, ra := range p.Roles {
		previous = append(previous, ra.Role)
	}

	// Rebuild Roles: keep every non-IDP_SYNC assignment, then append the
	// new IDP_SYNC set (deduped against what we kept).
	now := time.Now().UTC()
	idpSource := IdpSyncSource
	preserved := make([]serviceaccount.RoleAssignment, 0, len(p.Roles))
	keptByName := make(map[string]struct{}, len(p.Roles))
	for _, ra := range p.Roles {
		if ra.AssignmentSource != nil && *ra.AssignmentSource == IdpSyncSource {
			continue
		}
		preserved = append(preserved, ra)
		keptByName[ra.Role] = struct{}{}
	}
	for _, name := range cmd.PlatformRoles {
		if _, dup := keptByName[name]; dup {
			continue
		}
		preserved = append(preserved, serviceaccount.RoleAssignment{
			Role:             name,
			AssignmentSource: &idpSource,
			AssignedAt:       now,
		})
	}
	p.Roles = preserved
	p.UpdatedAt = now

	current := make([]string, 0, len(p.Roles))
	for _, ra := range p.Roles {
		current = append(current, ra.Role)
	}
	added := stringDifference(current, previous)
	removed := stringDifference(previous, current)

	event := RolesAssigned{
		Metadata: usecase.NewEventMetadata(ec, RolesAssignedType, Source, subjectFor(p.ID)),
		UserID:   p.ID,
		Roles:    current,
		Added:    added,
		Removed:  removed,
	}
	// RolesPersister writes the merged role set to iam_principal_roles in
	// the same tx as the event; the base Persist would skip the junction.
	return commit.Save(ctx, uow, p, principal.RolesPersister{Repository: principals}, event, cmd)
}
