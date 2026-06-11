//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/identityprovider/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustCreate seeds an INTERNAL-type IdP through the public operation —
// the same path production uses (INTERNAL needs no OIDC fields). Codes
// are hand-unique per test: the fixture never truncates between tests,
// so tests own their rows and never assert table-wide.
func mustCreate(t *testing.T, repo *identityprovider.Repository, uow *usecasepgx.UnitOfWork, code, name string) operations.IdentityProviderCreated {
	t.Helper()
	committed, err := operations.CreateIdentityProvider(context.Background(), repo, uow,
		operations.CreateCommand{Code: code, Name: name, Type: "INTERNAL"}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── Create ────────────────────────────────────────────────────────────────

func TestCreateIdentityProvider_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := identityprovider.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	issuer := "https://login.idpcrt.example.com/v2.0"
	clientID := "idpcrt-client-id"
	secretRef := "secret-ref-idpcrt"
	pattern := "https://login\\.idpcrt\\.example\\.com/.*"
	committed, err := operations.CreateIdentityProvider(ctx, repo, uow, operations.CreateCommand{
		Code:                "idpcrt-happy",
		Name:                "IdP Create Happy",
		Type:                "OIDC",
		OIDCIssuerURL:       &issuer,
		OIDCClientID:        &clientID,
		OIDCClientSecretRef: &secretRef,
		OIDCMultiTenant:     true,
		OIDCIssuerPattern:   &pattern,
		AllowedEmailDomains: []string{"idpcrt-a.example.com", "idpcrt-b.example.com"},
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.IdentityProviderID)
	assert.Equal(t, "idpcrt-happy", ev.Code)

	got, err := repo.FindByID(ctx, ev.IdentityProviderID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "idpcrt-happy", got.Code)
	assert.Equal(t, "IdP Create Happy", got.Name)
	assert.Equal(t, identityprovider.TypeOIDC, got.Type)
	require.NotNil(t, got.OIDCIssuerURL)
	assert.Equal(t, issuer, *got.OIDCIssuerURL)
	require.NotNil(t, got.OIDCClientID)
	assert.Equal(t, clientID, *got.OIDCClientID)
	require.NotNil(t, got.OIDCClientSecretRef)
	assert.Equal(t, secretRef, *got.OIDCClientSecretRef)
	assert.True(t, got.OIDCMultiTenant)
	require.NotNil(t, got.OIDCIssuerPattern)
	assert.Equal(t, pattern, *got.OIDCIssuerPattern)
	assert.ElementsMatch(t, []string{"idpcrt-a.example.com", "idpcrt-b.example.com"}, got.AllowedEmailDomains)
}

func TestCreateIdentityProvider_Validation(t *testing.T) {
	t.Parallel()
	repo := identityprovider.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	issuer := "https://login.idpcrt.example.com/v2.0"
	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty code", operations.CreateCommand{Name: "X", Type: "INTERNAL"}, "CODE_REQUIRED"},
		{"empty name", operations.CreateCommand{Code: "idpcrt-noname", Type: "INTERNAL"}, "NAME_REQUIRED"},
		{"oidc without issuer", operations.CreateCommand{
			Code: "idpcrt-noissuer", Name: "X", Type: "OIDC",
		}, "OIDC_ISSUER_REQUIRED"},
		{"oidc without client id", operations.CreateCommand{
			Code: "idpcrt-noclient", Name: "X", Type: "OIDC", OIDCIssuerURL: &issuer,
		}, "OIDC_CLIENT_ID_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateIdentityProvider(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// create IS the seed for the second.
func TestCreateIdentityProvider_DuplicateCode_Conflict(t *testing.T) {
	t.Parallel()
	repo := identityprovider.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreate(t, repo, uow, "idpdup", "First")

	_, err := operations.CreateIdentityProvider(context.Background(), repo, uow,
		operations.CreateCommand{Code: "idpdup", Name: "Second", Type: "INTERNAL"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "CODE_EXISTS")
}

// ── Update ────────────────────────────────────────────────────────────────

func TestUpdateIdentityProvider_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := identityprovider.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "idpupd-happy", "Before")

	newName := "  After  " // op must trim
	issuer := "https://login.idpupd.example.com"
	multiTenant := true
	committed, err := operations.UpdateIdentityProvider(ctx, repo, uow, operations.UpdateCommand{
		ID:                  seeded.IdentityProviderID,
		Name:                &newName,
		OIDCIssuerURL:       &issuer,
		OIDCMultiTenant:     &multiTenant,
		AllowedEmailDomains: []string{"idpupd.example.com"},
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.IdentityProviderID, committed.Event().IdentityProviderID)
	assert.Equal(t, "idpupd-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.IdentityProviderID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "After", got.Name, "name must be trimmed")
	assert.Equal(t, "idpupd-happy", got.Code, "code is immutable on update")
	require.NotNil(t, got.OIDCIssuerURL)
	assert.Equal(t, issuer, *got.OIDCIssuerURL)
	assert.True(t, got.OIDCMultiTenant)
	assert.ElementsMatch(t, []string{"idpupd.example.com"}, got.AllowedEmailDomains)
}

func TestUpdateIdentityProvider_Errors(t *testing.T) {
	t.Parallel()
	repo := identityprovider.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	blankName := "  "
	okName := "X"
	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: &okName}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank name when supplied", operations.UpdateCommand{
			ID: "idp_doesnotexist1", Name: &blankName,
		}, usecase.KindValidation, "NAME_REQUIRED"},
		{"unknown id", operations.UpdateCommand{ID: "idp_doesnotexist1", Name: &okName}, usecase.KindNotFound, "IdentityProvider_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateIdentityProvider(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteIdentityProvider_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := identityprovider.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "idpdel-happy", "Doomed")

	committed, err := operations.DeleteIdentityProvider(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.IdentityProviderID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.IdentityProviderID, committed.Event().IdentityProviderID)
	assert.Equal(t, "idpdel-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.IdentityProviderID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

func TestDeleteIdentityProvider_Errors(t *testing.T) {
	t.Parallel()
	repo := identityprovider.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteIdentityProvider(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteIdentityProvider(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "idp_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "IdentityProvider_NOT_FOUND")
}
