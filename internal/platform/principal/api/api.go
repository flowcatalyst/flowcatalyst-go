// Package api wires the HTTP routes for the principal subdomain via huma.
package api

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	platformauth "github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/client"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// State bundles deps. Principal ops need cross-aggregate validation
// against roles, applications, and clients.
type State struct {
	Repo              *principal.Repository
	GrantRepo         *principal.ClientAccessGrantRepo
	Roles             *role.Repository
	Applications      *application.Repository
	Clients           *client.Repository
	Mappings          *emaildomainmapping.Repository // for /check-email-domain + create-user scope derivation
	IdentityProviders *identityprovider.Repository   // for /check-email-domain + create-user idp-type
	AnchorDomains     *platformauth.AnchorDomainRepo // for create-user anchor-domain check (optional)
	UoW               *usecasepgx.UnitOfWork
	PasswordEmailer   operations.PasswordResetEmailer // optional; gates /send-password-reset
}

const tag = "principals"

// Register mounts the principal endpoints.
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "listPrincipals",
		Method:        http.MethodGet,
		Path:          "/api/principals",
		Summary:       "List principals",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.list)

	huma.Register(api, huma.Operation{
		OperationID:   "createPrincipal",
		Method:        http.MethodPost,
		Path:          "/api/principals",
		Summary:       "Create a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
	}, s.create)

	huma.Register(api, huma.Operation{
		OperationID:   "createUser",
		Method:        http.MethodPost,
		Path:          "/api/principals/users",
		Summary:       "Create a user principal (scope derived from email domain)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.createUser)

	huma.Register(api, huma.Operation{
		OperationID:   "getPrincipal",
		Method:        http.MethodGet,
		Path:          "/api/principals/{id}",
		Summary:       "Get a principal by id",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.getByID)

	huma.Register(api, huma.Operation{
		OperationID:   "updatePrincipal",
		Method:        http.MethodPut,
		Path:          "/api/principals/{id}",
		Summary:       "Update a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.update)

	huma.Register(api, huma.Operation{
		OperationID:   "activatePrincipal",
		Method:        http.MethodPost,
		Path:          "/api/principals/{id}/activate",
		Summary:       "Activate a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.activate)

	huma.Register(api, huma.Operation{
		OperationID:   "deactivatePrincipal",
		Method:        http.MethodPost,
		Path:          "/api/principals/{id}/deactivate",
		Summary:       "Deactivate a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.deactivate)

	huma.Register(api, huma.Operation{
		OperationID:   "resetPrincipalPassword",
		Method:        http.MethodPost,
		Path:          "/api/principals/{id}/reset-password",
		Summary:       "Reset a user's password",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.resetPassword)

	huma.Register(api, huma.Operation{
		OperationID:   "sendPrincipalPasswordReset",
		Method:        http.MethodPost,
		Path:          "/api/principals/{id}/send-password-reset",
		Summary:       "Send a password-reset email to a user",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.sendPasswordReset)

	huma.Register(api, huma.Operation{
		OperationID:   "checkPrincipalEmailDomain",
		Method:        http.MethodGet,
		Path:          "/api/principals/check-email-domain",
		Summary:       "Resolve auth-method for an email's domain",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.checkEmailDomain)

	huma.Register(api, huma.Operation{
		OperationID:   "deletePrincipal",
		Method:        http.MethodDelete,
		Path:          "/api/principals/{id}",
		Summary:       "Delete a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.delete)

	huma.Register(api, huma.Operation{
		OperationID:   "assignPrincipalRoles",
		Method:        http.MethodPut,
		Path:          "/api/principals/{id}/roles",
		Summary:       "Assign roles to a principal (replaces full set)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.assignRoles)

	huma.Register(api, huma.Operation{
		OperationID:   "listPrincipalRoles",
		Method:        http.MethodGet,
		Path:          "/api/principals/{id}/roles",
		Summary:       "List a principal's assigned roles",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listRoles)

	huma.Register(api, huma.Operation{
		OperationID:   "addPrincipalRole",
		Method:        http.MethodPost,
		Path:          "/api/principals/{id}/roles",
		Summary:       "Add a single role to a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.addRole)

	huma.Register(api, huma.Operation{
		OperationID:   "removePrincipalRole",
		Method:        http.MethodDelete,
		Path:          "/api/principals/{id}/roles/{role}",
		Summary:       "Remove a single role from a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.removeRole)

	huma.Register(api, huma.Operation{
		OperationID:   "assignPrincipalApplicationAccess",
		Method:        http.MethodPut,
		Path:          "/api/principals/{id}/application-access",
		Summary:       "Assign application access to a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.assignApplicationAccess)

	huma.Register(api, huma.Operation{
		OperationID:   "listPrincipalApplicationAccess",
		Method:        http.MethodGet,
		Path:          "/api/principals/{id}/application-access",
		Summary:       "List application IDs the principal can access",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listApplicationAccess)

	huma.Register(api, huma.Operation{
		OperationID:   "listPrincipalAvailableApplications",
		Method:        http.MethodGet,
		Path:          "/api/principals/{id}/available-applications",
		Summary:       "List applications a principal can be granted access to",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listAvailableApplications)

	huma.Register(api, huma.Operation{
		OperationID:   "listPrincipalClientAccess",
		Method:        http.MethodGet,
		Path:          "/api/principals/{id}/client-access",
		Summary:       "List client-access grants for a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listClientAccess)

	huma.Register(api, huma.Operation{
		OperationID:   "grantPrincipalClientAccess",
		Method:        http.MethodPost,
		Path:          "/api/principals/{id}/client-access",
		Summary:       "Grant a client-access for a principal",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.grantClientAccess)

	huma.Register(api, huma.Operation{
		OperationID:   "revokePrincipalClientAccess",
		Method:        http.MethodDelete,
		Path:          "/api/principals/{id}/client-access/{clientId}",
		Summary:       "Revoke a client-access grant",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.revokeClientAccess)
}

// listInput carries the filter / sort / pagination query params for
// GET /api/principals. Every field is optional; an absent param means
// "no filter" (or the documented default). The SPA's UserListPage drives all
// of these.
type listInput struct {
	Type      string `query:"type" doc:"Filter by principal type (USER or SERVICE)"`
	ClientID  string `query:"clientId" doc:"Filter to principals homed at, or granted access to, this client"`
	Active    string `query:"active" doc:"Filter by active status (true/false); absent = both"`
	Q         string `query:"q" doc:"Case-insensitive substring search across name and email"`
	Roles     string `query:"roles" doc:"CSV of role names; matches principals holding any of them"`
	Page      int    `query:"page" doc:"0-based page index (default 0)"`
	PageSize  int    `query:"pageSize" doc:"Page size; <=0 returns all matches (default: all)"`
	SortField string `query:"sortField" doc:"Sort key: name | email | createdAt (default createdAt)"`
	SortOrder string `query:"sortOrder" doc:"Sort direction: asc | desc (default asc)"`
}

type listOutput struct {
	Body PrincipalListResponse
}

func (s *State) list(ctx context.Context, in *listInput) (*listOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadPrincipals(ac); err != nil {
		return nil, err
	}
	rows, err := s.Repo.FindAll(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_all failed", err)
	}

	// Normalise the filter inputs once, outside the row loop.
	q := strings.ToLower(strings.TrimSpace(in.Q))
	wantType := strings.ToUpper(strings.TrimSpace(in.Type))
	wantClient := strings.TrimSpace(in.ClientID)
	wantRoles := splitCSV(in.Roles)

	filtered := make([]*principal.Principal, 0, len(rows))
	for i := range rows {
		p := &rows[i]
		// Access control (1:1 with Rust list_principals): anchors see all;
		// non-anchors see only client-scoped principals they can access.
		// Platform-level principals (client_id == nil) are hidden from
		// non-anchors. (get-by-id stays lenient on the nil case, matching
		// Rust get_principal, which only checks access when client_id is set.)
		if !(ac.IsAnchor() || (p.ClientID != nil && ac.CanAccessClient(*p.ClientID))) {
			continue
		}
		if wantType != "" && string(p.Type) != wantType {
			continue
		}
		if in.Active == "true" && !p.Active {
			continue
		}
		if in.Active == "false" && p.Active {
			continue
		}
		if wantClient != "" && !principalMatchesClient(p, wantClient) {
			continue
		}
		if q != "" && !principalMatchesQuery(p, q) {
			continue
		}
		if len(wantRoles) > 0 && !principalHasAnyRole(p, wantRoles) {
			continue
		}
		filtered = append(filtered, p)
	}

	sortPrincipals(filtered, in.SortField, in.SortOrder)

	// Total is the full filtered count (pre-pagination) so the SPA paginator
	// can compute page counts correctly.
	total := len(filtered)
	filtered = paginate(filtered, in.Page, in.PageSize)

	out := make([]PrincipalResponse, 0, len(filtered))
	for _, p := range filtered {
		out = append(out, fromEntity(p))
	}
	return &listOutput{Body: PrincipalListResponse{Principals: out, Total: total}}, nil
}

// --- list filtering / sorting / pagination helpers ---

func principalEmail(p *principal.Principal) string {
	if p.UserIdentity != nil {
		return p.UserIdentity.Email
	}
	return ""
}

func principalMatchesQuery(p *principal.Principal, qLower string) bool {
	return strings.Contains(strings.ToLower(p.Name), qLower) ||
		strings.Contains(strings.ToLower(principalEmail(p)), qLower)
}

// principalMatchesClient matches a principal's home client OR any granted
// client, so the Client filter surfaces both client-homed and partner users
// who can reach that client.
func principalMatchesClient(p *principal.Principal, clientID string) bool {
	if p.ClientID != nil && *p.ClientID == clientID {
		return true
	}
	for _, c := range p.AssignedClients {
		if c == clientID {
			return true
		}
	}
	return false
}

// principalHasAnyRole reports whether the principal holds at least one of the
// requested role names (OR semantics, matching the multi-select filter UX).
func principalHasAnyRole(p *principal.Principal, want []string) bool {
	for _, r := range p.Roles {
		for _, w := range want {
			if r.Role == w {
				return true
			}
		}
	}
	return false
}

func splitCSV(s string) []string {
	out := make([]string, 0)
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// sortPrincipals orders in place. Unknown/empty field falls back to createdAt;
// any order other than "desc" is treated as ascending.
func sortPrincipals(ps []*principal.Principal, field, order string) {
	var less func(i, j int) bool
	switch field {
	case "name":
		less = func(i, j int) bool { return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name) }
	case "email":
		less = func(i, j int) bool {
			return strings.ToLower(principalEmail(ps[i])) < strings.ToLower(principalEmail(ps[j]))
		}
	default: // "createdAt" and any unknown key
		less = func(i, j int) bool { return ps[i].CreatedAt.Before(ps[j].CreatedAt) }
	}
	sort.SliceStable(ps, less)
	if strings.EqualFold(order, "desc") {
		for i, j := 0, len(ps)-1; i < j; i, j = i+1, j-1 {
			ps[i], ps[j] = ps[j], ps[i]
		}
	}
}

// paginate returns the 0-based page slice. pageSize <= 0 returns everything.
func paginate(ps []*principal.Principal, page, pageSize int) []*principal.Principal {
	if pageSize <= 0 {
		return ps
	}
	if page < 0 {
		page = 0
	}
	start := page * pageSize
	if start > len(ps) {
		start = len(ps)
	}
	end := start + pageSize
	if end > len(ps) {
		end = len(ps)
	}
	return ps[start:end]
}

type getInput struct {
	ID string `path:"id"`
}

type getOutput struct {
	Body PrincipalResponse
}

func (s *State) getByID(ctx context.Context, in *getInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadPrincipals(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	if p.ClientID != nil && !ac.CanAccessClient(*p.ClientID) {
		return nil, httperror.Forbidden("No access to this principal")
	}
	return &getOutput{Body: fromEntity(p)}, nil
}

type createInput struct {
	Body CreatePrincipalRequest
}

type createOutput struct {
	Body apicommon.CreatedResponse
}

func (s *State) create(ctx context.Context, in *createInput) (*createOutput, error) {
	ac := auth.FromContext(ctx)
	// Creating principals is anchor-only (1:1 with Rust create_user's
	// require_anchor). The created principal's scope/clientId is taken from the
	// request body, so an anchor can create a user of any scope.
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	if in.Body.ClientID != nil && !ac.CanAccessClient(*in.Body.ClientID) {
		return nil, httperror.Forbidden("No access to client: " + *in.Body.ClientID)
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateUser(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().UserID}}, nil
}

type createUserInput struct {
	Body CreateUserRequest
}

// createUser ports Rust create_user (fc-platform principal/api.rs): anchor-only,
// derives scope + client association from the email domain (anchor-domain check
// + email-domain-mapping), then delegates to the shared CreateUser operation.
// Returns the full principal (the SDK reads it back). The SDK's
// enforcePasswordComplexity is accepted but not enforced (Go's create doesn't
// apply a complexity policy). Magic-link-on-passwordless-create is intentionally
// not ported — the SDK always supplies a password and the reset emailer isn't
// wired (matching Rust's unconfigured-emailer fallback).
func (s *State) createUser(ctx context.Context, in *createUserInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(in.Body.Email))
	at := strings.IndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return nil, httperror.BadRequest("INVALID_EMAIL", "Invalid email format")
	}
	domain := email[at+1:]

	isAnchorDomain := false
	if s.AnchorDomains != nil {
		ad, err := s.AnchorDomains.FindByDomain(ctx, domain)
		if err != nil {
			return nil, usecase.Internal("REPO", "anchor_domain lookup failed", err)
		}
		isAnchorDomain = ad != nil
	}

	var mapping *emaildomainmapping.EmailDomainMapping
	if s.Mappings != nil {
		m, err := s.Mappings.FindByEmailDomain(ctx, domain)
		if err != nil {
			return nil, usecase.Internal("REPO", "email_domain_mapping lookup failed", err)
		}
		mapping = m
	}

	// Resolve IdP type (INTERNAL / OIDC). Unmapped domains default to INTERNAL.
	idpType := "INTERNAL"
	if mapping != nil && s.IdentityProviders != nil {
		if idp, _ := s.IdentityProviders.FindByID(ctx, mapping.IdentityProviderID); idp != nil {
			idpType = string(idp.Type)
		}
	}

	scope, clientID, err := deriveUserScope(isAnchorDomain, mapping, in.Body.ClientID)
	if err != nil {
		return nil, err
	}

	ec := usecase.NewExecutionContext(ac.PrincipalID)

	// Partner-merge: when a PARTNER user already exists for this email, grant
	// access to the requested client rather than recreating (keeps events +
	// audit accurate). Mirrors Rust's GrantClientAccess branch.
	if scope == "PARTNER" {
		existing, ferr := s.Repo.FindByEmail(ctx, email)
		if ferr != nil {
			return nil, usecase.Internal("REPO", "find_by_email failed", ferr)
		}
		if existing != nil {
			if existing.ClientID != nil && clientID != nil && *existing.ClientID == *clientID {
				return nil, usecase.Conflict("EMAIL_EXISTS", "User with email '"+email+"' already exists")
			}
			if _, gerr := operations.GrantClientAccess(ctx, s.Repo, s.Clients, s.GrantRepo, s.UoW,
				operations.GrantClientAccessCommand{UserID: existing.ID, ClientID: derefStr(clientID)}, ec); gerr != nil {
				return nil, gerr
			}
			refreshed, rerr := s.Repo.FindByID(ctx, existing.ID)
			if rerr != nil || refreshed == nil {
				return nil, httperror.NotFound("Principal", existing.ID)
			}
			return &getOutput{Body: fromEntity(refreshed)}, nil
		}
	}

	name := in.Body.Name
	committed, err := operations.CreateUser(ctx, s.Repo, s.UoW, operations.CreateCommand{
		Email:    email,
		Name:     &name,
		Scope:    scope,
		ClientID: clientID,
		Password: in.Body.Password,
		IDPType:  &idpType,
	}, ec)
	if err != nil {
		return nil, err
	}

	// New PARTNER user: grant the requested client (parity with Rust's
	// granted_client_ids = [clientId]).
	if scope == "PARTNER" && clientID != nil {
		if _, gerr := operations.GrantClientAccess(ctx, s.Repo, s.Clients, s.GrantRepo, s.UoW,
			operations.GrantClientAccessCommand{UserID: committed.Event().UserID, ClientID: *clientID}, ec); gerr != nil {
			return nil, gerr
		}
	}

	created, err := s.Repo.FindByID(ctx, committed.Event().UserID)
	if err != nil || created == nil {
		return nil, httperror.NotFound("Principal", committed.Event().UserID)
	}
	return &getOutput{Body: fromEntity(created)}, nil
}

// deriveUserScope resolves (scope, home-client) from the email domain, mirroring
// Rust create_user. An anchor domain (or an ANCHOR mapping) → ANCHOR with no
// client. A PARTNER mapping requires a clientId allowed by the mapping. A CLIENT
// mapping uses the request's clientId or the mapping's primary. An unmapped
// domain → CLIENT with the request's clientId verbatim. Pure + unit-tested.
func deriveUserScope(isAnchorDomain bool, mapping *emaildomainmapping.EmailDomainMapping, reqClientID *string) (string, *string, error) {
	if isAnchorDomain {
		return "ANCHOR", nil, nil
	}
	if mapping == nil {
		return "CLIENT", reqClientID, nil
	}
	switch mapping.ScopeType {
	case emaildomainmapping.ScopeAnchor:
		return "ANCHOR", nil, nil
	case emaildomainmapping.ScopePartner:
		if reqClientID == nil || *reqClientID == "" {
			return "", nil, usecase.Validation("CLIENT_REQUIRED", "clientId is required for partner users")
		}
		allowed := (mapping.PrimaryClientID != nil && *mapping.PrimaryClientID == *reqClientID)
		for _, c := range mapping.GrantedClientIDs {
			if c == *reqClientID {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", nil, usecase.Validation("CLIENT_NOT_ALLOWED",
				"clientId "+*reqClientID+" is not allowed for partner domain "+mapping.EmailDomain)
		}
		return "PARTNER", reqClientID, nil
	case emaildomainmapping.ScopeClient:
		primary := reqClientID
		if primary == nil {
			primary = mapping.PrimaryClientID
		}
		return "CLIENT", primary, nil
	default:
		return "ANCHOR", nil, nil
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// requireScopeByID loads the principal and enforces per-resource scope (A2) on
// top of the coarse permission already checked: a non-anchor principal must not
// mutate another tenant's principal by id. (Rust additionally gates scope/
// client_id *changes* to anchors; the Go UpdatePrincipalRequest deliberately
// doesn't expose scope/client_id at all, so that escalation vector can't exist
// here — no extra gate needed.)
func (s *State) requireScopeByID(ctx context.Context, ac *auth.AuthContext, id string) error {
	p, err := s.Repo.FindByID(ctx, id)
	if err != nil {
		return usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return httperror.NotFound("Principal", id)
	}
	return auth.CheckScopeAccess(ac, p.ClientID)
}

type updateInput struct {
	ID   string `path:"id"`
	Body UpdatePrincipalRequest
}

type emptyOutput struct{}

func (s *State) update(ctx context.Context, in *updateInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePrincipals(ac); err != nil {
		return nil, err
	}
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.UpdateUser(ctx, s.Repo, s.UoW, in.Body.toCommand(in.ID), ec); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	return &getOutput{Body: fromEntity(p)}, nil
}

type idInput struct {
	ID string `path:"id"`
}

func (s *State) activate(ctx context.Context, in *idInput) (*statusMessageOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePrincipals(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ActivateUser(ctx, s.Repo, s.UoW, operations.ActivateCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &statusMessageOutput{Body: apicommon.StatusChangeResponse{Message: "Principal activated"}}, nil
}

func (s *State) deactivate(ctx context.Context, in *idInput) (*statusMessageOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePrincipals(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeactivateUser(ctx, s.Repo, s.UoW, operations.DeactivateCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &statusMessageOutput{Body: apicommon.StatusChangeResponse{Message: "Principal deactivated"}}, nil
}

type resetPasswordInput struct {
	ID   string `path:"id"`
	Body ResetPasswordRequest
}

func (s *State) resetPassword(ctx context.Context, in *resetPasswordInput) (*statusMessageOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanWritePrincipals(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.ResetPassword(ctx, s.Repo, s.UoW,
		operations.ResetPasswordCommand{
			ID:                        in.ID,
			NewPassword:               in.Body.NewPassword,
			EnforcePasswordComplexity: in.Body.EnforcePasswordComplexity,
		}, ec); err != nil {
		return nil, err
	}
	return &statusMessageOutput{Body: apicommon.StatusChangeResponse{Message: "Password reset successfully"}}, nil
}

func (s *State) delete(ctx context.Context, in *idInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanDeletePrincipals(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteUser(ctx, s.Repo, s.UoW, operations.DeleteCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

type assignRolesInput struct {
	ID   string `path:"id"`
	Body AssignPrincipalRolesRequest
}

type rolesAssignedOutput struct {
	Body RolesAssignedResponse
}

func (s *State) assignRoles(ctx context.Context, in *assignRolesInput) (*rolesAssignedOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	old := stringSet(roleNamesFrom(p.Roles))
	desired := stringSet(in.Body.Roles)
	added := setDifference(desired, old)
	removed := setDifference(old, desired)

	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.AssignRoles(ctx, s.Repo, s.Roles, s.UoW,
		operations.AssignRolesCommand{UserID: in.ID, Roles: in.Body.Roles}, ec); err != nil {
		return nil, err
	}
	refreshed, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if refreshed == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	return &rolesAssignedOutput{Body: RolesAssignedResponse{
		Roles:   roleAssignmentDTOs(in.ID, refreshed.Roles),
		Added:   added,
		Removed: removed,
	}}, nil
}

type assignAppAccessInput struct {
	ID   string `path:"id"`
	Body AssignApplicationAccessRequest
}

type setAppAccessOutput struct {
	Body SetApplicationAccessResponse
}

func (s *State) assignApplicationAccess(ctx context.Context, in *assignAppAccessInput) (*setAppAccessOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	old := stringSet(p.AccessibleApplicationIDs)
	desired := stringSet(in.Body.ApplicationIDs)
	added := len(setDifference(desired, old))
	removed := len(setDifference(old, desired))

	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.AssignApplicationAccess(ctx, s.Repo, s.Applications, s.UoW,
		operations.AssignApplicationAccessCommand{UserID: in.ID, ApplicationIDs: in.Body.ApplicationIDs}, ec); err != nil {
		return nil, err
	}
	apps, err := s.resolveApplications(ctx, in.Body.ApplicationIDs)
	if err != nil {
		return nil, err
	}
	return &setAppAccessOutput{Body: SetApplicationAccessResponse{
		Applications: apps,
		Added:        added,
		Removed:      removed,
	}}, nil
}

type listClientAccessOutput struct {
	Body ClientAccessGrantListResponse
}

func (s *State) listClientAccess(ctx context.Context, in *idInput) (*listClientAccessOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	grants, err := s.GrantRepo.FindByPrincipal(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "list grants failed", err)
	}
	out := make([]ClientAccessGrantResponse, 0, len(grants))
	for i := range grants {
		out = append(out, clientAccessGrantFromEntity(&grants[i]))
	}
	return &listClientAccessOutput{Body: ClientAccessGrantListResponse{Grants: out}}, nil
}

type grantClientAccessInput struct {
	ID   string `path:"id"`
	Body GrantClientAccessRequest
}

type clientAccessGrantOutput struct {
	Body ClientAccessGrantResponse
}

func (s *State) grantClientAccess(ctx context.Context, in *grantClientAccessInput) (*clientAccessGrantOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.GrantClientAccess(ctx, s.Repo, s.Clients, s.GrantRepo, s.UoW,
		operations.GrantClientAccessCommand{UserID: in.ID, ClientID: in.Body.ClientID}, ec); err != nil {
		return nil, err
	}
	g, err := s.GrantRepo.FindByPrincipalAndClient(ctx, in.ID, in.Body.ClientID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find grant failed", err)
	}
	if g == nil {
		return nil, usecase.Internal("REPO", "grant not found after create", nil)
	}
	return &clientAccessGrantOutput{Body: clientAccessGrantFromEntity(g)}, nil
}

type revokeClientAccessInput struct {
	ID       string `path:"id"`
	ClientID string `path:"clientId"`
}

func (s *State) revokeClientAccess(ctx context.Context, in *revokeClientAccessInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.RevokeClientAccess(ctx, s.Repo, s.GrantRepo, s.UoW,
		operations.RevokeClientAccessCommand{UserID: in.ID, ClientID: in.ClientID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

// ── send-password-reset (admin trigger) ──────────────────────────────────

type statusMessageOutput struct {
	Body apicommon.StatusChangeResponse
}

func (s *State) sendPasswordReset(ctx context.Context, in *idInput) (*statusMessageOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if err := operations.SendPasswordReset(ctx, s.Repo, s.PasswordEmailer,
		operations.SendPasswordResetCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &statusMessageOutput{Body: apicommon.StatusChangeResponse{Message: "Password reset email sent"}}, nil
}

// ── check-email-domain (admin) ───────────────────────────────────────────

type checkEmailDomainInput struct {
	Email string `query:"email"`
}

type checkEmailDomainOutput struct {
	Body CheckEmailDomainResponse
}

func (s *State) checkEmailDomain(ctx context.Context, in *checkEmailDomainInput) (*checkEmailDomainOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadPrincipals(ac); err != nil {
		return nil, err
	}
	email := strings.TrimSpace(in.Email)
	if email == "" {
		return nil, httperror.BadRequest("EMAIL_REQUIRED", "email query param is required")
	}
	at := strings.IndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		// Malformed — soft-fall-back to internal so the UI shows a password
		// prompt rather than leaking that the domain is unparseable.
		return &checkEmailDomainOutput{Body: CheckEmailDomainResponse{AuthMethod: "internal"}}, nil
	}
	domain := strings.ToLower(email[at+1:])

	// Resolve the domain → email-domain-mapping → identity-provider chain.
	// Any miss (no mapping, no IDP, internal IDP, repos not wired) collapses
	// to "internal" so the UI doesn't leak whether a domain has OIDC.
	if s.Mappings == nil || s.IdentityProviders == nil {
		return &checkEmailDomainOutput{Body: CheckEmailDomainResponse{AuthMethod: "internal"}}, nil
	}
	edm, err := s.Mappings.FindByEmailDomain(ctx, domain)
	if err != nil || edm == nil {
		return &checkEmailDomainOutput{Body: CheckEmailDomainResponse{AuthMethod: "internal"}}, nil
	}
	idp, err := s.IdentityProviders.FindByID(ctx, edm.IdentityProviderID)
	if err != nil || idp == nil || idp.Type != identityprovider.TypeOIDC {
		return &checkEmailDomainOutput{Body: CheckEmailDomainResponse{AuthMethod: "internal"}}, nil
	}
	resp := CheckEmailDomainResponse{
		AuthMethod: "external",
		LoginURL:   "/auth/oidc/login?domain=" + url.QueryEscape(domain),
	}
	if idp.OIDCIssuerURL != nil {
		resp.IDPIssuer = *idp.OIDCIssuerURL
	}
	return &checkEmailDomainOutput{Body: resp}, nil
}

// ── granular role endpoints ──────────────────────────────────────────────

type listRolesOutput struct {
	Body PrincipalRoleListResponse
}

func (s *State) listRoles(ctx context.Context, in *idInput) (*listRolesOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadPrincipals(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	return &listRolesOutput{Body: PrincipalRoleListResponse{Roles: roleAssignmentDTOs(in.ID, p.Roles)}}, nil
}

type addRoleInput struct {
	ID   string `path:"id"`
	Body AddRoleRequest
}

func (s *State) addRole(ctx context.Context, in *addRoleInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	roles := uniqueRoleNames(p.Roles)
	if _, ok := roles[in.Body.Role]; !ok { // skip mutation when already present (idempotent)
		desired := append(roleNamesFrom(p.Roles), in.Body.Role)
		ec := usecase.NewExecutionContext(ac.PrincipalID)
		if _, err := operations.AssignRoles(ctx, s.Repo, s.Roles, s.UoW,
			operations.AssignRolesCommand{UserID: in.ID, Roles: desired}, ec); err != nil {
			return nil, err
		}
		if p, err = s.Repo.FindByID(ctx, in.ID); err != nil {
			return nil, usecase.Internal("REPO", "find_by_id failed", err)
		} else if p == nil {
			return nil, httperror.NotFound("Principal", in.ID)
		}
	}
	// Return the updated principal (1:1 with Rust assign_role → PrincipalResponse).
	return &getOutput{Body: fromEntity(p)}, nil
}

type removeRoleInput struct {
	ID   string `path:"id"`
	Role string `path:"role"`
}

func (s *State) removeRole(ctx context.Context, in *removeRoleInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	current := roleNamesFrom(p.Roles)
	desired := make([]string, 0, len(current))
	found := false
	for _, r := range current {
		if r == in.Role {
			found = true
			continue
		}
		desired = append(desired, r)
	}
	if found { // skip mutation when absent (idempotent)
		ec := usecase.NewExecutionContext(ac.PrincipalID)
		if _, err := operations.AssignRoles(ctx, s.Repo, s.Roles, s.UoW,
			operations.AssignRolesCommand{UserID: in.ID, Roles: desired}, ec); err != nil {
			return nil, err
		}
		if p, err = s.Repo.FindByID(ctx, in.ID); err != nil {
			return nil, usecase.Internal("REPO", "find_by_id failed", err)
		} else if p == nil {
			return nil, httperror.NotFound("Principal", in.ID)
		}
	}
	// Return the updated principal (1:1 with Rust remove_role → PrincipalResponse).
	return &getOutput{Body: fromEntity(p)}, nil
}

func roleNamesFrom(rs []serviceaccount.RoleAssignment) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Role)
	}
	return out
}

func uniqueRoleNames(rs []serviceaccount.RoleAssignment) map[string]struct{} {
	out := make(map[string]struct{}, len(rs))
	for _, r := range rs {
		out[r.Role] = struct{}{}
	}
	return out
}

func stringSet(vs []string) map[string]struct{} {
	out := make(map[string]struct{}, len(vs))
	for _, v := range vs {
		out[v] = struct{}{}
	}
	return out
}

// setDifference returns members of a not present in b (unordered).
func setDifference(a, b map[string]struct{}) []string {
	out := make([]string, 0)
	for v := range a {
		if _, ok := b[v]; !ok {
			out = append(out, v)
		}
	}
	return out
}

// ── application-access listing ───────────────────────────────────────────

type listApplicationAccessOutput struct {
	Body ApplicationAccessListResponse
}

func (s *State) listApplicationAccess(ctx context.Context, in *idInput) (*listApplicationAccessOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadPrincipals(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	apps, err := s.resolveApplications(ctx, p.AccessibleApplicationIDs)
	if err != nil {
		return nil, err
	}
	return &listApplicationAccessOutput{Body: ApplicationAccessListResponse{
		Applications: apps,
		Total:        len(apps),
	}}, nil
}

// resolveApplications hydrates application IDs into {id, code, name} rows,
// skipping IDs that no longer resolve (matching Rust's lenient behaviour).
func (s *State) resolveApplications(ctx context.Context, ids []string) ([]ApplicationAccessResponse, error) {
	out := make([]ApplicationAccessResponse, 0, len(ids))
	for _, id := range ids {
		a, err := s.Applications.FindByID(ctx, id)
		if err != nil {
			return nil, usecase.Internal("REPO", "find_app_by_id failed", err)
		}
		if a == nil {
			continue
		}
		out = append(out, ApplicationAccessResponse{
			ApplicationID:   a.ID,
			ApplicationCode: a.Code,
			ApplicationName: a.Name,
		})
	}
	return out, nil
}

type listAvailableAppsOutput struct {
	Body PrincipalAvailableApplicationsResponse
}

func (s *State) listAvailableApplications(ctx context.Context, in *idInput) (*listAvailableAppsOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.CanReadPrincipals(ac); err != nil {
		return nil, err
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	// Available = all active applications the system knows about. Rust
	// filters by what's already enabled in the principal's clients;
	// Go matches the simpler "all active" pending product confirmation.
	apps, err := s.Applications.FindActive(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_active_apps failed", err)
	}
	out := make([]PrincipalAvailableApplication, 0, len(apps))
	for i := range apps {
		a := &apps[i]
		out = append(out, PrincipalAvailableApplication{
			ID:   a.ID,
			Code: a.Code,
			Name: a.Name,
		})
	}
	return &listAvailableAppsOutput{Body: PrincipalAvailableApplicationsResponse{
		Applications: out,
	}}, nil
}
