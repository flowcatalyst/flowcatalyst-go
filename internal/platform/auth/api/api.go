// Package api wires admin HTTP routes for the auth subdomain via huma.
// Runtime routes (/oauth/token, /oauth/authorize, /.well-known/*, OIDC
// login/callback) remain registered by the provider package.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	platformauth "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// State bundles deps.
type State struct {
	Repo *auth.Repository
	// Applications resolves oauth_client_application_ids to {id, name}
	// display refs for OAuthClientResponse.Applications. Optional: nil
	// leaves Applications empty (clients can still use applicationIds).
	Applications *application.Repository
	UoW          *usecasepgx.UnitOfWork
}

// fillApplicationRefs populates each response's Applications ({id,name})
// from its ApplicationIDs, resolving names via the application repo in a
// single deduped pass. Unresolved ids (e.g. a deleted application) fall
// back to the id as the name so the SPA still renders a chip. No-op when
// the application repo isn't wired.
func (s *State) fillApplicationRefs(ctx context.Context, resps ...*OAuthClientResponse) error {
	if s.Applications == nil {
		return nil
	}
	idSet := map[string]struct{}{}
	for _, r := range resps {
		for _, id := range r.ApplicationIDs {
			if id != "" {
				idSet[id] = struct{}{}
			}
		}
	}
	if len(idSet) == 0 {
		return nil
	}
	nameByID := make(map[string]string, len(idSet))
	for id := range idSet {
		app, err := s.Applications.FindByID(ctx, id)
		if err != nil {
			return usecase.Internal("REPO", "find_application failed", err)
		}
		if app != nil {
			nameByID[id] = app.Name
		}
	}
	for _, r := range resps {
		refs := make([]OAuthClientApplicationRef, 0, len(r.ApplicationIDs))
		for _, id := range r.ApplicationIDs {
			name := nameByID[id]
			if name == "" {
				name = id
			}
			refs = append(refs, OAuthClientApplicationRef{ID: id, Name: name})
		}
		r.Applications = refs
	}
	return nil
}

const (
	tagOAuth          = "oauth-clients"
	tagAnchorDomains  = "anchor-domains"
	tagAuthConfigs    = "auth-configs"
	tagIdpRoleMapping = "idp-role-mappings"
)

