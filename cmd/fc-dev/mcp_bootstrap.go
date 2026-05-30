package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/mcp"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// mcpLocalClientID is the fixed client_id for the local-dev MCP OAuth client.
// Matches the Rust bin/fc-dev mcp_bootstrap so a shared MCP client config works.
const mcpLocalClientID = "flowcatalyst-mcp-local"

// bootstrapMCPCredentials idempotently provisions a local MCP OAuth client so
// `fc-dev mcp` (and `fc-dev start --mcp`) authenticate with zero config. It
// mirrors the Rust fc-dev mcp_bootstrap: if the client already exists it is a
// no-op; otherwise it creates a SERVICE principal (super-admin, anchor scope) +
// service account + a CONFIDENTIAL client_credentials OAuth client, then writes
// the plaintext secret to mcp-credentials.json in the OS cache dir (macOS
// ~/Library/Caches, Linux ~/.cache; see mcp.CredentialsPath), mode 0600.
//
// Dev-only: it is called only from `fc-dev start`, which is the dev monolith.
// Requires a stable FLOWCATALYST_APP_KEY (ensured by start.go) so the stored
// encrypted secret stays decryptable at /oauth/token across restarts.
func bootstrapMCPCredentials(ctx context.Context, pool *pgxpool.Pool, baseURL string) error {
	authRepo := auth.NewRepository(pool)

	existing, err := authRepo.OAuthClients.FindByClientID(ctx, mcpLocalClientID)
	if err != nil {
		return fmt.Errorf("look up MCP client: %w", err)
	}
	if existing != nil {
		// Already provisioned on a prior boot; the credentials file persists
		// from first run. Idempotent (1:1 with Rust).
		return nil
	}

	enc, err := encryption.FromEnv()
	if err != nil {
		return fmt.Errorf("init encryption: %w", err)
	}
	if enc == nil {
		return fmt.Errorf("FLOWCATALYST_APP_KEY not set; cannot encrypt MCP client secret")
	}

	secret, err := generateSecret()
	if err != nil {
		return err
	}
	secretRef, err := enc.Encrypt(secret)
	if err != nil {
		return fmt.Errorf("encrypt MCP client secret: %w", err)
	}

	sa := serviceaccount.New("mcp:local", "fc-mcp local")
	saDesc := "Local MCP service account (provisioned by fc-dev)"
	sa.Description = &saDesc

	saPrincipal := principal.NewService(sa.ID, "fc-mcp local")
	saPrincipal.Scope = principal.ScopeAnchor

	oauthClient := auth.NewOAuthClient(mcpLocalClientID, "FlowCatalyst MCP (local dev)", auth.OAuthClientConfidential)
	oauthClient.SecretRef = &secretRef
	oauthClient.GrantTypes = []string{"client_credentials"}
	oauthClient.PrincipalID = &saPrincipal.ID

	principalRepo := principal.NewRepository(pool)
	saRepo := serviceaccount.NewRepository(pool)
	if err := infraPersist(ctx, pool, func(tx *usecasepgx.DbTx) error {
		if err := principalRepo.Persist(ctx, saPrincipal, tx); err != nil {
			return fmt.Errorf("mcp principal: %w", err)
		}
		if err := saRepo.Persist(ctx, sa, tx); err != nil {
			return fmt.Errorf("mcp service account: %w", err)
		}
		if err := authRepo.OAuthClients.Persist(ctx, oauthClient, tx); err != nil {
			return fmt.Errorf("mcp oauth client: %w", err)
		}
		// principal.Persist doesn't sync iam_principal_roles; grant
		// super-admin directly so the minted token has full read access
		// (1:1 with Rust, which assigns platform:super-admin to the SA).
		_, err := tx.Inner().Exec(ctx,
			`INSERT INTO iam_principal_roles
			     (principal_id, role_name, assignment_source, assigned_at)
			 VALUES ($1, 'platform:super-admin', 'BOOTSTRAP', NOW())
			 ON CONFLICT DO NOTHING`,
			saPrincipal.ID)
		return err
	}); err != nil {
		return err
	}

	if err := mcp.WriteCredentialsFile(mcpLocalClientID, secret, baseURL); err != nil {
		return fmt.Errorf("write MCP credentials file: %w", err)
	}
	path, _ := mcp.CredentialsPath()
	slog.Info("bootstrapped local MCP credentials", "client_id", mcpLocalClientID, "credentials", path)
	return nil
}

// ensureAppKeyFile reads, or generates and persists (0600), the field-encryption
// key (FLOWCATALYST_APP_KEY) so OAuth client secrets — including the bootstrapped
// MCP client — stay decryptable across restarts. Analogous to the JWT signing
// key file. fc-server requires operators to supply the key via env instead.
func ensureAppKeyFile(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if k := strings.TrimSpace(string(b)); k != "" {
			return k, nil
		}
	}
	key, err := encryption.GenerateKey()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return "", err
	}
	return key, nil
}
