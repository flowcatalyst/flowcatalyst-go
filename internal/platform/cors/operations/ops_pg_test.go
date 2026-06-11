//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/cors"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/cors/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustAdd seeds an origin through the public operation — the same path
// production uses. Origins are hand-unique per test: the fixture never
// truncates between tests, so tests own their rows and never assert
// table-wide.
func mustAdd(t *testing.T, repo *cors.Repository, uow *usecasepgx.UnitOfWork, origin string) operations.CorsOriginAdded {
	t.Helper()
	committed, err := operations.AddOrigin(context.Background(), repo, uow,
		operations.AddCommand{Origin: origin}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── AddOrigin ─────────────────────────────────────────────────────────────

func TestAddOrigin_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := cors.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	desc := "frontend dev origin"
	committed, err := operations.AddOrigin(ctx, repo, uow, operations.AddCommand{
		Origin:      "https://corsadd-happy.example.com:3000",
		Description: &desc,
	}, ec)
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.OriginID)
	assert.Equal(t, "https://corsadd-happy.example.com:3000", ev.Origin)

	got, err := repo.FindByID(ctx, ev.OriginID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "https://corsadd-happy.example.com:3000", got.Origin)
	require.NotNil(t, got.Description)
	assert.Equal(t, desc, *got.Description)
	require.NotNil(t, got.CreatedBy)
	assert.Equal(t, ec.PrincipalID, *got.CreatedBy)
}

func TestAddOrigin_Validation(t *testing.T) {
	t.Parallel()
	repo := cors.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.AddCommand
		code string
	}{
		{"empty origin", operations.AddCommand{}, "ORIGIN_REQUIRED"},
		{"whitespace origin", operations.AddCommand{Origin: "   "}, "ORIGIN_REQUIRED"},
		{"no scheme", operations.AddCommand{Origin: "corsadd-bad.example.com"}, "INVALID_ORIGIN_FORMAT"},
		{"wrong scheme", operations.AddCommand{Origin: "ftp://corsadd-bad.example.com"}, "INVALID_ORIGIN_FORMAT"},
		{"trailing path", operations.AddCommand{Origin: "https://corsadd-bad.example.com/path"}, "INVALID_ORIGIN_FORMAT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.AddOrigin(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// add IS the seed for the second.
func TestAddOrigin_Duplicate_Conflict(t *testing.T) {
	t.Parallel()
	repo := cors.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustAdd(t, repo, uow, "https://corsdup.example.com")

	_, err := operations.AddOrigin(context.Background(), repo, uow,
		operations.AddCommand{Origin: "https://corsdup.example.com"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "ORIGIN_ALREADY_EXISTS")
}

// ── DeleteOrigin ──────────────────────────────────────────────────────────

func TestDeleteOrigin_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := cors.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustAdd(t, repo, uow, "https://corsdel-happy.example.com")

	committed, err := operations.DeleteOrigin(ctx, repo, uow,
		operations.DeleteCommand{OriginID: seeded.OriginID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.OriginID, committed.Event().OriginID)
	assert.Equal(t, "https://corsdel-happy.example.com", committed.Event().Origin)

	got, err := repo.FindByID(ctx, seeded.OriginID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

// DeleteOrigin has no required-id validation in the operation: an empty
// OriginID falls through FindByID and surfaces as NotFound, same as an
// unknown id.
func TestDeleteOrigin_Errors(t *testing.T) {
	t.Parallel()
	repo := cors.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteOrigin(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "CorsOrigin_NOT_FOUND")

	_, err = operations.DeleteOrigin(context.Background(), repo, uow,
		operations.DeleteCommand{OriginID: "cor_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "CorsOrigin_NOT_FOUND")
}
