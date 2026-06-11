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
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apiroute"
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
	g := apiroute.New(api, tag)
	apiroute.Get(g, "listEventTypes", "/api/event-types", "List event types", s.list)
	apiroute.Post(g, "createEventType", "/api/event-types", "Create an event type", http.StatusCreated, s.create)
	apiroute.Get(g, "getEventType", "/api/event-types/{id}", "Get an event type by id", s.getByID)
	apiroute.Get(g, "getEventTypeByCode", "/api/event-types/by-code/{code}", "Get an event type by code", s.getByCode)
	apiroute.Put(g, "updateEventType", "/api/event-types/{id}", "Update an event type", http.StatusNoContent, s.update)
	apiroute.Delete(g, "deleteEventType", "/api/event-types/{id}", "Archive an event type", http.StatusNoContent, s.delete)
	apiroute.Post(g, "addEventTypeSchema", "/api/event-types/{id}/schemas", "Add a schema version to an event type (Go-historical alias)", http.StatusOK, s.addSchema)
	// /versions is the Rust-canonical path. Same handler; both paths
	// remain registered so existing SPA clients on /schemas keep working.
	apiroute.Post(g, "addEventTypeVersion", "/api/event-types/{id}/versions", "Add a schema version to an event type", http.StatusOK, s.addSchema)
}

// ── Handlers ──────────────────────────────────────────────────────────────

type listInput struct {
	Application string `query:"application" doc:"Filter by application code"`
	ClientID    string `query:"clientId" doc:"Filter by client id"`
	Status      string `query:"status" doc:"Filter by status (CURRENT, ARCHIVED)"`
	Subdomain   string `query:"subdomain" doc:"Filter by subdomain"`
	Aggregate   string `query:"aggregate" doc:"Filter by aggregate"`
}

func (s *State) list(ctx context.Context, in *listInput) (*apicommon.Out[EventTypeListResponse], error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadEventTypes(ac); err != nil {
		return nil, err
	}

	application := apicommon.OptStr(in.Application)
	clientID := apicommon.OptStr(in.ClientID)
	status := apicommon.OptStr(in.Status)
	subdomain := apicommon.OptStr(in.Subdomain)
	aggregate := apicommon.OptStr(in.Aggregate)
	if application == nil && clientID == nil && status == nil && subdomain == nil && aggregate == nil {
		def := "CURRENT"
		status = &def
	}

	rows, err := s.Repo.FindWithFilters(ctx, application, clientID, status, subdomain, aggregate)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_with_filters failed", err)
	}
	visible := auth.FilterClientScoped(ac, rows, func(et *eventtype.EventType) *string { return et.ClientID })
	out := apicommon.MapSlice(visible, fromEntity)
	return &apicommon.Out[EventTypeListResponse]{Body: EventTypeListResponse{Items: out}}, nil
}

type getByIDInput struct {
	ID string `path:"id" doc:"Event type id (TSID)"`
}

func (s *State) getByID(ctx context.Context, in *getByIDInput) (*apicommon.Out[EventTypeResponse], error) {
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
	return &apicommon.Out[EventTypeResponse]{Body: fromEntity(et)}, nil
}

type getByCodeInput struct {
	Code string `path:"code" doc:"Event type code (e.g. platform:iam:user:created)"`
}

func (s *State) getByCode(ctx context.Context, in *getByCodeInput) (*apicommon.Out[EventTypeResponse], error) {
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
	return &apicommon.Out[EventTypeResponse]{Body: fromEntity(et)}, nil
}

func (s *State) create(ctx context.Context, in *apicommon.In[CreateEventTypeRequest]) (*apicommon.Out[apicommon.CreatedResponse], error) {
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
	return &apicommon.Out[apicommon.CreatedResponse]{Body: apicommon.CreatedResponse{ID: committed.Event().EventTypeID}}, nil
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

func (s *State) update(ctx context.Context, in *updateInput) (*apicommon.Empty, error) {
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
	return &apicommon.Empty{}, nil
}

func (s *State) delete(ctx context.Context, in *apicommon.IDInput) (*apicommon.Empty, error) {
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
	return &apicommon.Empty{}, nil
}

type addSchemaInput struct {
	ID   string `path:"id"`
	Body AddSchemaRequest
}

func (s *State) addSchema(ctx context.Context, in *addSchemaInput) (*apicommon.Out[EventTypeResponse], error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteEventTypes(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.AddSchema(ctx, s.Repo, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	et, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if et == nil {
		return nil, httperror.NotFound("EventType", in.ID)
	}
	// Return the updated event type (1:1 with Rust add_schema_version → EventTypeResponse).
	return &apicommon.Out[EventTypeResponse]{Body: fromEntity(et)}, nil
}
