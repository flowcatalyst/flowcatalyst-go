package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"

	"github.com/flowcatalyst/flowcatalyst-go/internal/mcp"
)

// newMCPCmd runs the FlowCatalyst MCP server. It defaults to stdio (the usual
// way an MCP client launches a server as a subprocess); pass --http <bind> to
// listen for the streamable-HTTP transport instead. Config is resolved from
// the environment / the fc-dev-bootstrapped credentials file, with flag
// overrides. For the normal dev workflow `fc-dev start --mcp` boots the
// HTTP server alongside everything else.
func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the FlowCatalyst MCP server (stdio by default; --http to listen)",
		RunE:  runMCP,
	}
	cmd.Flags().String("http", "", "listen for streamable-HTTP MCP at this bind address (e.g. 127.0.0.1:8090); empty = stdio")
	cmd.Flags().String("platform-url", "", "override FLOWCATALYST_URL (platform base URL)")
	cmd.Flags().String("client-id", "", "override FLOWCATALYST_CLIENT_ID")
	cmd.Flags().String("client-secret", "", "override FLOWCATALYST_CLIENT_SECRET")
	return cmd
}

func runMCP(cmd *cobra.Command, _ []string) error {
	// env / credentials-file resolution, then flag overrides.
	cfg := mcp.LoadConfig()
	if v, _ := cmd.Flags().GetString("platform-url"); v != "" {
		cfg.BaseURL = v
	}
	if v, _ := cmd.Flags().GetString("client-id"); v != "" {
		cfg.ClientID = v
	}
	if v, _ := cmd.Flags().GetString("client-secret"); v != "" {
		cfg.ClientSecret = v
	}
	if err := mcp.RequireCredentials(cfg); err != nil {
		// Warn (to stderr — stdout is reserved for JSON-RPC in stdio mode) but
		// proceed: a localhost platform may not require auth, and the platform
		// will reject if it does.
		slog.Warn("starting MCP server without credentials", "err", err)
	}

	srv := mcp.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bind, _ := cmd.Flags().GetString("http")
	if bind == "" {
		// stdio transport: JSON-RPC over stdin/stdout, logs to stderr.
		slog.Info("fc-dev mcp serving over stdio", "platform_url", cfg.BaseURL)
		return srv.RunStdio(ctx)
	}
	return serveMCPHTTP(ctx, srv, bind, cfg.BaseURL)
}

func serveMCPHTTP(ctx context.Context, srv *mcp.Server, bind, platformURL string) error {
	r := chi.NewRouter()
	r.Handle("/mcp", srv.HTTPHandler())
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	httpSrv := &http.Server{Addr: bind, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("fc-dev mcp listening", "addr", bind, "platform_url", platformURL)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-errCh:
		return fmt.Errorf("mcp listener: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
