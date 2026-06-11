//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustGrant seeds an access grant through the public operation — the same
// path production uses. (App, role) pairs are hand-unique per test: the
// fixture never truncates between tests, so tests own their rows and never
// assert table-wide.
func mustGrant(t *testing.T, repo *platformconfig.Repository, uow *usecasepgx.UnitOfWork, app, role string, canWrite bool) operations.AccessGranted {
	t.Helper()
	committed, err := operations.GrantAccess(context.Background(), repo, uow,
		operations.GrantAccessCommand{ApplicationCode: app, RoleCode: role, CanWrite: canWrite}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── SetProperty ───────────────────────────────────────────────────────────

func TestSetProperty_HappyPath_CreateNew(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	desc := "SMTP relay host"
	committed, err := operations.SetProperty(ctx, repo, uow, operations.SetPropertyCommand{
		ApplicationCode: "pcset-new",
		Section:         "smtp",
		Property:        "host",
		Value:           "mail.example.com",
		Description:     &desc,
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.ConfigID)
	assert.Equal(t, "pcset-new", ev.ApplicationCode)
	assert.Equal(t, "smtp", ev.Section)
	assert.Equal(t, "host", ev.Property)

	got, err := repo.FindByID(ctx, ev.ConfigID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "mail.example.com", got.Value)
	assert.Equal(t, platformconfig.ScopeGlobal, got.Scope, "no clientId → GLOBAL scope")
	assert.Nil(t, got.ClientID)
	assert.Equal(t, platformconfig.ValuePlain, got.ValueType, "value type defaults to PLAIN")
	require.NotNil(t, got.Description)
	assert.Equal(t, desc, *got.Description)
}

// Second set on the same (app, section, property, scope, clientId)
// coordinate must UPDATE the existing row, not mint a new one.
func TestSetProperty_Upsert_OverwriteExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	first, err := operations.SetProperty(ctx, repo, uow, operations.SetPropertyCommand{
		ApplicationCode: "pcset-upsert", Section: "smtp", Property: "password", Value: "hunter2",
	}, ec)
	require.NoError(t, err)

	secret := "SECRET"
	second, err := operations.SetProperty(ctx, repo, uow, operations.SetPropertyCommand{
		ApplicationCode: "pcset-upsert", Section: "smtp", Property: "password", Value: "correct-horse",
		ValueType: &secret,
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, first.Event().ConfigID, second.Event().ConfigID,
		"upsert must reuse the existing row's id")

	got, err := repo.FindByID(ctx, first.Event().ConfigID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "correct-horse", got.Value, "stored value must change on overwrite")
	assert.Equal(t, platformconfig.ValueSecret, got.ValueType)
}

// All three required fields share the single FIELD_REQUIRED code.
func TestSetProperty_Validation(t *testing.T) {
	t.Parallel()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.SetPropertyCommand
		code string
	}{
		{"missing applicationCode", operations.SetPropertyCommand{Section: "s", Property: "p", Value: "v"}, "FIELD_REQUIRED"},
		{"missing section", operations.SetPropertyCommand{ApplicationCode: "a", Property: "p", Value: "v"}, "FIELD_REQUIRED"},
		{"missing property", operations.SetPropertyCommand{ApplicationCode: "a", Section: "s", Value: "v"}, "FIELD_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.SetProperty(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// ── GrantAccess ───────────────────────────────────────────────────────────

func TestGrantAccess_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	committed, err := operations.GrantAccess(ctx, repo, uow, operations.GrantAccessCommand{
		ApplicationCode: "pcgrant-happy", RoleCode: "auditor", CanWrite: false,
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.AccessID)
	assert.Equal(t, "pcgrant-happy", ev.ApplicationCode)
	assert.Equal(t, "auditor", ev.RoleCode)
	assert.False(t, ev.CanWrite)

	got, err := repo.FindAccessByID(ctx, ev.AccessID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.CanRead, "grant always confers read")
	assert.False(t, got.CanWrite)
}

// Granting the same (app, role) twice must UPDATE the existing grant in
// place — same id, flipped canWrite — not create a duplicate.
func TestGrantAccess_Upsert_DoubleGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	first := mustGrant(t, repo, uow, "pcgrant-upsert", "operator", false)

	second, err := operations.GrantAccess(ctx, repo, uow, operations.GrantAccessCommand{
		ApplicationCode: "pcgrant-upsert", RoleCode: "operator", CanWrite: true,
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, first.AccessID, second.Event().AccessID,
		"double grant must reuse the existing row's id")
	assert.True(t, second.Event().CanWrite)

	got, err := repo.FindAccessByID(ctx, first.AccessID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.CanRead)
	assert.True(t, got.CanWrite, "second grant must escalate canWrite")
}

func TestGrantAccess_Validation(t *testing.T) {
	t.Parallel()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.GrantAccess(context.Background(), repo, uow,
		operations.GrantAccessCommand{RoleCode: "r"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "APPLICATION_REQUIRED")

	_, err = operations.GrantAccess(context.Background(), repo, uow,
		operations.GrantAccessCommand{ApplicationCode: "a"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ROLE_REQUIRED")
}

// ── RevokeAccess ──────────────────────────────────────────────────────────

func TestRevokeAccess_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustGrant(t, repo, uow, "pcrevoke-happy", "ops", true)

	committed, err := operations.RevokeAccess(ctx, repo, uow,
		operations.RevokeAccessCommand{ID: seeded.AccessID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.AccessID, committed.Event().AccessID)
	assert.Equal(t, "pcrevoke-happy", committed.Event().ApplicationCode)
	assert.Equal(t, "ops", committed.Event().RoleCode)

	got, err := repo.FindAccessByID(ctx, seeded.AccessID)
	require.NoError(t, err)
	assert.Nil(t, got, "revoked grant must be gone")
}

func TestRevokeAccess_Errors(t *testing.T) {
	t.Parallel()
	repo := platformconfig.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.RevokeAccess(context.Background(), repo, uow,
		operations.RevokeAccessCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.RevokeAccess(context.Background(), repo, uow,
		operations.RevokeAccessCommand{ID: "pca_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "PlatformConfigAccess_NOT_FOUND")
}
