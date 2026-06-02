package bff

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs"
	openapiops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// DeveloperState holds the deps the /bff/developer/* endpoints reach
// into. Read-only for the six application/spec/event-type lookups;
// sync-platform-openapi is the one write endpoint.
//
// PlatformOpenAPI is a lazy accessor (not a cached []byte) because the
// huma API is constructed inside WirePlatform — the closure captures
// the spec generator and produces the live serialised document on
// demand.
type DeveloperState struct {
	Applications    *application.Repository
	Specs           *openapispecs.Repository
	EventTypes      *eventtype.Repository
	UoW             *usecasepgx.UnitOfWork
	PlatformOpenAPI func() (json.RawMessage, error)
}

// RegisterDeveloper mounts the dashboard's `/bff/developer/*`
// endpoints. Mirrors crates/fc-platform/src/shared/bff_developer_api.rs.
//
// Routes:
//
//	GET  /bff/developer/applications
//	GET  /bff/developer/applications/{appId}
//	GET  /bff/developer/applications/{appId}/openapi/current
//	GET  /bff/developer/applications/{appId}/openapi/versions
//	GET  /bff/developer/applications/{appId}/openapi/versions/{specId}
//	GET  /bff/developer/applications/{appId}/event-types
//	POST /bff/developer/sync-platform-openapi
//
// Access scoping: anchor sees every active application; non-anchor
// principals are restricted to the applications they were granted
// access to via iam_principal_application_access (already loaded into
// AuthContext.AccessibleApplicationIDs at JWT-mint time).
func RegisterDeveloper(r chi.Router, s *DeveloperState) {
	r.Route("/bff/developer", func(r chi.Router) {
		r.Get("/applications", s.listApplications)
		r.Get("/applications/{appId}", s.getApplication)
		r.Get("/applications/{appId}/openapi/current", s.getCurrentSpec)
		r.Get("/applications/{appId}/openapi/versions", s.listSpecVersions)
		r.Get("/applications/{appId}/openapi/versions/{specId}", s.getSpecVersion)
		r.Get("/applications/{appId}/event-types", s.listAppEventTypes)
		r.Post("/sync-platform-openapi", s.syncPlatformOpenAPI)
	})
}

// ── Wire DTOs ────────────────────────────────────────────────────────────

type bffDeveloperApplicationSummary struct {
	ID              string     `json:"id"`
	Code            string     `json:"code"`
	Name            string     `json:"name"`
	Description     *string    `json:"description,omitempty"`
	IconURL         *string    `json:"iconUrl,omitempty"`
	CurrentVersion  *string    `json:"currentVersion,omitempty"`
	CurrentSpecID   *string    `json:"currentSpecId,omitempty"`
	CurrentSyncedAt *time.Time `json:"currentSyncedAt,omitempty"`
}

type bffOpenAPISpecResponse struct {
	ID              string                    `json:"id"`
	ApplicationID   string                    `json:"applicationId"`
	Version         string                    `json:"version"`
	Status          string                    `json:"status"`
	Spec            json.RawMessage           `json:"spec"`
	ChangeNotesText *string                   `json:"changeNotesText,omitempty"`
	ChangeNotes     *openapispecs.ChangeNotes `json:"changeNotes,omitempty"`
	SyncedAt        time.Time                 `json:"syncedAt"`
}

type bffOpenAPIVersionSummary struct {
	ID              string    `json:"id"`
	Version         string    `json:"version"`
	Status          string    `json:"status"`
	ChangeNotesText *string   `json:"changeNotesText,omitempty"`
	HasBreaking     bool      `json:"hasBreaking"`
	SyncedAt        time.Time `json:"syncedAt"`
}

type bffDeveloperEventTypeSummary struct {
	ID           string                                    `json:"id"`
	Code         string                                    `json:"code"`
	Name         string                                    `json:"name"`
	Description  *string                                   `json:"description,omitempty"`
	Status       string                                    `json:"status"`
	Application  string                                    `json:"application"`
	Subdomain    string                                    `json:"subdomain"`
	Aggregate    string                                    `json:"aggregate"`
	EventName    string                                    `json:"eventName"`
	SpecVersions []bffDeveloperEventTypeSpecVersionSummary `json:"specVersions"`
}

