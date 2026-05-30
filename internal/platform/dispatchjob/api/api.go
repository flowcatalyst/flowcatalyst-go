// Package api wires the dispatch-job read-only HTTP endpoints via huma.
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchjob"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

// State bundles deps.
type State struct {
	Repo *dispatchjob.Repository
}

const (
	tag         = "dispatch-jobs"
	viewPerm    = "platform:messaging:dispatch-job:view"
	viewRawPerm = "platform:messaging:dispatch-job:view-raw"
)

// Register mounts the dispatch-job endpoints.
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchJobs",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-jobs",
		Summary:       "List dispatch jobs with filters",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.list)

	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchJobsRaw",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-jobs/list-raw",
		Summary:       "List dispatch jobs (raw)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listRaw)

	huma.Register(api, huma.Operation{
		OperationID:   "dispatchJobFilterOptions",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-jobs/filter-options",
		Summary:       "Distinct facet values for dispatch jobs",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.filterOptions)

	huma.Register(api, huma.Operation{
		OperationID:   "dispatchJobsByEvent",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-jobs/event/{eventId}",
		Summary:       "Dispatch jobs spawned by a specific event",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.byEvent)

	huma.Register(api, huma.Operation{
		OperationID:   "getDispatchJob",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-jobs/{id}",
		Summary:       "Get a dispatch job by id",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByID)

	huma.Register(api, huma.Operation{
		OperationID:   "getDispatchJobRaw",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-jobs/{id}/raw",
		Summary:       "Get a dispatch job (raw)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getRaw)

	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchJobAttempts",
		Method:        http.MethodGet,
		Path:          "/api/dispatch-jobs/{id}/attempts",
		Summary:       "List a dispatch job's attempt history",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.attempts)

	// BFF tier — /bff/dispatch-jobs mirrors the regular handlers under
	// cookie-auth. Mirrors Rust.
	registerBFF(api, s, "/bff/dispatch-jobs", "Bff", "bff-dispatch-jobs")

	// /bff/debug/dispatch-jobs is a SEPARATE raw-job view (write-side
	// msg_dispatch_jobs). The SPA's RawDispatchJobListPage binds a bare
	// array of the raw envelope shape, so it gets its own handler.
	// Mirrors Rust's shared/debug_api.rs.
	huma.Register(api, huma.Operation{
		OperationID:   "listDebugDispatchJobs",
		Method:        http.MethodGet,
		Path:          "/bff/debug/dispatch-jobs",
		Summary:       "List raw dispatch jobs (debug view of msg_dispatch_jobs)",
		Tags:          []string{"bff-debug-dispatch-jobs"},
		DefaultStatus: http.StatusOK,
	}, s.listDebugRaw)
}

// registerBFF dual-mounts the dispatch-job handlers under an alternate
// base path so the SPA can hit /bff/dispatch-jobs with cookie-auth.
func registerBFF(api huma.API, s *State, base, opPrefix, tag string) {
	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchJobs" + opPrefix,
		Method:        http.MethodGet,
		Path:          base,
		Summary:       "List dispatch jobs",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.list)

	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchJobsRaw" + opPrefix,
		Method:        http.MethodGet,
		Path:          base + "/list-raw",
		Summary:       "List dispatch jobs with raw rows",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listRaw)

	huma.Register(api, huma.Operation{
		OperationID:   "dispatchJobFilterOptions" + opPrefix,
		Method:        http.MethodGet,
		Path:          base + "/filter-options",
		Summary:       "Distinct filter values for dispatch jobs",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.filterOptions)

	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchJobsByEvent" + opPrefix,
		Method:        http.MethodGet,
		Path:          base + "/event/{eventId}",
		Summary:       "List dispatch jobs created by an event",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.byEvent)

	huma.Register(api, huma.Operation{
		OperationID:   "getDispatchJob" + opPrefix,
		Method:        http.MethodGet,
		Path:          base + "/{id}",
		Summary:       "Get a dispatch job by id",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByID)

	huma.Register(api, huma.Operation{
		OperationID:   "getDispatchJobRaw" + opPrefix,
		Method:        http.MethodGet,
		Path:          base + "/{id}/raw",
		Summary:       "Get a dispatch job with raw row",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getRaw)

	huma.Register(api, huma.Operation{
		OperationID:   "listDispatchJobAttempts" + opPrefix,
		Method:        http.MethodGet,
		Path:          base + "/{id}/attempts",
		Summary:       "List a dispatch job's attempt history",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.attempts)
}

type listInput struct {
	Status         string `query:"status"`
	ClientID       string `query:"clientId"`
	DispatchPoolID string `query:"dispatchPoolId"`
	SubscriptionID string `query:"subscriptionId"`
	Code           string `query:"code"`
	Since          string `query:"since" doc:"RFC3339 timestamp"`
	Until          string `query:"until" doc:"RFC3339 timestamp"`
	Limit          int    `query:"limit"`
	Offset         int    `query:"offset"`

	// SPA params (dispatch-jobs.ts:35-44). `size` caps rows; the plural
	// params are comma-separated multi-filters.
	Size         int    `query:"size" doc:"Max rows (default 50, max 1000)"`
	ClientIDs    string `query:"clientIds" doc:"CSV of client ids"`
	Statuses     string `query:"statuses" doc:"CSV of statuses"`
	Applications string `query:"applications" doc:"CSV of application codes"`
	Subdomains   string `query:"subdomains" doc:"CSV of subdomains"`
	Aggregates   string `query:"aggregates" doc:"CSV of aggregates"`
	Codes        string `query:"codes" doc:"CSV of codes"`
	Source       string `query:"source" doc:"Free-text source filter"`
}

// splitCSV mirrors Rust's split_csv (dispatch_job/api.rs): trim, drop empties.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (in *listInput) toFilters() dispatchjob.FilterParams {
	str := func(v string) *string {
		if v == "" {
			return nil
		}
		s := v
		return &s
	}
	ts := func(v string) *time.Time {
		if v == "" {
			return nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return &t
		}
		return nil
	}
	// `size` (SPA) and `limit` (SDK) both cap rows; size wins when set.
	limit := in.Limit
	if in.Size > 0 {
		limit = in.Size
	}
	// `source` free-text reuses the singular Source filter.
	src := str(in.Source)
	return dispatchjob.FilterParams{
		Status:         str(in.Status),
		ClientID:       str(in.ClientID),
		DispatchPoolID: str(in.DispatchPoolID),
		SubscriptionID: str(in.SubscriptionID),
		Code:           str(in.Code),
		Source:         src,
		Since:          ts(in.Since),
		Until:          ts(in.Until),
		Limit:          limit,
		Offset:         in.Offset,
		ClientIDs:      splitCSV(in.ClientIDs),
		Statuses:       splitCSV(in.Statuses),
		Applications:   splitCSV(in.Applications),
		Subdomains:     splitCSV(in.Subdomains),
		Aggregates:     splitCSV(in.Aggregates),
		Codes:          splitCSV(in.Codes),
	}
}

// listOutput Body is a bare JSON array — the SPA's DispatchJobListPage
// binds the returned array directly to its DataTable, so {items:[...]}
// would render zero rows. Mirrors Rust's list_dispatch_jobs returning
// Vec<DispatchJobReadResponse>.
type listOutput struct {
	Body []DispatchJobRead
}

func (s *State) list(ctx context.Context, in *listInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewPerm); err != nil {
		return nil, err
	}
	rows, err := s.Repo.FindWithFilters(ctx, in.toFilters())
	if err != nil {
		return nil, usecase.Internal("REPO", "find_with_filters failed", err)
	}
	out := make([]DispatchJobRead, 0, len(rows))
	for i := range rows {
		out = append(out, readFromEntity(&rows[i]))
	}
	return &listOutput{Body: out}, nil
}

func (s *State) listRaw(ctx context.Context, in *listInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewRawPerm); err != nil {
		return nil, err
	}
	rows, err := s.Repo.FindWithFilters(ctx, in.toFilters())
	if err != nil {
		return nil, usecase.Internal("REPO", "find_raw failed", err)
	}
	out := make([]DispatchJobRead, 0, len(rows))
	for i := range rows {
		out = append(out, readFromEntity(&rows[i]))
	}
	return &listOutput{Body: out}, nil
}

// ── debug raw dispatch jobs ──────────────────────────────────────────────

type rawListInput struct {
	Size int `query:"size" doc:"Max rows (default 50, max 1000)"`
}

// rawListOutput Body is a bare array of RawDispatchJobResponse — the SPA's
// RawDispatchJobListPage binds it directly.
type rawListOutput struct {
	Body []RawDispatchJobResponse
}

func (s *State) listDebugRaw(ctx context.Context, in *rawListInput) (*rawListOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewRawPerm); err != nil {
		return nil, err
	}
	limit := in.Size
	if limit <= 0 {
		limit = 50
	}
	// FindWithFilters already reads the write-side msg_dispatch_jobs table
	// (which carries the un-projected envelope the debug view needs); with
	// no filters set it returns the most-recent N jobs.
	rows, err := s.Repo.FindWithFilters(ctx, dispatchjob.FilterParams{Limit: limit})
	if err != nil {
		return nil, usecase.Internal("REPO", "find_recent_raw failed", err)
	}
	out := make([]RawDispatchJobResponse, 0, len(rows))
	for i := range rows {
		out = append(out, rawFromEntity(&rows[i]))
	}
	return &rawListOutput{Body: out}, nil
}

