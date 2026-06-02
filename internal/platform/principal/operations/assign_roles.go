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

type AssignRolesCommand struct {
	UserID string   `json:"userId"`
	Roles  []string `json:"roles"`
}

func AssignRoles(
	ctx context.Context,
	principals *principal.Repository,
	roles *role.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd AssignRolesCommand,
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
			"Roles can only be assigned to USER type principals")
	}

	for _, name := range cmd.Roles {
		r, err := roles.FindByName(ctx, name)
		if err != nil {
			return zero, usecase.Internal("REPO", "validate role failed", err)
		}
		if r == nil {
			return zero, usecase.Validation("ROLE_NOT_FOUND", "Role not found: "+name)
		}
	}

	previous := make([]string, 0, len(p.Roles))
	for _, ra := range p.Roles {
		previous = append(previous, ra.Role)
	}
	added := stringDifference(cmd.Roles, previous)
	removed := stringDifference(previous, cmd.Roles)

	now := time.Now().UTC()
	src := "ADMIN_ASSIGNED"
	newAssignments := make([]serviceaccount.RoleAssignment, 0, len(cmd.Roles))
	for _, name := range cmd.Roles {
		newAssignments = append(newAssignments, serviceaccount.RoleAssignment{
			Role:             name,
			AssignmentSource: &src,
			AssignedAt:       now,
		})
	}
	p.Roles = newAssignments
	p.UpdatedAt = now

	event := RolesAssigned{
		Metadata: usecase.NewEventMetadata(ec, RolesAssignedType, Source, subjectFor(p.ID)),
		UserID:   p.ID,
		Roles:    cmd.Roles,
		Added:    added,
		Removed:  removed,
	}
	// RolesPersister rewrites the iam_principal_roles junction from p.Roles
	// in the same tx as the event — the base principal Persist writes only
	// the iam_principals row.
	return commit.Save(ctx, uow, p, principal.RolesPersister{Repository: principals}, event, cmd)
}

func stringDifference(a, b []string) []string {
	in := make(map[string]struct{}, len(b))
	for _, x := range b {
		in[x] = struct{}{}
	}
	out := []string{}
	for _, x := range a {
		if _, ok := in[x]; !ok {
			out = append(out, x)
		}
	}
	return out
}