type bffDeveloperEventTypeSpecVersionSummary struct {
	ID      string  `json:"id"`
	Version string  `json:"version"`
	Status  string  `json:"status"`
	Schema  *string `json:"schema,omitempty"`
}

type bffSyncPlatformOpenAPIResponse struct {
	ApplicationCode      string  `json:"applicationCode"`
	SpecID               string  `json:"specId"`
	Version              string  `json:"version"`
	Status               string  `json:"status"`
	ArchivedPriorVersion *string `json:"archivedPriorVersion,omitempty"`
	HasBreaking          bool    `json:"hasBreaking"`
	Unchanged            bool    `json:"unchanged"`
}

// ── Handlers ─────────────────────────────────────────────────────────────

// GET /bff/developer/applications
func (s *DeveloperState) listApplications(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	apps, err := s.Applications.FindActive(r.Context())
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "list active applications failed", err))
		return
	}
	out := make([]bffDeveloperApplicationSummary, 0, len(apps))
	for i := range apps {
		current, err := s.Specs.FindCurrentByApplication(r.Context(), apps[i].ID)
		if err != nil {
			httperror.Write(w, usecase.Internal("REPO", "find current spec failed", err))
			return
		}
		out = append(out, toAppSummary(&apps[i], current))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// GET /bff/developer/applications/{appId}
func (s *DeveloperState) getApplication(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	appID := chi.URLParam(r, "appId")
	app, err := s.Applications.FindByID(r.Context(), appID)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "find application failed", err))
		return
	}
	if app == nil {
		httperror.Write(w, httperror.NotFound("Application", appID))
		return
	}
	current, err := s.Specs.FindCurrentByApplication(r.Context(), app.ID)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "find current spec failed", err))
		return
	}
	writeJSON(w, http.StatusOK, toAppSummary(app, current))
}

// GET /bff/developer/applications/{appId}/openapi/current
func (s *DeveloperState) getCurrentSpec(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	appID := chi.URLParam(r, "appId")
	spec, err := s.Specs.FindCurrentByApplication(r.Context(), appID)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "find current spec failed", err))
		return
	}
	if spec == nil {
		httperror.Write(w, httperror.NotFound("OpenApiSpec", appID))
		return
	}
	writeJSON(w, http.StatusOK, toSpecResponse(spec))
}

// GET /bff/developer/applications/{appId}/openapi/versions
func (s *DeveloperState) listSpecVersions(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	appID := chi.URLParam(r, "appId")
	specs, err := s.Specs.FindAllByApplication(r.Context(), appID)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "list specs failed", err))
		return
	}
	out := make([]bffOpenAPIVersionSummary, 0, len(specs))
	for i := range specs {
		out = append(out, toVersionSummary(&specs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// GET /bff/developer/applications/{appId}/openapi/versions/{specId}
func (s *DeveloperState) getSpecVersion(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	appID := chi.URLParam(r, "appId")
	specID := chi.URLParam(r, "specId")
	spec, err := s.Specs.FindByID(r.Context(), specID)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "find spec failed", err))
		return
	}
	if spec == nil || spec.ApplicationID != appID {
		httperror.Write(w, httperror.NotFound("OpenApiSpec", specID))
		return
	}
	writeJSON(w, http.StatusOK, toSpecResponse(spec))
}

