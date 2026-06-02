package bff

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/seed"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// EventTypesState holds the deps the BFF event-type endpoints reach into.
// Hold by reference so future BFF endpoints can grow the state without
// re-threading every caller.
type EventTypesState struct {
	Repo *eventtype.Repository
	UoW  *usecasepgx.UnitOfWork
}

// RegisterEventTypes mounts the dashboard's `/bff/event-types/*`
// endpoints. Distinct from `/api/event-types/*` because the BFF
// returns a UI-friendly response shape (denormalised
// application/subdomain/aggregate/event fields, ISO-8601 strings,
// embedded spec_versions with stringified schema) and accepts
// camelCase request bodies.
//
// Mirrors `crates/fc-platform/src/shared/bff_event_types_api.rs`.
func RegisterEventTypes(r chi.Router, s *EventTypesState) {
	r.Route("/bff/event-types", func(r chi.Router) {
		r.Get("/", s.list)
		r.Post("/", s.create)
		r.Post("/sync-platform", s.syncPlatform)
		r.Get("/filters/subdomains", s.filterSubdomains)
		r.Get("/filters/aggregates", s.filterAggregates)
		r.Get("/{id}", s.getByID)
		r.Put("/{id}", s.update)
		r.Delete("/{id}", s.delete)
		r.Post("/{id}/archive", s.archive)
		r.Post("/{id}/schemas", s.addSchema)
		r.Post("/{id}/schemas/{version}/finalise", s.finaliseSchema)
		r.Post("/{id}/schemas/{version}/deprecate", s.deprecateSchema)
	})
}

// ── Wire DTOs ────────────────────────────────────────────────────────────

