//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	appops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/application/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/passwordhash"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/client"
	clientops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/client/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/role"
	roleops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/role/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// NOTE: the API layer's email-domain scope derivation (deriveUserScope) is not
// exercised here — the CreateUser OPERATION takes the scope verbatim; the
// derivation lives in principal/api and has its own unit tests
// (api/create_user_test.go).

func ptr[T any](v T) *T { return &v }

// mustCreateUser seeds a user through the public operation — the production
// path. Emails are hand-unique per test (unique index on iam_principals.email,
// and the fixture never truncates), so tests own their rows.
func mustCreateUser(t *testing.T, repo *principal.Repository, uow *usecasepgx.UnitOfWork, email, scope string, clientID *string) operations.UserCreated {
	t.Helper()
	committed, err := operations.CreateUser(context.Background(), repo, uow,
		operations.CreateCommand{Email: email, Scope: scope, ClientID: clientID}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// mustCreateRole seeds a role (name = "appCode:roleName") for the ops that
// validate role existence (assign_roles, sync_idp_roles).
func mustCreateRole(t *testing.T, uow *usecasepgx.UnitOfWork, appCode, roleName string) string {
	t.Helper()
	committed, err := roleops.CreateRole(context.Background(), role.NewRepository(testpg.Pool(t)), uow,
		roleops.CreateCommand{ApplicationCode: appCode, RoleName: roleName, DisplayName: appCode + " " + roleName},
		testpg.TestEC())
	require.NoError(t, err)
	return committed.Event().Name
}

// mustCreateClient seeds a tnt_clients row for the client-existence checks in
// grant_client_access / set_client_association.
func mustCreateClient(t *testing.T, uow *usecasepgx.UnitOfWork, name, identifier string) string {
	t.Helper()
	committed, err := clientops.CreateClient(context.Background(), client.NewRepository(testpg.Pool(t)), uow,
		clientops.CreateCommand{Name: name, Identifier: identifier}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event().ClientID
}

// mustCreateApplication seeds an (active-by-default) application for
// assign_application_access.
func mustCreateApplication(t *testing.T, uow *usecasepgx.UnitOfWork, code, name string) string {
	t.Helper()
	committed, err := appops.CreateApplication(context.Background(), application.NewRepository(testpg.Pool(t)), uow,
		appops.CreateCommand{Code: code, Name: name}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event().ApplicationID
}

// seedPrincipalDirect persists a hand-built principal row through the repo's
// bootstrap seam (externally-managed tx via WrapTxForBootstrap) — the same
// seam production uses for the linked SERVICE principal in
// serviceaccount/operations.CreateServiceAccountWithCredentials. Used only for
// row shapes no public principal operation can mint (SERVICE principals,
// OIDC-federated users, a USER with no email) so the guard-rail error paths
// are reachable.
func seedPrincipalDirect(t *testing.T, p *principal.Principal) {
	t.Helper()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	tx, err := testpg.Pool(t).Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, repo.Persist(ctx, p, usecasepgx.WrapTxForBootstrap(tx)))
	require.NoError(t, tx.Commit(ctx))
}

// seedServicePrincipal seeds a SERVICE-type principal for the NOT_A_USER /
// NOT_USER guards. saID is hand-unique (unique index on service_account_id;
// there is no FK, so no iam_service_accounts row is needed).
func seedServicePrincipal(t *testing.T, saID, name string) *principal.Principal {
	t.Helper()
	p := principal.NewService(saID, name)
	seedPrincipalDirect(t, p)
	return p
}

// roleSources maps role name → assignment source on a reloaded principal —
// order-insensitive (iam_principal_roles hydration orders by assigned_at,
// which ties within a single op's batch).
func roleSources(p *principal.Principal) map[string]string {
	out := make(map[string]string, len(p.Roles))
	for _, ra := range p.Roles {
		src := ""
		if ra.AssignmentSource != nil {
			src = *ra.AssignmentSource
		}
		out[ra.Role] = src
	}
	return out
}

// ── CreateUser ────────────────────────────────────────────────────────────

func TestCreateUser_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	// Full shape: mixed-case + padded email must normalise, name must trim,
	// password must land as a verifiable hash (never plaintext).
	committed, err := operations.CreateUser(ctx, repo, uow, operations.CreateCommand{
		Email:    "  PRN-Create-Happy@Example.COM  ",
		Name:     ptr("  Jane Doe  "),
		Scope:    "ANCHOR",
		Password: ptr("s3cret-pass!"),
	}, ec)
	require.NoError(t, err)
	ev := committed.Event()
	assert.NotEmpty(t, ev.UserID)
	assert.Equal(t, "prn-create-happy@example.com", ev.Email, "email is lower-cased + trimmed")

	got, err := repo.FindByID(ctx, ev.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, principal.TypeUser, got.Type)
	assert.Equal(t, principal.ScopeAnchor, got.Scope)
	assert.True(t, got.Active)
	assert.Equal(t, "Jane Doe", got.Name, "name is trimmed")
	assert.True(t, got.AllApplications, "new users default to all-applications access (326772d)")
	assert.Empty(t, got.Roles)
	require.NotNil(t, got.UserIdentity)
	assert.Equal(t, "prn-create-happy@example.com", got.UserIdentity.Email)
	require.NotNil(t, got.UserIdentity.Provider)
	assert.Equal(t, "INTERNAL", *got.UserIdentity.Provider, "users without an IDP default to INTERNAL")
	require.NotNil(t, got.UserIdentity.PasswordHash)
	assert.NoError(t, passwordhash.Verify("s3cret-pass!", *got.UserIdentity.PasswordHash))

	// Minimal shape: no name → name falls back to the email (DisplayName).
	minimal := mustCreateUser(t, repo, uow, "prn-create-min@example.com", "ANCHOR", nil)
	gotMin, err := repo.FindByID(ctx, minimal.UserID)
	require.NoError(t, err)
	require.NotNil(t, gotMin)
	assert.Equal(t, "prn-create-min@example.com", gotMin.Name)
	assert.Nil(t, gotMin.UserIdentity.PasswordHash)
}

func TestCreateUser_OIDCAndClientScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	// IDPType OIDC: password hash is discarded (IDP owns credentials) and the
	// provider is recorded.
	committed, err := operations.CreateUser(ctx, repo, uow, operations.CreateCommand{
		Email:    "prn-create-oidc@example.com",
		Scope:    "ANCHOR",
		Password: ptr("ignored-password1"),
		IDPType:  ptr("OIDC"),
	}, testpg.TestEC())
	require.NoError(t, err)
	got, err := repo.FindByID(ctx, committed.Event().UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.UserIdentity)
	assert.Nil(t, got.UserIdentity.PasswordHash, "OIDC users must not keep a local password hash")
	require.NotNil(t, got.UserIdentity.Provider)
	assert.Equal(t, "OIDC", *got.UserIdentity.Provider)

	// CLIENT scope stores the home client. Existence of the client is NOT
	// validated by the operation (source: create.go) — a dummy id suffices.
	clientID := "clt_homedummy01"
	ev := mustCreateUser(t, repo, uow, "prn-create-client@example.com", "CLIENT", &clientID)
	gotC, err := repo.FindByID(ctx, ev.UserID)
	require.NoError(t, err)
	require.NotNil(t, gotC)
	assert.Equal(t, principal.ScopeClient, gotC.Scope)
	require.NotNil(t, gotC.ClientID)
	assert.Equal(t, clientID, *gotC.ClientID)
}

func TestCreateUser_Validation(t *testing.T) {
	t.Parallel()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty email", operations.CreateCommand{Scope: "ANCHOR"}, "EMAIL_REQUIRED"},
		{"whitespace email", operations.CreateCommand{Email: "   ", Scope: "ANCHOR"}, "EMAIL_REQUIRED"},
		{"no at-sign", operations.CreateCommand{Email: "plainaddress", Scope: "ANCHOR"}, "INVALID_EMAIL"},
		{"no tld", operations.CreateCommand{Email: "user@host", Scope: "ANCHOR"}, "INVALID_EMAIL"},
		{"empty scope", operations.CreateCommand{Email: "prn-val@example.com"}, "INVALID_SCOPE"},
		{"unknown scope", operations.CreateCommand{Email: "prn-val@example.com", Scope: "GLOBAL"}, "INVALID_SCOPE"},
		{"client scope without client", operations.CreateCommand{Email: "prn-val@example.com", Scope: "CLIENT"}, "CLIENT_REQUIRED"},
		{"partner scope without client", operations.CreateCommand{Email: "prn-val@example.com", Scope: "PARTNER"}, "CLIENT_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateUser(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself; the second
// create uses a case+whitespace variant to prove the conflict check runs on
// the normalised email.
func TestCreateUser_DuplicateEmail_Conflict(t *testing.T) {
	t.Parallel()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreateUser(t, repo, uow, "prn-create-dup@example.com", "ANCHOR", nil)

	_, err := operations.CreateUser(context.Background(), repo, uow,
		operations.CreateCommand{Email: " PRN-CREATE-DUP@EXAMPLE.COM ", Scope: "ANCHOR"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "EMAIL_EXISTS")
}

// ── UpdateUser ────────────────────────────────────────────────────────────

func TestUpdateUser_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateUser(t, repo, uow, "prn-update-happy@example.com", "ANCHOR", nil)

	// Same email in a different case is accepted as a stable identity
	// assertion (PUT-a-full-object support), not a change.
	committed, err := operations.UpdateUser(ctx, repo, uow, operations.UpdateCommand{
		ID:     seeded.UserID,
		Name:   ptr("  Renamed User  "),
		Active: ptr(false),
		Email:  ptr("PRN-UPDATE-HAPPY@example.com"),
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.UserID, committed.Event().UserID)
	assert.Equal(t, "Renamed User", committed.Event().Name)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Renamed User", got.Name)
	assert.False(t, got.Active)
	assert.Equal(t, "prn-update-happy@example.com", got.UserIdentity.Email, "email unchanged")
}

func TestUpdateUser_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateUser(t, repo, uow, "prn-update-err@example.com", "ANCHOR", nil)

	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: ptr("X")}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank name", operations.UpdateCommand{ID: "prn_doesnotexist1", Name: ptr("  ")}, usecase.KindValidation, "NAME_REQUIRED"},
		{"unknown id", operations.UpdateCommand{ID: "prn_doesnotexist1", Name: ptr("X")}, usecase.KindNotFound, "Principal_NOT_FOUND"},
		{"email change refused", operations.UpdateCommand{ID: seeded.UserID, Email: ptr("other@example.com")}, usecase.KindValidation, "EMAIL_IMMUTABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateUser(ctx, repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Activate / Deactivate ─────────────────────────────────────────────────

func TestActivateDeactivateUser_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateUser(t, repo, uow, "prn-actdeact@example.com", "ANCHOR", nil)

	deact, err := operations.DeactivateUser(ctx, repo, uow,
		operations.DeactivateCommand{ID: seeded.UserID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.UserID, deact.Event().UserID)
	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.Active)

	act, err := operations.ActivateUser(ctx, repo, uow,
		operations.ActivateCommand{ID: seeded.UserID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.UserID, act.Event().UserID)
	got, err = repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Active)
}

func TestActivateUser_Errors(t *testing.T) {
	t.Parallel()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.ActivateUser(context.Background(), repo, uow,
		operations.ActivateCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.ActivateUser(context.Background(), repo, uow,
		operations.ActivateCommand{ID: "prn_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Principal_NOT_FOUND")
}

func TestDeactivateUser_Errors(t *testing.T) {
	t.Parallel()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeactivateUser(context.Background(), repo, uow,
		operations.DeactivateCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeactivateUser(context.Background(), repo, uow,
		operations.DeactivateCommand{ID: "prn_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Principal_NOT_FOUND")
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteUser_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateUser(t, repo, uow, "prn-delete@example.com", "ANCHOR", nil)

	committed, err := operations.DeleteUser(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.UserID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.UserID, committed.Event().UserID)
	assert.Equal(t, "prn-delete@example.com", committed.Event().Email)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted principal must be gone")
}

func TestDeleteUser_Errors(t *testing.T) {
	t.Parallel()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteUser(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteUser(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "prn_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Principal_NOT_FOUND")
}

// ── AssignRoles ───────────────────────────────────────────────────────────

func TestAssignRoles_HappyPathAndReplace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	roles := role.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	viewer := mustCreateRole(t, uow, "prnasgn", "viewer")
	editor := mustCreateRole(t, uow, "prnasgn", "editor")
	seeded := mustCreateUser(t, repo, uow, "prn-asgnroles@example.com", "ANCHOR", nil)

	first, err := operations.AssignRoles(ctx, repo, roles, uow,
		operations.AssignRolesCommand{UserID: seeded.UserID, Roles: []string{viewer, editor}}, ec)
	require.NoError(t, err)
	ev := first.Event()
	assert.Equal(t, seeded.UserID, ev.UserID)
	assert.Equal(t, []string{viewer, editor}, ev.Roles)
	assert.ElementsMatch(t, []string{viewer, editor}, ev.Added)
	assert.Empty(t, ev.Removed)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, map[string]string{
		viewer: "ADMIN_ASSIGNED",
		editor: "ADMIN_ASSIGNED",
	}, roleSources(got), "assignments land in iam_principal_roles with ADMIN_ASSIGNED source")

	// Reassign replaces the whole set: dropping viewer must show in Removed
	// and on reload.
	second, err := operations.AssignRoles(ctx, repo, roles, uow,
		operations.AssignRolesCommand{UserID: seeded.UserID, Roles: []string{editor}}, ec)
	require.NoError(t, err)
	assert.Empty(t, second.Event().Added)
	assert.Equal(t, []string{viewer}, second.Event().Removed)

	got, err = repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, map[string]string{editor: "ADMIN_ASSIGNED"}, roleSources(got))
}

func TestAssignRoles_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	roles := role.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	user := mustCreateUser(t, repo, uow, "prn-asgnroles-err@example.com", "ANCHOR", nil)
	svc := seedServicePrincipal(t, "sa_prnasgnerr01", "prn-asgnroles-svc")

	cases := []struct {
		name string
		cmd  operations.AssignRolesCommand
		kind usecase.Kind
		code string
	}{
		{"missing user id", operations.AssignRolesCommand{Roles: []string{"x"}}, usecase.KindValidation, "USER_ID_REQUIRED"},
		{"unknown user", operations.AssignRolesCommand{UserID: "prn_doesnotexist1"}, usecase.KindNotFound, "User_NOT_FOUND"},
		{"service principal", operations.AssignRolesCommand{UserID: svc.ID, Roles: []string{"x"}}, usecase.KindBusinessRule, "NOT_A_USER"},
		{"unknown role", operations.AssignRolesCommand{UserID: user.UserID, Roles: []string{"prnasgn:doesnotexist"}}, usecase.KindValidation, "ROLE_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.AssignRoles(ctx, repo, roles, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── AssignApplicationAccess (326772d: allApplications flag + id list) ─────

func TestAssignApplicationAccess_HappyPath_AllApplicationsFlag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	apps := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	app1 := mustCreateApplication(t, uow, "prnappa1", "Prn App A1")
	app2 := mustCreateApplication(t, uow, "prnappa2", "Prn App A2")
	seeded := mustCreateUser(t, repo, uow, "prn-appaccess@example.com", "ANCHOR", nil)

	// 1. Restrict: ids + AllApplications=false. New users default to
	// AllApplications=true (pinned in the create test), so this flips it.
	first, err := operations.AssignApplicationAccess(ctx, repo, apps, uow,
		operations.AssignApplicationAccessCommand{
			UserID:          seeded.UserID,
			ApplicationIDs:  []string{app1, app2},
			AllApplications: ptr(false),
		}, ec)
	require.NoError(t, err)
	ev := first.Event()
	assert.Equal(t, seeded.UserID, ev.UserID)
	assert.Equal(t, []string{app1, app2}, ev.ApplicationIDs)
	assert.ElementsMatch(t, []string{app1, app2}, ev.Added)
	assert.Empty(t, ev.Removed)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.ElementsMatch(t, []string{app1, app2}, got.AccessibleApplicationIDs,
		"junction rewritten from the id list")
	assert.False(t, got.AllApplications, "explicit false is stored on iam_principals (326772d)")

	// 2. Nil AllApplications leaves the stored flag unchanged; the id list is
	// still replaced wholesale.
	second, err := operations.AssignApplicationAccess(ctx, repo, apps, uow,
		operations.AssignApplicationAccessCommand{
			UserID:         seeded.UserID,
			ApplicationIDs: []string{app1},
		}, ec)
	require.NoError(t, err)
	assert.Equal(t, []string{app2}, second.Event().Removed)

	got, err = repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []string{app1}, got.AccessibleApplicationIDs)
	assert.False(t, got.AllApplications, "nil flag must not touch the stored value (326772d)")

	// 3. Back to unrestricted: empty list + AllApplications=true.
	_, err = operations.AssignApplicationAccess(ctx, repo, apps, uow,
		operations.AssignApplicationAccessCommand{
			UserID:          seeded.UserID,
			ApplicationIDs:  []string{},
			AllApplications: ptr(true),
		}, ec)
	require.NoError(t, err)

	got, err = repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got.AccessibleApplicationIDs)
	assert.True(t, got.AllApplications)
}

func TestAssignApplicationAccess_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	apps := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	user := mustCreateUser(t, repo, uow, "prn-appaccess-err@example.com", "ANCHOR", nil)
	svc := seedServicePrincipal(t, "sa_prnappaerr01", "prn-appaccess-svc")

	inactiveApp := mustCreateApplication(t, uow, "prnappinact", "Prn App Inactive")
	_, err := appops.DeactivateApplication(ctx, apps, uow,
		appops.DeactivateCommand{ID: inactiveApp}, testpg.TestEC())
	require.NoError(t, err)

	cases := []struct {
		name string
		cmd  operations.AssignApplicationAccessCommand
		kind usecase.Kind
		code string
	}{
		{"missing user id", operations.AssignApplicationAccessCommand{}, usecase.KindValidation, "USER_ID_REQUIRED"},
		{"unknown user", operations.AssignApplicationAccessCommand{UserID: "prn_doesnotexist1"}, usecase.KindNotFound, "User_NOT_FOUND"},
		{"service principal", operations.AssignApplicationAccessCommand{UserID: svc.ID}, usecase.KindBusinessRule, "NOT_A_USER"},
		{"unknown application", operations.AssignApplicationAccessCommand{UserID: user.UserID, ApplicationIDs: []string{"app_doesnotexist"}}, usecase.KindValidation, "APPLICATION_NOT_FOUND"},
		{"inactive application", operations.AssignApplicationAccessCommand{UserID: user.UserID, ApplicationIDs: []string{inactiveApp}}, usecase.KindBusinessRule, "APPLICATION_INACTIVE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.AssignApplicationAccess(ctx, repo, apps, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── GrantClientAccess / RevokeClientAccess ────────────────────────────────

func TestGrantRevokeClientAccess_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	clients := client.NewRepository(testpg.Pool(t))
	grants := principal.NewClientAccessGrantRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	clientID := mustCreateClient(t, uow, "Prn Grant RT", "prn-grant-rt")
	partner := mustCreateUser(t, repo, uow, "prn-grantrt@example.com", "PARTNER", &clientID)

	granted, err := operations.GrantClientAccess(ctx, repo, clients, grants, uow,
		operations.GrantClientAccessCommand{UserID: partner.UserID, ClientID: clientID}, ec)
	require.NoError(t, err)
	assert.Equal(t, partner.UserID, granted.Event().UserID)
	assert.Equal(t, clientID, granted.Event().ClientID)

	grant, err := grants.FindByPrincipalAndClient(ctx, partner.UserID, clientID)
	require.NoError(t, err)
	require.NotNil(t, grant)
	assert.Equal(t, ec.PrincipalID, grant.GrantedBy)

	reloaded, err := repo.FindByID(ctx, partner.UserID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, []string{clientID}, reloaded.AssignedClients, "grant hydrates onto the principal")

	// Granting twice is refused.
	_, err = operations.GrantClientAccess(ctx, repo, clients, grants, uow,
		operations.GrantClientAccessCommand{UserID: partner.UserID, ClientID: clientID}, ec)
	testpg.RequireUsecaseError(t, err, usecase.KindBusinessRule, "GRANT_EXISTS")

	// Revoke → gone on reload, and a second revoke is a NotFound.
	revoked, err := operations.RevokeClientAccess(ctx, repo, grants, uow,
		operations.RevokeClientAccessCommand{UserID: partner.UserID, ClientID: clientID}, ec)
	require.NoError(t, err)
	assert.Equal(t, clientID, revoked.Event().ClientID)

	grant, err = grants.FindByPrincipalAndClient(ctx, partner.UserID, clientID)
	require.NoError(t, err)
	assert.Nil(t, grant)

	reloaded, err = repo.FindByID(ctx, partner.UserID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Empty(t, reloaded.AssignedClients)

	_, err = operations.RevokeClientAccess(ctx, repo, grants, uow,
		operations.RevokeClientAccessCommand{UserID: partner.UserID, ClientID: clientID}, ec)
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Grant_NOT_FOUND")
}

func TestGrantClientAccess_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	clients := client.NewRepository(testpg.Pool(t))
	grants := principal.NewClientAccessGrantRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	homeID := "clt_homedummy03"
	partner := mustCreateUser(t, repo, uow, "prn-grant-err@example.com", "PARTNER", &homeID)
	anchor := mustCreateUser(t, repo, uow, "prn-grant-anchor@example.com", "ANCHOR", nil)
	svc := seedServicePrincipal(t, "sa_prngranterr1", "prn-grant-svc")

	cases := []struct {
		name string
		cmd  operations.GrantClientAccessCommand
		kind usecase.Kind
		code string
	}{
		{"missing user id", operations.GrantClientAccessCommand{ClientID: "clt_doesnotexist1"}, usecase.KindValidation, "USER_ID_REQUIRED"},
		{"missing client id", operations.GrantClientAccessCommand{UserID: partner.UserID}, usecase.KindValidation, "CLIENT_ID_REQUIRED"},
		{"unknown user", operations.GrantClientAccessCommand{UserID: "prn_doesnotexist1", ClientID: "clt_doesnotexist1"}, usecase.KindNotFound, "User_NOT_FOUND"},
		{"service principal", operations.GrantClientAccessCommand{UserID: svc.ID, ClientID: "clt_doesnotexist1"}, usecase.KindBusinessRule, "NOT_A_USER"},
		{"anchor scope", operations.GrantClientAccessCommand{UserID: anchor.UserID, ClientID: "clt_doesnotexist1"}, usecase.KindBusinessRule, "NOT_PARTNER_SCOPE"},
		{"unknown client", operations.GrantClientAccessCommand{UserID: partner.UserID, ClientID: "clt_doesnotexist1"}, usecase.KindNotFound, "Client_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.GrantClientAccess(ctx, repo, clients, grants, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

func TestRevokeClientAccess_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	grants := principal.NewClientAccessGrantRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	svc := seedServicePrincipal(t, "sa_prnrevokeerr", "prn-revoke-svc")

	cases := []struct {
		name string
		cmd  operations.RevokeClientAccessCommand
		kind usecase.Kind
		code string
	}{
		{"missing user id", operations.RevokeClientAccessCommand{ClientID: "clt_doesnotexist1"}, usecase.KindValidation, "USER_ID_REQUIRED"},
		{"missing client id", operations.RevokeClientAccessCommand{UserID: "prn_doesnotexist1"}, usecase.KindValidation, "CLIENT_ID_REQUIRED"},
		{"unknown user", operations.RevokeClientAccessCommand{UserID: "prn_doesnotexist1", ClientID: "clt_doesnotexist1"}, usecase.KindNotFound, "User_NOT_FOUND"},
		{"service principal", operations.RevokeClientAccessCommand{UserID: svc.ID, ClientID: "clt_doesnotexist1"}, usecase.KindBusinessRule, "NOT_A_USER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.RevokeClientAccess(ctx, repo, grants, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── SetClientAssociation ──────────────────────────────────────────────────

// The "*" wildcard promotes to ANCHOR regardless of mode (no mode needed) and
// clears the home client.
func TestSetClientAssociation_AnchorWildcard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	clients := client.NewRepository(testpg.Pool(t))
	grants := principal.NewClientAccessGrantRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	homeID := "clt_homedummy04"
	seeded := mustCreateUser(t, repo, uow, "prn-assoc-anchor@example.com", "CLIENT", &homeID)

	committed, err := operations.SetClientAssociation(ctx, repo, clients, grants, uow,
		operations.SetClientAssociationCommand{UserID: seeded.UserID, ClientID: "*"}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.UserID, committed.Event().UserID)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, principal.ScopeAnchor, got.Scope)
	assert.Nil(t, got.ClientID)
}

func TestSetClientAssociation_ChangeClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	clients := client.NewRepository(testpg.Pool(t))
	grants := principal.NewClientAccessGrantRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	homeID := "clt_homedummy05"
	newClient := mustCreateClient(t, uow, "Prn Assoc Change", "prn-assoc-change")
	seeded := mustCreateUser(t, repo, uow, "prn-assoc-change@example.com", "CLIENT", &homeID)

	_, err := operations.SetClientAssociation(ctx, repo, clients, grants, uow,
		operations.SetClientAssociationCommand{
			UserID:   seeded.UserID,
			ClientID: newClient,
			Mode:     operations.ModeChangeClient,
		}, testpg.TestEC())
	require.NoError(t, err)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, principal.ScopeClient, got.Scope, "CHANGE_CLIENT keeps CLIENT scope")
	require.NotNil(t, got.ClientID)
	assert.Equal(t, newClient, *got.ClientID)
	assert.Empty(t, got.AssignedClients, "no partner grants on a home-client change")
}

func TestSetClientAssociation_ToPartner_PreservesOldHomeAsGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	clients := client.NewRepository(testpg.Pool(t))
	grants := principal.NewClientAccessGrantRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	oldHome := mustCreateClient(t, uow, "Prn Assoc Partner A", "prn-assoc-partner-a")
	newClient := mustCreateClient(t, uow, "Prn Assoc Partner B", "prn-assoc-partner-b")
	seeded := mustCreateUser(t, repo, uow, "prn-assoc-partner@example.com", "CLIENT", &oldHome)

	_, err := operations.SetClientAssociation(ctx, repo, clients, grants, uow,
		operations.SetClientAssociationCommand{
			UserID:   seeded.UserID,
			ClientID: newClient,
			Mode:     operations.ModeToPartner,
		}, ec)
	require.NoError(t, err)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, principal.ScopePartner, got.Scope)
	assert.Nil(t, got.ClientID, "partner users have no home client")
	assert.ElementsMatch(t, []string{oldHome, newClient}, got.AssignedClients,
		"old home client is preserved as an access grant alongside the new one")

	oldGrant, err := grants.FindByPrincipalAndClient(ctx, seeded.UserID, oldHome)
	require.NoError(t, err)
	require.NotNil(t, oldGrant)
	assert.Equal(t, ec.PrincipalID, oldGrant.GrantedBy)
}

func TestSetClientAssociation_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	clients := client.NewRepository(testpg.Pool(t))
	grants := principal.NewClientAccessGrantRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	user := mustCreateUser(t, repo, uow, "prn-assoc-err@example.com", "ANCHOR", nil)
	svc := seedServicePrincipal(t, "sa_prnassocerr1", "prn-assoc-svc")

	cases := []struct {
		name string
		cmd  operations.SetClientAssociationCommand
		kind usecase.Kind
		code string
	}{
		{"missing user id", operations.SetClientAssociationCommand{ClientID: "*"}, usecase.KindValidation, "USER_ID_REQUIRED"},
		{"missing client id", operations.SetClientAssociationCommand{UserID: user.UserID}, usecase.KindValidation, "CLIENT_ID_REQUIRED"},
		{"unknown user", operations.SetClientAssociationCommand{UserID: "prn_doesnotexist1", ClientID: "*"}, usecase.KindNotFound, "User_NOT_FOUND"},
		{"service principal", operations.SetClientAssociationCommand{UserID: svc.ID, ClientID: "*"}, usecase.KindBusinessRule, "NOT_A_USER"},
		{"specific client without mode", operations.SetClientAssociationCommand{UserID: user.UserID, ClientID: "clt_doesnotexist1"}, usecase.KindValidation, "MODE_REQUIRED"},
		{"unknown client change", operations.SetClientAssociationCommand{UserID: user.UserID, ClientID: "clt_doesnotexist1", Mode: operations.ModeChangeClient}, usecase.KindNotFound, "Client_NOT_FOUND"},
		{"unknown client to-partner", operations.SetClientAssociationCommand{UserID: user.UserID, ClientID: "clt_doesnotexist1", Mode: operations.ModeToPartner}, usecase.KindNotFound, "Client_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.SetClientAssociation(ctx, repo, clients, grants, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── ResetPassword ─────────────────────────────────────────────────────────

func TestResetPassword_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	committed, err := operations.CreateUser(ctx, repo, uow, operations.CreateCommand{
		Email:    "prn-resetpw@example.com",
		Scope:    "ANCHOR",
		Password: ptr("original-pass-99"),
	}, testpg.TestEC())
	require.NoError(t, err)
	userID := committed.Event().UserID

	before, err := repo.FindByID(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, before.UserIdentity.PasswordHash)
	oldHash := *before.UserIdentity.PasswordHash

	reset, err := operations.ResetPassword(ctx, repo, uow,
		operations.ResetPasswordCommand{ID: userID, NewPassword: "brand-new-pw-1234"}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, userID, reset.Event().UserID)

	after, err := repo.FindByID(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, after.UserIdentity.PasswordHash)
	newHash := *after.UserIdentity.PasswordHash
	assert.NotEqual(t, oldHash, newHash, "hash must change")
	assert.NoError(t, passwordhash.Verify("brand-new-pw-1234", newHash))
	assert.Error(t, passwordhash.Verify("original-pass-99", newHash), "old password no longer verifies")
}

// EnforcePasswordComplexity=false relaxes the minimum length to 2 (Rust
// relaxed() policy) — the caller owns its own password rules.
func TestResetPassword_RelaxedComplexity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateUser(t, repo, uow, "prn-resetpw-relaxed@example.com", "ANCHOR", nil)

	_, err := operations.ResetPassword(ctx, repo, uow, operations.ResetPasswordCommand{
		ID:                        seeded.UserID,
		NewPassword:               "ab",
		EnforcePasswordComplexity: ptr(false),
	}, testpg.TestEC())
	require.NoError(t, err)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got.UserIdentity.PasswordHash)
	assert.NoError(t, passwordhash.Verify("ab", *got.UserIdentity.PasswordHash))
}

func TestResetPassword_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	svc := seedServicePrincipal(t, "sa_prnresetperr", "prn-resetpw-svc")

	cases := []struct {
		name string
		cmd  operations.ResetPasswordCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.ResetPasswordCommand{NewPassword: "longenough99"}, usecase.KindValidation, "ID_REQUIRED"},
		{"strict default rejects 7 chars", operations.ResetPasswordCommand{ID: "prn_doesnotexist1", NewPassword: "seven77"}, usecase.KindValidation, "PASSWORD_TOO_SHORT"},
		{"relaxed still rejects 1 char", operations.ResetPasswordCommand{ID: "prn_doesnotexist1", NewPassword: "a", EnforcePasswordComplexity: ptr(false)}, usecase.KindValidation, "PASSWORD_TOO_SHORT"},
		{"unknown id", operations.ResetPasswordCommand{ID: "prn_doesnotexist1", NewPassword: "longenough99"}, usecase.KindNotFound, "Principal_NOT_FOUND"},
		// NOT_A_USER is a Conflict here (not BusinessRule like the other ops) —
		// pinned from reset_password.go.
		{"service principal", operations.ResetPasswordCommand{ID: svc.ID, NewPassword: "longenough99"}, usecase.KindConflict, "NOT_A_USER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.ResetPassword(ctx, repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── SendPasswordReset ─────────────────────────────────────────────────────

// recordingEmailer is a test-local PasswordResetEmailer: SendPasswordReset's
// only side effect is the SendResetEmail call, so the fake records it (or
// fails on demand). No token row is minted by the operation itself — that is
// the emailer implementation's job.
type recordingEmailer struct {
	err   error
	calls []struct {
		principalID string
		email       string
		reset2FA    bool
	}
}

func (f *recordingEmailer) SendResetEmail(_ context.Context, p *principal.Principal, reset2FA bool) error {
	if f.err != nil {
		return f.err
	}
	email := ""
	if p.UserIdentity != nil {
		email = p.UserIdentity.Email
	}
	f.calls = append(f.calls, struct {
		principalID string
		email       string
		reset2FA    bool
	}{p.ID, email, reset2FA})
	return nil
}

func TestSendPasswordReset_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateUser(t, repo, uow, "prn-sendreset@example.com", "ANCHOR", nil)

	fake := &recordingEmailer{}
	err := operations.SendPasswordReset(ctx, repo, fake,
		operations.SendPasswordResetCommand{ID: seeded.UserID, Reset2FA: true}, testpg.TestEC())
	require.NoError(t, err)

	require.Len(t, fake.calls, 1)
	assert.Equal(t, seeded.UserID, fake.calls[0].principalID)
	assert.Equal(t, "prn-sendreset@example.com", fake.calls[0].email)
	assert.True(t, fake.calls[0].reset2FA, "reset2fa flag is plumbed through to the emailer")
}

func TestSendPasswordReset_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	svc := seedServicePrincipal(t, "sa_prnsendreset", "prn-sendreset-svc")

	// OIDC-federated user: only the auth subsystem's federated provisioning
	// sets external_idp_id, so seed via the bootstrap-persist seam.
	oidcUser := principal.NewUser("prn-sendreset-oidc@example.com", principal.ScopeAnchor)
	oidcUser.ExternalIdentity = &principal.ExternalIdentity{ProviderID: "ms-entra", ExternalID: "ext-oidc-1"}
	seedPrincipalDirect(t, oidcUser)

	// USER row with an empty email — unreachable through CreateUser
	// (EMAIL_REQUIRED), seeded directly to pin the NO_EMAIL guard.
	noEmailUser := principal.NewUser("", principal.ScopeAnchor)
	seedPrincipalDirect(t, noEmailUser)

	fake := &recordingEmailer{}

	t.Run("missing id", func(t *testing.T) {
		t.Parallel()
		err := operations.SendPasswordReset(ctx, repo, fake, operations.SendPasswordResetCommand{}, ec)
		testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")
	})
	t.Run("emailer not configured", func(t *testing.T) {
		t.Parallel()
		err := operations.SendPasswordReset(ctx, repo, nil,
			operations.SendPasswordResetCommand{ID: "prn_doesnotexist1"}, ec)
		testpg.RequireUsecaseError(t, err, usecase.KindInternal, "EMAILER_NOT_CONFIGURED")
	})
	t.Run("unknown id", func(t *testing.T) {
		t.Parallel()
		err := operations.SendPasswordReset(ctx, repo, fake,
			operations.SendPasswordResetCommand{ID: "prn_doesnotexist1"}, ec)
		testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Principal_NOT_FOUND")
	})
	t.Run("service principal", func(t *testing.T) {
		t.Parallel()
		err := operations.SendPasswordReset(ctx, repo, fake,
			operations.SendPasswordResetCommand{ID: svc.ID}, ec)
		testpg.RequireUsecaseError(t, err, usecase.KindValidation, "NOT_USER")
	})
	t.Run("oidc-federated user", func(t *testing.T) {
		t.Parallel()
		err := operations.SendPasswordReset(ctx, repo, fake,
			operations.SendPasswordResetCommand{ID: oidcUser.ID}, ec)
		testpg.RequireUsecaseError(t, err, usecase.KindValidation, "OIDC_USER")
	})
	t.Run("no email on file", func(t *testing.T) {
		t.Parallel()
		err := operations.SendPasswordReset(ctx, repo, fake,
			operations.SendPasswordResetCommand{ID: noEmailUser.ID}, ec)
		testpg.RequireUsecaseError(t, err, usecase.KindValidation, "NO_EMAIL")
	})
	t.Run("emailer failure surfaces as internal", func(t *testing.T) {
		t.Parallel()
		failUser := mustCreateUser(t, repo, uow, "prn-sendreset-fail@example.com", "ANCHOR", nil)
		failing := &recordingEmailer{err: assert.AnError}
		err := operations.SendPasswordReset(ctx, repo, failing,
			operations.SendPasswordResetCommand{ID: failUser.UserID}, ec)
		testpg.RequireUsecaseError(t, err, usecase.KindInternal, "EMAILER")
	})
}

// ── SyncPrincipals (SDK app-scoped sync) ──────────────────────────────────

// All SDK_SYNC role assignments in this package are created by THIS test only:
// RemoveUnlisted strips SDK_SYNC assignments db-wide from any USER whose email
// is absent from the payload, so spreading SDK_SYNC seeds across parallel
// tests would make the Deactivated rollup (and other tests' role asserts)
// racy. Keeping every SDK_SYNC write inside one sequential test makes the
// counts deterministic.
func TestSyncPrincipals_UpsertMergeAndRemoveUnlisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	roles := role.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	// Only the ADMIN_ASSIGNED merge seed must exist as a role row —
	// SyncPrincipals (like Rust) does NOT validate the SDK role names it
	// writes, and iam_principal_roles has no FK on role_name.
	admin := mustCreateRole(t, uow, "prnsync", "admin")
	existing := mustCreateUser(t, repo, uow, "prn-sync-existing@example.com", "ANCHOR", nil)
	_, err := operations.AssignRoles(ctx, repo, roles, uow,
		operations.AssignRolesCommand{UserID: existing.UserID, Roles: []string{admin}}, ec)
	require.NoError(t, err)

	// First sync: one update (merge) + one create. Role names and the new
	// email arrive mixed-case to pin the lower-casing.
	first, err := operations.SyncPrincipals(ctx, repo, uow, operations.SyncPrincipalsCommand{
		ApplicationCode: "prnsync",
		Principals: []operations.SyncPrincipalInput{
			{Email: "prn-sync-existing@example.com", Name: "Existing Renamed", Roles: []string{"PRNSYNC:VIEWER"}, Active: true},
			{Email: "Prn-Sync-NEW@Example.com", Name: "Fresh User", Roles: []string{"prnsync:editor"}, Active: true},
		},
	}, ec)
	require.NoError(t, err)
	ev := first.Event()
	assert.Equal(t, "prnsync", ev.ApplicationCode)
	assert.Equal(t, uint32(1), ev.Created)
	assert.Equal(t, uint32(1), ev.Updated)
	assert.Equal(t, uint32(0), ev.Deactivated)
	assert.Equal(t, []string{"prn-sync-existing@example.com", "prn-sync-new@example.com"}, ev.SyncedEmails)

	gotExisting, err := repo.FindByID(ctx, existing.UserID)
	require.NoError(t, err)
	require.NotNil(t, gotExisting)
	assert.Equal(t, "Existing Renamed", gotExisting.Name)
	assert.Equal(t, map[string]string{
		admin:            "ADMIN_ASSIGNED",
		"prnsync:viewer": "SDK_SYNC",
	}, roleSources(gotExisting), "merge keeps non-SDK assignments and replaces the SDK set")

	gotNew, err := repo.FindByEmail(ctx, "prn-sync-new@example.com")
	require.NoError(t, err)
	require.NotNil(t, gotNew, "new principal is created under the lower-cased email")
	assert.Equal(t, principal.TypeUser, gotNew.Type)
	assert.Equal(t, principal.ScopeClient, gotNew.Scope, "sync-created principals are CLIENT-scoped users")
	assert.True(t, gotNew.Active)
	assert.Equal(t, "Fresh User", gotNew.Name)
	assert.Equal(t, map[string]string{"prnsync:editor": "SDK_SYNC"}, roleSources(gotNew))

	// Second sync with RemoveUnlisted: the new user is absent from the
	// payload, so its SDK_SYNC roles are stripped — the principal itself is
	// NOT deleted or deactivated.
	second, err := operations.SyncPrincipals(ctx, repo, uow, operations.SyncPrincipalsCommand{
		ApplicationCode: "prnsync",
		Principals: []operations.SyncPrincipalInput{
			{Email: "prn-sync-existing@example.com", Name: "Existing Renamed", Roles: []string{"prnsync:viewer"}, Active: true},
		},
		RemoveUnlisted: true,
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), second.Event().Created)
	assert.Equal(t, uint32(1), second.Event().Updated)
	assert.Equal(t, uint32(1), second.Event().Deactivated)

	gotNew, err = repo.FindByEmail(ctx, "prn-sync-new@example.com")
	require.NoError(t, err)
	require.NotNil(t, gotNew, "RemoveUnlisted strips roles, it never deletes the principal")
	assert.True(t, gotNew.Active, "the principal row is not deactivated either")
	assert.Empty(t, gotNew.Roles)

	gotExisting, err = repo.FindByID(ctx, existing.UserID)
	require.NoError(t, err)
	require.NotNil(t, gotExisting)
	assert.Equal(t, map[string]string{
		admin:            "ADMIN_ASSIGNED",
		"prnsync:viewer": "SDK_SYNC",
	}, roleSources(gotExisting), "listed principal keeps both sets")
}

func TestSyncPrincipals_Validation(t *testing.T) {
	t.Parallel()
	repo := principal.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.SyncPrincipals(context.Background(), repo, uow,
		operations.SyncPrincipalsCommand{
			Principals: []operations.SyncPrincipalInput{{Email: "x@example.com", Active: true}},
		}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "APPLICATION_CODE_REQUIRED")

	_, err = operations.SyncPrincipals(context.Background(), repo, uow,
		operations.SyncPrincipalsCommand{ApplicationCode: "prnsyncval"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "PRINCIPALS_REQUIRED")
}

// ── SyncIdpRoles ──────────────────────────────────────────────────────────

func TestSyncIdpRoles_HappyPathReplaceAndDedup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	roles := role.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	base := mustCreateRole(t, uow, "prnidp", "base")
	viewer := mustCreateRole(t, uow, "prnidp", "viewer")
	seeded := mustCreateUser(t, repo, uow, "prn-idpsync@example.com", "ANCHOR", nil)
	_, err := operations.AssignRoles(ctx, repo, roles, uow,
		operations.AssignRolesCommand{UserID: seeded.UserID, Roles: []string{base}}, ec)
	require.NoError(t, err)

	// 1. Add an IDP role: the ADMIN_ASSIGNED one is preserved untouched.
	first, err := operations.SyncIdpRoles(ctx, repo, roles, uow,
		operations.SyncIdpRolesCommand{UserID: seeded.UserID, PlatformRoles: []string{viewer}}, ec)
	require.NoError(t, err)
	assert.Equal(t, []string{base, viewer}, first.Event().Roles, "preserved-then-appended order")
	assert.Equal(t, []string{viewer}, first.Event().Added)
	assert.Empty(t, first.Event().Removed)

	got, err := repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, map[string]string{
		base:   "ADMIN_ASSIGNED",
		viewer: "IDP_SYNC",
	}, roleSources(got))

	// 2. Empty set = the user lost every group upstream → only the IDP_SYNC
	// assignment is removed.
	second, err := operations.SyncIdpRoles(ctx, repo, roles, uow,
		operations.SyncIdpRolesCommand{UserID: seeded.UserID, PlatformRoles: []string{}}, ec)
	require.NoError(t, err)
	assert.Empty(t, second.Event().Added)
	assert.Equal(t, []string{viewer}, second.Event().Removed)

	got, err = repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, map[string]string{base: "ADMIN_ASSIGNED"}, roleSources(got))

	// 3. Dedup: an incoming IDP role that already exists as a non-IDP
	// assignment is skipped — the ADMIN_ASSIGNED source survives.
	third, err := operations.SyncIdpRoles(ctx, repo, roles, uow,
		operations.SyncIdpRolesCommand{UserID: seeded.UserID, PlatformRoles: []string{base}}, ec)
	require.NoError(t, err)
	assert.Empty(t, third.Event().Added)
	assert.Empty(t, third.Event().Removed)

	got, err = repo.FindByID(ctx, seeded.UserID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, map[string]string{base: "ADMIN_ASSIGNED"}, roleSources(got),
		"existing non-IDP assignment must not be re-sourced to IDP_SYNC")
}

func TestSyncIdpRoles_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := principal.NewRepository(testpg.Pool(t))
	roles := role.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	user := mustCreateUser(t, repo, uow, "prn-idpsync-err@example.com", "ANCHOR", nil)
	svc := seedServicePrincipal(t, "sa_prnidpsyncer", "prn-idpsync-svc")

	cases := []struct {
		name string
		cmd  operations.SyncIdpRolesCommand
		kind usecase.Kind
		code string
	}{
		{"missing user id", operations.SyncIdpRolesCommand{}, usecase.KindValidation, "USER_ID_REQUIRED"},
		{"unknown user", operations.SyncIdpRolesCommand{UserID: "prn_doesnotexist1"}, usecase.KindNotFound, "User_NOT_FOUND"},
		{"service principal", operations.SyncIdpRolesCommand{UserID: svc.ID}, usecase.KindBusinessRule, "NOT_A_USER"},
		{"unknown role", operations.SyncIdpRolesCommand{UserID: user.UserID, PlatformRoles: []string{"prnidp:doesnotexist"}}, usecase.KindValidation, "ROLE_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.SyncIdpRoles(ctx, repo, roles, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}
