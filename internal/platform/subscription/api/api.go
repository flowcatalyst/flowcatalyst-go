// Package api wires HTTP routes for the subscription subdomain via huma.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription/operations"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// State bundles the dependencies.
type State struct {
	Repo *subscription.Repository
	UoW  *usecasepgx.UnitOfWork
}

const tag = "subscriptions"

// Register mounts the subscription endpoints.
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "listSubscriptions",
		Method:        http.MethodGet,
		Path:          "/api/subscriptions",
		Summary:       "List subscriptions",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.list)

	huma.Register(api, huma.Operation{
		OperationID:   "createSubscription",
		Method:        http.MethodPost,
		Path:          "/api/subscriptions",
		Summary:       "Create a subscription",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
	}, s.create)

	huma.Register(api, huma.Operation{
		OperationID:   "getSubscription",
		Method:        http.MethodGet,
		Path:          "/api/subscriptions/{id}",
		Summary:       "Get a subscription by id",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByID)

	huma.Register(api, huma.Operation{
		OperationID:   "updateSubscription",
		Method:        http.MethodPut,
		Path:          "/api/subscriptions/{id}",
		Summary:       "Update a subscription",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.update)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteSubscription",
		Method:        http.MethodDelete,
		Path:          "/api/subscriptions/{id}",
		Summary:       "Delete a subscription",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.delete)

	huma.Register(api, huma.Operation{
		OperationID:   "pauseSubscription",
		Method:        http.MethodPost,
		Path:          "/api/subscriptions/{id}/pause",
		Summary:       "Pause a subscription",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.pause)

	huma.Register(api, huma.Operation{
		OperationID:   "resumeSubscription",
		Method:        http.MethodPost,
		Path:          "/api/subscriptions/{id}/resume",
		Summary:       "Resume a subscription",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.resume)
}

type listInput struct {
	Status   string `query:"status"`
	ClientID string `query:"clientId"`
}

type listOutput struct {
	Body SubscriptionListResponse
}

func (s *State) list(ctx context.Context, in *listInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadSubscriptions(ac); err != nil {
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
	out := make([]SubscriptionResponse, 0, len(rows))
	for i := range rows {
		sub := &rows[i]
		if sub.ClientID == nil || ac.CanAccessClient(*sub.ClientID) {
			out = append(out, fromEntity(sub))
		}
	}
	return &listOutput{Body: SubscriptionListResponse{Subscriptions: out, Total: len(out)}}, nil
}

type getInput struct {
	ID string `path:"id"`
}

type getOutput struct {
	Body SubscriptionResponse
}

func (s *State) getByID(ctx context.Context, in *getInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadSubscriptions(ac); err != nil {
		return nil, err
	}
	sub, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if sub == nil {
		return nil, httperror.NotFound("Subscription", in.ID)
	}
	if sub.ClientID != nil && !ac.CanAccessClient(*sub.ClientID) {
		return nil, httperror.Forbidden("No access to this subscription")
	}
	return &getOutput{Body: fromEntity(sub)}, nil
}

type createInput struct {
	Body CreateSubscriptionRequest
}

type createOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) create(ctx context.Context, in *createInput) (*createOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteSubscriptions(ac); err != nil {
		return nil, err
	}
	if in.Body.ClientID != nil && !ac.CanAccessClient(*in.Body.ClientID) {
		return nil, httperror.Forbidden("No access to client: " + *in.Body.ClientID)
	}
	if in.Body.ClientID == nil && !ac.IsAnchor() {
		return nil, httperror.Forbidden("Only anchor users can create anchor-level subscriptions")
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateSubscription(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().SubscriptionID}}, nil
}

// requireScopeByID loads the subscription and enforces per-resource scope
// (A2) on top of the coarse permission the caller already checked: a non-anchor
// principal must not mutate another tenant's subscription by guessing its id.
func (s *State) requireScopeByID(ctx context.Context, ac *auth.AuthContext, id string) error {
	sub, err := s.Repo.FindByID(ctx, id)
	if err != nil {
		return usecase.Internal("REPO", "find_by_id failed", err)
	}
	if sub == nil {
		return httperror.NotFound("Subscription", id)
	}
	return auth.CheckScopeAccess(ac, sub.ClientID)
}

type updateInput struct {
	ID   string `path:"id"`
	Body UpdateSubscriptionRequest
}

type emptyOutput struct{}

func (s *State) update(ctx context.Context, in *updateInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteSubscriptions(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateSubscription(ctx, s.Repo, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type deleteInput struct {
	ID string `path:"id"`
}

func (s *State) delete(ctx context.Context, in *deleteInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanDeleteSubscriptions(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteSubscription(ctx, s.Repo, s.UoW, operations.DeleteCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type pauseInput struct {
	ID string `path:"id"`
}

func (s *State) pause(ctx context.Context, in *pauseInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteSubscriptions(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.PauseSubscription(ctx, s.Repo, s.UoW, operations.PauseCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type resumeInput struct {
	ID string `path:"id"`
}

func (s *State) resume(ctx context.Context, in *resumeInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteSubscriptions(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ResumeSubscription(ctx, s.Repo, s.UoW, operations.ResumeCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}
