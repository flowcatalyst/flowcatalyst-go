// Package api wires HTTP routes for dispatch_pool via huma.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// State bundles deps.
type State struct {
	Repo *dispatchpool.Repository
	UoW  *usecasepgx.UnitOfWork
}

const tag = "dispatch-pools"

// Register mounts the dispatch-pool endpoints.
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchPools",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-pools",
		Summary:       "List dispatch pools",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.list)

	huma.Register(api, huma.Operation{
		OperationID:   "createDispatchPool",
		Method:        http.MethodPost,
		Path:          "/api/dispatch-pools",
		Summary:       "Create a dispatch pool",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
	}, s.create)

	huma.Register(api, huma.Operation{
		OperationID:   "getDispatchPool",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-pools/{id}",
		Summary:       "Get a dispatch pool by id",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByID)

	huma.Register(api, huma.Operation{
		OperationID:   "updateDispatchPool",
		Method:        http.MethodPut,
		Path:          "/api/dispatch-pools/{id}",
		Summary:       "Update a dispatch pool",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.update)

	huma.Register(api, huma.Operation{
		OperationID:   "archiveDispatchPool",
		Method:        http.MethodPost,
		Path:          "/api/dispatch-pools/{id}/archive",
		Summary:       "Archive a dispatch pool",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.archive)

	huma.Register(api, huma.Operation{
		OperationID:   "suspendDispatchPool",
		Method:        http.MethodPost,
		Path:          "/api/dispatch-pools/{id}/suspend",
		Summary:       "Suspend dispatch into a pool",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.suspend)

	huma.Register(api, huma.Operation{
		OperationID:   "activateDispatchPool",
		Method:        http.MethodPost,
		Path:          "/api/dispatch-pools/{id}/activate",
		Summary:       "Resume a suspended dispatch pool",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.activate)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteDispatchPool",
		Method:        http.MethodDelete,
		Path:          "/api/dispatch-pools/{id}",
		Summary:       "Delete a dispatch pool",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.delete)
}

type listInput struct {
	Status   string `query:"status" doc:"Filter by status (ACTIVE, SUSPENDED, ARCHIVED)"`
	ClientID string `query:"clientId" doc:"Filter by client id"`
}

type listOutput struct {
	Body DispatchPoolListResponse
}

func (s *State) list(ctx context.Context, in *listInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadDispatchPools(ac); err != nil {
		return nil, err
	}
	var status, clientID *string
	if in.Status != "" {
		status = &in.Status
	}
	if in.ClientID != "" {
		clientID = &in.ClientID
	}
	rows, err := s.Repo.FindWithFilters(ctx, status, clientID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_with_filters failed", err)
	}
	out := make([]DispatchPoolResponse, 0, len(rows))
	for i := range rows {
		p := &rows[i]
		if p.ClientID == nil || ac.CanAccessClient(*p.ClientID) {
			out = append(out, fromEntity(p))
		}
	}
	return &listOutput{Body: DispatchPoolListResponse{Pools: out, Total: len(out)}}, nil
}

type getInput struct {
	ID string `path:"id"`
}

type getOutput struct {
	Body DispatchPoolResponse
}

func (s *State) getByID(ctx context.Context, in *getInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadDispatchPools(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("DispatchPool", in.ID)
	}
	if p.ClientID != nil && !ac.CanAccessClient(*p.ClientID) {
		return nil, httperror.Forbidden("No access to this dispatch pool")
	}
	return &getOutput{Body: fromEntity(p)}, nil
}

type createInput struct {
	Body CreateDispatchPoolRequest
}

type createOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) create(ctx context.Context, in *createInput) (*createOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteDispatchPools(ac); err != nil {
		return nil, err
	}
	if in.Body.ClientID != nil && !ac.CanAccessClient(*in.Body.ClientID) {
		return nil, httperror.Forbidden("No access to client: " + *in.Body.ClientID)
	}
	if in.Body.ClientID == nil && !ac.IsAnchor() {
		return nil, httperror.Forbidden("Only anchor users can create anchor-level dispatch pools")
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateDispatchPool(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().PoolID}}, nil
}

// requireScopeByID loads the pool and enforces per-resource scope (A2) on top
// of the coarse permission already checked: a non-anchor principal must not
// mutate another tenant's dispatch pool by id.
func (s *State) requireScopeByID(ctx context.Context, ac *auth.AuthContext, id string) error {
	p, err := s.Repo.FindByID(ctx, id)
	if err != nil {
		return usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return httperror.NotFound("DispatchPool", id)
	}
	return auth.CheckScopeAccess(ac, p.ClientID)
}

type updateInput struct {
	ID   string `path:"id"`
	Body UpdateDispatchPoolRequest
}

type emptyOutput struct{}

func (s *State) update(ctx context.Context, in *updateInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteDispatchPools(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateDispatchPool(ctx, s.Repo, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type archiveInput struct {
	ID string `path:"id"`
}

func (s *State) archive(ctx context.Context, in *archiveInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteDispatchPools(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ArchiveDispatchPool(ctx, s.Repo, s.UoW, operations.ArchiveCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

func (s *State) suspend(ctx context.Context, in *archiveInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteDispatchPools(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.SuspendDispatchPool(ctx, s.Repo, s.UoW, operations.SuspendCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

func (s *State) activate(ctx context.Context, in *archiveInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteDispatchPools(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ActivateDispatchPool(ctx, s.Repo, s.UoW, operations.ActivateCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type deleteInput struct {
	ID string `path:"id"`
}

func (s *State) delete(ctx context.Context, in *deleteInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanDeleteDispatchPools(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteDispatchPool(ctx, s.Repo, s.UoW, operations.DeleteCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}
