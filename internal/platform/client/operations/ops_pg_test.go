//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/client"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/client/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustCreate seeds a client through the public operation — the same path
// production uses. Identifiers are hand-unique per test: the fixture never
// truncates between tests, so tests own their rows and never assert
// table-wide.
func mustCreate(t *testing.T, repo *client.Repository, uow *usecasepgx.UnitOfWork, name, identifier string) operations.ClientCreated {
	t.Helper()
	committed, err := operations.CreateClient(context.Background(), repo, uow,
		operations.CreateCommand{Name: name, Identifier: identifier}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── Create ────────────────────────────────────────────────────────────────

func TestCreateClient_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	committed, err := operations.CreateClient(ctx, repo, uow, operations.CreateCommand{
		Name:       "  Acme Corp  ",
		Identifier: "  CL-Create-Happy  ",
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.ClientID)
	assert.Equal(t, "Acme Corp", ev.Name, "name is trimmed")
	assert.Equal(t, "cl-create-happy", ev.Identifier, "identifier is lowercased + trimmed")

	got, err := repo.FindByID(ctx, ev.ClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Acme Corp", got.Name)
	assert.Equal(t, "cl-create-happy", got.Identifier)
	assert.Equal(t, client.StatusActive, got.Status)
	assert.Nil(t, got.StatusReason)
	assert.Empty(t, got.Notes)
}

func TestCreateClient_Validation(t *testing.T) {
	t.Parallel()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty name", operations.CreateCommand{Identifier: "cl-noname"}, "NAME_REQUIRED"},
		{"empty identifier", operations.CreateCommand{Name: "X"}, "IDENTIFIER_REQUIRED"},
		{"underscore rejected", operations.CreateCommand{Name: "X", Identifier: "my_client"}, "INVALID_IDENTIFIER"},
		{"leading hyphen rejected", operations.CreateCommand{Name: "X", Identifier: "-abc"}, "INVALID_IDENTIFIER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateClient(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// create IS the seed for the second. The duplicate is submitted uppercase
// to pin that uniqueness applies to the normalized (lowercased) identifier.
func TestCreateClient_DuplicateIdentifier_Conflict(t *testing.T) {
	t.Parallel()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreate(t, repo, uow, "First", "cl-dup")

	_, err := operations.CreateClient(context.Background(), repo, uow,
		operations.CreateCommand{Name: "Second", Identifier: "CL-DUP"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "IDENTIFIER_EXISTS")
}

// ── Update ────────────────────────────────────────────────────────────────

func TestUpdateClient_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "Before", "cl-upd-happy")

	newName := "  After  "
	committed, err := operations.UpdateClient(ctx, repo, uow, operations.UpdateCommand{
		ID: seeded.ClientID, Name: &newName,
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ClientID, committed.Event().ClientID)
	assert.Equal(t, "After", committed.Event().Name, "name is trimmed")

	got, err := repo.FindByID(ctx, seeded.ClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "After", got.Name)
	assert.Equal(t, "cl-upd-happy", got.Identifier, "identifier is immutable on update")
}

func TestUpdateClient_Errors(t *testing.T) {
	t.Parallel()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	blank := " "
	name := "X"
	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: &name}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank name", operations.UpdateCommand{ID: "cli_doesnotexist1", Name: &blank}, usecase.KindValidation, "NAME_REQUIRED"},
		{"unknown id", operations.UpdateCommand{ID: "cli_doesnotexist1", Name: &name}, usecase.KindNotFound, "Client_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateClient(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteClient_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "Doomed", "cl-del-happy")

	committed, err := operations.DeleteClient(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.ClientID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ClientID, committed.Event().ClientID)
	assert.Equal(t, "cl-del-happy", committed.Event().Identifier)

	got, err := repo.FindByID(ctx, seeded.ClientID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

func TestDeleteClient_Errors(t *testing.T) {
	t.Parallel()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteClient(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteClient(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "cli_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Client_NOT_FOUND")
}

// ── Suspend → Activate (status round-trip) ────────────────────────────────

func TestSuspendThenActivateClient_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "Suspend Me", "cl-susp-happy")

	suspended, err := operations.SuspendClient(ctx, repo, uow, operations.SuspendCommand{
		ID: seeded.ClientID, Reason: "billing overdue",
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ClientID, suspended.Event().ClientID)
	assert.Equal(t, "billing overdue", suspended.Event().Reason)

	got, err := repo.FindByID(ctx, seeded.ClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, client.StatusSuspended, got.Status)
	require.NotNil(t, got.StatusReason)
	assert.Equal(t, "billing overdue", *got.StatusReason)
	assert.NotNil(t, got.StatusChangedAt)

	activated, err := operations.ActivateClient(ctx, repo, uow,
		operations.ActivateCommand{ID: seeded.ClientID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ClientID, activated.Event().ClientID)

	got, err = repo.FindByID(ctx, seeded.ClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, client.StatusActive, got.Status)
	assert.Nil(t, got.StatusReason, "activation clears the suspension reason")
}

func TestSuspendClient_Errors(t *testing.T) {
	t.Parallel()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.SuspendCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.SuspendCommand{Reason: "r"}, usecase.KindValidation, "ID_REQUIRED"},
		{"missing reason", operations.SuspendCommand{ID: "cli_doesnotexist1"}, usecase.KindValidation, "REASON_REQUIRED"},
		{"unknown id", operations.SuspendCommand{ID: "cli_doesnotexist1", Reason: "r"}, usecase.KindNotFound, "Client_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.SuspendClient(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

func TestActivateClient_Errors(t *testing.T) {
	t.Parallel()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.ActivateClient(context.Background(), repo, uow,
		operations.ActivateCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.ActivateClient(context.Background(), repo, uow,
		operations.ActivateCommand{ID: "cli_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "Client_NOT_FOUND")
}

// ── AddNote ───────────────────────────────────────────────────────────────

func TestAddNote_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()
	seeded := mustCreate(t, repo, uow, "Note Me", "cl-note-happy")

	committed, err := operations.AddNote(ctx, repo, uow, operations.AddNoteCommand{
		ClientID: seeded.ClientID, Category: "billing", Text: "switched to annual plan",
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, seeded.ClientID, committed.Event().ClientID)
	assert.Equal(t, "billing", committed.Event().Category)
	assert.Equal(t, "switched to annual plan", committed.Event().Text)

	got, err := repo.FindByID(ctx, seeded.ClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.Notes, 1, "note must persist on reload")
	assert.Equal(t, "billing", got.Notes[0].Category)
	assert.Equal(t, "switched to annual plan", got.Notes[0].Text)
	require.NotNil(t, got.Notes[0].AddedBy)
	assert.Equal(t, ec.PrincipalID, *got.Notes[0].AddedBy)
	assert.False(t, got.Notes[0].AddedAt.IsZero())
}

func TestAddNote_Errors(t *testing.T) {
	t.Parallel()
	repo := client.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.AddNoteCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.AddNoteCommand{Category: "c", Text: "t"}, usecase.KindValidation, "ID_REQUIRED"},
		{"missing category", operations.AddNoteCommand{ClientID: "cli_doesnotexist1", Text: "t"}, usecase.KindValidation, "CATEGORY_REQUIRED"},
		{"missing text", operations.AddNoteCommand{ClientID: "cli_doesnotexist1", Category: "c"}, usecase.KindValidation, "TEXT_REQUIRED"},
		{"unknown id", operations.AddNoteCommand{ClientID: "cli_doesnotexist1", Category: "c", Text: "t"}, usecase.KindNotFound, "Client_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.AddNote(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}