// Register mounts the auth admin endpoints. Anchor-only.
func Register(api huma.API, s *State) {
	// OAuth clients
	huma.Register(api, huma.Operation{
		OperationID:   "listOAuthClients",
		Method:        http.MethodGet,
		Path:          "/api/oauth-clients",
		Summary:       "List OAuth clients",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusOK,
	}, s.listOAuthClients)

	huma.Register(api, huma.Operation{
		OperationID:   "createOAuthClient",
		Method:        http.MethodPost,
		Path:          "/api/oauth-clients",
		Summary:       "Create an OAuth client",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusCreated,
	}, s.createOAuthClient)

	huma.Register(api, huma.Operation{
		OperationID:   "getOAuthClient",
		Method:        http.MethodGet,
		Path:          "/api/oauth-clients/{id}",
		Summary:       "Get an OAuth client by id",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusOK,
	}, s.getOAuthClient)

	huma.Register(api, huma.Operation{
		OperationID:   "updateOAuthClient",
		Method:        http.MethodPut,
		Path:          "/api/oauth-clients/{id}",
		Summary:       "Update an OAuth client",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusNoContent,
	}, s.updateOAuthClient)

	huma.Register(api, huma.Operation{
		OperationID:   "activateOAuthClient",
		Method:        http.MethodPost,
		Path:          "/api/oauth-clients/{id}/activate",
		Summary:       "Activate an OAuth client",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusOK,
	}, s.activateOAuthClient)

	huma.Register(api, huma.Operation{
		OperationID:   "deactivateOAuthClient",
		Method:        http.MethodPost,
		Path:          "/api/oauth-clients/{id}/deactivate",
		Summary:       "Deactivate an OAuth client",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusOK,
	}, s.deactivateOAuthClient)

	huma.Register(api, huma.Operation{
		OperationID:   "rotateOAuthClientSecret",
		Method:        http.MethodPost,
		Path:          "/api/oauth-clients/{id}/rotate-secret",
		Summary:       "Rotate an OAuth client's secret",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusOK,
	}, s.rotateOAuthClientSecret)

	// SDK-compatibility aliases. The Laravel/Rust client calls
	// /api/oauth-clients/{id}/regenerate-secret (same as rotate-secret) and
	// looks clients up by their client_id via /by-client-id/{clientId}.
	huma.Register(api, huma.Operation{
		OperationID:   "regenerateOAuthClientSecret",
		Method:        http.MethodPost,
		Path:          "/api/oauth-clients/{id}/regenerate-secret",
		Summary:       "Regenerate an OAuth client's secret (SDK alias of rotate-secret)",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusOK,
	}, s.rotateOAuthClientSecret)

	huma.Register(api, huma.Operation{
		OperationID:   "getOAuthClientByClientID",
		Method:        http.MethodGet,
		Path:          "/api/oauth-clients/by-client-id/{clientId}",
		Summary:       "Get an OAuth client by its client_id (SDK lookup)",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusOK,
	}, s.getOAuthClientByClientID)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteOAuthClient",
		Method:        http.MethodDelete,
		Path:          "/api/oauth-clients/{id}",
		Summary:       "Delete an OAuth client",
		Tags:          []string{tagOAuth},
		DefaultStatus: http.StatusNoContent,
	}, s.deleteOAuthClient)

	// Anchor domains
	huma.Register(api, huma.Operation{
		OperationID:   "listAnchorDomains",
		Method:        http.MethodGet,
		Path:          "/api/anchor-domains",
		Summary:       "List anchor domains",
		Tags:          []string{tagAnchorDomains},
		DefaultStatus: http.StatusOK,
	}, s.listAnchorDomains)

	huma.Register(api, huma.Operation{
		OperationID:   "createAnchorDomain",
		Method:        http.MethodPost,
		Path:          "/api/anchor-domains",
		Summary:       "Create an anchor domain",
		Tags:          []string{tagAnchorDomains},
		DefaultStatus: http.StatusCreated,
	}, s.createAnchorDomain)

	huma.Register(api, huma.Operation{
		OperationID:   "updateAnchorDomain",
		Method:        http.MethodPut,
		Path:          "/api/anchor-domains/{id}",
		Summary:       "Update an anchor domain",
		Tags:          []string{tagAnchorDomains},
		DefaultStatus: http.StatusNoContent,
	}, s.updateAnchorDomain)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteAnchorDomain",
		Method:        http.MethodDelete,
		Path:          "/api/anchor-domains/{id}",
		Summary:       "Delete an anchor domain",
		Tags:          []string{tagAnchorDomains},
		DefaultStatus: http.StatusNoContent,
	}, s.deleteAnchorDomain)

	// Auth configs
	huma.Register(api, huma.Operation{
		OperationID:   "listAuthConfigs",
		Method:        http.MethodGet,
		Path:          "/api/auth-configs",
		Summary:       "List client auth configs",
		Tags:          []string{tagAuthConfigs},
		DefaultStatus: http.StatusOK,
	}, s.listAuthConfigs)

	huma.Register(api, huma.Operation{
		OperationID:   "createAuthConfig",
		Method:        http.MethodPost,
		Path:          "/api/auth-configs",
		Summary:       "Create a client auth config",
		Tags:          []string{tagAuthConfigs},
		DefaultStatus: http.StatusCreated,
	}, s.createAuthConfig)

	huma.Register(api, huma.Operation{
		OperationID:   "updateAuthConfig",
		Method:        http.MethodPut,
		Path:          "/api/auth-configs/{id}",
		Summary:       "Update a client auth config",
		Tags:          []string{tagAuthConfigs},
		DefaultStatus: http.StatusNoContent,
	}, s.updateAuthConfig)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteAuthConfig",
		Method:        http.MethodDelete,
		Path:          "/api/auth-configs/{id}",
		Summary:       "Delete a client auth config",
		Tags:          []string{tagAuthConfigs},
		DefaultStatus: http.StatusNoContent,
	}, s.deleteAuthConfig)

	// IDP role mappings
	huma.Register(api, huma.Operation{
		OperationID:   "listIdpRoleMappings",
		Method:        http.MethodGet,
		Path:          "/api/idp-role-mappings",
		Summary:       "List IDP role mappings",
		Tags:          []string{tagIdpRoleMapping},
		DefaultStatus: http.StatusOK,
	}, s.listIdpRoleMappings)

	huma.Register(api, huma.Operation{
		OperationID:   "createIdpRoleMapping",
		Method:        http.MethodPost,
		Path:          "/api/idp-role-mappings",
		Summary:       "Create an IDP role mapping",
		Tags:          []string{tagIdpRoleMapping},
		DefaultStatus: http.StatusCreated,
	}, s.createIdpRoleMapping)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteIdpRoleMapping",
		Method:        http.MethodDelete,
		Path:          "/api/idp-role-mappings/{id}",
		Summary:       "Delete an IDP role mapping",
		Tags:          []string{tagIdpRoleMapping},
		DefaultStatus: http.StatusNoContent,
	}, s.deleteIdpRoleMapping)
}

