//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/connection"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/connection/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustCreate seeds a connection through the public operation — the same
// path production uses. Codes are hand-unique per test: the fixture never
// truncates between tests, so tests own their rows and never assert
// table-wide. ServiceAccountID is not validated yet (TODO wave-3c), so an
// arbitrary id string suffices.
func mustCreate(t *testing.T, repo *connection.Repository, uow *usecasepgx.UnitOfWork, code, name string) operations.ConnectionCreated {
	t.Helper()
	committed, err := operations.CreateConnection(context.Background(), repo, uow,
		operations.CreateCommand{Code: code, Name: name, ServiceAccountID: "sva_conntestseed1"}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── Create ────────────────────────────────────────────────────────────────

func TestCreateConnection_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	desc := "outbound webhook target"
	external := "ext-conncrt-1"
	committed, err := operations.CreateConnection(ctx, repo, uow, operations.CreateCommand{
		Code:             "  CONNCRT-Happy  ", // op must trim + lowercase
		Name:             "  Conn Create Happy  ",
		Description:      &desc,
		ServiceAccountID: "sva_conncrthappy1",
		ExternalID:       &external,
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.ConnectionID)
	assert.Equal(t, "conncrt-happy", ev.Code, "code must be trimmed + lowercased")
	assert.Equal(t, "Conn Create Happy", ev.Name, "name must be trimmed")

	got, err := repo.FindByID(ctx, ev.ConnectionID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "conncrt-happy", got.Code)
	assert.Equal(t, "Conn Create Happy", got.Name)
	assert.Equal(t, connection.StatusActive, got.Status, "new connections start ACTIVE")
	assert.Equal(t, "sva_conncrthappy1", got.ServiceAccountID)
	require.NotNil(t, got.Description)
	assert.Equal(t, desc, *got.Description)
	require.NotNil(t, got.ExternalID)
	assert.Equal(t, external, *got.ExternalID)
	assert.Nil(t, got.ClientID)
}

func TestCreateConnection_Validation(t *testing.T) {
	t.Parallel()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty code", operations.CreateCommand{Name: "X", ServiceAccountID: "sva_x"}, "CODE_REQUIRED"},
		{"code starts with digit", operations.CreateCommand{
			Code: "1conncrt-bad", Name: "X", ServiceAccountID: "sva_x",
		}, "INVALID_CODE_FORMAT"},
		{"code with underscore", operations.CreateCommand{
			Code: "conncrt_bad", Name: "X", ServiceAccountID: "sva_x",
		}, "INVALID_CODE_FORMAT"},
		{"empty name", operations.CreateCommand{
			Code: "conncrt-noname", ServiceAccountID: "sva_x",
		}, "NAME_REQUIRED"},
		{"missing service account", operations.CreateCommand{
			Code: "conncrt-nosa", Name: "X",
		}, "SERVICE_ACCOUNT_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateConnection(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// create IS the seed for the second (both anchor-scoped: nil ClientID).
func TestCreateConnection_DuplicateCode_Conflict(t *testing.T) {
	t.Parallel()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreate(t, repo, uow, "conndup", "First")

	_, err := operations.CreateConnection(context.Background(), repo, uow,
		operations.CreateCommand{Code: "conndup", Name: "Second", ServiceAccountID: "sva_conndup2"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "CODE_EXISTS")
}

// ── Update ────────────────────────────────────────────────────────────────

func TestUpdateConnection_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "connupd-happy", "Before")

	desc := "after"
	external := "ext-connupd-1"
	status := "PAUSED"
	committed, err := operations.UpdateConnection(ctx, repo, uow, operations.UpdateCommand{
		ID:          seeded.ConnectionID,
		Name:        "After",
		Description: &desc,
		ExternalID:  &external,
		Status:      &status,
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ConnectionID, committed.Event().ConnectionID)
	assert.Equal(t, "After", committed.Event().Name)

	got, err := repo.FindByID(ctx, seeded.ConnectionID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "After", got.Name)
	require.NotNil(t, got.Description)
	assert.Equal(t, "after", *got.Description)
	require.NotNil(t, got.ExternalID)
	assert.Equal(t, external, *got.ExternalID)
	assert.Equal(t, connection.StatusPaused, got.Status, "status flip via update must persist")
	assert.Equal(t, "connupd-happy", got.Code, "code is immutable on update")
}

func TestUpdateConnection_Errors(t *testing.T) {
	t.Parallel()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: "X"}, usecase.KindValidation, "ID_REQUIRED"},
		{"missing name", operations.UpdateCommand{ID: "con_doesnotexist1", Name: " "}, usecase.KindValidation, "NAME_REQUIRED"},
		{"unknown id", operations.UpdateCommand{ID: "con_doesnotexist1", Name: "X"}, usecase.KindNotFound, "Connection_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateConnection(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Pause / Activate (status flips) ───────────────────────────────────────

func TestPauseAndActivateConnection_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "connsts-happy", "Flip Me")

	paused, err := operations.PauseConnection(ctx, repo, uow,
		operations.PauseCommand{ID: seeded.ConnectionID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ConnectionID, paused.Event().ConnectionID)
	assert.Equal(t, "Flip Me", paused.Event().Name)

	got, err := repo.FindByID(ctx, seeded.ConnectionID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, connection.StatusPaused, got.Status, "pause must flip ACTIVE → PAUSED")

	activated, err := operations.ActivateConnection(ctx, repo, uow,
		operations.ActivateCommand{ID: seeded.ConnectionID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ConnectionID, activated.Event().ConnectionID)

	got, err = repo.FindByID(ctx, seeded.ConnectionID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, connection.StatusActive, got.Status, "activate must flip PAUSED → ACTIVE")
}

func TestPauseConnection_Errors(t *testing.T) {
	t.Parallel()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.PauseConnection(context.Background(), repo, uow,
		operations.PauseCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.PauseConnection(context.Background(), repo, uow,
		operations.PauseCommand{ID: "con_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Connection_NOT_FOUND")
}

func TestActivateConnection_Errors(t *testing.T) {
	t.Parallel()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.ActivateConnection(context.Background(), repo, uow,
		operations.ActivateCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.ActivateConnection(context.Background(), repo, uow,
		operations.ActivateCommand{ID: "con_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Connection_NOT_FOUND")
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteConnection_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "conndel-happy", "Doomed")

	committed, err := operations.DeleteConnection(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.ConnectionID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ConnectionID, committed.Event().ConnectionID)
	assert.Equal(t, "conndel-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.ConnectionID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

func TestDeleteConnection_Errors(t *testing.T) {
	t.Parallel()
	repo := connection.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteConnection(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteConnection(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "con_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Connection_NOT_FOUND")
}
