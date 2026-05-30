package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/client"
)

// registerTools mounts the full read-only tool catalogue (1:1 with the Rust
// fc-mcp server). Optional args use `omitempty` (→ not required in the input
// schema); required args (id, applicationCode) are plain strings.
func (s *Server) registerTools(m *mcpsdk.Server) {
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "list_event_types",
		Description: "List event types. Optionally filter by status, application, subdomain, aggregate, or clientId."}, s.listEventTypes)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "get_event_type",
		Description: "Get a single event type by id, including all schema spec versions."}, s.getEventType)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "get_schema",
		Description: "Extract the JSON Schema for an event type's spec version (defaults to CURRENT, falls back to FINALISING)."}, s.getSchema)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "list_subscriptions",
		Description: "List webhook subscriptions, scoped to the caller unless an admin clientId is given."}, s.listSubscriptions)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "get_subscription",
		Description: "Get a single subscription by id."}, s.getSubscription)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "list_applications",
		Description: "List registered applications. Defaults to active only; pass active=false to include inactive."}, s.listApplications)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "list_roles",
		Description: "List platform roles. Optionally filter by source (e.g. CODE, BOOTSTRAP, API, UI)."}, s.listRoles)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "get_role",
		Description: "Get a single role by id, including its assigned permissions."}, s.getRole)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "get_openapi",
		Description: "Fetch the CURRENT OpenAPI spec for an application (defaults to the 'platform' application)."}, s.getOpenAPI)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "whoami",
		Description: "Identify the caller: id, type (USER/SERVICE), scope, roles, and accessible clients/apps. Start here."}, s.whoami)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "list_my_applications",
		Description: "List the applications the caller has access to."}, s.listMyApplications)
	mcpsdk.AddTool(m, &mcpsdk.Tool{Name: "get_application_capabilities",
		Description: "Bundle an application's metadata, CURRENT OpenAPI spec, assignable roles, and CURRENT event types."}, s.getApplicationCapabilities)
}

// ─── argument structs ────────────────────────────────────────────────────

type listEventTypesArgs struct {
	Status      string `json:"status,omitempty" jsonschema:"filter by lifecycle status, e.g. CURRENT or FINALISING"`
	Application string `json:"application,omitempty" jsonschema:"filter by application code"`
	Subdomain   string `json:"subdomain,omitempty" jsonschema:"filter by subdomain"`
	Aggregate   string `json:"aggregate,omitempty" jsonschema:"filter by aggregate"`
	ClientID    string `json:"clientId,omitempty" jsonschema:"filter by client id (admin-scoped)"`
}

type getByIDArgs struct {
	ID string `json:"id" jsonschema:"the resource id"`
}

type getSchemaArgs struct {
	ID      string `json:"id" jsonschema:"the event type id"`
	Version string `json:"version,omitempty" jsonschema:"spec version status to extract; defaults to CURRENT (falls back to FINALISING)"`
}

type listSubscriptionsArgs struct {
	ClientID string `json:"clientId,omitempty" jsonschema:"admin-scoped: list a specific client's subscriptions"`
}

type listApplicationsArgs struct {
	Active *bool `json:"active,omitempty" jsonschema:"filter by active flag; defaults to true (active only)"`
}

type listRolesArgs struct {
	Source string `json:"source,omitempty" jsonschema:"filter by role source, e.g. CODE, BOOTSTRAP, API, UI"`
}

type appCodeArgs struct {
	ApplicationCode string `json:"applicationCode" jsonschema:"the application code"`
}

type getOpenAPIArgs struct {
	ApplicationCode string `json:"applicationCode,omitempty" jsonschema:"the application code; defaults to 'platform'"`
}

type emptyArgs struct{}

// ─── tool handlers ───────────────────────────────────────────────────────

func (s *Server) listEventTypes(ctx context.Context, _ *mcpsdk.CallToolRequest, a listEventTypesArgs) (*mcpsdk.CallToolResult, any, error) {
	q := client.NewQuery().
		String("status", a.Status).
		String("application", a.Application).
		String("subdomain", a.Subdomain).
		String("aggregate", a.Aggregate).
		String("clientId", a.ClientID)
	return s.getJSON(ctx, "/api/event-types"+q.Encode())
}

func (s *Server) getEventType(ctx context.Context, _ *mcpsdk.CallToolRequest, a getByIDArgs) (*mcpsdk.CallToolResult, any, error) {
	return s.getJSON(ctx, "/api/event-types/"+url.PathEscape(a.ID))
}

// specVersion is the subset of an event type's spec version get_schema reads.
type specVersion struct {
	Status string          `json:"status"`
	Schema json.RawMessage `json:"schema"`
}

func (s *Server) getSchema(ctx context.Context, _ *mcpsdk.CallToolRequest, a getSchemaArgs) (*mcpsdk.CallToolResult, any, error) {
	var et struct {
		SpecVersions []specVersion `json:"specVersions"`
	}
	if err := s.platform.Get(ctx, "/api/event-types/"+url.PathEscape(a.ID), &et); err != nil {
		return nil, nil, err
	}
	want := a.Version
	if want == "" {
		want = "CURRENT"
	}
	schema := findSchema(et.SpecVersions, want)
	// CURRENT falls back to FINALISING, matching Rust.
	if schema == nil && want == "CURRENT" {
		schema = findSchema(et.SpecVersions, "FINALISING")
	}
	if schema == nil {
		return textResult(fmt.Sprintf("no %s schema found for event type %s", want, a.ID)), nil, nil
	}
	return textResult(jsonText(schema)), nil, nil
}

