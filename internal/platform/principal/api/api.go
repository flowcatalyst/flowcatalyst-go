// Package api wires the HTTP routes for the principal subdomain via huma.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/audit"
	platformauth "github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/client"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/mfa"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/notify"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
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
	// ClientConfigs resolves which applications a client is entitled to (its
	// enabled client-configs). It bounds what a non-anchor (client-admin) may
	// assign: a client-admin can grant a user ANY application the client can
	// access, not merely the apps the admin personally holds.
	ClientConfigs *application.ClientConfigRepo
	Clients       *client.Repository
	Mappings          *emaildomainmapping.Repository // for /check-email-domain + create-user scope derivation
	IdentityProviders *identityprovider.Repository   // for /check-email-domain + create-user idp-type
	AnchorDomains     *platformauth.AnchorDomainRepo // for create-user anchor-domain check (optional)
	UoW               *usecasepgx.UnitOfWork
	PasswordEmailer   operations.PasswordResetEmailer // optional; gates /send-password-reset

	// InviteEmailer (optional) sends a "set your password" link to a newly
	// created internal user that has no password yet. Notifier (optional) sends
	// the "your account was created" welcome to users created WITH a password.
	InviteEmailer InviteEmailer
	Notifier      *notify.Notifier
	// MFA (optional) backs POST /api/principals/{id}/reset-2fa.
	MFA *mfa.Service
	// Audit (optional) records the admin 2FA reset to the audit trail.
	Audit *audit.Repository
}

