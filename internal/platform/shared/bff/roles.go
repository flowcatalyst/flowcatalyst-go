package bff

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// RolesState holds the deps the BFF role endpoints reach into.
//
// Applications is optional — when nil, /filters/applications returns
// an empty options array (matches Rust's behaviour when the
// application_repo is not wired).
type RolesState struct {
	Roles        *role.Repository
	Applications *application.Repository
	UoW          *usecasepgx.UnitOfWork
}

// RegisterRoles mounts the dashboard's `/bff/roles/*` endpoints.
//
// Mirrors `crates/fc-platform/src/shared/bff_roles_api.rs`. Response
// shapes match Rust's BffRoleResponse / BffRoleListResponse /
// BffApplicationOptionsResponse / BffPermissionListResponse exactly:
// camelCase fields, ISO-8601 timestamps as strings, items wrapped in
// `{items, total}`.
//
// **Not yet ported** (each blocked on infrastructure that doesn't
// exist on the Go side today):
//
//   - POST /bff/roles/sync-platform — needs a RoleSyncService that
//     can diff platformRoles() against the database and emit
//     created/updated/removed counts. The Go seeder is insert-only.
func RegisterRoles(r chi.Router, s *RolesState) {
	r.Route("/bff/roles", func(r chi.Router) {
		r.Get("/", s.list)
		r.Post("/", s.create)
		r.Get("/filters/applications", s.filterApplications)
		r.Get("/permissions", s.listPermissions)
		r.Get("/permissions/{permission}", s.getPermission)
		r.Get("/{roleName}", s.get)
		r.Put("/{roleName}", s.update)
		r.Delete("/{roleName}", s.delete)
	})
}

// ── Wire DTOs ────────────────────────────────────────────────────────────

