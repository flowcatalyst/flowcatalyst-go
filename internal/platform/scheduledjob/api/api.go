// Package api wires HTTP routes for scheduled_job via huma.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// State bundles deps. Instances is optional — leave nil if the instance
// surface isn't wired (the routes will then 501).
type State struct {
	Repo      *scheduledjob.Repository
	Instances *scheduledjob.InstanceRepository
	UoW       *usecasepgx.UnitOfWork
}

const tag = "scheduled-jobs"

// Register mounts the scheduled-job endpoints.
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "listScheduledJobs",
		Method:        http.MethodGet,
		Path:          "/api/scheduled-jobs",
		Summary:       "List scheduled jobs",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.list)

	huma.Register(api, huma.Operation{
		OperationID:   "createScheduledJob",
		Method:        http.MethodPost,
		Path:          "/api/scheduled-jobs",
		Summary:       "Create a scheduled job",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
	}, s.create)

	huma.Register(api, huma.Operation{
		OperationID:   "getScheduledJob",
		Method:        http.MethodGet,
		Path:          "/api/scheduled-jobs/{id}",
		Summary:       "Get a scheduled job by id",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByID)

	huma.Register(api, huma.Operation{
		OperationID:   "updateScheduledJob",
		Method:        http.MethodPut,
		Path:          "/api/scheduled-jobs/{id}",
		Summary:       "Update a scheduled job",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.update)

	huma.Register(api, huma.Operation{
		OperationID:   "pauseScheduledJob",
		Method:        http.MethodPost,
		Path:          "/api/scheduled-jobs/{id}/pause",
		Summary:       "Pause a scheduled job",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.pause)

	huma.Register(api, huma.Operation{
		OperationID:   "resumeScheduledJob",
		Method:        http.MethodPost,
		Path:          "/api/scheduled-jobs/{id}/resume",
		Summary:       "Resume a scheduled job",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.resume)

	huma.Register(api, huma.Operation{
		OperationID:   "archiveScheduledJob",
		Method:        http.MethodPost,
		Path:          "/api/scheduled-jobs/{id}/archive",
		Summary:       "Archive a scheduled job",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.archive)

	huma.Register(api, huma.Operation{
		OperationID:   "fireScheduledJobNow",
		Method:        http.MethodPost,
		Path:          "/api/scheduled-jobs/{id}/fire",
		Summary:       "Fire a scheduled job immediately",
		Tags:          []string{tag},
		DefaultStatus: http.StatusAccepted,
	}, s.fireNow)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteScheduledJob",
		Method:        http.MethodDelete,
		Path:          "/api/scheduled-jobs/{id}",
		Summary:       "Delete a scheduled job",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.delete)

	huma.Register(api, huma.Operation{
		OperationID:   "getScheduledJobByCode",
		Method:        http.MethodGet,
		Path:          "/api/scheduled-jobs/by-code/{code}",
		Summary:       "Get a scheduled job by code",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByCode)

	huma.Register(api, huma.Operation{
		OperationID:   "listScheduledJobInstances",
		Method:        http.MethodGet,
		Path:          "/api/scheduled-jobs/{id}/instances",
		Summary:       "List firings for a scheduled job",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listInstances)

	huma.Register(api, huma.Operation{
		OperationID:   "getScheduledJobInstance",
		Method:        http.MethodGet,
		Path:          "/api/scheduled-jobs/instances/{instanceId}",
		Summary:       "Get a single scheduled-job instance",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getInstance)

	huma.Register(api, huma.Operation{
		OperationID:   "listScheduledJobInstanceLogs",
		Method:        http.MethodGet,
		Path:          "/api/scheduled-jobs/instances/{instanceId}/logs",
		Summary:       "List log entries for an instance",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listInstanceLogs)

	huma.Register(api, huma.Operation{
		OperationID:   "writeScheduledJobInstanceLog",
		Method:        http.MethodPost,
		Path:          "/api/scheduled-jobs/instances/{instanceId}/log",
		Summary:       "Append a log entry to an instance",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.writeInstanceLog)

	huma.Register(api, huma.Operation{
		OperationID:   "completeScheduledJobInstance",
		Method:        http.MethodPost,
		Path:          "/api/scheduled-jobs/instances/{instanceId}/complete",
		Summary:       "Mark a scheduled-job instance as completed",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.completeInstance)
}

type listInput struct {
	Status   string `query:"status"`
	ClientID string `query:"clientId"`
	Search   string `query:"search"`
	apicommon.PageQuery
}

type listOutput struct {
	Body apicommon.OffsetPage[ScheduledJobResponse]
}

func (s *State) list(ctx context.Context, in *listInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadScheduledJobs(ac); err != nil {
		return nil, err
	}
	filters := scheduledjob.ListFilters{}
	if in.Status != "" {
		filters.Status = &in.Status
	}
	if in.ClientID != "" {
		// The literal "platform" selects platform-scoped jobs (client_id IS
		// NULL), which the repo expresses as a pointer-to-"". Mirrors the
		// Rust list handler's Some("platform") => Some(None) mapping.
		if in.ClientID == "platform" {
			empty := ""
			filters.ClientID = &empty
		} else {
			filters.ClientID = &in.ClientID
		}
	}
	if in.Search != "" {
		filters.Search = &in.Search
	}
	// Scope to accessible clients in SQL (anchor sees all → no scoping) so
	// COUNT and LIMIT/OFFSET stay consistent across pages.
	if !ac.IsAnchor() {
		clients := ac.Clients
		filters.AccessibleClientIDs = &clients
	}
	total, err := s.Repo.CountWithFilters(ctx, filters)
	if err != nil {
		return nil, usecase.Internal("REPO", "count_with_filters failed", err)
	}
	limit, offset := in.LimitVal(), in.OffsetVal()
	filters.Limit, filters.Offset = &limit, &offset
	rows, err := s.Repo.FindWithFilters(ctx, filters)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_with_filters failed", err)
	}
	out := make([]ScheduledJobResponse, 0, len(rows))
	for i := range rows {
		resp := fromEntity(&rows[i])
		if active, err := s.Instances.HasActiveInstance(ctx, rows[i].ID); err == nil {
			resp.HasActiveInstance = active
		}
		out = append(out, resp)
	}
	page := apicommon.NewOffsetPage(out, in.PageIndex(), in.PageSizeVal(), total)
	return &listOutput{Body: page}, nil
}

type getInput struct {
	ID string `path:"id"`
}

type getOutput struct {
	Body ScheduledJobResponse
}

func (s *State) getByID(ctx context.Context, in *getInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadScheduledJobs(ac); err != nil {
		return nil, err
	}
	j, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return nil, httperror.NotFound("ScheduledJob", in.ID)
	}
	if j.ClientID != nil && !ac.CanAccessClient(*j.ClientID) {
		return nil, httperror.Forbidden("No access to this scheduled job")
	}
	resp := fromEntity(j)
	if active, err := s.Instances.HasActiveInstance(ctx, j.ID); err == nil {
		resp.HasActiveInstance = active
	}
	return &getOutput{Body: resp}, nil
}

type createInput struct {
	Body CreateScheduledJobRequest
}

type createOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) create(ctx context.Context, in *createInput) (*createOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if in.Body.ClientID != nil && !ac.CanAccessClient(*in.Body.ClientID) {
		return nil, httperror.Forbidden("No access to client: " + *in.Body.ClientID)
	}
	if in.Body.ClientID == nil && !ac.IsAnchor() {
		return nil, httperror.Forbidden("Only anchor users can create platform-scoped jobs")
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateScheduledJob(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().ScheduledJobID}}, nil
}

// requireScopeByID loads the scheduled job and enforces per-resource scope
// (A2) on top of the coarse permission already checked: a non-anchor principal
// must not mutate another tenant's scheduled job by id. Mirrors Rust
// check_scope_access(auth, job.client_id).
func (s *State) requireScopeByID(ctx context.Context, ac *auth.AuthContext, id string) error {
	j, err := s.Repo.FindByID(ctx, id)
	if err != nil {
		return usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return httperror.NotFound("ScheduledJob", id)
	}
	return auth.CheckScopeAccess(ac, j.ClientID)
}

type updateInput struct {
	ID   string `path:"id"`
	Body UpdateScheduledJobRequest
}

type emptyOutput struct{}

func (s *State) update(ctx context.Context, in *updateInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateScheduledJob(ctx, s.Repo, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type idInput struct {
	ID string `path:"id"`
}

func (s *State) pause(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.PauseScheduledJob(ctx, s.Repo, s.UoW, operations.PauseCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

func (s *State) resume(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ResumeScheduledJob(ctx, s.Repo, s.UoW, operations.ResumeCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

func (s *State) archive(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ArchiveScheduledJob(ctx, s.Repo, s.UoW, operations.ArchiveCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type fireNowInput struct {
	ID   string `path:"id"`
	Body *FireNowRequest
}

type fireNowOutput struct {
	Body FireNowResponse
}

func (s *State) fireNow(ctx context.Context, in *fireNowInput) (*fireNowOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanFireScheduledJobs(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	var correlationID *string
	if in.Body != nil {
		correlationID = in.Body.CorrelationID
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.FireNow(ctx, s.Repo, s.Instances, s.UoW,
		operations.FireNowCommand{ID: in.ID, CorrelationID: correlationID}, ec)
	if err != nil {
		return nil, err
	}
	return &fireNowOutput{Body: FireNowResponse{
		ScheduledJobID: committed.Event().ScheduledJobID,
		InstanceID:     committed.Event().InstanceID,
	}}, nil
}

func (s *State) delete(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanDeleteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteScheduledJob(ctx, s.Repo, s.UoW, operations.DeleteCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

// ── by-code lookup ──────────────────────────────────────────────────────

type byCodeInput struct {
	Code     string `path:"code"`
	ClientID string `query:"clientId" doc:"Optional client scope; omit for platform-scoped lookup"`
}

func (s *State) getByCode(ctx context.Context, in *byCodeInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadScheduledJobs(ac); err != nil {
		return nil, err
	}
	var clientID *string
	if in.ClientID != "" {
		clientID = &in.ClientID
	}
	j, err := s.Repo.FindByCode(ctx, in.Code, clientID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_code failed", err)
	}
	if j == nil {
		return nil, httperror.NotFound("ScheduledJob", in.Code)
	}
	if j.ClientID != nil && !ac.CanAccessClient(*j.ClientID) {
		return nil, httperror.Forbidden("No access to this scheduled job")
	}
	resp := fromEntity(j)
	if active, err := s.Instances.HasActiveInstance(ctx, j.ID); err == nil {
		resp.HasActiveInstance = active
	}
	return &getOutput{Body: resp}, nil
}

// ── instance endpoints ──────────────────────────────────────────────────

type listInstancesInput struct {
	ID     string `path:"id"`
	Status string `query:"status"`
	apicommon.PageQuery
}

type listInstancesOutput struct {
	Body apicommon.OffsetPage[ScheduledJobInstanceResponse]
}

func (s *State) listInstances(ctx context.Context, in *listInstancesInput) (*listInstancesOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadScheduledJobs(ac); err != nil {
		return nil, err
	}
	if s.Instances == nil {
		return nil, usecase.Internal("WIRING", "instances repo not configured", nil)
	}
	filters := scheduledjob.InstanceListFilters{ScheduledJobID: &in.ID}
	if in.Status != "" {
		st := scheduledjob.ParseInstanceStatus(in.Status)
		filters.Status = &st
	}
	total, err := s.Instances.Count(ctx, filters)
	if err != nil {
		return nil, usecase.Internal("REPO", "count_instances failed", err)
	}
	limit, offset := in.LimitVal(), in.OffsetVal()
	filters.Limit, filters.Offset = &limit, &offset
	rows, err := s.Instances.List(ctx, filters)
	if err != nil {
		return nil, usecase.Internal("REPO", "list_instances failed", err)
	}
	out := make([]ScheduledJobInstanceResponse, 0, len(rows))
	for i := range rows {
		out = append(out, instanceToResponse(&rows[i]))
	}
	page := apicommon.NewOffsetPage(out, in.PageIndex(), in.PageSizeVal(), total)
	return &listInstancesOutput{Body: page}, nil
}

type instanceInput struct {
	InstanceID string `path:"instanceId"`
}

type instanceOutput struct {
	Body ScheduledJobInstanceResponse
}

func (s *State) getInstance(ctx context.Context, in *instanceInput) (*instanceOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadScheduledJobs(ac); err != nil {
		return nil, err
	}
	if s.Instances == nil {
		return nil, usecase.Internal("WIRING", "instances repo not configured", nil)
	}
	inst, err := s.Instances.FindByID(ctx, in.InstanceID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_instance failed", err)
	}
	if inst == nil {
		return nil, httperror.NotFound("ScheduledJobInstance", in.InstanceID)
	}
	if inst.ClientID != nil && !ac.CanAccessClient(*inst.ClientID) {
		return nil, httperror.Forbidden("No access to this instance")
	}
	return &instanceOutput{Body: instanceToResponse(inst)}, nil
}

// instanceLogsOutput Body is a bare JSON array — the Rust shape for
// GET /api/scheduled-jobs/instances/{instanceId}/logs.
type instanceLogsOutput struct {
	Body []ScheduledJobInstanceLogResponse
}

func (s *State) listInstanceLogs(ctx context.Context, in *instanceInput) (*instanceLogsOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadScheduledJobs(ac); err != nil {
		return nil, err
	}
	if s.Instances == nil {
		return nil, usecase.Internal("WIRING", "instances repo not configured", nil)
	}
	rows, err := s.Instances.ListLogs(ctx, in.InstanceID, 500)
	if err != nil {
		return nil, usecase.Internal("REPO", "list_logs failed", err)
	}
	out := make([]ScheduledJobInstanceLogResponse, 0, len(rows))
	for i := range rows {
		out = append(out, instanceLogToResponse(&rows[i]))
	}
	return &instanceLogsOutput{Body: out}, nil
}

type writeLogInput struct {
	InstanceID string `path:"instanceId"`
	Body       WriteInstanceLogRequest
}

func (s *State) writeInstanceLog(ctx context.Context, in *writeLogInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if s.Instances == nil {
		return nil, usecase.Internal("WIRING", "instances repo not configured", nil)
	}
	inst, err := s.Instances.FindByID(ctx, in.InstanceID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_instance failed", err)
	}
	if inst == nil {
		return nil, httperror.NotFound("ScheduledJobInstance", in.InstanceID)
	}
	if err := auth.CheckScopeAccess(ac, inst.ClientID); err != nil { // A2: per-instance client scope
		return nil, err
	}
	log := &scheduledjob.ScheduledJobInstanceLog{
		ID:             tsid.Generate(tsid.ScheduledJobInstanceLog),
		InstanceID:     in.InstanceID,
		ScheduledJobID: &inst.ScheduledJobID,
		ClientID:       inst.ClientID,
		Level:          in.Body.Level,
		Message:        in.Body.Message,
		Metadata:       in.Body.Metadata,
	}
	if err := s.Instances.WriteLog(ctx, log); err != nil {
		return nil, usecase.Internal("REPO", "write_log failed", err)
	}
	return &emptyOutput{}, nil
}

type completeInstanceInput struct {
	InstanceID string `path:"instanceId"`
	Body       CompleteInstanceRequest
}

func (s *State) completeInstance(ctx context.Context, in *completeInstanceInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWriteScheduledJobs(ac); err != nil {
		return nil, err
	}
	if s.Instances == nil {
		return nil, usecase.Internal("WIRING", "instances repo not configured", nil)
	}
	inst, err := s.Instances.FindByID(ctx, in.InstanceID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_instance failed", err)
	}
	if inst == nil {
		return nil, httperror.NotFound("ScheduledJobInstance", in.InstanceID)
	}
	if err := auth.CheckScopeAccess(ac, inst.ClientID); err != nil { // A2: per-instance client scope
		return nil, err
	}
	status := scheduledjob.InstanceStatusCompleted
	if in.Body.Status != "" {
		status = scheduledjob.ParseInstanceStatus(in.Body.Status)
	}
	var compStatus *string
	if in.Body.CompletionStatus != "" {
		compStatus = &in.Body.CompletionStatus
	}
	if err := s.Instances.MarkComplete(ctx, in.InstanceID, status, compStatus, in.Body.CompletionResult); err != nil {
		return nil, usecase.Internal("REPO", "mark_complete failed", err)
	}
	return &emptyOutput{}, nil
}