// InviteEmailer mints a first-time set-password link for a new user. The
// passwordreset principalEmailer satisfies it.
type InviteEmailer interface {
	SendInvite(ctx context.Context, p *principal.Principal) error
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
		OperationID:   "bulkImportUsers",
		Method:        http.MethodPost,
		Path:          "/api/principals/bulk-import",
		Summary:       "Bulk-import CLIENT users for a client (CSV onboarding)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.bulkImport)

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
		OperationID:   "resetPrincipalTwoFactor",
		Method:        http.MethodPost,
		Path:          "/api/principals/{id}/reset-2fa",
		Summary:       "Clear a user's two-factor methods (forces re-enrollment)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.resetTwoFactor)

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

	huma.Register(api, huma.Operation{
		OperationID:   "setPrincipalClientAssociation",
		Method:        http.MethodPut,
		Path:          "/api/principals/{id}/client-association",
		Summary:       "Change a principal's scope/client association (anchor-gated)",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.setClientAssociation)
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
		if !ac.IsAnchor() && (p.ClientID == nil || !ac.CanAccessClient(*p.ClientID)) {
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
	// Anchors create any scope/client. A non-anchor administrator
	// (client-admin) may only create CLIENT-scope users in a client they can
	// access — never ANCHOR/PARTNER users, and never a clientless principal.
	if !ac.IsAnchor() && in.Body.Scope != "CLIENT" {
		return nil, httperror.Forbidden("Client administrators can only create client-scope users")
	}
	if err := auth.RequireUserAdmin(ac, in.Body.ClientID); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.CreateUser(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	if created, ferr := s.Repo.FindByID(ctx, committed.Event().UserID); ferr == nil && created != nil {
		s.notifyNewUser(ctx, created, in.Body.Password)
	}
	return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().UserID}}, nil
}

type bulkImportInput struct {
	Body BulkImportRequest
}

type bulkImportOutput struct {
	Body BulkImportResponse
}

// bulkImport onboards a list of CLIENT users under one client (CSV import).
// Each missing user is created (passwordless → invite email) with its listed
// roles; existing users are skipped. Roles are validated against the client's
// applications for a non-anchor administrator — exactly like single role
// assignment — so a client-admin can't grant a role the client isn't entitled to.
func (s *State) bulkImport(ctx context.Context, in *bulkImportInput) (*bulkImportOutput, error) {
	ac := auth.FromContext(ctx)
	clientID := strings.TrimSpace(in.Body.ClientID)
	if clientID == "" {
		return nil, httperror.BadRequest("CLIENT_REQUIRED", "A target client is required")
	}
	// Non-anchor administrators create only CLIENT-scope users in a client they
	// can access; RequireUserAdmin enforces both.
	if err := auth.RequireUserAdmin(ac, &clientID); err != nil {
		return nil, err
	}
	if len(in.Body.Users) == 0 {
		return nil, httperror.BadRequest("NO_ROWS", "No users to import")
	}
	if len(in.Body.Users) > 1000 {
		return nil, httperror.BadRequest("TOO_MANY", "Import is limited to 1000 users at a time")
	}

	ec := usecase.NewExecutionContext(ac.PrincipalID)
	out := BulkImportResponse{Results: make([]BulkImportResult, 0, len(in.Body.Users))}
	seen := make(map[string]struct{}, len(in.Body.Users))

	for i, u := range in.Body.Users {
		email := strings.ToLower(strings.TrimSpace(u.Email))
		status, msg := s.importRow(ctx, ac, ec, clientID, strings.TrimSpace(u.Name), email, cleanRoles(u.Roles), seen)
		switch status {
		case "created":
			out.Created++
		case "exists":
			out.Skipped++
		default:
			out.Failed++
		}
		out.Results = append(out.Results, BulkImportResult{Row: i + 1, Email: email, Status: status, Message: msg})
	}
	return &bulkImportOutput{Body: out}, nil
}

// importRow processes one CSV row and returns its outcome status
// ("created" | "exists" | "error") plus a human message.
func (s *State) importRow(ctx context.Context, ac *auth.AuthContext, ec usecase.ExecutionContext, clientID, name, email string, roles []string, seen map[string]struct{}) (string, string) {
	if email == "" || !strings.Contains(email, "@") {
		return "error", "invalid email address"
	}
	if name == "" {
		return "error", "name is required"
	}
	if _, dup := seen[email]; dup {
		return "error", "duplicate email in file"
	}
	seen[email] = struct{}{}

	// Validate the row's roles are available to the client (non-anchor admins).
	if !ac.IsAnchor() {
		allowed, err := s.clientAppIDs(ctx, clientID)
		if err != nil {
			return "error", errMessage(err)
		}
		if err := s.assertAssignableRoles(ctx, roles, allowed); err != nil {
			return "error", errMessage(err)
		}
	}
	// Only create missing users; an existing email is skipped untouched.
	existing, err := s.Repo.FindByEmail(ctx, email)
	if err != nil {
		return "error", "lookup failed"
	}
	if existing != nil {
		return "exists", "already exists — skipped"
	}
	// Create the CLIENT user with no password → notifyNewUser sends the invite
	// ("set your password") so they can onboard.
	cid := clientID
	uname := name
	committed, err := operations.CreateUser(ctx, s.Repo, s.UoW, operations.CreateCommand{
		Email: email, Name: &uname, Scope: "CLIENT", ClientID: &cid,
	}, ec)
	if err != nil {
		return "error", errMessage(err)
	}
	userID := committed.Event().UserID
	if len(roles) > 0 {
		if _, err := operations.AssignRoles(ctx, s.Repo, s.Roles, s.UoW,
			operations.AssignRolesCommand{UserID: userID, Roles: roles}, ec); err != nil {
			return "created", "created, but roles not applied: " + errMessage(err)
		}
	}
	if created, gerr := s.Repo.FindByID(ctx, userID); gerr == nil && created != nil {
		s.notifyNewUser(ctx, created, nil)
	}
	return "created", ""
}

// cleanRoles trims, drops blanks, and dedupes the role names from a CSV cell.
func cleanRoles(roles []string) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if t := strings.TrimSpace(r); t != "" {
			out = append(out, t)
		}
	}
	return dedupeStrings(out)
}