// bffSpecVersionResponse matches Rust's BffSpecVersionResponse —
// `schema` is the stringified JSON content (frontend re-parses lazily
// per the editor's needs).
type bffSpecVersionResponse struct {
	ID         string  `json:"id"`
	Version    string  `json:"version"`
	Status     string  `json:"status"`
	SchemaType string  `json:"schemaType"`
	MimeType   string  `json:"mimeType"`
	Schema     *string `json:"schema,omitempty"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
}

// bffEventTypeResponse matches Rust's BffEventTypeResponse exactly.
// Fields are camelCase per the frontend's expectation.
type bffEventTypeResponse struct {
	ID           string                   `json:"id"`
	Code         string                   `json:"code"`
	Application  string                   `json:"application"`
	Subdomain    string                   `json:"subdomain"`
	Aggregate    string                   `json:"aggregate"`
	Event        string                   `json:"event"`
	Name         string                   `json:"name"`
	Description  *string                  `json:"description,omitempty"`
	Status       string                   `json:"status"`
	ClientScoped bool                     `json:"clientScoped"`
	SpecVersions []bffSpecVersionResponse `json:"specVersions"`
	CreatedAt    string                   `json:"createdAt"`
	UpdatedAt    string                   `json:"updatedAt"`
}

type bffEventTypeListResponse struct {
	Items []bffEventTypeResponse `json:"items"`
	Total int                    `json:"total"`
}

// bffCreateEventTypeRequest matches Rust's BffCreateEventTypeRequest.
// Schema is optional; when present, gets used to mint the initial
// spec version inside the same use case.
type bffCreateEventTypeRequest struct {
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	ClientID    *string         `json:"clientId,omitempty"`
}

type bffUpdateEventTypeRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

type bffAddSchemaRequest struct {
	Schema     json.RawMessage `json:"schema"`
	MimeType   string          `json:"mimeType,omitempty"`
	SchemaType *string         `json:"schemaType,omitempty"`
	// The frontend supplies a version string ("1.0.0" etc.) — the API
	// layer requires it; this DTO accepts it inline rather than at the
	// path so the request shape matches Rust's BFF.
	Version string `json:"version"`
}

// ── Handlers ─────────────────────────────────────────────────────────────

// GET /bff/event-types
//
// Filters: status, application, subdomain, aggregate. All optional.
// Returns `{items, total}` rather than `{items}` so the frontend can
// render a count without a separate roundtrip.
func (s *EventTypesState) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := strPtr(q.Get("status"))
	application := strPtr(q.Get("application"))
	subdomain := strPtr(q.Get("subdomain"))
	aggregate := strPtr(q.Get("aggregate"))

	rows, err := s.Repo.FindWithFilters(r.Context(), application, nil, status, subdomain, aggregate)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "list event types failed", err))
		return
	}
	out := make([]bffEventTypeResponse, 0, len(rows))
	for _, et := range rows {
		out = append(out, toBffEventType(et))
	}
	writeJSON(w, http.StatusOK, bffEventTypeListResponse{Items: out, Total: len(out)})
}

// GET /bff/event-types/{id}
func (s *EventTypesState) getByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	et, err := s.Repo.FindByID(r.Context(), id)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "find event type failed", err))
		return
	}
	if et == nil {
		httperror.Write(w, httperror.NotFound("EventType", id))
		return
	}
	writeJSON(w, http.StatusOK, toBffEventType(*et))
}

// POST /bff/event-types
//
// Creates the event type and (when `schema` is present) its initial
// spec version in one use case invocation. Returns 201 + the full
// BFF response. Note: the underlying CreateUseCase emits an
// EventTypeCreated DomainEvent in the same UoW transaction — same
// behaviour as `/api/event-types`.
func (s *EventTypesState) create(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.CanCreateEventTypes(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	var body bffCreateEventTypeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	cmd := operations.CreateCommand{
		Code:        body.Code,
		Name:        body.Name,
		Description: body.Description,
		ClientID:    body.ClientID,
		Schema:      body.Schema,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateEventType(r.Context(), s.Repo, s.UoW, cmd, ec)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	// Re-fetch the row so the response carries the persisted
	// spec_versions ordering (the emitted event doesn't carry them).
	et, err := s.Repo.FindByID(r.Context(), committed.Event().EventTypeID)
	if err != nil || et == nil {
		httperror.Write(w, usecase.Internal("REPO", "post-create reload failed", err))
		return
	}
	writeJSON(w, http.StatusCreated, toBffEventType(*et))
}

// PUT /bff/event-types/{id}
//
// Updates metadata only (name + description). Returns 204; frontend
// re-fetches via GET if it needs the canonical response.
func (s *EventTypesState) update(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.CanUpdateEventTypes(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	var body bffUpdateEventTypeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	cmd := operations.UpdateCommand{ID: chi.URLParam(r, "id")}
	if body.Name != nil {
		cmd.Name = *body.Name
	}
	cmd.Description = body.Description
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateEventType(r.Context(), s.Repo, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /bff/event-types/{id}
//
// Hard delete via DeleteUseCase. Matches `/api/event-types/{id}` —
// kept here because the frontend routes through /bff for cookie-session
// auth.
func (s *EventTypesState) delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.CanDeleteEventTypes(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	cmd := operations.DeleteCommand{ID: chi.URLParam(r, "id")}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteEventType(r.Context(), s.Repo, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /bff/event-types/{id}/schemas
//
// Adds a new spec version. Returns the full BFF response (with the
// new version included in spec_versions).
func (s *EventTypesState) addSchema(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.CanUpdateEventTypes(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	var body bffAddSchemaRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	if body.Version == "" {
		httperror.Write(w, httperror.BadRequest("VERSION_REQUIRED", "version is required"))
		return
	}
	cmd := operations.AddSchemaCommand{
		EventTypeID: chi.URLParam(r, "id"),
		Version:     body.Version,
		Schema:      body.Schema,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.AddSchema(r.Context(), s.Repo, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	et, err := s.Repo.FindByID(r.Context(), cmd.EventTypeID)
	if err != nil || et == nil {
		httperror.Write(w, usecase.Internal("REPO", "post-add-schema reload failed", err))
		return
	}
	writeJSON(w, http.StatusOK, toBffEventType(*et))
}

// GET /bff/event-types/filters/subdomains[?application=...]
//
// Cascading filter — subdomains scoped to the supplied application.
// Returns the Rust BffFilterOptionsResponse: `{"options": [...]}` of
// distinct subdomain strings.
func (s *EventTypesState) filterSubdomains(w http.ResponseWriter, r *http.Request) {
	app := strPtr(r.URL.Query().Get("application"))
	rows, err := s.Repo.FindWithFilters(r.Context(), app, nil, nil, nil, nil)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "list event types failed", err))
		return
	}
	writeJSON(w, http.StatusOK, distinctOptions(rows, func(et eventtype.EventType) string { return et.Subdomain }))
}

// GET /bff/event-types/filters/aggregates[?application=...&subdomain=...]
//
// Cascading filter — aggregates scoped to application + subdomain.
func (s *EventTypesState) filterAggregates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	app := strPtr(q.Get("application"))
	sub := strPtr(q.Get("subdomain"))
	rows, err := s.Repo.FindWithFilters(r.Context(), app, nil, nil, sub, nil)
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "list event types failed", err))
		return
	}
	writeJSON(w, http.StatusOK, distinctOptions(rows, func(et eventtype.EventType) string { return et.Aggregate }))
}

// ── Helpers ──────────────────────────────────────────────────────────────

func toBffEventType(et eventtype.EventType) bffEventTypeResponse {
	versions := make([]bffSpecVersionResponse, 0, len(et.SpecVersions))
	for _, v := range et.SpecVersions {
		versions = append(versions, toBffSpecVersion(v))
	}
	return bffEventTypeResponse{
		ID:           et.ID,
		Code:         et.Code,
		Application:  et.Application,
		Subdomain:    et.Subdomain,
		Aggregate:    et.Aggregate,
		Event:        et.EventName,
		Name:         et.Name,
		Description:  et.Description,
		Status:       string(et.Status),
		ClientScoped: et.ClientScoped,
		SpecVersions: versions,
		CreatedAt:    et.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    et.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toBffSpecVersion(v eventtype.SpecVersion) bffSpecVersionResponse {
	out := bffSpecVersionResponse{
		ID:         v.ID,
		Version:    v.Version,
		Status:     string(v.Status),
		SchemaType: string(v.SchemaType),
		MimeType:   v.MimeType,
		CreatedAt:  v.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:  v.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if len(v.SchemaContent) > 0 {
		s := string(v.SchemaContent)
		out.Schema = &s
	}
	return out
}

// distinctOptions extracts the distinct values of `pick(row)` across
// rows, returns them sorted in the BffFilterOptionsResponse shape.
func distinctOptions(rows []eventtype.EventType, pick func(eventtype.EventType) string) map[string]any {
	seen := map[string]struct{}{}
	for _, et := range rows {
		v := pick(et)
		if v == "" {
			continue
		}
		seen[v] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return map[string]any{"options": out}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// POST /bff/event-types/{id}/archive
//
// Transitions an event type CURRENT → ARCHIVED. Returns the
// re-hydrated bffEventTypeResponse so the frontend can update its
// row without a follow-up GET.
func (s *EventTypesState) archive(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.CanUpdateEventTypes(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	cmd := operations.ArchiveCommand{ID: chi.URLParam(r, "id")}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ArchiveEventType(r.Context(), s.Repo, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	et, err := s.Repo.FindByID(r.Context(), cmd.ID)
	if err != nil || et == nil {
		httperror.Write(w, usecase.Internal("REPO", "post-archive reload failed", err))
		return
	}
	writeJSON(w, http.StatusOK, toBffEventType(*et))
}

// POST /bff/event-types/{id}/schemas/{version}/finalise
//
// Transitions a spec version FINALISING → CURRENT. If another spec
// version is already CURRENT with the same major version, that one is
// auto-deprecated in the same transaction.
func (s *EventTypesState) finaliseSchema(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.CanUpdateEventTypes(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	cmd := operations.FinaliseSchemaCommand{
		EventTypeID: chi.URLParam(r, "id"),
		Version:     chi.URLParam(r, "version"),
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.FinaliseEventTypeSchema(r.Context(), s.Repo, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	et, err := s.Repo.FindByID(r.Context(), cmd.EventTypeID)
	if err != nil || et == nil {
		httperror.Write(w, usecase.Internal("REPO", "post-finalise reload failed", err))
		return
	}
	writeJSON(w, http.StatusOK, toBffEventType(*et))
}

// POST /bff/event-types/{id}/schemas/{version}/deprecate
//
// Transitions a spec version CURRENT → DEPRECATED. Refuses
// FINALISING and already-DEPRECATED versions with a conflict.
func (s *EventTypesState) deprecateSchema(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.CanUpdateEventTypes(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	cmd := operations.DeprecateSchemaCommand{
		EventTypeID: chi.URLParam(r, "id"),
		Version:     chi.URLParam(r, "version"),
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeprecateEventTypeSchema(r.Context(), s.Repo, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	et, err := s.Repo.FindByID(r.Context(), cmd.EventTypeID)
	if err != nil || et == nil {
		httperror.Write(w, usecase.Internal("REPO", "post-deprecate reload failed", err))
		return
	}
	writeJSON(w, http.StatusOK, toBffEventType(*et))
}

// bffSyncPlatformRequest matches Rust's BffSyncPlatformRequest.
type bffSyncPlatformRequest struct {
	ApplicationCode string `json:"applicationCode"`
}

// bffSyncPlatformResponse matches Rust's BffSyncPlatformResponse —
// schemas tally is wire-compatible but currently not instrumented
// (created=updated=unchanged=0). The event-type-level counts ARE
// correct.
type bffSyncPlatformResponse struct {
	Created uint32                 `json:"created"`
	Updated uint32                 `json:"updated"`
	Deleted uint32                 `json:"deleted"`
	Total   uint32                 `json:"total"`
	Schemas bffSyncPlatformSchemas `json:"schemas"`
}

type bffSyncPlatformSchemas struct {
	Created   uint32 `json:"created"`
	Updated   uint32 `json:"updated"`
	Unchanged uint32 `json:"unchanged"`
}

// POST /bff/event-types/sync-platform
//
// Re-runs the code-defined event-type catalogue sync against the
// supplied applicationCode (defaults to "platform"). Mirrors Rust's
// sync-platform handler. RemoveUnlisted is hard-coded true to keep
// the catalogue authoritative — stale API-sourced rows in the same
// application get dropped, matching Rust.
//
// Schemas-tally fields in the response are currently zero;
// instrumenting them needs a tighter sync use case that tracks per-
// schema outcomes (the underlying commit.Sync helper has the data;
// it's the eventtype sync use case that doesn't extract it). Filed
// as a follow-up in HANDOFF §0.
func (s *EventTypesState) syncPlatform(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	var body bffSyncPlatformRequest
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional
	applicationCode := body.ApplicationCode
	if applicationCode == "" {
		applicationCode = "platform"
	}

	defs := seed.PlatformEventTypes()
	inputs := make([]operations.SyncEventTypeInput, 0, len(defs))
	for _, d := range defs {
		inputs = append(inputs, operations.SyncEventTypeInput{
			Code:   d.Code,
			Name:   d.Name,
			Schema: d.Schema,
		})
	}

	cmd := operations.SyncEventTypesCommand{
		ApplicationCode: applicationCode,
		EventTypes:      inputs,
		RemoveUnlisted:  true,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.SyncEventTypes(r.Context(), s.Repo, s.UoW, cmd, ec)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	ev := committed.Event()
	writeJSON(w, http.StatusOK, bffSyncPlatformResponse{
		Created: ev.Created,
		Updated: ev.Updated,
		Deleted: ev.Deleted,
		Total:   uint32(len(defs)),
	})
}
