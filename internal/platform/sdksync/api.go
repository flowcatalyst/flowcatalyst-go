// Package sdksync wires the SDK self-registration ("sync") routes, scoped
// under /api/applications/{appCode}. These are the declarative endpoints an
// application's SDK calls at boot to register its resources (event types,
// roles, subscriptions, dispatch pools, principals, processes, scheduled
// jobs, openapi spec) in one idempotent batch.
//
// Mirrors crates/fc-platform/src/shared/sdk_sync_api.rs (the Rust
// sdk_sync_router nested under /api/applications) exactly: path, method,
// request body shape, and the shared SyncResultResponse
// {applicationCode, created, updated, deleted, syncedCodes} wire shape.
//
// Each handler resolves {appCode} to an Application (404 when unknown),
// checks the resource's sync permission, then delegates to that resource's
// Sync<Resource> use case and maps its rollup event onto the response.
//
// Endpoints land incrementally; event-types is wired first (it reuses the
// existing, tested eventtype Sync use case). The remaining resources
// (roles, subscriptions, dispatch-pools, principals, processes,
// scheduled-jobs, openapi) follow the same shape.
package sdksync

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/connection"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool"
	dispatchpoolops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype"
	eventtypeops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs"
	openapiops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	principalops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/process"
	processops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/process/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	roleops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/role/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	scheduledjobops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription"
	subscriptionops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription/operations"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

const tag = "sdk-sync"

// State bundles the deps shared by the SDK sync handlers.
type State struct {
	Apps          *application.Repository
	EventTypes    *eventtype.Repository
	Roles         *role.Repository
	Subscriptions *subscription.Repository
	Connections   *connection.Repository
	Processes     *process.Repository
	DispatchPools *dispatchpool.Repository
	Principals    *principal.Repository
	ScheduledJobs *scheduledjob.Repository
	Specs         *openapispecs.Repository
	UoW           *usecasepgx.UnitOfWork
}

// SyncResultResponse is the shared result for the list-based sync
// endpoints. Mirrors the Rust SyncResultResponse (camelCase wire shape).
type SyncResultResponse struct {
	ApplicationCode string   `json:"applicationCode"`
	Created         uint32   `json:"created"`
	Updated         uint32   `json:"updated"`
	Deleted         uint32   `json:"deleted"`
	SyncedCodes     []string `json:"syncedCodes"`
}

