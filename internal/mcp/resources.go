package mcp

import (
	"context"
	"net/url"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerResources mounts the read-only resource catalogue (1:1 with Rust
// fc-mcp): 5 fixed collection resources plus 5 hierarchical templates for
// single-entity reads. Each resolves to the same platform endpoints the tools
// use, returning pretty-printed JSON.
func (s *Server) registerResources(m *mcpsdk.Server) {
	// Fixed collection resources.
	m.AddResource(&mcpsdk.Resource{
		URI: "flowcatalyst://openapi/platform", Name: "Platform OpenAPI", MIMEType: "application/json",
		Description: "CURRENT OpenAPI spec for the platform application.",
	}, s.readResource(func(ctx context.Context) ([]byte, error) { return s.fetchOpenAPI(ctx, "platform") }))

	m.AddResource(&mcpsdk.Resource{
		URI: "flowcatalyst://applications", Name: "Applications", MIMEType: "application/json",
		Description: "Registered applications (active only).",
	}, s.readPath("/api/applications?active=true"))

	m.AddResource(&mcpsdk.Resource{
		URI: "flowcatalyst://roles", Name: "Roles", MIMEType: "application/json",
		Description: "Platform roles.",
	}, s.readPath("/api/roles"))

	m.AddResource(&mcpsdk.Resource{
		URI: "flowcatalyst://event-types", Name: "Event Types", MIMEType: "application/json",
		Description: "Event types.",
	}, s.readPath("/api/event-types"))

	m.AddResource(&mcpsdk.Resource{
		URI: "flowcatalyst://subscriptions", Name: "Subscriptions", MIMEType: "application/json",
		Description: "Webhook subscriptions.",
	}, s.readPath("/api/subscriptions"))

	// Hierarchical single-entity templates. The {var} is parsed from the
	// requested URI by stripping the template's literal prefix.
	m.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "flowcatalyst://event-types/{id}", Name: "Event Type", MIMEType: "application/json",
		Description: "A single event type by id.",
	}, s.readByPrefix("flowcatalyst://event-types/", "/api/event-types/"))

	m.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "flowcatalyst://subscriptions/{id}", Name: "Subscription", MIMEType: "application/json",
		Description: "A single subscription by id.",
	}, s.readByPrefix("flowcatalyst://subscriptions/", "/api/subscriptions/"))

	m.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "flowcatalyst://roles/{id}", Name: "Role", MIMEType: "application/json",
		Description: "A single role by id.",
	}, s.readByPrefix("flowcatalyst://roles/", "/api/roles/"))

	m.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "flowcatalyst://applications/{code}", Name: "Application", MIMEType: "application/json",
		Description: "A single application by code.",
	}, s.readByPrefix("flowcatalyst://applications/", "/api/applications/by-code/"))

	m.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "flowcatalyst://openapi/{applicationCode}", Name: "Application OpenAPI", MIMEType: "application/json",
		Description: "CURRENT OpenAPI spec for an application by code.",
	}, func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		code := strings.TrimPrefix(req.Params.URI, "flowcatalyst://openapi/")
		raw, err := s.fetchOpenAPI(ctx, code)
		if err != nil {
			return nil, err
		}
		return resourceJSON(req.Params.URI, jsonText(raw)), nil
	})
}

// readPath returns a handler that fetches a fixed platform path.
func (s *Server) readPath(path string) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		raw, err := s.fetchRaw(ctx, path)
		if err != nil {
			return nil, err
		}
		return resourceJSON(req.Params.URI, jsonText(raw)), nil
	}
}

// readResource returns a handler backed by an arbitrary fetch func (e.g. the
// two-hop OpenAPI lookup).
func (s *Server) readResource(fetch func(context.Context) ([]byte, error)) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		raw, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSON(req.Params.URI, jsonText(raw)), nil
	}
}

// readByPrefix returns a template handler that extracts the trailing path
// segment from the requested URI (stripping uriPrefix) and fetches
// {apiPrefix}{escaped-id}.
func (s *Server) readByPrefix(uriPrefix, apiPrefix string) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		id := strings.TrimPrefix(req.Params.URI, uriPrefix)
		raw, err := s.fetchRaw(ctx, apiPrefix+url.PathEscape(id))
		if err != nil {
			return nil, err
		}
		return resourceJSON(req.Params.URI, jsonText(raw)), nil
	}
}

func resourceJSON(uri, text string) *mcpsdk.ReadResourceResult {
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{URI: uri, MIMEType: "application/json", Text: text}},
	}
}
