//go:build integration

package operations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// TestMain seeds FLOWCATALYST_APP_KEY before the embedded-PG boot:
// CONFIDENTIAL client creation / secret rotation encrypt the secret via
// encryption.FromEnv, which reads the env at call time. os.Setenv (not
// t.Setenv) because every test here runs t.Parallel().
func TestMain(m *testing.M) {
	key, err := encryption.GenerateKey()
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("FLOWCATALYST_APP_KEY", key)
	testpg.RunMain(m)
}

// ══ AnchorDomain ══════════════════════════════════════════════════════════

func mustCreateAnchor(t *testing.T, repo *auth.AnchorDomainRepo, uow *usecasepgx.UnitOfWork, domain string) operations.AnchorDomainCreated {
	t.Helper()
	committed, err := operations.CreateAnchorDomain(context.Background(), repo, uow,
		operations.CreateAnchorDomainCommand{Domain: domain}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

func TestCreateAnchorDomain_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).AnchorDomains
	uow := testpg.NewUoW(t)

	committed, err := operations.CreateAnchorDomain(ctx, repo, uow,
		operations.CreateAnchorDomainCommand{Domain: "  ADCreate-Happy.Example.COM  "}, testpg.TestEC())
	require.NoError(t, err)
	ev := committed.Event()
	assert.NotEmpty(t, ev.AnchorDomainID)
	assert.Equal(t, "adcreate-happy.example.com", ev.Domain, "domain is trimmed + lower-cased")

	got, err := repo.FindByID(ctx, ev.AnchorDomainID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "adcreate-happy.example.com", got.Domain)
}