func findSchema(versions []specVersion, status string) json.RawMessage {
	for _, v := range versions {
		if v.Status == status && len(v.Schema) > 0 && string(v.Schema) != "null" {
			return v.Schema
		}
	}
	return nil
}

func (s *Server) listSubscriptions(ctx context.Context, _ *mcpsdk.CallToolRequest, a listSubscriptionsArgs) (*mcpsdk.CallToolResult, any, error) {
	return s.getJSON(ctx, "/api/subscriptions"+client.NewQuery().String("clientId", a.ClientID).Encode())
}

func (s *Server) getSubscription(ctx context.Context, _ *mcpsdk.CallToolRequest, a getByIDArgs) (*mcpsdk.CallToolResult, any, error) {
	return s.getJSON(ctx, "/api/subscriptions/"+url.PathEscape(a.ID))
}

func (s *Server) listApplications(ctx context.Context, _ *mcpsdk.CallToolRequest, a listApplicationsArgs) (*mcpsdk.CallToolResult, any, error) {
	active := true // Rust default: active only.
	if a.Active != nil {
		active = *a.Active
	}
	return s.getJSON(ctx, "/api/applications"+client.NewQuery().Bool("active", &active).Encode())
}

func (s *Server) listRoles(ctx context.Context, _ *mcpsdk.CallToolRequest, a listRolesArgs) (*mcpsdk.CallToolResult, any, error) {
	return s.getJSON(ctx, "/api/roles"+client.NewQuery().String("source", a.Source).Encode())
}

func (s *Server) getRole(ctx context.Context, _ *mcpsdk.CallToolRequest, a getByIDArgs) (*mcpsdk.CallToolResult, any, error) {
	return s.getJSON(ctx, "/api/roles/"+url.PathEscape(a.ID))
}

func (s *Server) getOpenAPI(ctx context.Context, _ *mcpsdk.CallToolRequest, a getOpenAPIArgs) (*mcpsdk.CallToolResult, any, error) {
	code := a.ApplicationCode
	if code == "" {
		code = "platform"
	}
	spec, err := s.fetchOpenAPI(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	return textResult(jsonText(spec)), nil, nil
}

func (s *Server) whoami(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyArgs) (*mcpsdk.CallToolResult, any, error) {
	return s.getJSON(ctx, "/api/me")
}

func (s *Server) listMyApplications(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyArgs) (*mcpsdk.CallToolResult, any, error) {
	return s.getJSON(ctx, "/api/me/applications")
}

func (s *Server) getApplicationCapabilities(ctx context.Context, _ *mcpsdk.CallToolRequest, a appCodeArgs) (*mcpsdk.CallToolResult, any, error) {
	var app struct {
		ID string `json:"id"`
	}
	var appRaw json.RawMessage
	if err := s.platform.Get(ctx, "/api/applications/by-code/"+url.PathEscape(a.ApplicationCode), &appRaw); err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal(appRaw, &app)

	// Sub-resources tolerate a 404 (return null), matching Rust.
	bundle := map[string]any{
		"application":     appRaw,
		"openapi":         rawOrNil(tolerate404(s.fetchOpenAPIByID(ctx, app.ID))),
		"assignableRoles": rawOrNil(tolerate404(s.fetchRaw(ctx, "/api/roles/by-application/"+url.PathEscape(app.ID)))),
		"eventTypes": rawOrNil(tolerate404(s.fetchRaw(ctx,
			"/api/event-types"+client.NewQuery().String("application", a.ApplicationCode).String("status", "CURRENT").Encode()))),
	}
	out, _ := json.MarshalIndent(bundle, "", "  ")
	return textResult(string(out)), nil, nil
}

// ─── platform helpers ────────────────────────────────────────────────────

// fetchOpenAPI resolves an application by code, then fetches its CURRENT
// OpenAPI spec via the developer BFF (two-hop, 1:1 with Rust).
func (s *Server) fetchOpenAPI(ctx context.Context, code string) (json.RawMessage, error) {
	var app struct {
		ID string `json:"id"`
	}
	if err := s.platform.Get(ctx, "/api/applications/by-code/"+url.PathEscape(code), &app); err != nil {
		return nil, err
	}
	return s.fetchOpenAPIByID(ctx, app.ID)
}

func (s *Server) fetchOpenAPIByID(ctx context.Context, appID string) (json.RawMessage, error) {
	if appID == "" {
		return nil, fmt.Errorf("application has no id")
	}
	return s.fetchRaw(ctx, "/bff/developer/applications/"+url.PathEscape(appID)+"/openapi/current")
}

func (s *Server) fetchRaw(ctx context.Context, path string) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := s.platform.Get(ctx, path, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// getJSON fetches path and returns the raw response pretty-printed as text. A
// platform error is returned to the SDK, which surfaces it as an IsError tool
// result (visible to the agent) rather than a protocol-level failure.
func (s *Server) getJSON(ctx context.Context, path string) (*mcpsdk.CallToolResult, any, error) {
	raw, err := s.fetchRaw(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	return textResult(jsonText(raw)), nil, nil
}

// tolerate404 maps a 404 to a nil result with no error, so a bundle can carry
// a null sub-resource instead of failing. Other errors propagate.
func tolerate404(raw json.RawMessage, err error) json.RawMessage {
	if err == nil {
		return raw
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
		return nil
	}
	return nil // any sub-resource error degrades to null; the application body still returns
}

func rawOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// ─── result formatting (M7 JSON fix) ─────────────────────────────────────

// jsonText pretty-prints raw JSON. Replaces the old fmt.Sprintf("%v", …) which
// emitted Go map syntax instead of JSON. Falls back to the raw bytes if the
// payload isn't valid JSON to indent.
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

func textResult(text string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}}
}
