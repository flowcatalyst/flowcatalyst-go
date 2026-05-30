// Package mcp implements the FlowCatalyst MCP (Model Context Protocol)
// server: read-only, agent-facing access to the platform's event types,
// subscriptions, applications, roles, and OpenAPI specs.
//
// It is built on the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk), so the initialize handshake,
// capability negotiation, and both transports (stdio + streamable-HTTP) are
// handled by the SDK. This package supplies the tool + resource catalogue and
// the platform API plumbing. 1:1 in surface with the Rust fc-mcp crate.
package mcp

import (
	"context"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/client"
)

const serverName = "flowcatalyst"

// serverVersion is advertised in the initialize handshake. Overridable at
// build time via -ldflags "-X .../internal/mcp.serverVersion=...".
var serverVersion = "dev"

// instructions guide the connected agent. Mirrors the Rust server's guidance:
// start with whoami, then explore the read-only catalogue.
const instructions = `FlowCatalyst MCP server — read-only access to the platform's metadata.

Start with "whoami" to learn your identity, scope, and accessible clients/apps,
then "list_my_applications" for what you can act on. Use the list_* tools to
browse event types, subscriptions, applications, and roles; the get_* tools to
fetch one by id; "get_schema" for an event type's JSON Schema; "get_openapi"
and "get_application_capabilities" for an application's API surface. All
responses are JSON.`

// Server wraps an MCP SDK server bound to a FlowCatalyst platform client.
type Server struct {
	platform *client.FlowCatalystClient
	mcp      *mcpsdk.Server
}

// New builds an MCP server from resolved config, constructing the platform
// client (and its token manager / static token) from the config's credentials.
func New(cfg Config) *Server {
	return NewWithClient(newPlatformClient(cfg))
}

// NewWithClient builds the server around an existing platform client. Used by
// the in-process launcher (which builds the client from EnvCfg) and by tests
// (which point it at an httptest server).
func NewWithClient(pc *client.FlowCatalystClient) *Server {
	s := &Server{platform: pc}
	m := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: serverName, Version: serverVersion},
		&mcpsdk.ServerOptions{Instructions: instructions},
	)
	s.mcp = m
	s.registerTools(m)
	s.registerResources(m)
	return s
}

// MCP returns the underlying SDK server (for custom transport wiring).
func (s *Server) MCP() *mcpsdk.Server { return s.mcp }

// HTTPHandler returns the streamable-HTTP handler serving this server. Mount it
// at /mcp. The same server instance is reused across requests (stateless tools).
func (s *Server) HTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s.mcp }, nil)
}

// RunStdio serves the MCP server over stdin/stdout until the client
// disconnects or ctx is cancelled. Logs must go to stderr (the SDK keeps
// stdout for JSON-RPC framing).
func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcpsdk.StdioTransport{})
}