func TestCreateAnchorDomain_Validation(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).AnchorDomains
	uow := testpg.NewUoW(t)

	cases := []struct {
		name, domain, code string
	}{
		{"empty", "", "DOMAIN_REQUIRED"},
		{"no dot", "nodots", "INVALID_DOMAIN"},
		{"embedded space", "has space.com", "INVALID_DOMAIN"},
		{"at sign", "user@example.com", "INVALID_DOMAIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateAnchorDomain(context.Background(), repo, uow,
				operations.CreateAnchorDomainCommand{Domain: tc.domain}, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

func TestCreateAnchorDomain_Duplicate_Conflict(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).AnchorDomains
	uow := testpg.NewUoW(t)
	mustCreateAnchor(t, repo, uow, "addup-conflict.example.com")

	// Case-insensitive: the lookup lower-cases, so a re-cased dup still conflicts.
	_, err := operations.CreateAnchorDomain(context.Background(), repo, uow,
		operations.CreateAnchorDomainCommand{Domain: "ADDup-Conflict.Example.com"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "DOMAIN_EXISTS")
}

func TestUpdateAnchorDomain_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).AnchorDomains
	uow := testpg.NewUoW(t)
	seeded := mustCreateAnchor(t, repo, uow, "adupd-before.example.com")

	committed, err := operations.UpdateAnchorDomain(ctx, repo, uow, operations.UpdateAnchorDomainCommand{
		ID: seeded.AnchorDomainID, Domain: "ADUpd-After.Example.com",
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.AnchorDomainID, committed.Event().AnchorDomainID)
	assert.Equal(t, "adupd-after.example.com", committed.Event().Domain)

	got, err := repo.FindByID(ctx, seeded.AnchorDomainID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "adupd-after.example.com", got.Domain)
}

func TestUpdateAnchorDomain_Errors(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).AnchorDomains
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.UpdateAnchorDomainCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateAnchorDomainCommand{Domain: "x.example.com"}, usecase.KindValidation, "ID_REQUIRED"},
		// Update folds "empty" into INVALID_DOMAIN (create distinguishes DOMAIN_REQUIRED).
		{"empty domain", operations.UpdateAnchorDomainCommand{ID: "and_doesnotexist1"}, usecase.KindValidation, "INVALID_DOMAIN"},
		{"no dot", operations.UpdateAnchorDomainCommand{ID: "and_doesnotexist1", Domain: "nodots"}, usecase.KindValidation, "INVALID_DOMAIN"},
		{"unknown id", operations.UpdateAnchorDomainCommand{ID: "and_doesnotexist1", Domain: "x.example.com"}, usecase.KindNotFound, "AnchorDomain_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateAnchorDomain(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

func TestDeleteAnchorDomain_HappyPathAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).AnchorDomains
	uow := testpg.NewUoW(t)
	seeded := mustCreateAnchor(t, repo, uow, "addel-happy.example.com")

	committed, err := operations.DeleteAnchorDomain(ctx, repo, uow,
		operations.DeleteAnchorDomainCommand{ID: seeded.AnchorDomainID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, "addel-happy.example.com", committed.Event().Domain)

	got, err := repo.FindByID(ctx, seeded.AnchorDomainID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")

	_, err = operations.DeleteAnchorDomain(ctx, repo, uow,
		operations.DeleteAnchorDomainCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteAnchorDomain(ctx, repo, uow,
		operations.DeleteAnchorDomainCommand{ID: "and_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "AnchorDomain_NOT_FOUND")
}

// ══ AuthConfig ════════════════════════════════════════════════════════════

func mustCreateConfig(t *testing.T, repo *auth.ClientAuthConfigRepo, uow *usecasepgx.UnitOfWork, emailDomain string) operations.AuthConfigCreated {
	t.Helper()
	committed, err := operations.CreateAuthConfig(context.Background(), repo, uow,
		operations.CreateAuthConfigCommand{
			EmailDomain: emailDomain, ConfigType: "CLIENT", AuthProvider: "INTERNAL",
		}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

func TestCreateAuthConfig_HappyPath_OIDC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).ClientAuthConfigs
	uow := testpg.NewUoW(t)

	issuer := "https://idp.accreate-happy.example.com"
	clientID := "accreate-happy-oidc"
	primary := "clt_accreatehappy"
	committed, err := operations.CreateAuthConfig(ctx, repo, uow, operations.CreateAuthConfigCommand{
		EmailDomain:         "ACCreate-Happy.Example.com", // lower-cased by the op
		ConfigType:          "PARTNER",
		AuthProvider:        "OIDC",
		PrimaryClientID:     &primary,
		AdditionalClientIDs: []string{"clt_accreateextra"},
		GrantedClientIDs:    []string{"clt_accreategrant"},
		OIDCIssuerURL:       &issuer,
		OIDCClientID:        &clientID,
		OIDCMultiTenant:     true,
	}, testpg.TestEC())
	require.NoError(t, err)
	ev := committed.Event()
	assert.NotEmpty(t, ev.AuthConfigID)
	assert.Equal(t, "accreate-happy.example.com", ev.EmailDomain)

	got, err := repo.FindByID(ctx, ev.AuthConfigID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "accreate-happy.example.com", got.EmailDomain)
	assert.Equal(t, auth.ConfigPartner, got.ConfigType)
	assert.Equal(t, auth.ProviderOIDC, got.AuthProvider)
	require.NotNil(t, got.PrimaryClientID)
	assert.Equal(t, primary, *got.PrimaryClientID)
	assert.Equal(t, []string{"clt_accreateextra"}, got.AdditionalClientIDs)
	assert.Equal(t, []string{"clt_accreategrant"}, got.GrantedClientIDs)
	require.NotNil(t, got.OIDCIssuerURL)
	assert.Equal(t, issuer, *got.OIDCIssuerURL)
	require.NotNil(t, got.OIDCClientID)
	assert.Equal(t, clientID, *got.OIDCClientID)
	assert.True(t, got.OIDCMultiTenant)
}

func TestCreateAuthConfig_Validation(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).ClientAuthConfigs
	uow := testpg.NewUoW(t)
	issuer := "https://idp.example.com"

	cases := []struct {
		name string
		cmd  operations.CreateAuthConfigCommand
		code string
	}{
		{"empty email domain", operations.CreateAuthConfigCommand{ConfigType: "CLIENT"}, "INVALID_EMAIL_DOMAIN"},
		{"dotless email domain", operations.CreateAuthConfigCommand{EmailDomain: "nodot", ConfigType: "CLIENT"}, "INVALID_EMAIL_DOMAIN"},
		{"bad config type", operations.CreateAuthConfigCommand{EmailDomain: "acval.example.com", ConfigType: "WEIRD"}, "INVALID_CONFIG_TYPE"},
		{"oidc missing issuer", operations.CreateAuthConfigCommand{
			EmailDomain: "acval.example.com", ConfigType: "CLIENT", AuthProvider: "OIDC",
		}, "OIDC_ISSUER_REQUIRED"},
		{"oidc missing client id", operations.CreateAuthConfigCommand{
			EmailDomain: "acval.example.com", ConfigType: "CLIENT", AuthProvider: "OIDC", OIDCIssuerURL: &issuer,
		}, "OIDC_CLIENT_ID_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateAuthConfig(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

func TestCreateAuthConfig_Duplicate_Conflict(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).ClientAuthConfigs
	uow := testpg.NewUoW(t)
	mustCreateConfig(t, repo, uow, "acdup-conflict.example.com")

	_, err := operations.CreateAuthConfig(context.Background(), repo, uow, operations.CreateAuthConfigCommand{
		EmailDomain: "ACDup-Conflict.Example.com", ConfigType: "CLIENT", AuthProvider: "INTERNAL",
	}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "DOMAIN_ALREADY_CONFIGURED")
}

func TestUpdateAuthConfig_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).ClientAuthConfigs
	uow := testpg.NewUoW(t)
	seeded := mustCreateConfig(t, repo, uow, "acupd-happy.example.com")

	primary := "clt_acupdprimary"
	multi := true
	committed, err := operations.UpdateAuthConfig(ctx, repo, uow, operations.UpdateAuthConfigCommand{
		ID:               seeded.AuthConfigID,
		PrimaryClientID:  &primary,
		GrantedClientIDs: []string{"clt_acupdgrant"},
		OIDCMultiTenant:  &multi,
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.AuthConfigID, committed.Event().AuthConfigID)
	assert.Equal(t, "acupd-happy.example.com", committed.Event().EmailDomain,
		"email domain is immutable on update")

	got, err := repo.FindByID(ctx, seeded.AuthConfigID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.PrimaryClientID)
	assert.Equal(t, primary, *got.PrimaryClientID)
	assert.Equal(t, []string{"clt_acupdgrant"}, got.GrantedClientIDs)
	assert.True(t, got.OIDCMultiTenant)
	assert.Equal(t, auth.ProviderInternal, got.AuthProvider, "unset fields stay untouched")
}

func TestUpdateAuthConfig_Errors(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).ClientAuthConfigs
	uow := testpg.NewUoW(t)

	_, err := operations.UpdateAuthConfig(context.Background(), repo, uow,
		operations.UpdateAuthConfigCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.UpdateAuthConfig(context.Background(), repo, uow,
		operations.UpdateAuthConfigCommand{ID: "cac_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "AuthConfig_NOT_FOUND")
}

func TestDeleteAuthConfig_HappyPathAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).ClientAuthConfigs
	uow := testpg.NewUoW(t)
	seeded := mustCreateConfig(t, repo, uow, "acdel-happy.example.com")

	committed, err := operations.DeleteAuthConfig(ctx, repo, uow,
		operations.DeleteAuthConfigCommand{ID: seeded.AuthConfigID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, "acdel-happy.example.com", committed.Event().EmailDomain)

	got, err := repo.FindByID(ctx, seeded.AuthConfigID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")

	_, err = operations.DeleteAuthConfig(ctx, repo, uow,
		operations.DeleteAuthConfigCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteAuthConfig(ctx, repo, uow,
		operations.DeleteAuthConfigCommand{ID: "cac_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "AuthConfig_NOT_FOUND")
}

// ══ IdpRoleMapping ════════════════════════════════════════════════════════

func TestCreateIdpRoleMapping_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).IdpRoleMappings
	uow := testpg.NewUoW(t)

	committed, err := operations.CreateIdpRoleMapping(ctx, repo, uow, operations.CreateIdpRoleMappingCommand{
		IdpType: "keycloak", IdpRoleName: "irm-create-upstream", PlatformRoleName: "irmcreate:admin",
	}, testpg.TestEC())
	require.NoError(t, err)
	ev := committed.Event()
	assert.NotEmpty(t, ev.MappingID)
	assert.Equal(t, "keycloak", ev.IdpType)
	assert.Equal(t, "irm-create-upstream", ev.IdpRoleName)
	assert.Equal(t, "irmcreate:admin", ev.PlatformRoleName)

	got, err := repo.FindByID(ctx, ev.MappingID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "irm-create-upstream", got.IdpRoleName)
	assert.Equal(t, "irmcreate:admin", got.PlatformRoleName)
	// KNOWN GAP: oauth_idp_role_mappings has no idp_type column (matches
	// Rust — it was dropped); the repo ignores it on persist and reads ""
	// back. Pin actual behavior; the event above still carries the input.
	assert.Empty(t, got.IdpType, "idp_type is not persisted (no DB column)")
}

func TestCreateIdpRoleMapping_Validation(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).IdpRoleMappings
	uow := testpg.NewUoW(t)

	// All three fields share the one FIELD_REQUIRED code.
	cases := []struct {
		name string
		cmd  operations.CreateIdpRoleMappingCommand
	}{
		{"missing idpType", operations.CreateIdpRoleMappingCommand{IdpRoleName: "r", PlatformRoleName: "p"}},
		{"missing idpRoleName", operations.CreateIdpRoleMappingCommand{IdpType: "t", PlatformRoleName: "p"}},
		{"missing platformRoleName", operations.CreateIdpRoleMappingCommand{IdpType: "t", IdpRoleName: "r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateIdpRoleMapping(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, "FIELD_REQUIRED")
		})
	}
}

func TestDeleteIdpRoleMapping_HappyPathAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).IdpRoleMappings
	uow := testpg.NewUoW(t)

	seeded, err := operations.CreateIdpRoleMapping(ctx, repo, uow, operations.CreateIdpRoleMappingCommand{
		IdpType: "entra", IdpRoleName: "irm-delete-upstream", PlatformRoleName: "irmdelete:viewer",
	}, testpg.TestEC())
	require.NoError(t, err)

	committed, err := operations.DeleteIdpRoleMapping(ctx, repo, uow,
		operations.DeleteIdpRoleMappingCommand{ID: seeded.Event().MappingID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, "irm-delete-upstream", committed.Event().IdpRoleName)

	got, err := repo.FindByID(ctx, seeded.Event().MappingID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")

	_, err = operations.DeleteIdpRoleMapping(ctx, repo, uow,
		operations.DeleteIdpRoleMappingCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteIdpRoleMapping(ctx, repo, uow,
		operations.DeleteIdpRoleMappingCommand{ID: "irm_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "IdpRoleMapping_NOT_FOUND")
}

// ══ OAuthClient ═══════════════════════════════════════════════════════════

func mustCreateClient(t *testing.T, repo *auth.OAuthClientRepo, uow *usecasepgx.UnitOfWork, clientID, name, clientType string) operations.OAuthClientCreated {
	t.Helper()
	committed, err := operations.CreateOAuthClient(context.Background(), repo, uow,
		operations.CreateOAuthClientCommand{ClientID: clientID, ClientName: name, ClientType: clientType},
		testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

func TestCreateOAuthClient_PublicHappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)

	committed, err := operations.CreateOAuthClient(ctx, repo, uow, operations.CreateOAuthClientCommand{
		ClientID:               "oc-create-public",
		ClientName:             "Public SPA",
		ClientType:             "PUBLIC",
		RedirectURIs:           []string{"https://spa.example.com/callback"},
		PostLogoutRedirectURIs: []string{"https://spa.example.com/bye"},
		GrantTypes:             []string{"authorization_code"},
		Scopes:                 []string{"openid", "profile"},
		AllowedOrigins:         []string{"https://spa.example.com"},
		ApplicationIDs:         []string{"app_occreatepub01"},
	}, testpg.TestEC())
	require.NoError(t, err)
	ev := committed.Event()
	assert.NotEmpty(t, ev.OAuthClientID)
	assert.Equal(t, "oc-create-public", ev.ClientID)
	assert.Equal(t, "Public SPA", ev.ClientName)

	got, err := repo.FindByID(ctx, ev.OAuthClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, auth.OAuthClientPublic, got.ClientType)
	assert.True(t, got.Active)
	assert.True(t, got.PKCERequired, "PKCE defaults to required")
	assert.Equal(t, []string{"https://spa.example.com/callback"}, got.RedirectURIs)
	assert.Equal(t, []string{"https://spa.example.com/bye"}, got.PostLogoutRedirectURIs)
	assert.Equal(t, []string{"authorization_code"}, got.GrantTypes)
	assert.ElementsMatch(t, []string{"openid", "profile"}, got.Scopes)
	assert.Equal(t, []string{"https://spa.example.com"}, got.AllowedOrigins)
	assert.Equal(t, []string{"app_occreatepub01"}, got.ApplicationIDs)

	// PUBLIC clients mint no secret: no ref at rest, nothing stashed.
	assert.Nil(t, got.SecretRef)
	_, ok := operations.PopStashedSecret(ev.OAuthClientID)
	assert.False(t, ok, "PUBLIC create must not stash a secret")
}

func TestCreateOAuthClient_ConfidentialSecretStash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)

	ev := mustCreateClient(t, repo, uow, "oc-create-conf", "Server Client", "CONFIDENTIAL")

	// One-shot stash, keyed by the row id: first pop returns the plaintext,
	// second pop misses — the handler's "show it exactly once" contract.
	plaintext, ok := operations.PopStashedSecret(ev.OAuthClientID)
	require.True(t, ok, "first pop must return the minted secret")
	assert.NotEmpty(t, plaintext)

	again, ok := operations.PopStashedSecret(ev.OAuthClientID)
	assert.False(t, ok, "second pop must miss — stash is one-shot")
	assert.Empty(t, again)

	// At rest: "encrypted:"-prefixed envelope (Rust wire parity) that
	// decrypts back to the popped plaintext (decrypt-and-compare contract).
	got, err := repo.FindByID(ctx, ev.OAuthClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, auth.OAuthClientConfidential, got.ClientType)
	require.NotNil(t, got.SecretRef)
	assert.True(t, strings.HasPrefix(*got.SecretRef, "encrypted:"))
	enc, err := encryption.FromEnv()
	require.NoError(t, err)
	require.NotNil(t, enc)
	decrypted, err := enc.Decrypt(*got.SecretRef)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestCreateOAuthClient_Validation(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)

	_, err := operations.CreateOAuthClient(context.Background(), repo, uow,
		operations.CreateOAuthClientCommand{ClientName: "X"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "CLIENT_ID_REQUIRED")

	_, err = operations.CreateOAuthClient(context.Background(), repo, uow,
		operations.CreateOAuthClientCommand{ClientID: "oc-val-noname"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "CLIENT_NAME_REQUIRED")
}

func TestCreateOAuthClient_DuplicateClientID_Conflict(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	mustCreateClient(t, repo, uow, "oc-dup-conflict", "First", "PUBLIC")

	_, err := operations.CreateOAuthClient(context.Background(), repo, uow,
		operations.CreateOAuthClientCommand{ClientID: "oc-dup-conflict", ClientName: "Second"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "CLIENT_ID_EXISTS")
}

func TestUpdateOAuthClient_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	seeded := mustCreateClient(t, repo, uow, "oc-upd-happy", "Before", "PUBLIC")

	newName := "  After  "
	pkce := false
	committed, err := operations.UpdateOAuthClient(ctx, repo, uow, operations.UpdateOAuthClientCommand{
		ID:           seeded.OAuthClientID,
		ClientName:   &newName,
		RedirectURIs: []string{"https://after.example.com/cb"},
		PKCERequired: &pkce,
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.OAuthClientID, committed.Event().OAuthClientID)
	assert.Equal(t, "After", committed.Event().ClientName, "name is trimmed")

	got, err := repo.FindByID(ctx, seeded.OAuthClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "After", got.ClientName)
	assert.Equal(t, []string{"https://after.example.com/cb"}, got.RedirectURIs)
	assert.False(t, got.PKCERequired)
	assert.Equal(t, "oc-upd-happy", got.ClientID, "client_id is immutable on update")
}

func TestUpdateOAuthClient_Errors(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	name := "X"
	blank := " "

	cases := []struct {
		name string
		cmd  operations.UpdateOAuthClientCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateOAuthClientCommand{ClientName: &name}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank name", operations.UpdateOAuthClientCommand{ID: "oac_doesnotexist1", ClientName: &blank}, usecase.KindValidation, "CLIENT_NAME_REQUIRED"},
		{"unknown id", operations.UpdateOAuthClientCommand{ID: "oac_doesnotexist1", ClientName: &name}, usecase.KindNotFound, "OAuthClient_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateOAuthClient(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

func TestDeactivateActivateOAuthClient_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	seeded := mustCreateClient(t, repo, uow, "oc-actcycle", "Cycle", "PUBLIC")

	deactivated, err := operations.DeactivateOAuthClient(ctx, repo, uow,
		operations.DeactivateOAuthClientCommand{ID: seeded.OAuthClientID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.OAuthClientID, deactivated.Event().OAuthClientID)

	got, err := repo.FindByID(ctx, seeded.OAuthClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.Active, "deactivate must flip Active → false")

	activated, err := operations.ActivateOAuthClient(ctx, repo, uow,
		operations.ActivateOAuthClientCommand{ID: seeded.OAuthClientID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.OAuthClientID, activated.Event().OAuthClientID)

	got, err = repo.FindByID(ctx, seeded.OAuthClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Active, "activate must flip Active → true")
}

func TestActivateDeactivateOAuthClient_Errors(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	ctx := context.Background()

	_, err := operations.ActivateOAuthClient(ctx, repo, uow,
		operations.ActivateOAuthClientCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")
	_, err = operations.ActivateOAuthClient(ctx, repo, uow,
		operations.ActivateOAuthClientCommand{ID: "oac_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "OAuthClient_NOT_FOUND")

	_, err = operations.DeactivateOAuthClient(ctx, repo, uow,
		operations.DeactivateOAuthClientCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")
	_, err = operations.DeactivateOAuthClient(ctx, repo, uow,
		operations.DeactivateOAuthClientCommand{ID: "oac_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "OAuthClient_NOT_FOUND")
}

func TestDeleteOAuthClient_HappyPathAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	seeded := mustCreateClient(t, repo, uow, "oc-del-happy", "Doomed", "PUBLIC")

	committed, err := operations.DeleteOAuthClient(ctx, repo, uow,
		operations.DeleteOAuthClientCommand{ID: seeded.OAuthClientID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, "oc-del-happy", committed.Event().ClientID)

	got, err := repo.FindByID(ctx, seeded.OAuthClientID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")

	_, err = operations.DeleteOAuthClient(ctx, repo, uow,
		operations.DeleteOAuthClientCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteOAuthClient(ctx, repo, uow,
		operations.DeleteOAuthClientCommand{ID: "oac_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "OAuthClient_NOT_FOUND")
}

func TestRotateOAuthClientSecret_HappyPathAndStash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	seeded := mustCreateClient(t, repo, uow, "oc-rotate-happy", "Rotate Me", "CONFIDENTIAL")

	// Drain the create-time stash so the pops below see only the rotation.
	createSecret, ok := operations.PopStashedSecret(seeded.OAuthClientID)
	require.True(t, ok)
	before, err := repo.FindByID(ctx, seeded.OAuthClientID)
	require.NoError(t, err)
	require.NotNil(t, before.SecretRef)
	oldRef := *before.SecretRef

	committed, err := operations.RotateOAuthClientSecret(ctx, repo, uow,
		operations.RotateOAuthClientSecretCommand{ID: seeded.OAuthClientID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.OAuthClientID, committed.Event().OAuthClientID)

	rotated, ok := operations.PopStashedSecret(seeded.OAuthClientID)
	require.True(t, ok, "first pop must return the rotated secret")
	assert.NotEmpty(t, rotated)
	assert.NotEqual(t, createSecret, rotated, "rotation must mint a NEW secret")

	_, ok = operations.PopStashedSecret(seeded.OAuthClientID)
	assert.False(t, ok, "second pop must miss — stash is one-shot")

	after, err := repo.FindByID(ctx, seeded.OAuthClientID)
	require.NoError(t, err)
	require.NotNil(t, after.SecretRef)
	assert.NotEqual(t, oldRef, *after.SecretRef, "stored ref must change on rotation")
	assert.True(t, strings.HasPrefix(*after.SecretRef, "encrypted:"))
	enc, err := encryption.FromEnv()
	require.NoError(t, err)
	require.NotNil(t, enc)
	decrypted, err := enc.Decrypt(*after.SecretRef)
	require.NoError(t, err)
	assert.Equal(t, rotated, decrypted)
}

func TestRotateOAuthClientSecret_PublicClient_Conflict(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)
	seeded := mustCreateClient(t, repo, uow, "oc-rotate-public", "No Secret", "PUBLIC")

	_, err := operations.RotateOAuthClientSecret(context.Background(), repo, uow,
		operations.RotateOAuthClientSecretCommand{ID: seeded.OAuthClientID}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "NOT_CONFIDENTIAL")
}

func TestRotateOAuthClientSecret_Errors(t *testing.T) {
	t.Parallel()
	repo := auth.NewRepository(testpg.Pool(t)).OAuthClients
	uow := testpg.NewUoW(t)

	_, err := operations.RotateOAuthClientSecret(context.Background(), repo, uow,
		operations.RotateOAuthClientSecretCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.RotateOAuthClientSecret(context.Background(), repo, uow,
		operations.RotateOAuthClientSecretCommand{ID: "oac_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "OAuthClient_NOT_FOUND")
}