// errMessage extracts a user-facing message from a usecase error (or falls back
// to the raw error text).
func errMessage(err error) string {
	var ue *usecase.Error
	if errors.As(err, &ue) {
		return ue.Message
	}
	if err != nil {
		return err.Error()
	}
	return ""
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
	// Authorization is enforced after scope derivation below (a client-admin may
	// only create CLIENT-scope users in a client they can access).
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

	// Anchors create any scope/client. A non-anchor administrator (client-admin)
	// may only create CLIENT-scope users in a client they can access.
	if !ac.IsAnchor() && scope != "CLIENT" {
		return nil, httperror.Forbidden("Client administrators can only create client-scope users")
	}
	if err := auth.RequireUserAdmin(ac, clientID); err != nil {
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
	s.notifyNewUser(ctx, created, in.Body.Password)
	return &getOutput{Body: fromEntity(created)}, nil
}

// notifyNewUser emails a freshly-created INTERNAL user (best-effort): a
// passwordless account gets a "set your password" invite (which carries the
// invite token + the 2FA enrollment hand-off); an account created WITH a
// password gets a plain "account created" welcome (2FA, if required, is then
// enforced at first sign-in). Federated/OIDC users get nothing.
func (s *State) notifyNewUser(ctx context.Context, p *principal.Principal, password *string) {
	if p == nil || p.UserIdentity == nil {
		return // service account
	}
	// Skip federated/OIDC users — they manage credentials at their IdP. NB:
	// the repo loads UserIdentity.Provider from idp_type for ALL users (so it's
	// "INTERNAL" for internal users, not nil) — federated must be detected via
	// ExternalIdentity, or Provider=="OIDC" for an OIDC user created before its
	// first callback populates ExternalIdentity.
	if p.ExternalIdentity != nil ||
		(p.UserIdentity.Provider != nil && *p.UserIdentity.Provider == "OIDC") {
		return
	}
	emailAddr := strings.TrimSpace(p.UserIdentity.Email)
	if emailAddr == "" {
		return
	}
	if (password == nil || *password == "") && s.InviteEmailer != nil {
		if err := s.InviteEmailer.SendInvite(ctx, p); err != nil {
			slog.Warn("send account invite failed", "principal", p.ID, "err", err)
		}
		return
	}
	s.Notifier.AccountCreated(ctx, emailAddr)
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
	if err := blockNonClientTarget(ac, p); err != nil {
		return err
	}
	return auth.CheckScopeAccess(ac, p.ClientID)
}

// blockNonClientTarget stops a non-anchor administrator (client-admin) from
// acting on an ANCHOR- or PARTNER-scoped principal. Anchors are unrestricted.
// Client access alone is not enough: a PARTNER user's home client may be one the
// admin can reach, but partner/anchor users are out of a client-admin's remit —
// they manage only CLIENT-scope users. Pairs with CanAccessClient/CheckScopeAccess,
// which bound *which* client; this bounds *which kind* of user.
func blockNonClientTarget(ac *auth.AuthContext, p *principal.Principal) error {
	if ac != nil && !ac.IsAnchor() && p != nil && p.Scope != principal.ScopeClient {
		return httperror.Forbidden("Client administrators can only manage client-scope users")
	}
	return nil
}

// assertAssignableRoles bounds a non-anchor (client-admin) role assignment:
// every role must be application-scoped (ApplicationID set — i.e. NOT a platform
// role) and belong to an application the TARGET CLIENT can access (allowed). A
// client-admin can therefore grant any role for any application the client is
// entitled to — not just the apps the admin personally holds. This still stops
// granting platform roles (escalation) or app roles the client isn't entitled
// to. Anchors are not subject to this (callers skip it).
func (s *State) assertAssignableRoles(ctx context.Context, roleNames []string, allowed map[string]bool) error {
	for _, name := range roleNames {
		r, err := s.Roles.FindByName(ctx, name)
		if err != nil {
			return usecase.Internal("REPO", "find_role failed", err)
		}
		if r == nil {
			return usecase.Validation("UNKNOWN_ROLE", "role not found: "+name)
		}
		if r.ApplicationID == nil {
			return usecase.Authorization("PLATFORM_ROLE_FORBIDDEN",
				"client administrators cannot assign platform roles")
		}
		if !allowed[*r.ApplicationID] {
			return usecase.Authorization("ROLE_APP_FORBIDDEN",
				"role belongs to an application the client cannot access")
		}
	}
	return nil
}

// clientAppIDs returns the set of application IDs the given client is entitled
// to — the applications it has an enabled client-config for. This is the bound
// a non-anchor (client-admin) is held to when assigning roles/applications.
func (s *State) clientAppIDs(ctx context.Context, clientID string) (map[string]bool, error) {
	allowed := map[string]bool{}
	if clientID == "" || s.ClientConfigs == nil {
		return allowed, nil
	}
	cfgs, err := s.ClientConfigs.FindByClient(ctx, clientID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_client_configs failed", err)
	}
	for _, c := range cfgs {
		if c.Enabled {
			allowed[c.ApplicationID] = true
		}
	}
	return allowed, nil
}

// clientIDOf returns a principal's client id, or "" when it has none.
func clientIDOf(p *principal.Principal) string {
	if p == nil || p.ClientID == nil {
		return ""
	}
	return *p.ClientID
}

// assertAssignableApplications bounds a non-anchor (client-admin) application
// grant: every application in the requested set must be one the TARGET CLIENT
// can access (allowed). This stops a client-admin from granting a user access to
// an application the client isn't entitled to. Anchors are not subject to this.
func (s *State) assertAssignableApplications(appIDs []string, allowed map[string]bool) error {
	for _, id := range appIDs {
		if !allowed[id] {
			return usecase.Authorization("APP_FORBIDDEN",
				"application the client cannot access: "+id)
		}
	}
	return nil
}

// preservedApplications returns the subset of a user's existing application
// grants that a non-anchor admin may NOT manage (apps outside the CLIENT's
// reach). A client-admin's SET preserves these so it can't strip a user's access
// to applications the client itself can't see — mirrors protectedRoleNames.
func preservedApplications(existing []string, allowed map[string]bool) []string {
	var out []string
	for _, id := range existing {
		if !allowed[id] {
			out = append(out, id)
		}
	}
	return out
}

// protectedRoleNames returns the subset of roleNames a non-anchor admin may NOT
// manage — platform roles (ApplicationID nil), unknown roles, or roles for apps
// the TARGET CLIENT can't access (allowed). A client-admin's role SET preserves
// these so it can't strip a user's platform / other-application roles.
func (s *State) protectedRoleNames(ctx context.Context, roleNames []string, allowed map[string]bool) ([]string, error) {
	var out []string
	for _, name := range roleNames {
		r, err := s.Roles.FindByName(ctx, name)
		if err != nil {
			return nil, usecase.Internal("REPO", "find_role failed", err)
		}
		if r == nil || r.ApplicationID == nil || !allowed[*r.ApplicationID] {
			out = append(out, name)
		}
	}
	return out, nil
}

func dedupeStrings(xs []string) []string {
	seen := make(map[string]struct{}, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
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
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
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
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
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
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
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
	if err := s.requireScopeByID(ctx, ac, in.ID); err != nil {
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
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	if err := auth.RequireUserAdmin(ac, p.ClientID); err != nil {
		return nil, err
	}
	if err := blockNonClientTarget(ac, p); err != nil {
		return nil, err
	}
	// A non-anchor administrator (client-admin) may only assign
	// application-scoped roles for applications their client can access —
	// never platform roles, never apps outside their reach. Because this is a
	// SET, we also preserve the user's existing protected roles so the client
	// admin can't strip a user's platform / other-application roles.
	effectiveRoles := in.Body.Roles
	if !ac.IsAnchor() {
		allowed, aerr := s.clientAppIDs(ctx, clientIDOf(p))
		if aerr != nil {
			return nil, aerr
		}
		if err := s.assertAssignableRoles(ctx, in.Body.Roles, allowed); err != nil {
			return nil, err
		}
		preserved, perr := s.protectedRoleNames(ctx, roleNamesFrom(p.Roles), allowed)
		if perr != nil {
			return nil, perr
		}
		effectiveRoles = dedupeStrings(append(append([]string{}, in.Body.Roles...), preserved...))
	}
	old := stringSet(roleNamesFrom(p.Roles))
	desired := stringSet(effectiveRoles)
	added := setDifference(desired, old)
	removed := setDifference(old, desired)

	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.AssignRoles(ctx, s.Repo, s.Roles, s.UoW,
		operations.AssignRolesCommand{UserID: in.ID, Roles: effectiveRoles}, ec); err != nil {
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
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	// Anchors manage any user's application access. A non-anchor administrator
	// (client-admin) may manage their own CLIENT users, bounded to applications
	// the client can access — they can't grant an app outside the client's reach
	// and can't strip an existing grant for an app outside it.
	if err := auth.RequireUserAdmin(ac, p.ClientID); err != nil {
		return nil, err
	}
	if err := blockNonClientTarget(ac, p); err != nil {
		return nil, err
	}
	desiredIDs := in.Body.ApplicationIDs
	if !ac.IsAnchor() {
		allowed, aerr := s.clientAppIDs(ctx, clientIDOf(p))
		if aerr != nil {
			return nil, aerr
		}
		if err := s.assertAssignableApplications(in.Body.ApplicationIDs, allowed); err != nil {
			return nil, err
		}
		preserved := preservedApplications(p.AccessibleApplicationIDs, allowed)
		desiredIDs = dedupeStrings(append(append([]string{}, in.Body.ApplicationIDs...), preserved...))
	}
	old := stringSet(p.AccessibleApplicationIDs)
	desired := stringSet(desiredIDs)
	added := len(setDifference(desired, old))
	removed := len(setDifference(old, desired))

	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.AssignApplicationAccess(ctx, s.Repo, s.Applications, s.UoW,
		operations.AssignApplicationAccessCommand{UserID: in.ID, ApplicationIDs: desiredIDs}, ec); err != nil {
		return nil, err
	}
	apps, err := s.resolveApplications(ctx, desiredIDs)
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

type setClientAssociationInput struct {
	ID   string `path:"id"`
	Body ClientAssociationRequest
}

// setClientAssociation changes a principal's scope + client association with
// explicit intent (anchor-gated). clientId "*" → ANCHOR; mode CHANGE_CLIENT →
// new home client; mode TO_PARTNER → promote to PARTNER (old + new client).
// Returns the updated principal.
func (s *State) setClientAssociation(ctx context.Context, in *setClientAssociationInput) (*getOutput, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	mode := ""
	if in.Body.Mode != nil {
		mode = strings.ToUpper(strings.TrimSpace(*in.Body.Mode))
	}
	if _, err := operations.SetClientAssociation(ctx, s.Repo, s.Clients, s.GrantRepo, s.UoW,
		operations.SetClientAssociationCommand{
			UserID:   in.ID,
			ClientID: in.Body.ClientID,
			Mode:     operations.ClientAssociationMode(mode),
		}, ec); err != nil {
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

// sendPasswordResetInput carries the path id plus an optional body asking to
// also reset the user's 2FA as part of the reset (lost-device recovery). Body
// is a POINTER so the common "just send the reset email" action can POST with
// no body at all: with a non-pointer Body, huma marks the request body
// required and rejects a body-less call with "request body is required".
type sendPasswordResetInput struct {
	ID   string `path:"id"`
	Body *struct {
		Reset2FA bool `json:"reset2fa,omitempty"`
	}
}

func (s *State) sendPasswordReset(ctx context.Context, in *sendPasswordResetInput) (*statusMessageOutput, error) {
	ac := auth.FromContext(ctx)
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	if err := auth.RequireUserAdmin(ac, p.ClientID); err != nil {
		return nil, err
	}
	if err := blockNonClientTarget(ac, p); err != nil {
		return nil, err
	}
	reset2FA := false
	if in.Body != nil {
		reset2FA = in.Body.Reset2FA
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if err := operations.SendPasswordReset(ctx, s.Repo, s.PasswordEmailer,
		operations.SendPasswordResetCommand{ID: in.ID, Reset2FA: reset2FA}, ec); err != nil {
		return nil, err
	}
	return &statusMessageOutput{Body: apicommon.StatusChangeResponse{Message: "Password reset email sent"}}, nil
}

// resetTwoFactor clears a user's enrolled 2FA (factors, recovery codes, pending
// PINs, trusted devices). Anchor or a client-administrator of the user's client.
// The user must re-enroll at next sign-in if their domain requires 2FA.
func (s *State) resetTwoFactor(ctx context.Context, in *idInput) (*statusMessageOutput, error) {
	ac := auth.FromContext(ctx)
	if s.MFA == nil {
		return nil, usecase.Internal("MFA_NOT_CONFIGURED", "Two-factor service not configured", nil)
	}
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	if err := auth.RequireUserAdmin(ac, p.ClientID); err != nil {
		return nil, err
	}
	if err := blockNonClientTarget(ac, p); err != nil {
		return nil, err
	}
	if !p.IsUser() {
		return nil, usecase.Validation("NOT_USER", "Two-factor reset only applies to user accounts")
	}
	if err := s.MFA.ResetAll(ctx, p.ID); err != nil {
		return nil, usecase.Internal("MFA", "reset failed", err)
	}
	if p.UserIdentity != nil {
		s.Notifier.TwoFactorReset(ctx, p.UserIdentity.Email)
	}
	if s.Audit != nil {
		actor := ac.PrincipalID
		_ = s.Audit.Insert(ctx, &audit.Log{
			ID:          tsid.Generate(tsid.AuditLog),
			EntityType:  "PRINCIPAL",
			EntityID:    p.ID,
			Operation:   "2FA_RESET_BY_ADMIN",
			PrincipalID: &actor,
			PerformedAt: time.Now().UTC(),
		})
	}
	return &statusMessageOutput{Body: apicommon.StatusChangeResponse{Message: "Two-factor authentication reset"}}, nil
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
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		return nil, httperror.BadRequest("EMAIL_REQUIRED", "email query param is required")
	}
	at := strings.IndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return nil, httperror.BadRequest("INVALID_EMAIL", "Invalid email format")
	}
	domain := email[at+1:]

	// Does a principal already exist for this exact address? Drives the SPA's
	// "email already exists" guard so the create form can't double-create.
	emailExists := false
	if s.Repo != nil {
		existing, err := s.Repo.FindByEmail(ctx, email)
		if err != nil {
			return nil, usecase.Internal("REPO", "find_by_email failed", err)
		}
		emailExists = existing != nil
	}

	// Resolve the domain → scope exactly the way createUser does, so the
	// preview matches what submit will actually do: an anchor domain → ANCHOR;
	// otherwise the email-domain-mapping's scope (or CLIENT when unmapped).
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

	// IdP type (INTERNAL / OIDC / SAML). Unmapped domains and missing IDPs
	// default to INTERNAL — the SPA shows a password field for those.
	idpType := "INTERNAL"
	idpIssuer := ""
	if mapping != nil && s.IdentityProviders != nil {
		if idp, _ := s.IdentityProviders.FindByID(ctx, mapping.IdentityProviderID); idp != nil {
			idpType = string(idp.Type)
			if idp.OIDCIssuerURL != nil {
				idpIssuer = *idp.OIDCIssuerURL
			}
		}
	}
	external := idpType == string(identityprovider.TypeOIDC)

	scope := deriveScopeForDomain(isAnchorDomain, mapping)
	resp := CheckEmailDomainResponse{
		AuthMethod:       "internal",
		Domain:           domain,
		AuthProvider:     idpType,
		IsAnchorDomain:   isAnchorDomain,
		HasIDPConfig:     external,
		EmailExists:      emailExists,
		DerivedScope:     scope,
		RequiresClientID: scope != "ANCHOR",
		AllowedClientIDs: allowedClientIDsForDomain(mapping),
	}
	if external {
		resp.AuthMethod = "external"
		resp.LoginURL = "/auth/oidc/login?domain=" + url.QueryEscape(domain)
		resp.IDPIssuer = idpIssuer
	}
	if emailExists {
		w := "A user with this email address already exists."
		resp.Warning = &w
	}
	return &checkEmailDomainOutput{Body: resp}, nil
}

// deriveScopeForDomain reports the scope a new user from this domain will get,
// independent of any chosen clientId (the check runs before the client is
// picked). Mirrors the scope branch of [deriveUserScope]: an anchor domain or
// an ANCHOR mapping → ANCHOR; a PARTNER mapping → PARTNER; a CLIENT mapping or
// an unmapped domain → CLIENT.
func deriveScopeForDomain(isAnchorDomain bool, mapping *emaildomainmapping.EmailDomainMapping) string {
	if isAnchorDomain {
		return "ANCHOR"
	}
	if mapping == nil {
		return "CLIENT"
	}
	switch mapping.ScopeType {
	case emaildomainmapping.ScopeAnchor:
		return "ANCHOR"
	case emaildomainmapping.ScopePartner:
		return "PARTNER"
	case emaildomainmapping.ScopeClient:
		return "CLIENT"
	default:
		return "ANCHOR"
	}
}

// allowedClientIDsForDomain returns the client IDs the create-user picker
// should be constrained to, or an empty slice when the domain imposes no
// restriction (the form then shows the full active-clients list). A PARTNER
// mapping allows the primary plus any granted clients; a CLIENT mapping allows
// just the primary (when set). Always non-nil so the field serializes as [].
func allowedClientIDsForDomain(mapping *emaildomainmapping.EmailDomainMapping) []string {
	out := []string{}
	if mapping == nil {
		return out
	}
	switch mapping.ScopeType {
	case emaildomainmapping.ScopePartner:
		seen := map[string]struct{}{}
		add := func(id string) {
			if id == "" {
				return
			}
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		if mapping.PrimaryClientID != nil {
			add(*mapping.PrimaryClientID)
		}
		for _, c := range mapping.GrantedClientIDs {
			add(c)
		}
	case emaildomainmapping.ScopeClient:
		if mapping.PrimaryClientID != nil && *mapping.PrimaryClientID != "" {
			out = append(out, *mapping.PrimaryClientID)
		}
	}
	return out
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
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	if err := auth.RequireUserAdmin(ac, p.ClientID); err != nil {
		return nil, err
	}
	if err := blockNonClientTarget(ac, p); err != nil {
		return nil, err
	}
	if !ac.IsAnchor() {
		allowed, aerr := s.clientAppIDs(ctx, clientIDOf(p))
		if aerr != nil {
			return nil, aerr
		}
		if err := s.assertAssignableRoles(ctx, []string{in.Body.Role}, allowed); err != nil {
			return nil, err
		}
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
	p, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return nil, httperror.NotFound("Principal", in.ID)
	}
	if err := auth.RequireUserAdmin(ac, p.ClientID); err != nil {
		return nil, err
	}
	if err := blockNonClientTarget(ac, p); err != nil {
		return nil, err
	}
	// A non-anchor admin may only remove roles they could also assign — so they
	// can't strip a user's platform / other-application roles.
	if !ac.IsAnchor() {
		allowed, aerr := s.clientAppIDs(ctx, clientIDOf(p))
		if aerr != nil {
			return nil, aerr
		}
		if err := s.assertAssignableRoles(ctx, []string{in.Role}, allowed); err != nil {
			return nil, err
		}
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
	// For a non-anchor administrator (client-admin) we bound the menu to the
	// applications the caller's client can access, so they can only grant what
	// the client is entitled to (mirrors the role-assignment bounding).
	apps, err := s.Applications.FindActive(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_active_apps failed", err)
	}
	// For a non-anchor admin, bound the menu to applications the target user's
	// CLIENT is entitled to — so a client-admin can grant any app the client can
	// access, not just the apps the admin personally holds.
	allowed := map[string]bool{}
	if !ac.IsAnchor() {
		allowed, err = s.clientAppIDs(ctx, clientIDOf(p))
		if err != nil {
			return nil, err
		}
	}
	out := make([]PrincipalAvailableApplication, 0, len(apps))
	for i := range apps {
		a := &apps[i]
		if !ac.IsAnchor() && !allowed[a.ID] {
			continue
		}
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
