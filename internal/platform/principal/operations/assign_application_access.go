package operations

import (
	"context"
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

type AssignApplicationAccessCommand struct {
	UserID         string   `json:"userId"`
	ApplicationIDs []string `json:"applicationIds"`
}

func AssignApplicationAccess(
	ctx context.Context,
	principals *principal.Repository,
	applications *application.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd AssignApplicationAccessCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ApplicationAccessAssigned], error) {
	var zero commit.Committed[ApplicationAccessAssigned]
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
		return zero, usecase.BusinessRule("NOT_A_USER", "Principal is not a user")
	}

	for _, appID := range cmd.ApplicationIDs {
		app, err := applications.FindByID(ctx, appID)
		if err != nil {
			return zero, usecase.Internal("REPO", "find_application failed", err)
		}
		if app == nil {
			return zero, usecase.Validation("APPLICATION_NOT_FOUND", "Application not found: "+appID)
		}
		if !app.Active {
			return zero, usecase.BusinessRule("APPLICATION_INACTIVE", "Application is not active: "+appID)
		}
	}

	added := stringDifference(cmd.ApplicationIDs, p.AccessibleApplicationIDs)
	removed := stringDifference(p.AccessibleApplicationIDs, cmd.ApplicationIDs)

	p.AccessibleApplicationIDs = append([]string(nil), cmd.ApplicationIDs...)
	p.UpdatedAt = time.Now().UTC()

	event := ApplicationAccessAssigned{
		Metadata:       usecase.NewEventMetadata(ec, ApplicationAccessType, Source, subjectFor(p.ID)),
		UserID:         p.ID,
		ApplicationIDs: cmd.ApplicationIDs,
		Added:          added,
		Removed:        removed,
	}
	// AppAccessPersister rewrites the iam_principal_application_access
	// junction from p.AccessibleApplicationIDs in the same tx as the event;
	// the base principal Persist writes only the iam_principals row.
	return commit.Save(ctx, uow, p, principal.AppAccessPersister{Repository: principals}, event, cmd)
}
