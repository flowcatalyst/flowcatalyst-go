// Package api wires the HTTP routes for the event_type subdomain via
// danielgtaylor/huma/v2. The Input/Output structs in dto.go are the
// source of truth for the OpenAPI spec; huma derives the spec from
// them at registration time.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// State bundles deps for the event-type handlers.
type State struct {
	Repo *eventtype.Repository
	UoW  *usecasepgx.UnitOfWork
}

const tag = "event-types"

// Register mounts the event-type endpoints on the supplied huma API.
// Routes match the existing Rust eventtype/api.rs exactly (path,
// method, status code).
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "listEventTypes",
		Method:        http.MethodGet,
		Path:          "/api/event-types",
		Summary:       "List event types",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.list)

	huma.Register(api, huma.Operation{
		OperationID:   "createEventType",
		Method:        http.MethodPost,
		Path:          "/api/event-types",
		Summary:       "Create an event type",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
	}, s.create)

	huma.Register(api, huma.Operation{
		OperationID:   "getEventType",
		Method:        http.MethodGet,
		Path:          "/api/event-types/{id}",
		Summary:       "Get an event type by id",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByID)

	huma.Register(api, huma.Operation{
		OperationID:   "getEventTypeByCode",
		Method:        http.MethodGet,
		Path:          "/api/event-types/by-code/{code}",
		Summary:       "Get an event type by code",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByCode)

	huma.Register(api, huma.Operation{
		OperationID:   "updateEventType",
		Method:        http.MethodPut,
		Path:          "/api/event-types/{id}",
		Summary:       "Update an event type",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.update)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteEventType",
		Method:        http.MethodDelete,
		Path:          "/api/event-types/{id}",
		Summary:       "Archive an event type",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.delete)

	huma.Register(api, huma.Operation{
		OperationID:   "addEventTypeSchema",
		Method:        http.MethodPost,
		Path:          "/api/event-types/{id}/schemas",
		Summary:       "Add a schema version to an event type (Go-historical alias)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
	}, s.addSchema)

	// /versions is the Rust-canonical path. Same handler; both paths
	// remain registered so existing SPA clients on /schemas keep working.
	huma.Register(api, huma.Operation{
		OperationID:   "addEventTypeVersion",
		Method:        http.MethodPost,
		Path:          "/api/event-types/{id}/versions",
		Summary:       "Add a schema version to an event type",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
	}, s.addSchema)
}

// ── Handlers ──────────────────────────────────────────────────────────────

type listInput struct {
	Application string `query:"application" doc:"Filter by application code"`
	ClientID    string `query:"clientId" doc:"Filter by client id"`
	Status      string `query:"status" doc:"Filter by status (CURRENT, ARCHIVED)"`
	Subdomain   string `query:"subdomain" doc:"Filter by subdomain"`
	Aggregate   string `query:"aggregate" doc:"Filter by aggregate"`
}

type listOutput struct {
	Body EventTypeListResponse
}

func (s *State) list(ctx context.Context, in *listInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadEventTypes(ac); err != nil {
		return nil, err
	}

	var application, clientID, status, subdomain, aggregate *string
	if in.Application != "" {
		application = &in.Application
	}
	if in.ClientID != "" {
		clientID = &in.ClientID
	}
	if in.Status != "" {
		status = &in.Status
	}
	if in.Subdomain != "" {
		subdomain = &in.Subdomain
	}
	if in.Aggregate != "" {
		aggregate = &in.Aggregate
	}
	if application == nil && clientID == nil && status == nil && subdomain == nil && aggregate == nil {
		def := "CURRENT"
		status = &def
	}

	rows, err := s.Repo.FindWithFilters(ctx, application, clientID, status, subdomain, aggregate)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_with_filters failed", err)
	}
	out := make([]EventTypeResponse, 0, len(rows))
	for _, et := range rows {
		if et.ClientID == nil || ac.CanAccessClient(*et.ClientID) {
			out = append(out, fromEntity(&et))
		}
	}
	return &listOutput{Body: EventTypeListResponse{Items: out}}, nil
}

type getByIDInput struct {
	ID string `path:"id" doc:"Event type id (TSID)"`
}

type getOutput struct {
	Body EventTypeResponse
}

func (s *State) getByID(ctx context.Context, in *getByIDInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadEventTypes(ac); err != nil {
		return nil, err
	}
	et, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if et == nil {
		return nil, httperror.NotFound("EventType", in.ID)
	}
	if et.ClientID != nil && !ac.CanAccessClient(*et.ClientID) {
		return nil, httperror.Forbidden("No access to this event type")
	}
	return &getOutput{Body: fromEntity(et)}, nil
}

type getByCodeInput struct {
	Code string `path:"code" doc:"Event type code (e.g. platform:iam:user:created)"`
}

func (s *State) getByCode(ctx context.Context, in *getByCodeInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadEventTypes(ac); err != nil {
		return nil, err
	}
	et, err := s.Repo.FindByCode(ctx, in.Code)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_code failed", err)
	}
	if et == nil {
		return nil, httperror.NotFound("EventType", in.Code)
	}
	if et.ClientID != nil && !ac.CanAccessClient(*et.ClientID) {
		return nil, httperror.Forbidden("No access to this event type")
	}
	return &getOutput{Body: fromEntity(et)}, nil
}

type createInput struct {
	Body CreateEventTypeRequest
}

type createOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) create(ctx context.Context, in *createInput) (*createOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteEventTypes(ac); err != nil {
		return nil, err
	}
	if in.Body.ClientID != nil {
		if !ac.CanAccessClient(*in.Body.ClientID) {
			return nil, httperror.Forbidden("No access to client: " + *in.Body.ClientID)
		}
	} else if !ac.IsAnchor() {
		return nil, httperror.Forbidden("Only anchor users can create anchor-level event types")
	}

	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateEventType(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().EventTypeID}}, nil
}

// requireScopeByID loads the event type and enforces per-resource scope (A2)
// on top of the coarse permission already checked: a non-anchor principal must
// not mutate another tenant's event type by id.
func (s *State) requireScopeByID(ctx context.Context, ac *auth.AuthContext, id string) error {
	et, err := s.Repo.FindByID(ctx, id)
	if err != nil {
		return usecase.Internal("REPO", "find_by_id failed", err)
	}
	if et == nil {
		return httperror.NotFound("EventType", id)
	}
	return auth.CheckScopeAccess(ac, et.ClientID)
}

type updateInput struct {
	ID   string `path:"id"`
	Body UpdateEventTypeRequest
}

type emptyOutput struct{}

func (s *State) update(ctx context.Context, in *updateInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteEventTypes(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateEventType(ctx, s.Repo, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type deleteInput struct {
	ID string `path:"id"`
}

func (s *State) delete(ctx context.Context, in *deleteInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanDeleteEventTypes(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteEventType(ctx, s.Repo, s.UoW, operations.DeleteCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type addSchemaInput struct {
	ID   string `path:"id"`
	Body AddSchemaRequest
}

func (s *State) addSchema(ctx context.Context, in *addSchemaInput) (*createOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteEventTypes(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.AddSchema(ctx, s.Repo, s.UoW, in.Body.toCommand(in.ID), ec)
	if err != nil {
		return nil, err
	}
	return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().EventTypeID}}, nil
}
