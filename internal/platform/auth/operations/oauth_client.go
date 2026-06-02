package operations

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// ── Create ────────────────────────────────────────────────────────────────

type CreateOAuthClientCommand struct {
	ClientID               string   `json:"clientId"`
	ClientName             string   `json:"clientName"`
	ClientType             string   `json:"clientType"`
	RedirectURIs           []string `json:"redirectUris,omitempty"`
	PostLogoutRedirectURIs []string `json:"postLogoutRedirectUris,omitempty"`
	GrantTypes             []string `json:"grantTypes,omitempty"`
	Scopes                 []string `json:"scopes,omitempty"`
	AllowedOrigins         []string `json:"allowedOrigins,omitempty"`
	ApplicationIDs         []string `json:"applicationIds,omitempty"`
	PrincipalID            *string  `json:"principalId,omitempty"`
}

func CreateOAuthClient(
	ctx context.Context,
	repo *auth.OAuthClientRepo,
	uow *usecasepgx.UnitOfWork,
	cmd CreateOAuthClientCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[OAuthClientCreated], error) {
	var zero commit.Committed[OAuthClientCreated]
	if strings.TrimSpace(cmd.ClientID) == "" {
		return zero, usecase.Validation("CLIENT_ID_REQUIRED", "clientId is required")
	}
	if strings.TrimSpace(cmd.ClientName) == "" {
		return zero, usecase.Validation("CLIENT_NAME_REQUIRED", "clientName is required")
	}

	existing, err := repo.FindByClientID(ctx, cmd.ClientID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_client_id failed", err)
	}
	if existing != nil {
		return zero, usecase.Conflict("CLIENT_ID_EXISTS", "OAuth client_id '"+cmd.ClientID+"' already exists")
	}
	t := auth.ParseOAuthClientType(cmd.ClientType)
	c := auth.NewOAuthClient(cmd.ClientID, cmd.ClientName, t)
	c.RedirectURIs = cmd.RedirectURIs
	c.PostLogoutRedirectURIs = cmd.PostLogoutRedirectURIs
	c.GrantTypes = cmd.GrantTypes
	c.Scopes = cmd.Scopes
	c.AllowedOrigins = cmd.AllowedOrigins
	c.ApplicationIDs = cmd.ApplicationIDs
	c.PrincipalID = cmd.PrincipalID
	if t == auth.OAuthClientConfidential {
		plaintext, ref, err := generateSecret()
		if err != nil {
			return zero, usecase.Internal("SECRET", "generate client secret failed", err)
		}
		c.SetSecretRef(ref)
		stashSecret(c.ID, plaintext)
	}

	event := OAuthClientCreated{
		Metadata:      usecase.NewEventMetadata(ec, OAuthClientCreatedType, Source, oauthSubject(c.ID)),
		OAuthClientID: c.ID,
		ClientID:      c.ClientID,
		ClientName:    c.ClientName,
	}
	return commit.Save(ctx, uow, c, repo, event, cmd)
}

// ── Update ────────────────────────────────────────────────────────────────

type UpdateOAuthClientCommand struct {
	ID                     string   `json:"id"`
	ClientName             *string  `json:"clientName,omitempty"`
	RedirectURIs           []string `json:"redirectUris,omitempty"`
	PostLogoutRedirectURIs []string `json:"postLogoutRedirectUris,omitempty"`
	GrantTypes             []string `json:"grantTypes,omitempty"`
	Scopes                 []string `json:"scopes,omitempty"`
	AllowedOrigins         []string `json:"allowedOrigins,omitempty"`
	ApplicationIDs         []string `json:"applicationIds,omitempty"`
	PKCERequired           *bool    `json:"pkceRequired,omitempty"`
}

func UpdateOAuthClient(
	ctx context.Context,
	repo *auth.OAuthClientRepo,
	uow *usecasepgx.UnitOfWork,
	cmd UpdateOAuthClientCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[OAuthClientUpdated], error) {
	var zero commit.Committed[OAuthClientUpdated]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	if cmd.ClientName != nil && strings.TrimSpace(*cmd.ClientName) == "" {
		return zero, usecase.Validation("CLIENT_NAME_REQUIRED", "clientName cannot be empty")
	}
	c, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return zero, httperror.NotFound("OAuthClient", cmd.ID)
	}
	if cmd.ClientName != nil {
		c.ClientName = strings.TrimSpace(*cmd.ClientName)
	}
	if cmd.RedirectURIs != nil {
		c.RedirectURIs = cmd.RedirectURIs
	}
	if cmd.PostLogoutRedirectURIs != nil {
		c.PostLogoutRedirectURIs = cmd.PostLogoutRedirectURIs
	}
	if cmd.GrantTypes != nil {
		c.GrantTypes = cmd.GrantTypes
	}
	if cmd.Scopes != nil {
		c.Scopes = cmd.Scopes
	}
	if cmd.AllowedOrigins != nil {
		c.AllowedOrigins = cmd.AllowedOrigins
	}
	if cmd.ApplicationIDs != nil {
		c.ApplicationIDs = cmd.ApplicationIDs
	}
	if cmd.PKCERequired != nil {
		c.PKCERequired = *cmd.PKCERequired
	}

	event := OAuthClientUpdated{
		Metadata:      usecase.NewEventMetadata(ec, OAuthClientUpdatedType, Source, oauthSubject(c.ID)),
		OAuthClientID: c.ID,
		ClientName:    c.ClientName,
	}
	return commit.Save(ctx, uow, c, repo, event, cmd)
}