// ── shared types ──────────────────────────────────────────────────────────

type emptyInput struct{}
type emptyOutput struct{}

type idInput struct {
	ID string `path:"id"`
}

func authedAnchor(ctx context.Context) (*platformauth.AuthContext, error) {
	ac := platformauth.FromContext(ctx)
	if err := platformauth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	return ac, nil
}

// ── OAuthClient ───────────────────────────────────────────────────────────

type listOAuthClientsOutput struct {
	Body OAuthClientListResponse
}

func (s *State) listOAuthClients(ctx context.Context, _ *emptyInput) (*listOAuthClientsOutput, error) {
	if _, err := authedAnchor(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Repo.OAuthClients.FindAll(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_all failed", err)
	}
	out := make([]OAuthClientResponse, 0, len(rows))
	for i := range rows {
		out = append(out, oauthClientFromEntity(&rows[i]))
	}
	ptrs := make([]*OAuthClientResponse, len(out))
	for i := range out {
		ptrs[i] = &out[i]
	}
	if err := s.fillApplicationRefs(ctx, ptrs...); err != nil {
		return nil, err
	}
	return &listOAuthClientsOutput{Body: OAuthClientListResponse{Clients: out}}, nil
}

type getOAuthClientOutput struct {
	Body OAuthClientResponse
}

func (s *State) getOAuthClient(ctx context.Context, in *idInput) (*getOAuthClientOutput, error) {
	if _, err := authedAnchor(ctx); err != nil {
		return nil, err
	}
	c, err := s.Repo.OAuthClients.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return nil, httperror.NotFound("OAuthClient", in.ID)
	}
	resp := oauthClientFromEntity(c)
	if err := s.fillApplicationRefs(ctx, &resp); err != nil {
		return nil, err
	}
	return &getOAuthClientOutput{Body: resp}, nil
}

type clientIDPathInput struct {
	ClientID string `path:"clientId"`
}

// getOAuthClientByClientID backs GET /api/oauth-clients/by-client-id/{clientId}
// (SDK lookup by the OAuth client_id rather than the internal TSID).
func (s *State) getOAuthClientByClientID(ctx context.Context, in *clientIDPathInput) (*getOAuthClientOutput, error) {
	if _, err := authedAnchor(ctx); err != nil {
		return nil, err
	}
	c, err := s.Repo.OAuthClients.FindByClientID(ctx, in.ClientID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_client_id failed", err)
	}
	if c == nil {
		return nil, httperror.NotFound("OAuthClient", in.ClientID)
	}
	resp := oauthClientFromEntity(c)
	if err := s.fillApplicationRefs(ctx, &resp); err != nil {
		return nil, err
	}
	return &getOAuthClientOutput{Body: resp}, nil
}

type createOAuthClientInput struct {
	Body CreateOAuthClientRequest
}

type createOAuthClientOutput struct {
	Body CreateOAuthClientResponse
}