// GET /bff/developer/applications/{appId}/event-types
func (s *DeveloperState) listAppEventTypes(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	appID := chi.URLParam(r, "appId")
	app, err := s.Applications.FindByID(r.Context(), appID)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "find application failed", err))
		return
	}
	if app == nil {
		httperror.Write(w, httperror.NotFound("Application", appID))
		return
	}
	ets, err := s.EventTypes.FindByApplication(r.Context(), app.Code)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "list event types failed", err))
		return
	}
	sort.Slice(ets, func(i, j int) bool { return ets[i].Code < ets[j].Code })
	out := make([]bffDeveloperEventTypeSummary, 0, len(ets))
	for i := range ets {
		out = append(out, toDeveloperEventType(&ets[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// POST /bff/developer/sync-platform-openapi
//
// Captures the live huma-generated platform OpenAPI document and runs
// the sync use case against the seeded `platform` application row.
// Returns a wire-shape mirroring Rust's BffSyncPlatformOpenAPIResponse.
func (s *DeveloperState) syncPlatformOpenAPI(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	if s.PlatformOpenAPI == nil {
		httperror.Write(w, usecase.Internal("CONFIG", "platform OpenAPI accessor not wired", nil))
		return
	}
	app, err := s.Applications.FindByCode(r.Context(), "platform")
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "find platform application failed", err))
		return
	}
	if app == nil {
		httperror.Write(w, usecase.Internal("SEED", "platform application missing — run seed", nil))
		return
	}
	spec, err := s.PlatformOpenAPI()
	if err != nil {
		httperror.Write(w, usecase.Internal("OPENAPI", "marshal platform spec failed", err))
		return
	}
	cmd := openapiops.SyncOpenApiSpecCommand{
		ApplicationID:   app.ID,
		ApplicationCode: app.Code,
		Spec:            spec,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := openapiops.SyncOpenApiSpec(r.Context(), s.Specs, s.UoW, cmd, ec)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	ev := committed.Event()
	status := "CURRENT"
	if ev.Unchanged {
		status = "UNCHANGED"
	}
	writeJSON(w, http.StatusOK, bffSyncPlatformOpenAPIResponse{
		ApplicationCode:      app.Code,
		SpecID:               ev.SpecID,
		Version:              ev.Version,
		Status:               status,
		ArchivedPriorVersion: ev.ArchivedPriorVersion,
		HasBreaking:          ev.HasBreaking,
		Unchanged:            ev.Unchanged,
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────

func toAppSummary(a *application.Application, current *openapispecs.OpenApiSpec) bffDeveloperApplicationSummary {
	out := bffDeveloperApplicationSummary{
		ID:          a.ID,
		Code:        a.Code,
		Name:        a.Name,
		Description: a.Description,
		IconURL:     a.IconURL,
	}
	if current != nil {
		v := current.Version
		id := current.ID
		t := current.SyncedAt
		out.CurrentVersion = &v
		out.CurrentSpecID = &id
		out.CurrentSyncedAt = &t
	}
	return out
}

func toSpecResponse(s *openapispecs.OpenApiSpec) bffOpenAPISpecResponse {
	return bffOpenAPISpecResponse{
		ID:              s.ID,
		ApplicationID:   s.ApplicationID,
		Version:         s.Version,
		Status:          string(s.Status),
		Spec:            s.Spec,
		ChangeNotesText: s.ChangeNotesText,
		ChangeNotes:     s.ChangeNotes,
		SyncedAt:        s.SyncedAt,
	}
}

func toVersionSummary(s *openapispecs.OpenApiSpec) bffOpenAPIVersionSummary {
	out := bffOpenAPIVersionSummary{
		ID:              s.ID,
		Version:         s.Version,
		Status:          string(s.Status),
		ChangeNotesText: s.ChangeNotesText,
		SyncedAt:        s.SyncedAt,
	}
	if s.ChangeNotes != nil {
		out.HasBreaking = s.ChangeNotes.HasBreaking
	}
	return out
}

func toDeveloperEventType(et *eventtype.EventType) bffDeveloperEventTypeSummary {
	out := bffDeveloperEventTypeSummary{
		ID:          et.ID,
		Code:        et.Code,
		Name:        et.Name,
		Description: et.Description,
		Status:      string(et.Status),
		Application: et.Application,
		Subdomain:   et.Subdomain,
		Aggregate:   et.Aggregate,
		EventName:   et.EventName,
	}
	for i := range et.SpecVersions {
		sv := &et.SpecVersions[i]
		var schema *string
		if len(sv.SchemaContent) > 0 {
			s := string(sv.SchemaContent)
			schema = &s
		}
		out.SpecVersions = append(out.SpecVersions, bffDeveloperEventTypeSpecVersionSummary{
			ID:      sv.ID,
			Version: sv.Version,
			Status:  string(sv.Status),
			Schema:  schema,
		})
	}
	return out
}