// bffRoleResponse matches Rust's BffRoleResponse exactly. Fields are
// camelCase per the frontend's expectation.
type bffRoleResponse struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ShortName       string   `json:"shortName"`
	DisplayName     string   `json:"displayName"`
	Description     *string  `json:"description,omitempty"`
	Permissions     []string `json:"permissions"`
	ApplicationCode string   `json:"applicationCode"`
	Source          string   `json:"source"`
	ClientManaged   bool     `json:"clientManaged"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}

type bffRoleListResponse struct {
	Items []bffRoleResponse `json:"items"`
	Total int               `json:"total"`
}

type bffApplicationOption struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type bffApplicationOptionsResponse struct {
	Options []bffApplicationOption `json:"options"`
}

type bffPermissionResponse struct {
	Permission  string `json:"permission"`
	Application string `json:"application"`
	Context     string `json:"context"`
	Aggregate   string `json:"aggregate"`
	Action      string `json:"action"`
	Description string `json:"description"`
}

type bffPermissionListResponse struct {
	Items []bffPermissionResponse `json:"items"`
	Total int                     `json:"total"`
}

type bffCreateRoleRequest struct {
	ApplicationCode string   `json:"applicationCode"`
	RoleName        string   `json:"roleName"`
	DisplayName     string   `json:"displayName"`
	Description     *string  `json:"description,omitempty"`
	Permissions     []string `json:"permissions"`
	ClientManaged   bool     `json:"clientManaged"`
}

type bffUpdateRoleRequest struct {
	DisplayName   *string   `json:"displayName,omitempty"`
	Description   *string   `json:"description,omitempty"`
	ClientManaged *bool     `json:"clientManaged,omitempty"`
	Permissions   *[]string `json:"permissions,omitempty"`
}

type createdResponse struct {
	ID string `json:"id"`
}

// ── Handlers ─────────────────────────────────────────────────────────────

// GET /bff/roles?application=&source=
//
// Filters are applied in-memory after FindAll. Cheap given typical
// row counts (<100). Repo doesn't expose FindByApplication /
// FindBySource yet — when those land, swap to dispatch.
func (s *RolesState) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	applicationFilter := q.Get("application")
	sourceFilter := q.Get("source")

	rows, err := s.Roles.FindAll(r.Context())
	if err != nil {
		httperror.Write(w, usecase.Internal("REPO", "list roles failed", err))
		return
	}
	out := make([]bffRoleResponse, 0, len(rows))
	for _, role := range rows {
		if applicationFilter != "" && role.ApplicationCode != applicationFilter {
			continue
		}
		if sourceFilter != "" && !strings.EqualFold(string(role.Source), sourceFilter) {
			continue
		}
		out = append(out, toBffRole(role))
	}
	writeJSON(w, http.StatusOK, bffRoleListResponse{Items: out, Total: len(out)})
}

// GET /bff/roles/filters/applications
//
// Active applications only — mirrors Rust's application_repo.find_active().
func (s *RolesState) filterApplications(w http.ResponseWriter, r *http.Request) {
	options := []bffApplicationOption{}
	if s.Applications != nil {
		active := "true"
		apps, err := s.Applications.FindWithFilters(r.Context(), nil, &active)
		if err != nil {
			httperror.Write(w, usecase.Internal("REPO", "list applications failed", err))
			return
		}
		for _, a := range apps {
			options = append(options, bffApplicationOption{
				ID:   a.ID,
				Code: a.Code,
				Name: a.Name,
			})
		}
	}
	writeJSON(w, http.StatusOK, bffApplicationOptionsResponse{Options: options})
}

// GET /bff/roles/permissions
func (s *RolesState) listPermissions(w http.ResponseWriter, r *http.Request) {
	perms := builtinPermissions()
	writeJSON(w, http.StatusOK, bffPermissionListResponse{Items: perms, Total: len(perms)})
}

// GET /bff/roles/permissions/{permission}
func (s *RolesState) getPermission(w http.ResponseWriter, r *http.Request) {
	wanted := chi.URLParam(r, "permission")
	for _, p := range builtinPermissions() {
		if p.Permission == wanted {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	httperror.Write(w, httperror.NotFound("Permission", wanted))
}

// GET /bff/roles/{roleName}
//
// Names contain a colon ("platform:admin"); ids don't. Frontend uses
// either interchangeably.
func (s *RolesState) get(w http.ResponseWriter, r *http.Request) {
	role, err := s.resolveRole(r, chi.URLParam(r, "roleName"))
	if err != nil {
		httperror.Write(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBffRole(*role))
}

// POST /bff/roles
func (s *RolesState) create(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	var body bffCreateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	cmd := operations.CreateCommand{
		ApplicationCode: body.ApplicationCode,
		RoleName:        body.RoleName,
		DisplayName:     body.DisplayName,
		Description:     body.Description,
		Permissions:     body.Permissions,
		ClientManaged:   body.ClientManaged,
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateRole(r.Context(), s.Roles, s.UoW, cmd, ec)
	if err != nil {
		httperror.Write(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createdResponse{ID: committed.Event().RoleID})
}

// PUT /bff/roles/{roleName}
func (s *RolesState) update(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	var body bffUpdateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
		return
	}
	role, err := s.resolveRole(r, chi.URLParam(r, "roleName"))
	if err != nil {
		httperror.Write(w, err)
		return
	}
	cmd := operations.UpdateCommand{
		ID:            role.ID,
		DisplayName:   body.DisplayName,
		Description:   body.Description,
		ClientManaged: body.ClientManaged,
	}
	if body.Permissions != nil {
		cmd.Permissions = *body.Permissions
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateRole(r.Context(), s.Roles, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /bff/roles/{roleName}
func (s *RolesState) delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.FromContext(r.Context())
	if err := auth.RequireAnchor(ac); err != nil {
		httperror.Write(w, err)
		return
	}
	role, err := s.resolveRole(r, chi.URLParam(r, "roleName"))
	if err != nil {
		httperror.Write(w, err)
		return
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	cmd := operations.DeleteCommand{ID: role.ID}
	if _, err := operations.DeleteRole(r.Context(), s.Roles, s.UoW, cmd, ec); err != nil {
		httperror.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ──────────────────────────────────────────────────────────────

// resolveRole dispatches between FindByName (colon-containing) and
// FindByID. Returns a typed NotFound error when missing.
func (s *RolesState) resolveRole(r *http.Request, key string) (*role.Role, error) {
	var (
		out *role.Role
		err error
	)
	if strings.Contains(key, ":") {
		out, err = s.Roles.FindByName(r.Context(), key)
	} else {
		out, err = s.Roles.FindByID(r.Context(), key)
	}
	if err != nil {
		return nil, usecase.Internal("REPO", "find role failed", err)
	}
	if out == nil {
		return nil, httperror.NotFound("Role", key)
	}
	return out, nil
}

func toBffRole(r role.Role) bffRoleResponse {
	shortName := r.Name
	if idx := strings.LastIndex(r.Name, ":"); idx >= 0 && idx+1 < len(r.Name) {
		shortName = r.Name[idx+1:]
	}
	perms := r.Permissions
	if perms == nil {
		perms = []string{}
	}
	return bffRoleResponse{
		ID:              r.ID,
		Name:            r.Name,
		ShortName:       shortName,
		DisplayName:     r.DisplayName,
		Description:     r.Description,
		Permissions:     perms,
		ApplicationCode: r.ApplicationCode,
		Source:          string(r.Source),
		ClientManaged:   r.ClientManaged,
		CreatedAt:       r.CreatedAt.Format("2006-01-02T15:04:05.000000Z07:00"),
		UpdatedAt:       r.UpdatedAt.Format("2006-01-02T15:04:05.000000Z07:00"),
	}
}

// ── Permissions registry ─────────────────────────────────────────────────

// builtinPermissions ports `get_builtin_permissions` from
// bff_roles_api.rs. The catalog is static — the dashboard renders it
// directly. Must stay in lockstep with `internal/platform/seed/permissions.go`
// (the actual permission strings the platform recognises) until the
// two are merged.
func builtinPermissions() []bffPermissionResponse {
	out := []bffPermissionResponse{}
	// IAM
	out = appendPerm(out, "platform", "iam", "user", "view", "View users")
	out = appendPerm(out, "platform", "iam", "user", "create", "Create users")
	out = appendPerm(out, "platform", "iam", "user", "update", "Update users")
	out = appendPerm(out, "platform", "iam", "user", "delete", "Delete users")
	out = appendPerm(out, "platform", "iam", "role", "view", "View roles")
	out = appendPerm(out, "platform", "iam", "role", "create", "Create roles")
	out = appendPerm(out, "platform", "iam", "role", "update", "Update roles")
	out = appendPerm(out, "platform", "iam", "role", "delete", "Delete roles")
	out = appendPerm(out, "platform", "iam", "permission", "view", "View permissions")
	out = appendPerm(out, "platform", "iam", "service-account", "view", "View service accounts")
	out = appendPerm(out, "platform", "iam", "service-account", "create", "Create service accounts")
	out = appendPerm(out, "platform", "iam", "service-account", "update", "Update service accounts")
	out = appendPerm(out, "platform", "iam", "service-account", "delete", "Delete service accounts")
	out = appendPerm(out, "platform", "iam", "idp", "manage", "Manage identity providers")
	// Admin
	out = appendPerm(out, "platform", "admin", "client", "view", "View clients")
	out = appendPerm(out, "platform", "admin", "client", "create", "Create clients")
	out = appendPerm(out, "platform", "admin", "client", "update", "Update clients")
	out = appendPerm(out, "platform", "admin", "client", "delete", "Delete clients")
	out = appendPerm(out, "platform", "admin", "application", "view", "View applications")
	out = appendPerm(out, "platform", "admin", "application", "create", "Create applications")
	out = appendPerm(out, "platform", "admin", "application", "update", "Update applications")
	out = appendPerm(out, "platform", "admin", "application", "delete", "Delete applications")
	out = appendPerm(out, "platform", "admin", "config", "view", "View platform config")
	out = appendPerm(out, "platform", "admin", "config", "update", "Update platform config")
	// Messaging
	out = appendPerm(out, "platform", "messaging", "event", "view", "View events")
	out = appendPerm(out, "platform", "messaging", "event", "view-raw", "View raw event data")
	out = appendPerm(out, "platform", "messaging", "event-type", "view", "View event types")
	out = appendPerm(out, "platform", "messaging", "event-type", "create", "Create event types")
	out = appendPerm(out, "platform", "messaging", "event-type", "update", "Update event types")
	out = appendPerm(out, "platform", "messaging", "event-type", "delete", "Delete event types")
	out = appendPerm(out, "platform", "messaging", "subscription", "view", "View subscriptions")
	out = appendPerm(out, "platform", "messaging", "subscription", "create", "Create subscriptions")
	out = appendPerm(out, "platform", "messaging", "subscription", "update", "Update subscriptions")
	out = appendPerm(out, "platform", "messaging", "subscription", "delete", "Delete subscriptions")
	out = appendPerm(out, "platform", "messaging", "dispatch-job", "view", "View dispatch jobs")
	out = appendPerm(out, "platform", "messaging", "dispatch-job", "view-raw", "View raw dispatch job data")
	out = appendPerm(out, "platform", "messaging", "dispatch-job", "create", "Create dispatch jobs")
	out = appendPerm(out, "platform", "messaging", "dispatch-job", "retry", "Retry dispatch jobs")
	out = appendPerm(out, "platform", "messaging", "dispatch-pool", "view", "View dispatch pools")
	out = appendPerm(out, "platform", "messaging", "dispatch-pool", "create", "Create dispatch pools")
	out = appendPerm(out, "platform", "messaging", "dispatch-pool", "update", "Update dispatch pools")
	out = appendPerm(out, "platform", "messaging", "dispatch-pool", "delete", "Delete dispatch pools")
	sort.Slice(out, func(i, j int) bool { return out[i].Permission < out[j].Permission })
	return out
}

func appendPerm(out []bffPermissionResponse, app, ctx, agg, action, desc string) []bffPermissionResponse {
	return append(out, bffPermissionResponse{
		Permission:  app + ":" + ctx + ":" + agg + ":" + action,
		Application: app,
		Context:     ctx,
		Aggregate:   agg,
		Action:      action,
		Description: desc,
	})
}