// ── Activate ──────────────────────────────────────────────────────────────

type ActivateOAuthClientCommand struct {
	ID string `json:"id"`
}

func ActivateOAuthClient(
	ctx context.Context,
	repo *auth.OAuthClientRepo,
	uow *usecasepgx.UnitOfWork,
	cmd ActivateOAuthClientCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[OAuthClientActivated], error) {
	var zero commit.Committed[OAuthClientActivated]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	c, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return zero, httperror.NotFound("OAuthClient", cmd.ID)
	}
	c.Activate()
	event := OAuthClientActivated{
		Metadata:      usecase.NewEventMetadata(ec, OAuthClientActivatedType, Source, oauthSubject(c.ID)),
		OAuthClientID: c.ID,
	}
	return commit.Save(ctx, uow, c, repo, event, cmd)
}

// ── Deactivate ────────────────────────────────────────────────────────────

type DeactivateOAuthClientCommand struct {
	ID string `json:"id"`
}

func DeactivateOAuthClient(
	ctx context.Context,
	repo *auth.OAuthClientRepo,
	uow *usecasepgx.UnitOfWork,
	cmd DeactivateOAuthClientCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[OAuthClientDeactivated], error) {
	var zero commit.Committed[OAuthClientDeactivated]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	c, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return zero, httperror.NotFound("OAuthClient", cmd.ID)
	}
	c.Deactivate()
	event := OAuthClientDeactivated{
		Metadata:      usecase.NewEventMetadata(ec, OAuthClientDeactivatedType, Source, oauthSubject(c.ID)),
		OAuthClientID: c.ID,
	}
	return commit.Save(ctx, uow, c, repo, event, cmd)
}

// ── Delete ────────────────────────────────────────────────────────────────

type DeleteOAuthClientCommand struct {
	ID string `json:"id"`
}

func DeleteOAuthClient(
	ctx context.Context,
	repo *auth.OAuthClientRepo,
	uow *usecasepgx.UnitOfWork,
	cmd DeleteOAuthClientCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[OAuthClientDeleted], error) {
	var zero commit.Committed[OAuthClientDeleted]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	c, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return zero, httperror.NotFound("OAuthClient", cmd.ID)
	}
	event := OAuthClientDeleted{
		Metadata:      usecase.NewEventMetadata(ec, OAuthClientDeletedType, Source, oauthSubject(c.ID)),
		OAuthClientID: c.ID,
		ClientID:      c.ClientID,
	}
	return commit.Delete(ctx, uow, c, repo, event, cmd)
}

// ── RotateSecret ──────────────────────────────────────────────────────────

type RotateOAuthClientSecretCommand struct {
	ID string `json:"id"`
}

func RotateOAuthClientSecret(
	ctx context.Context,
	repo *auth.OAuthClientRepo,
	uow *usecasepgx.UnitOfWork,
	cmd RotateOAuthClientSecretCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[OAuthClientSecretRotated], error) {
	var zero commit.Committed[OAuthClientSecretRotated]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	c, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return zero, httperror.NotFound("OAuthClient", cmd.ID)
	}
	if c.ClientType != auth.OAuthClientConfidential {
		return zero, usecase.Conflict("NOT_CONFIDENTIAL", "Only CONFIDENTIAL clients have rotatable secrets")
	}
	plaintext, ref, err := generateSecret()
	if err != nil {
		return zero, usecase.Internal("SECRET", "generate client secret failed", err)
	}
	c.SetSecretRef(ref)
	stashSecret(c.ID, plaintext)

	event := OAuthClientSecretRotated{
		Metadata:      usecase.NewEventMetadata(ec, OAuthClientSecretRotatedType, Source, oauthSubject(c.ID)),
		OAuthClientID: c.ID,
	}
	return commit.Save(ctx, uow, c, repo, event, cmd)
}

// ── helpers ───────────────────────────────────────────────────────────────

// generateSecret mints a random client secret and returns it alongside
// its encrypted reference (the value stored in client_secret_ref).
// Mirrors Rust: the secret is reversibly encrypted with FLOWCATALYST_APP_KEY
// and verified at /oauth/token by decrypt-and-compare. Fails if no app
// key is configured rather than storing a plaintext or unverifiable secret.
func generateSecret() (plaintext, ref string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(b)
	enc, err := encryption.FromEnv()
	if err != nil {
		return "", "", err
	}
	if enc == nil {
		return "", "", errors.New("FLOWCATALYST_APP_KEY not configured; cannot encrypt client secret")
	}
	encrypted, err := enc.Encrypt(plaintext)
	if err != nil {
		return "", "", err
	}
	// Store with the "encrypted:" prefix so the persisted string is
	// byte-identical to what Rust writes (oauth_clients_api.rs:
	// format!("encrypted:{}", encrypted)). Decrypt strips the prefix on
	// read, so verification is unaffected; this keeps client_secret_ref
	// values uniform across a mixed Go/Rust deployment.
	ref = "encrypted:" + encrypted
	return plaintext, ref, nil
}

func stashSecret(clientID, plaintext string) { secretStash.Store(clientID, plaintext) }

// PopStashedSecret returns the once-readable plaintext for clientID.
// Called by the HTTP handler immediately after the use case succeeds.
func PopStashedSecret(clientID string) (string, bool) {
	v, ok := secretStash.LoadAndDelete(clientID)
	if !ok {
		return "", false
	}
	return v.(string), true
}
