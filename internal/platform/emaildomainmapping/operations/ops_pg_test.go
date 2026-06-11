//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustCreate seeds an ANCHOR mapping through the public operation — the
// same path production uses. Domains are hand-unique per test: the fixture
// never truncates between tests, so tests own their rows and never assert
// table-wide. The identityProviderId is NOT validated against the IDP
// table on create, so an arbitrary id string suffices.
func mustCreate(t *testing.T, repo *emaildomainmapping.Repository, uow *usecasepgx.UnitOfWork, domain string) operations.EmailDomainMappingCreated {
	t.Helper()
	committed, err := operations.CreateMapping(context.Background(), repo, uow,
		operations.CreateCommand{
			EmailDomain:        domain,
			IdentityProviderID: "idp_edmtestseed1",
			ScopeType:          "ANCHOR",
		}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── CreateMapping ─────────────────────────────────────────────────────────

func TestCreateMapping_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	primary := "cli_edmcrtprimary"
	tenant := "tenant-edmcrt"
	committed, err := operations.CreateMapping(ctx, repo, uow, operations.CreateCommand{
		EmailDomain:           "EDMCRT-Happy.Example.com", // mixed case: op must lowercase
		IdentityProviderID:    "idp_edmcrthappy1",
		ScopeType:             "CLIENT",
		PrimaryClientID:       &primary,
		AdditionalClientIDs:   []string{"cli_edmcrtadd1", "cli_edmcrtadd2"},
		GrantedClientIDs:      []string{"cli_edmcrtgrant1"},
		RequiredOIDCTenantID:  &tenant,
		AllowedRoleIDs:        []string{"rol_edmcrtrole1"},
		SyncRolesFromIDP:      true,
		Require2FA:            true,
		Allowed2FAMethods:     []string{"TOTP", "EMAIL_PIN"},
		RememberDeviceEnabled: true,
		RememberDeviceDays:    14,
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.MappingID)
	assert.Equal(t, "edmcrt-happy.example.com", ev.EmailDomain, "domain must be lowercased")

	got, err := repo.FindByID(ctx, ev.MappingID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "edmcrt-happy.example.com", got.EmailDomain)
	assert.Equal(t, "idp_edmcrthappy1", got.IdentityProviderID)
	assert.Equal(t, emaildomainmapping.ScopeClient, got.ScopeType)
	require.NotNil(t, got.PrimaryClientID)
	assert.Equal(t, primary, *got.PrimaryClientID)
	require.NotNil(t, got.RequiredOIDCTenantID)
	assert.Equal(t, tenant, *got.RequiredOIDCTenantID)
	assert.ElementsMatch(t, []string{"cli_edmcrtadd1", "cli_edmcrtadd2"}, got.AdditionalClientIDs)
	assert.ElementsMatch(t, []string{"cli_edmcrtgrant1"}, got.GrantedClientIDs)
	assert.ElementsMatch(t, []string{"rol_edmcrtrole1"}, got.AllowedRoleIDs)
	assert.True(t, got.SyncRolesFromIDP)
	assert.True(t, got.Require2FA)
	assert.ElementsMatch(t, []string{"TOTP", "EMAIL_PIN"}, got.Allowed2FAMethods)
	assert.True(t, got.RememberDeviceEnabled)
	assert.Equal(t, 14, got.RememberDeviceDays)
}

func TestCreateMapping_Validation(t *testing.T) {
	t.Parallel()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	primary := "cli_edmcrtval"
	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty domain", operations.CreateCommand{
			IdentityProviderID: "idp_x", ScopeType: "ANCHOR",
		}, "EMAIL_DOMAIN_REQUIRED"},
		{"no dot", operations.CreateCommand{
			EmailDomain: "edmcrtnodot", IdentityProviderID: "idp_x", ScopeType: "ANCHOR",
		}, "INVALID_EMAIL_DOMAIN"},
		{"contains @", operations.CreateCommand{
			EmailDomain: "user@edmcrt.example.com", IdentityProviderID: "idp_x", ScopeType: "ANCHOR",
		}, "INVALID_EMAIL_DOMAIN"},
		{"missing idp", operations.CreateCommand{
			EmailDomain: "edmcrt-noidp.example.com", ScopeType: "ANCHOR",
		}, "IDP_REQUIRED"},
		{"bad scope", operations.CreateCommand{
			EmailDomain: "edmcrt-badscope.example.com", IdentityProviderID: "idp_x", ScopeType: "GLOBAL",
		}, "INVALID_SCOPE_TYPE"},
		{"partner without primary client", operations.CreateCommand{
			EmailDomain: "edmcrt-noprimary.example.com", IdentityProviderID: "idp_x", ScopeType: "PARTNER",
		}, "PRIMARY_CLIENT_REQUIRED"},
		{"unknown 2fa method", operations.CreateCommand{
			EmailDomain: "edmcrt-badmethod.example.com", IdentityProviderID: "idp_x", ScopeType: "CLIENT",
			PrimaryClientID: &primary, Allowed2FAMethods: []string{"SMS"},
		}, "INVALID_2FA_METHOD"},
		{"require2fa without methods", operations.CreateCommand{
			EmailDomain: "edmcrt-nomethod.example.com", IdentityProviderID: "idp_x", ScopeType: "ANCHOR",
			Require2FA: true,
		}, "2FA_METHOD_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateMapping(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself; the second
// create uses a different case to also pin the lowercase-before-lookup.
func TestCreateMapping_DuplicateDomain_Conflict(t *testing.T) {
	t.Parallel()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreate(t, repo, uow, "edmdup.example.com")

	_, err := operations.CreateMapping(context.Background(), repo, uow, operations.CreateCommand{
		EmailDomain:        "EDMDUP.Example.COM",
		IdentityProviderID: "idp_edmduptest1",
		ScopeType:          "ANCHOR",
	}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "DOMAIN_ALREADY_MAPPED")
}

// ── UpdateMapping ─────────────────────────────────────────────────────────

func TestUpdateMapping_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "edmupd-happy.example.com")

	newIDP := "idp_edmupdafter1"
	primary := "cli_edmupdprimary"
	sync := true
	require2FA := true
	rememberOn := true
	days := 7
	committed, err := operations.UpdateMapping(ctx, repo, uow, operations.UpdateCommand{
		ID:                    seeded.MappingID,
		IdentityProviderID:    &newIDP,
		PrimaryClientID:       &primary,
		AllowedRoleIDs:        []string{"rol_edmupdrole1", "rol_edmupdrole2"},
		SyncRolesFromIDP:      &sync,
		Require2FA:            &require2FA,
		Allowed2FAMethods:     []string{"TOTP"},
		RememberDeviceEnabled: &rememberOn,
		RememberDeviceDays:    &days,
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.MappingID, committed.Event().MappingID)
	assert.Equal(t, "edmupd-happy.example.com", committed.Event().EmailDomain)

	got, err := repo.FindByID(ctx, seeded.MappingID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "edmupd-happy.example.com", got.EmailDomain, "domain is immutable on update")
	assert.Equal(t, newIDP, got.IdentityProviderID)
	require.NotNil(t, got.PrimaryClientID)
	assert.Equal(t, primary, *got.PrimaryClientID)
	assert.ElementsMatch(t, []string{"rol_edmupdrole1", "rol_edmupdrole2"}, got.AllowedRoleIDs)
	assert.True(t, got.SyncRolesFromIDP)
	assert.True(t, got.Require2FA)
	assert.ElementsMatch(t, []string{"TOTP"}, got.Allowed2FAMethods)
	assert.True(t, got.RememberDeviceEnabled)
	assert.Equal(t, 7, got.RememberDeviceDays)
}

func TestUpdateMapping_Errors(t *testing.T) {
	t.Parallel()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	emptyIDP := "  "
	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank idp when supplied", operations.UpdateCommand{
			ID: "edm_doesnotexist1", IdentityProviderID: &emptyIDP,
		}, usecase.KindValidation, "INVALID_IDP"},
		{"unknown id", operations.UpdateCommand{ID: "edm_doesnotexist1"}, usecase.KindNotFound, "EmailDomainMapping_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateMapping(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// UpdateMapping re-validates the merged 2FA state after applying the
// command, so the 2FA validation codes are reachable on update too —
// these need a persisted row to get past FindByID.
func TestUpdateMapping_2FAValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "edmupd-2fa.example.com")

	_, err := operations.UpdateMapping(ctx, repo, uow, operations.UpdateCommand{
		ID:                seeded.MappingID,
		Allowed2FAMethods: []string{"SMS"},
	}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "INVALID_2FA_METHOD")

	require2FA := true
	_, err = operations.UpdateMapping(ctx, repo, uow, operations.UpdateCommand{
		ID:         seeded.MappingID,
		Require2FA: &require2FA,
	}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "2FA_METHOD_REQUIRED")
}

// ── DeleteMapping ─────────────────────────────────────────────────────────

func TestDeleteMapping_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "edmdel-happy.example.com")

	committed, err := operations.DeleteMapping(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.MappingID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.MappingID, committed.Event().MappingID)
	assert.Equal(t, "edmdel-happy.example.com", committed.Event().EmailDomain)

	got, err := repo.FindByID(ctx, seeded.MappingID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

func TestDeleteMapping_Errors(t *testing.T) {
	t.Parallel()
	repo := emaildomainmapping.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteMapping(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteMapping(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "edm_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "EmailDomainMapping_NOT_FOUND")
}