// Register mounts the SDK sync endpoints on the supplied huma API. Paths
// match the Rust sdk_sync_router nested under /api/applications.
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "syncRoles",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/roles/sync",
		Summary:       "Sync an application's roles (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncRoles)

	huma.Register(api, huma.Operation{
		OperationID:   "syncEventTypes",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/event-types/sync",
		Summary:       "Sync an application's event types (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncEventTypes)

	huma.Register(api, huma.Operation{
		OperationID:   "syncSubscriptions",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/subscriptions/sync",
		Summary:       "Sync an application's subscriptions (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncSubscriptions)

	huma.Register(api, huma.Operation{
		OperationID:   "syncDispatchPools",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/dispatch-pools/sync",
		Summary:       "Sync dispatch pools (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncDispatchPools)

	huma.Register(api, huma.Operation{
		OperationID:   "syncPrincipals",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/principals/sync",
		Summary:       "Sync an application's principals (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncPrincipals)

	huma.Register(api, huma.Operation{
		OperationID:   "syncProcesses",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/processes/sync",
		Summary:       "Sync an application's processes (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncProcesses)

	huma.Register(api, huma.Operation{
		OperationID:   "syncScheduledJobs",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/scheduled-jobs/sync",
		Summary:       "Sync scheduled jobs (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncScheduledJobs)

	huma.Register(api, huma.Operation{
		OperationID:   "syncOpenapi",
		Method:        http.MethodPost,
		Path:          "/api/applications/{appCode}/openapi/sync",
		Summary:       "Sync an application's OpenAPI document (SDK self-registration)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.syncOpenapi)
}

// resolveApp loads the application by code, returning a 404 when unknown —
// matching the Rust handlers' 404-on-unknown-application contract.
func (s *State) resolveApp(ctx context.Context, code string) (*application.Application, error) {
	app, err := s.Apps.FindByCode(ctx, code)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_code failed", err)
	}
	if app == nil {
		return nil, httperror.NotFound("Application", code)
	}
	return app, nil
}

// ── Event types ─────────────────────────────────────────────────────────

type syncEventTypeInputRequest struct {
	Code        string  `json:"code" doc:"Full code (application:subdomain:aggregate:event)"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type syncEventTypesRequest struct {
	EventTypes []syncEventTypeInputRequest `json:"eventTypes"`
}

type syncEventTypesInput struct {
	AppCode        string `path:"appCode" doc:"Application code"`
	RemoveUnlisted bool   `query:"removeUnlisted" doc:"Remove API-sourced event types not in the list"`
	Body           syncEventTypesRequest
}

type syncResultOutput struct {
	Body SyncResultResponse
}

func (s *State) syncEventTypes(ctx context.Context, in *syncEventTypesInput) (*syncResultOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncEventTypes(ac); err != nil {
		return nil, err
	}
	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	inputs := make([]eventtypeops.SyncEventTypeInput, 0, len(in.Body.EventTypes))
	for _, et := range in.Body.EventTypes {
		inputs = append(inputs, eventtypeops.SyncEventTypeInput{
			Code:        et.Code,
			Name:        et.Name,
			Description: et.Description,
		})
	}

	cmd := eventtypeops.SyncEventTypesCommand{
		ApplicationCode: app.Code,
		EventTypes:      inputs,
		RemoveUnlisted:  in.RemoveUnlisted,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := eventtypeops.SyncEventTypes(ctx, s.EventTypes, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	return &syncResultOutput{Body: SyncResultResponse{
		ApplicationCode: ev.ApplicationCode,
		Created:         ev.Created,
		Updated:         ev.Updated,
		Deleted:         ev.Deleted,
		SyncedCodes:     ev.SyncedCodes,
	}}, nil
}

// ── Roles ─────────────────────────────────────────────────────────────────

type syncRoleInputRequest struct {
	Name          string   `json:"name"`
	DisplayName   *string  `json:"displayName,omitempty"`
	Description   *string  `json:"description,omitempty"`
	Permissions   []string `json:"permissions,omitempty"`
	ClientManaged bool     `json:"clientManaged,omitempty"`
}

type syncRolesRequest struct {
	Roles []syncRoleInputRequest `json:"roles"`
}

type syncRolesInput struct {
	AppCode        string `path:"appCode" doc:"Application code"`
	RemoveUnlisted bool   `query:"removeUnlisted" doc:"Remove SDK roles not in the list"`
	Body           syncRolesRequest
}

func (s *State) syncRoles(ctx context.Context, in *syncRolesInput) (*syncResultOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncRoles(ac); err != nil {
		return nil, err
	}
	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	inputs := make([]roleops.SyncRoleInput, 0, len(in.Body.Roles))
	for _, r := range in.Body.Roles {
		inputs = append(inputs, roleops.SyncRoleInput{
			Name:          r.Name,
			DisplayName:   r.DisplayName,
			Description:   r.Description,
			Permissions:   r.Permissions,
			ClientManaged: r.ClientManaged,
		})
	}

	cmd := roleops.SyncRolesCommand{
		ApplicationCode: app.Code,
		ApplicationID:   app.ID,
		Roles:           inputs,
		RemoveUnlisted:  in.RemoveUnlisted,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := roleops.SyncRoles(ctx, s.Roles, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	return &syncResultOutput{Body: SyncResultResponse{
		ApplicationCode: ev.ApplicationCode,
		Created:         ev.Created,
		Updated:         ev.Updated,
		Deleted:         ev.Removed,
		SyncedCodes:     ev.SyncedCodes,
	}}, nil
}

// ── Subscriptions ─────────────────────────────────────────────────────────

type syncSubscriptionEventTypeRequest struct {
	EventTypeCode string  `json:"eventTypeCode"`
	Filter        *string `json:"filter,omitempty"`
}

type syncSubscriptionInputRequest struct {
	Code             string                             `json:"code"`
	Name             string                             `json:"name"`
	Description      *string                            `json:"description,omitempty"`
	Target           string                             `json:"target"`
	ConnectionID     *string                            `json:"connectionId,omitempty"`
	EventTypes       []syncSubscriptionEventTypeRequest `json:"eventTypes"`
	DispatchPoolCode *string                            `json:"dispatchPoolCode,omitempty"`
	Mode             *string                            `json:"mode,omitempty"`
	MaxRetries       *int32                             `json:"maxRetries,omitempty"`
	TimeoutSeconds   *int32                             `json:"timeoutSeconds,omitempty"`
	DataOnly         bool                               `json:"dataOnly,omitempty"`
}

type syncSubscriptionsRequest struct {
	Subscriptions []syncSubscriptionInputRequest `json:"subscriptions"`
}

type syncSubscriptionsInput struct {
	AppCode        string `path:"appCode" doc:"Application code"`
	RemoveUnlisted bool   `query:"removeUnlisted" doc:"Remove API/CODE subscriptions not in the list"`
	Body           syncSubscriptionsRequest
}

func (s *State) syncSubscriptions(ctx context.Context, in *syncSubscriptionsInput) (*syncResultOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncSubscriptions(ac); err != nil {
		return nil, err
	}
	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	inputs := make([]subscriptionops.SyncSubscriptionInput, 0, len(in.Body.Subscriptions))
	for _, sub := range in.Body.Subscriptions {
		bindings := make([]subscriptionops.SyncEventTypeBindingInput, 0, len(sub.EventTypes))
		for _, et := range sub.EventTypes {
			bindings = append(bindings, subscriptionops.SyncEventTypeBindingInput{
				EventTypeCode: et.EventTypeCode,
				Filter:        et.Filter,
			})
		}
		inputs = append(inputs, subscriptionops.SyncSubscriptionInput{
			Code:             sub.Code,
			Name:             sub.Name,
			Description:      sub.Description,
			Target:           sub.Target,
			ConnectionID:     sub.ConnectionID,
			EventTypes:       bindings,
			DispatchPoolCode: sub.DispatchPoolCode,
			Mode:             sub.Mode,
			MaxRetries:       sub.MaxRetries,
			TimeoutSeconds:   sub.TimeoutSeconds,
			DataOnly:         sub.DataOnly,
		})
	}

	cmd := subscriptionops.SyncSubscriptionsCommand{
		ApplicationCode: app.Code,
		Subscriptions:   inputs,
		RemoveUnlisted:  in.RemoveUnlisted,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := subscriptionops.SyncSubscriptions(ctx, s.Subscriptions, s.Connections, s.DispatchPools, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	return &syncResultOutput{Body: SyncResultResponse{
		ApplicationCode: ev.ApplicationCode,
		Created:         ev.Created,
		Updated:         ev.Updated,
		Deleted:         ev.Deleted,
		SyncedCodes:     ev.SyncedCodes,
	}}, nil
}

// ── Principals ────────────────────────────────────────────────────────────

type syncPrincipalInputRequest struct {
	Email  string   `json:"email" doc:"User's email address (unique identifier for matching)"`
	Name   string   `json:"name"`
	Roles  []string `json:"roles,omitempty" doc:"Role short names (prefixed with applicationCode)"`
	Active *bool    `json:"active,omitempty" doc:"Whether the user is active (default true)"`
}

type syncPrincipalsRequest struct {
	Principals []syncPrincipalInputRequest `json:"principals"`
}

type syncPrincipalsInput struct {
	AppCode        string `path:"appCode" doc:"Application code"`
	RemoveUnlisted bool   `query:"removeUnlisted" doc:"Strip SDK_SYNC roles from unlisted principals"`
	Body           syncPrincipalsRequest
}

func (s *State) syncPrincipals(ctx context.Context, in *syncPrincipalsInput) (*syncResultOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncPrincipals(ac); err != nil {
		return nil, err
	}
	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	inputs := make([]principalops.SyncPrincipalInput, 0, len(in.Body.Principals))
	for _, p := range in.Body.Principals {
		active := true // serde default_active
		if p.Active != nil {
			active = *p.Active
		}
		inputs = append(inputs, principalops.SyncPrincipalInput{
			Email:  p.Email,
			Name:   p.Name,
			Roles:  p.Roles,
			Active: active,
		})
	}

	cmd := principalops.SyncPrincipalsCommand{
		ApplicationCode: app.Code,
		Principals:      inputs,
		RemoveUnlisted:  in.RemoveUnlisted,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := principalops.SyncPrincipals(ctx, s.Principals, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	return &syncResultOutput{Body: SyncResultResponse{
		ApplicationCode: ev.ApplicationCode,
		Created:         ev.Created,
		Updated:         ev.Updated,
		Deleted:         ev.Deactivated,
		SyncedCodes:     ev.SyncedEmails,
	}}, nil
}

// ── Dispatch pools ────────────────────────────────────────────────────────

type syncDispatchPoolInputRequest struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	RateLimit   *int32  `json:"rateLimit,omitempty" doc:"Messages per minute; omit for concurrency-only"`
	// Concurrency defaults to 10 when omitted (matches the Rust default).
	Concurrency *int32 `json:"concurrency,omitempty"`
}

type syncDispatchPoolsRequest struct {
	Pools []syncDispatchPoolInputRequest `json:"pools"`
}

type syncDispatchPoolsInput struct {
	AppCode        string `path:"appCode" doc:"Application code"`
	RemoveUnlisted bool   `query:"removeUnlisted" doc:"Archive pools not in the list"`
	Body           syncDispatchPoolsRequest
}

func (s *State) syncDispatchPools(ctx context.Context, in *syncDispatchPoolsInput) (*syncResultOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncDispatchPools(ac); err != nil {
		return nil, err
	}
	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	inputs := make([]dispatchpoolops.SyncDispatchPoolInput, 0, len(in.Body.Pools))
	for _, p := range in.Body.Pools {
		concurrency := int32(10) // serde default_concurrency
		if p.Concurrency != nil {
			concurrency = *p.Concurrency
		}
		inputs = append(inputs, dispatchpoolops.SyncDispatchPoolInput{
			Code:        p.Code,
			Name:        p.Name,
			Description: p.Description,
			RateLimit:   p.RateLimit,
			Concurrency: concurrency,
		})
	}

	cmd := dispatchpoolops.SyncDispatchPoolsCommand{
		ApplicationCode: app.Code,
		Pools:           inputs,
		RemoveUnlisted:  in.RemoveUnlisted,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := dispatchpoolops.SyncDispatchPools(ctx, s.DispatchPools, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	return &syncResultOutput{Body: SyncResultResponse{
		ApplicationCode: ev.ApplicationCode,
		Created:         ev.Created,
		Updated:         ev.Updated,
		Deleted:         ev.Deleted,
		SyncedCodes:     ev.SyncedCodes,
	}}, nil
}

// ── Processes ─────────────────────────────────────────────────────────────

type syncProcessInputRequest struct {
	Code        string   `json:"code" doc:"Full code (application:subdomain:process-name)"`
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	Body        string   `json:"body,omitempty" doc:"Diagram body (typically Mermaid source)"`
	DiagramType *string  `json:"diagramType,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type syncProcessesRequest struct {
	Processes []syncProcessInputRequest `json:"processes"`
}

type syncProcessesInput struct {
	AppCode        string `path:"appCode" doc:"Application code"`
	RemoveUnlisted bool   `query:"removeUnlisted" doc:"Remove API/CODE processes not in the list"`
	Body           syncProcessesRequest
}

func (s *State) syncProcesses(ctx context.Context, in *syncProcessesInput) (*syncResultOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncProcesses(ac); err != nil {
		return nil, err
	}
	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	inputs := make([]processops.SyncProcessInput, 0, len(in.Body.Processes))
	for _, p := range in.Body.Processes {
		inputs = append(inputs, processops.SyncProcessInput{
			Code:        p.Code,
			Name:        p.Name,
			Description: p.Description,
			Body:        p.Body,
			DiagramType: p.DiagramType,
			Tags:        p.Tags,
		})
	}

	cmd := processops.SyncProcessesCommand{
		ApplicationCode: app.Code,
		Processes:       inputs,
		RemoveUnlisted:  in.RemoveUnlisted,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := processops.SyncProcesses(ctx, s.Processes, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	return &syncResultOutput{Body: SyncResultResponse{
		ApplicationCode: ev.ApplicationCode,
		Created:         ev.Created,
		Updated:         ev.Updated,
		Deleted:         ev.Deleted,
		SyncedCodes:     ev.SyncedCodes,
	}}, nil
}

// ── Scheduled jobs ────────────────────────────────────────────────────────

// SyncScheduledJobsResultResponse is the scheduled-job sync result. Unlike
// the list-based resources it returns the affected job IDs (not counts) and
// uses archive (not delete) semantics — mirrors the Rust shape.
type SyncScheduledJobsResultResponse struct {
	ApplicationCode string   `json:"applicationCode"`
	Created         []string `json:"created"`
	Updated         []string `json:"updated"`
	Archived        []string `json:"archived"`
}

type syncScheduledJobInputRequest struct {
	Code                string          `json:"code"`
	Name                string          `json:"name"`
	Description         *string         `json:"description,omitempty"`
	Crons               []string        `json:"crons"`
	Timezone            string          `json:"timezone,omitempty" doc:"IANA timezone (default UTC)"`
	Payload             json.RawMessage `json:"payload,omitempty"`
	Concurrent          bool            `json:"concurrent,omitempty"`
	TracksCompletion    bool            `json:"tracksCompletion,omitempty"`
	TimeoutSeconds      *int32          `json:"timeoutSeconds,omitempty"`
	DeliveryMaxAttempts *int32          `json:"deliveryMaxAttempts,omitempty" doc:"Default 3 when omitted"`
	TargetURL           *string         `json:"targetUrl,omitempty"`
}

type syncScheduledJobsRequest struct {
	// ClientID nil/omitted = platform-scoped jobs (anchor only).
	ClientID        *string                        `json:"clientId,omitempty"`
	Jobs            []syncScheduledJobInputRequest `json:"jobs"`
	ArchiveUnlisted bool                           `json:"archiveUnlisted,omitempty"`
}

type syncScheduledJobsInput struct {
	AppCode string `path:"appCode" doc:"Application code"`
	Body    syncScheduledJobsRequest
}

type syncScheduledJobsOutput struct {
	Body SyncScheduledJobsResultResponse
}

func (s *State) syncScheduledJobs(ctx context.Context, in *syncScheduledJobsInput) (*syncScheduledJobsOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncScheduledJobs(ac); err != nil {
		return nil, err
	}

	// Resource-level scope check: caller must have access to the target client
	// (or be anchor/super-admin when targeting platform-scoped jobs). Mirrors
	// the Rust handler.
	if cid := in.Body.ClientID; cid != nil {
		if !ac.CanAccessClient(*cid) {
			return nil, httperror.Forbidden("No access to client: " + *cid)
		}
	} else if !ac.IsAnchor() && !ac.IsSuperAdmin() {
		return nil, httperror.Forbidden("Only anchor users can sync platform-scoped scheduled jobs")
	}

	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	jobs := make([]scheduledjobops.ScheduledJobSyncEntry, 0, len(in.Body.Jobs))
	for _, j := range in.Body.Jobs {
		tz := j.Timezone // serde default_tz
		if tz == "" {
			tz = "UTC"
		}
		maxAttempts := int32(3) // serde default_attempts
		if j.DeliveryMaxAttempts != nil {
			maxAttempts = *j.DeliveryMaxAttempts
		}
		jobs = append(jobs, scheduledjobops.ScheduledJobSyncEntry{
			Code:                j.Code,
			Name:                j.Name,
			Description:         j.Description,
			Crons:               j.Crons,
			Timezone:            tz,
			Payload:             j.Payload,
			Concurrent:          j.Concurrent,
			TracksCompletion:    j.TracksCompletion,
			TimeoutSeconds:      j.TimeoutSeconds,
			DeliveryMaxAttempts: maxAttempts,
			TargetURL:           j.TargetURL,
		})
	}

	cmd := scheduledjobops.SyncScheduledJobsCommand{
		ApplicationCode: app.Code,
		ClientID:        in.Body.ClientID,
		Jobs:            jobs,
		ArchiveUnlisted: in.Body.ArchiveUnlisted,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := scheduledjobops.SyncScheduledJobs(ctx, s.ScheduledJobs, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	return &syncScheduledJobsOutput{Body: SyncScheduledJobsResultResponse{
		ApplicationCode: app.Code,
		Created:         defaultEmptySlice(ev.Created),
		Updated:         defaultEmptySlice(ev.Updated),
		Archived:        defaultEmptySlice(ev.Archived),
	}}, nil
}

// defaultEmptySlice returns a non-nil empty slice so the JSON arrays serialize
// as [] rather than null.
func defaultEmptySlice(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// ── OpenAPI document ──────────────────────────────────────────────────────

// SyncOpenApiSpecResponse is the openapi sync result. Unlike the list-based
// resources this is a single-document, versioned sync, so it has its own
// shape (mirrors the Rust SyncOpenApiSpecResponse).
type SyncOpenApiSpecResponse struct {
	ApplicationCode      string  `json:"applicationCode"`
	SpecID               string  `json:"specId"`
	Version              string  `json:"version"`
	Status               string  `json:"status"`
	ArchivedPriorVersion *string `json:"archivedPriorVersion,omitempty"`
	HasBreaking          bool    `json:"hasBreaking"`
	Unchanged            bool    `json:"unchanged"`
}

type syncOpenapiRequest struct {
	Spec json.RawMessage `json:"spec" doc:"The OpenAPI document (OpenAPI 3.x or Swagger 2.x)"`
}

type syncOpenapiInput struct {
	AppCode string `path:"appCode" doc:"Application code"`
	Body    syncOpenapiRequest
}

type syncOpenapiOutput struct {
	Body SyncOpenApiSpecResponse
}

func (s *State) syncOpenapi(ctx context.Context, in *syncOpenapiInput) (*syncOpenapiOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanSyncApplicationOpenAPI(ac); err != nil {
		return nil, err
	}
	app, err := s.resolveApp(ctx, in.AppCode)
	if err != nil {
		return nil, err
	}

	// Resource-level guard: anchor / super-admin may sync any application;
	// otherwise the caller must BE this application's bound service account.
	// Mirrors the Rust handler's per-application gate.
	isAppServiceAccount := app.ServiceAccountID != nil && *app.ServiceAccountID == ac.PrincipalID
	if !(ac.IsAnchor() || ac.IsSuperAdmin() || isAppServiceAccount) {
		return nil, httperror.Forbidden("Service account is not authorised for application '" + app.Code + "'")
	}

	cmd := openapiops.SyncOpenApiSpecCommand{
		ApplicationID:   app.ID,
		ApplicationCode: app.Code,
		Spec:            in.Body.Spec,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := openapiops.SyncOpenApiSpec(ctx, s.Specs, s.UoW, cmd, ec)
	if err != nil {
		return nil, err
	}
	ev := committed.Event()
	status := "CURRENT"
	if ev.Unchanged {
		status = "UNCHANGED"
	}
	return &syncOpenapiOutput{Body: SyncOpenApiSpecResponse{
		ApplicationCode:      ev.ApplicationCode,
		SpecID:               ev.SpecID,
		Version:              ev.Version,
		Status:               status,
		ArchivedPriorVersion: ev.ArchivedPriorVersion,
		HasBreaking:          ev.HasBreaking,
		Unchanged:            ev.Unchanged,
	}}, nil
}
