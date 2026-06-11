//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/client"
	clientops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/client/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

func ptr(s string) *string { return &s }

// mustCreateApp seeds an application through the public operation — the
// same path production uses. Codes are hand-unique per test: the fixture
// never truncates between tests, so tests own their rows and never assert
// table-wide.
func mustCreateApp(t *testing.T, repo *application.Repository, uow *usecasepgx.UnitOfWork, code, name string) operations.ApplicationCreated {
	t.Helper()
	committed, err := operations.CreateApplication(context.Background(), repo, uow,
		operations.CreateCommand{Code: code, Name: name}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// mustCreateClient seeds a real client via client/operations — the
// enable/disable ops verify the client row exists, so a fabricated id
// won't do.
func mustCreateClient(t *testing.T, uow *usecasepgx.UnitOfWork, name, identifier string) clientops.ClientCreated {
	t.Helper()
	repo := client.NewRepository(testpg.Pool(t))
	committed, err := clientops.CreateClient(context.Background(), repo, uow,
		clientops.CreateCommand{Name: name, Identifier: identifier}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── Create ────────────────────────────────────────────────────────────────

func TestCreateApplication_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	committed, err := operations.CreateApplication(ctx, repo, uow, operations.CreateCommand{
		Code:        "  AppCreate-Happy1  ",
		Name:        "  First App  ",
		Description: ptr("the first"),
		Website:     ptr("https://example.com"),
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.ApplicationID)
	assert.Equal(t, "appcreate-happy1", ev.Code, "code is lowercased + trimmed")
	assert.Equal(t, "First App", ev.Name, "name is trimmed")

	got, err := repo.FindByID(ctx, ev.ApplicationID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "appcreate-happy1", got.Code)
	assert.Equal(t, "First App", got.Name)
	assert.Equal(t, application.TypeApplication, got.Type, "default type is APPLICATION")
	assert.True(t, got.Active, "new applications start active")
	require.NotNil(t, got.Description)
	assert.Equal(t, "the first", *got.Description)
	require.NotNil(t, got.Website)
	assert.Equal(t, "https://example.com", *got.Website)
}

// Underscores are explicitly allowed (real codes like logistics_portal use
// them — the Rust reference enforced no pattern at all).
func TestCreateApplication_UnderscoreCodeAllowed(t *testing.T) {
	t.Parallel()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	ev := mustCreateApp(t, repo, uow, "app_with_underscores", "Underscore App")
	assert.Equal(t, "app_with_underscores", ev.Code)

	got, err := repo.FindByCode(context.Background(), "app_with_underscores")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ev.ApplicationID, got.ID)
}

func TestCreateApplication_Validation(t *testing.T) {
	t.Parallel()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"missing code", operations.CreateCommand{Name: "X"}, "CODE_REQUIRED"},
		{"leading digit", operations.CreateCommand{Code: "1bad", Name: "X"}, "INVALID_CODE_FORMAT"},
		{"missing name", operations.CreateCommand{Code: "appcrtval"}, "NAME_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateApplication(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// create IS the seed for the second.
func TestCreateApplication_DuplicateCode_Conflict(t *testing.T) {
	t.Parallel()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreateApp(t, repo, uow, "appdup1", "First")

	_, err := operations.CreateApplication(context.Background(), repo, uow,
		operations.CreateCommand{Code: "appdup1", Name: "Second"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "CODE_EXISTS")
}

// ── Update ────────────────────────────────────────────────────────────────

func TestUpdateApplication_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateApp(t, repo, uow, "appupd1", "Before")

	committed, err := operations.UpdateApplication(ctx, repo, uow, operations.UpdateCommand{
		ID:          seeded.ApplicationID,
		Name:        ptr("  After  "),
		Description: ptr("after"),
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ApplicationID, committed.Event().ApplicationID)
	assert.Equal(t, "After", committed.Event().Name)

	got, err := repo.FindByID(ctx, seeded.ApplicationID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "After", got.Name, "name is trimmed")
	require.NotNil(t, got.Description)
	assert.Equal(t, "after", *got.Description)
	assert.Equal(t, "appupd1", got.Code, "code is immutable on update")
}

func TestUpdateApplication_Errors(t *testing.T) {
	t.Parallel()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: ptr("X")}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank name", operations.UpdateCommand{ID: "app_doesnotexist1", Name: ptr("  ")}, usecase.KindValidation, "NAME_REQUIRED"},
		{"unknown id", operations.UpdateCommand{ID: "app_doesnotexist1", Name: ptr("X")}, usecase.KindNotFound, "Application_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateApplication(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteApplication_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateApp(t, repo, uow, "appdel1", "Doomed")

	committed, err := operations.DeleteApplication(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.ApplicationID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ApplicationID, committed.Event().ApplicationID)
	assert.Equal(t, "appdel1", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.ApplicationID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

func TestDeleteApplication_Errors(t *testing.T) {
	t.Parallel()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteApplication(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteApplication(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "app_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Application_NOT_FOUND")
}

// ── Activate / Deactivate ─────────────────────────────────────────────────

func TestDeactivateAndActivateApplication_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateApp(t, repo, uow, "appact1", "Toggle Me")

	committed, err := operations.DeactivateApplication(ctx, repo, uow,
		operations.DeactivateCommand{ID: seeded.ApplicationID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ApplicationID, committed.Event().ApplicationID)

	got, err := repo.FindByID(ctx, seeded.ApplicationID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.Active, "deactivate must flip Active → false")

	reactivated, err := operations.ActivateApplication(ctx, repo, uow,
		operations.ActivateCommand{ID: seeded.ApplicationID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ApplicationID, reactivated.Event().ApplicationID)

	got, err = repo.FindByID(ctx, seeded.ApplicationID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Active, "activate must flip Active → true")
}

func TestActivateApplication_Errors(t *testing.T) {
	t.Parallel()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.ActivateApplication(context.Background(), repo, uow,
		operations.ActivateCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.ActivateApplication(context.Background(), repo, uow,
		operations.ActivateCommand{ID: "app_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Application_NOT_FOUND")
}

func TestDeactivateApplication_Errors(t *testing.T) {
	t.Parallel()
	repo := application.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeactivateApplication(context.Background(), repo, uow,
		operations.DeactivateCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeactivateApplication(context.Background(), repo, uow,
		operations.DeactivateCommand{ID: "app_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Application_NOT_FOUND")
}

// ── Enable / Disable for client ───────────────────────────────────────────

func TestEnableApplicationForClient_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testpg.Pool(t)
	apps := application.NewRepository(pool)
	clients := client.NewRepository(pool)
	configs := application.NewClientConfigRepo(pool)
	uow := testpg.NewUoW(t)

	app := mustCreateApp(t, apps, uow, "appencl1", "Enable Me")
	cl := mustCreateClient(t, uow, "Enable Client", "appencl-client1")

	committed, err := operations.EnableApplicationForClient(ctx, apps, clients, configs, uow,
		operations.EnableForClientCommand{ApplicationID: app.ApplicationID, ClientID: cl.ClientID},
		testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.Equal(t, app.ApplicationID, ev.ApplicationID)
	assert.Equal(t, cl.ClientID, ev.ClientID)
	assert.NotEmpty(t, ev.ConfigID)

	cfg, err := configs.FindByApplicationAndClient(ctx, app.ApplicationID, cl.ClientID)
	require.NoError(t, err)
	require.NotNil(t, cfg, "enable must create the app_client_configs row")
	assert.Equal(t, ev.ConfigID, cfg.ID)
	assert.True(t, cfg.Enabled)
}

func TestEnableApplicationForClient_Errors(t *testing.T) {
	t.Parallel()
	pool := testpg.Pool(t)
	apps := application.NewRepository(pool)
	clients := client.NewRepository(pool)
	configs := application.NewClientConfigRepo(pool)
	uow := testpg.NewUoW(t)

	// Application_NOT_FOUND is checked before the client lookup, so the
	// unknown-client case needs a real application.
	app := mustCreateApp(t, apps, uow, "appenclerr1", "Enable Errors")

	cases := []struct {
		name string
		cmd  operations.EnableForClientCommand
		kind usecase.Kind
		code string
	}{
		{"missing application id", operations.EnableForClientCommand{ClientID: "clt_doesnotexist1"}, usecase.KindValidation, "APPLICATION_ID_REQUIRED"},
		{"missing client id", operations.EnableForClientCommand{ApplicationID: "app_doesnotexist1"}, usecase.KindValidation, "CLIENT_ID_REQUIRED"},
		{"unknown application", operations.EnableForClientCommand{ApplicationID: "app_doesnotexist1", ClientID: "clt_doesnotexist1"}, usecase.KindNotFound, "Application_NOT_FOUND"},
		{"unknown client", operations.EnableForClientCommand{ApplicationID: app.ApplicationID, ClientID: "clt_doesnotexist1"}, usecase.KindNotFound, "Client_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.EnableApplicationForClient(context.Background(),
				apps, clients, configs, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

func TestDisableApplicationForClient_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testpg.Pool(t)
	apps := application.NewRepository(pool)
	clients := client.NewRepository(pool)
	configs := application.NewClientConfigRepo(pool)
	uow := testpg.NewUoW(t)

	app := mustCreateApp(t, apps, uow, "appdiscl1", "Disable Me")
	cl := mustCreateClient(t, uow, "Disable Client", "appdiscl-client1")

	enabled, err := operations.EnableApplicationForClient(ctx, apps, clients, configs, uow,
		operations.EnableForClientCommand{ApplicationID: app.ApplicationID, ClientID: cl.ClientID},
		testpg.TestEC())
	require.NoError(t, err)

	disabled, err := operations.DisableApplicationForClient(ctx, configs, uow,
		operations.DisableForClientCommand{ApplicationID: app.ApplicationID, ClientID: cl.ClientID},
		testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, enabled.Event().ConfigID, disabled.Event().ConfigID,
		"disable flips the SAME config row, not a new one")

	cfg, err := configs.FindByApplicationAndClient(ctx, app.ApplicationID, cl.ClientID)
	require.NoError(t, err)
	require.NotNil(t, cfg, "disable keeps the row (soft flag, not delete)")
	assert.False(t, cfg.Enabled)

	// Round-trip: re-enabling flips the existing disabled row back.
	reenabled, err := operations.EnableApplicationForClient(ctx, apps, clients, configs, uow,
		operations.EnableForClientCommand{ApplicationID: app.ApplicationID, ClientID: cl.ClientID},
		testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, cfg.ID, reenabled.Event().ConfigID, "re-enable reuses the existing row")

	cfg, err = configs.FindByApplicationAndClient(ctx, app.ApplicationID, cl.ClientID)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.Enabled)
}

func TestDisableApplicationForClient_Errors(t *testing.T) {
	t.Parallel()
	configs := application.NewClientConfigRepo(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.DisableForClientCommand
		kind usecase.Kind
		code string
	}{
		{"missing application id", operations.DisableForClientCommand{ClientID: "clt_doesnotexist1"}, usecase.KindValidation, "APPLICATION_ID_REQUIRED"},
		{"missing client id", operations.DisableForClientCommand{ApplicationID: "app_doesnotexist1"}, usecase.KindValidation, "CLIENT_ID_REQUIRED"},
		{"no config row", operations.DisableForClientCommand{ApplicationID: "app_doesnotexist1", ClientID: "clt_doesnotexist1"}, usecase.KindNotFound, "ClientConfig_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.DisableApplicationForClient(context.Background(),
				configs, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── UpdateClientApplications (bulk replace) ───────────────────────────────

func TestUpdateClientApplications_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testpg.Pool(t)
	apps := application.NewRepository(pool)
	clients := client.NewRepository(pool)
	configs := application.NewClientConfigRepo(pool)
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	appA := mustCreateApp(t, apps, uow, "appbulk-a1", "Bulk A")
	appB := mustCreateApp(t, apps, uow, "appbulk-b1", "Bulk B")
	appC := mustCreateApp(t, apps, uow, "appbulk-c1", "Bulk C")
	cl := mustCreateClient(t, uow, "Bulk Client", "appbulk-client1")

	// enabledByApp reloads the client's configs as appID → Enabled.
	enabledByApp := func() map[string]bool {
		t.Helper()
		rows, err := configs.FindByClient(ctx, cl.ClientID)
		require.NoError(t, err)
		out := make(map[string]bool, len(rows))
		for _, row := range rows {
			out[row.ApplicationID] = row.Enabled
		}
		return out
	}

	// 1. Initial set: [A, B] — both freshly created enabled.
	first, err := operations.UpdateClientApplications(ctx, apps, clients, configs, uow,
		operations.UpdateClientApplicationsCommand{
			ClientID:              cl.ClientID,
			EnabledApplicationIDs: []string{appA.ApplicationID, appB.ApplicationID},
		}, ec)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{appA.ApplicationID, appB.ApplicationID}, first.Event().EnabledAdded)
	assert.Empty(t, first.Event().DisabledRemoved)
	assert.Equal(t, map[string]bool{appA.ApplicationID: true, appB.ApplicationID: true}, enabledByApp())

	// 2. Replace with [B, C]: A flips to disabled (row kept), B untouched,
	//    C freshly enabled.
	second, err := operations.UpdateClientApplications(ctx, apps, clients, configs, uow,
		operations.UpdateClientApplicationsCommand{
			ClientID:              cl.ClientID,
			EnabledApplicationIDs: []string{appB.ApplicationID, appC.ApplicationID},
		}, ec)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{appC.ApplicationID}, second.Event().EnabledAdded)
	assert.ElementsMatch(t, []string{appA.ApplicationID}, second.Event().DisabledRemoved)
	assert.Equal(t, map[string]bool{
		appA.ApplicationID: false,
		appB.ApplicationID: true,
		appC.ApplicationID: true,
	}, enabledByApp(), "disable keeps the row; the desired set is enabled")

	// 3. Idempotent re-apply: empty diff still emits the rollup event.
	third, err := operations.UpdateClientApplications(ctx, apps, clients, configs, uow,
		operations.UpdateClientApplicationsCommand{
			ClientID:              cl.ClientID,
			EnabledApplicationIDs: []string{appB.ApplicationID, appC.ApplicationID},
		}, ec)
	require.NoError(t, err)
	assert.Empty(t, third.Event().EnabledAdded)
	assert.Empty(t, third.Event().DisabledRemoved)
}

func TestUpdateClientApplications_Errors(t *testing.T) {
	t.Parallel()
	pool := testpg.Pool(t)
	apps := application.NewRepository(pool)
	clients := client.NewRepository(pool)
	configs := application.NewClientConfigRepo(pool)
	uow := testpg.NewUoW(t)

	// The client lookup runs before per-app validation, so the app-side
	// cases need a real client.
	cl := mustCreateClient(t, uow, "Bulk Errors Client", "appbulkerr-client1")

	cases := []struct {
		name string
		cmd  operations.UpdateClientApplicationsCommand
		kind usecase.Kind
		code string
	}{
		{"missing client id", operations.UpdateClientApplicationsCommand{}, usecase.KindValidation, "CLIENT_ID_REQUIRED"},
		{"unknown client", operations.UpdateClientApplicationsCommand{ClientID: "clt_doesnotexist1"}, usecase.KindNotFound, "Client_NOT_FOUND"},
		{"blank application id", operations.UpdateClientApplicationsCommand{ClientID: cl.ClientID, EnabledApplicationIDs: []string{"  "}}, usecase.KindValidation, "APPLICATION_ID_REQUIRED"},
		{"unknown application", operations.UpdateClientApplicationsCommand{ClientID: cl.ClientID, EnabledApplicationIDs: []string{"app_doesnotexist1"}}, usecase.KindNotFound, "Application_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateClientApplications(context.Background(),
				apps, clients, configs, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}