type getInput struct {
	ID string `path:"id"`
}

type getOutput struct {
	Body DispatchJobResponse
}

func (s *State) getByID(ctx context.Context, in *getInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewPerm); err != nil {
		return nil, err
	}
	j, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return nil, httperror.NotFound("DispatchJob", in.ID)
	}
	if err := auth.CheckScopeAccess(ac, j.ClientID); err != nil { // A2: per-resource client scope
		return nil, err
	}
	return &getOutput{Body: fromEntity(j)}, nil
}

func (s *State) getRaw(ctx context.Context, in *getInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewRawPerm); err != nil {
		return nil, err
	}
	j, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_raw failed", err)
	}
	if j == nil {
		return nil, httperror.NotFound("DispatchJob", in.ID)
	}
	if err := auth.CheckScopeAccess(ac, j.ClientID); err != nil { // A2: per-resource client scope
		return nil, err
	}
	return &getOutput{Body: fromEntity(j)}, nil
}

// attemptsOutput Body is a bare JSON array — the Rust shape for
// GET /api/dispatch-jobs/{id}/attempts.
type attemptsOutput struct {
	Body []AttemptDTO
}

func (s *State) attempts(ctx context.Context, in *getInput) (*attemptsOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewPerm); err != nil {
		return nil, err
	}
	// A2: load the job to enforce per-resource client scope before exposing
	// its attempts (the attempts query itself isn't client-scoped).
	j, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return nil, httperror.NotFound("DispatchJob", in.ID)
	}
	if err := auth.CheckScopeAccess(ac, j.ClientID); err != nil {
		return nil, err
	}
	rows, err := s.Repo.AttemptsByJob(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "attempts failed", err)
	}
	out := make([]AttemptDTO, 0, len(rows))
	for i := range rows {
		out = append(out, attemptFromEntity(&rows[i]))
	}
	return &attemptsOutput{Body: out}, nil
}