func (s *State) createOAuthClient(ctx context.Context, in *createOAuthClientInput) (*createOAuthClientOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateOAuthClient(ctx, s.Repo.OAuthClients, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	event := committed.Event()
	// Re-fetch the persisted client so the SPA receives the full
	// OAuthClientResponse under `client` (oauth-clients.ts:56). Matches
	// Rust oauth_clients_api.rs:294-305.
	c, err := s.Repo.OAuthClients.FindByID(ctx, event.OAuthClientID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return nil, usecase.Internal("REPO", "oauth client created but row not found", nil)
	}
	resp := CreateOAuthClientResponse{Client: oauthClientFromEntity(c)}
	if err := s.fillApplicationRefs(ctx, &resp.Client); err != nil {
		return nil, err
	}
	if plaintext, ok := operations.PopStashedSecret(event.OAuthClientID); ok {
		resp.ClientSecret = plaintext
	}
	return &createOAuthClientOutput{Body: resp}, nil
}

type updateOAuthClientInput struct {
	ID   string `path:"id"`
	Body UpdateOAuthClientRequest
}

func (s *State) updateOAuthClient(ctx context.Context, in *updateOAuthClientInput) (*emptyOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateOAuthClient(ctx, s.Repo.OAuthClients, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

// oauthClientStatusChangeOutput carries the {success, message?} body, matching
// Rust's SuccessResponse from activate/deactivate. The SPA reads `.message`
// (oauth-clients.ts:109-117), which is preserved. Returns 200 + body rather
// than 204 so apiFetch does not resolve to undefined.
type oauthClientStatusChangeOutput struct {
	Body apicommon.SuccessResponse
}

func (s *State) activateOAuthClient(ctx context.Context, in *idInput) (*oauthClientStatusChangeOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ActivateOAuthClient(ctx, s.Repo.OAuthClients, s.UoW,
		operations.ActivateOAuthClientCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &oauthClientStatusChangeOutput{Body: apicommon.SuccessResponse{Success: true, Message: "OAuth client activated"}}, nil
}

func (s *State) deactivateOAuthClient(ctx context.Context, in *idInput) (*oauthClientStatusChangeOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeactivateOAuthClient(ctx, s.Repo.OAuthClients, s.UoW,
		operations.DeactivateOAuthClientCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &oauthClientStatusChangeOutput{Body: apicommon.SuccessResponse{Success: true, Message: "OAuth client deactivated"}}, nil
}

type rotateOAuthClientSecretOutput struct {
	Body RotateOAuthClientSecretResponse
}

func (s *State) rotateOAuthClientSecret(ctx context.Context, in *idInput) (*rotateOAuthClientSecretOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.RotateOAuthClientSecret(ctx, s.Repo.OAuthClients, s.UoW,
		operations.RotateOAuthClientSecretCommand{ID: in.ID}, ec)
	if err != nil {
		return nil, err
	}
	event := committed.Event()
	// The SPA expects the public client_id string, not the internal id
	// (oauth-clients.ts:62-65). Re-fetch to obtain it.
	c, err := s.Repo.OAuthClients.FindByID(ctx, event.OAuthClientID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if c == nil {
		return nil, httperror.NotFound("OAuthClient", event.OAuthClientID)
	}
	resp := RotateOAuthClientSecretResponse{ClientID: c.ClientID}
	if plaintext, ok := operations.PopStashedSecret(event.OAuthClientID); ok {
		resp.ClientSecret = plaintext
	}
	return &rotateOAuthClientSecretOutput{Body: resp}, nil
}

func (s *State) deleteOAuthClient(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteOAuthClient(ctx, s.Repo.OAuthClients, s.UoW,
		operations.DeleteOAuthClientCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

// ── AnchorDomain ──────────────────────────────────────────────────────────

type listAnchorDomainsOutput struct {
	Body AnchorDomainListResponse
}

func (s *State) listAnchorDomains(ctx context.Context, _ *emptyInput) (*listAnchorDomainsOutput, error) {
	if _, err := authedAnchor(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Repo.AnchorDomains.FindAll(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_all failed", err)
	}
	out := make([]AnchorDomainResponse, 0, len(rows))
	for i := range rows {
		out = append(out, anchorDomainFromEntity(&rows[i]))
	}
	return &listAnchorDomainsOutput{Body: AnchorDomainListResponse{Items: out}}, nil
}

type createAnchorDomainInput struct {
	Body CreateAnchorDomainRequest
}

type createAnchorDomainOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) createAnchorDomain(ctx context.Context, in *createAnchorDomainInput) (*createAnchorDomainOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateAnchorDomain(ctx, s.Repo.AnchorDomains, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createAnchorDomainOutput{Body: apicommon.CreatedResponse{ID: committed.Event().AnchorDomainID}}, nil
}

type updateAnchorDomainInput struct {
	ID   string `path:"id"`
	Body UpdateAnchorDomainRequest
}

func (s *State) updateAnchorDomain(ctx context.Context, in *updateAnchorDomainInput) (*emptyOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateAnchorDomain(ctx, s.Repo.AnchorDomains, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

func (s *State) deleteAnchorDomain(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteAnchorDomain(ctx, s.Repo.AnchorDomains, s.UoW,
		operations.DeleteAnchorDomainCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

// ── AuthConfig ────────────────────────────────────────────────────────────

type listAuthConfigsOutput struct {
	Body AuthConfigListResponse
}

func (s *State) listAuthConfigs(ctx context.Context, _ *emptyInput) (*listAuthConfigsOutput, error) {
	if _, err := authedAnchor(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Repo.ClientAuthConfigs.FindAll(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_all failed", err)
	}
	out := make([]AuthConfigResponse, 0, len(rows))
	for i := range rows {
		out = append(out, authConfigFromEntity(&rows[i]))
	}
	return &listAuthConfigsOutput{Body: AuthConfigListResponse{Items: out}}, nil
}

type createAuthConfigInput struct {
	Body CreateAuthConfigRequest
}

type createAuthConfigOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) createAuthConfig(ctx context.Context, in *createAuthConfigInput) (*createAuthConfigOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateAuthConfig(ctx, s.Repo.ClientAuthConfigs, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createAuthConfigOutput{Body: apicommon.CreatedResponse{ID: committed.Event().AuthConfigID}}, nil
}

type updateAuthConfigInput struct {
	ID   string `path:"id"`
	Body UpdateAuthConfigRequest
}

func (s *State) updateAuthConfig(ctx context.Context, in *updateAuthConfigInput) (*emptyOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateAuthConfig(ctx, s.Repo.ClientAuthConfigs, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

func (s *State) deleteAuthConfig(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteAuthConfig(ctx, s.Repo.ClientAuthConfigs, s.UoW,
		operations.DeleteAuthConfigCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

// ── IdpRoleMapping ────────────────────────────────────────────────────────

type listIdpRoleMappingsOutput struct {
	Body IdpRoleMappingListResponse
}

func (s *State) listIdpRoleMappings(ctx context.Context, _ *emptyInput) (*listIdpRoleMappingsOutput, error) {
	if _, err := authedAnchor(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Repo.IdpRoleMappings.FindAll(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_all failed", err)
	}
	out := make([]IdpRoleMappingResponse, 0, len(rows))
	for i := range rows {
		out = append(out, idpRoleMappingFromEntity(&rows[i]))
	}
	return &listIdpRoleMappingsOutput{Body: IdpRoleMappingListResponse{Items: out}}, nil
}

type createIdpRoleMappingInput struct {
	Body CreateIdpRoleMappingRequest
}

type createIdpRoleMappingOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) createIdpRoleMapping(ctx context.Context, in *createIdpRoleMappingInput) (*createIdpRoleMappingOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateIdpRoleMapping(ctx, s.Repo.IdpRoleMappings, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createIdpRoleMappingOutput{Body: apicommon.CreatedResponse{ID: committed.Event().MappingID}}, nil
}

func (s *State) deleteIdpRoleMapping(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac, err := authedAnchor(ctx)
	if err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteIdpRoleMapping(ctx, s.Repo.IdpRoleMappings, s.UoW,
		operations.DeleteIdpRoleMappingCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}