type byEventInput struct {
	EventID string `path:"eventId"`
}

func (s *State) byEvent(ctx context.Context, in *byEventInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewPerm); err != nil {
		return nil, err
	}
	rows, err := s.Repo.FindByEventID(ctx, in.EventID)
	if err != nil {
		return nil, usecase.Internal("REPO", "by_event failed", err)
	}
	out := make([]DispatchJobRead, 0, len(rows))
	for i := range rows {
		// A2: a non-anchor caller only sees jobs for clients it can access.
		if !auth.CanAccessScope(ac, rows[i].ClientID) {
			continue
		}
		out = append(out, readFromEntity(&rows[i]))
	}
	return &listOutput{Body: out}, nil
}

type emptyInput struct{}

type filterOptionsOutput struct {
	Body DispatchJobFilterOptionsResponse
}

func (s *State) filterOptions(ctx context.Context, _ *emptyInput) (*filterOptionsOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePermission(ac, viewPerm); err != nil {
		return nil, err
	}
	q := func(col string) []string {
		out, _ := s.Repo.DistinctValues(ctx, col, 200)
		return out
	}
	return &filterOptionsOutput{Body: DispatchJobFilterOptionsResponse{
		Statuses:        q("status"),
		Codes:           q("code"),
		ClientIDs:       q("client_id"),
		DispatchPoolIDs: q("dispatch_pool_id"),
		SubscriptionIDs: q("subscription_id"),
		Kinds:           q("kind"),
	}}, nil
}
