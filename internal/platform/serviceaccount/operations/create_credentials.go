package operations

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	platformauth "github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth"
	authops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/validate"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// CreateWithCredentialsResult carries the freshly-minted account plus the
// one-time plaintext secrets. The OAuth client secret + webhook secrets are
// stored hashed/at-rest; the plaintext is only ever returned here, once.
type CreateWithCredentialsResult struct {
	ServiceAccount    *serviceaccount.ServiceAccount
	PrincipalID       string
	OAuthClientRowID  string
	OAuthClientID     string
	OAuthClientSecret string
	AuthToken         string
	SigningSecret     string
}

// CreateServiceAccountWithCredentials creates a service account, its linked
// SERVICE principal, and a confidential OAuth client in a single
// transaction, and mints a webhook bearer token + HMAC signing secret on the
// account. Mirrors the application provision_service_account flow but for
// standalone service-account creation. Returns every identifier + plaintext
// secret so the HTTP handler can present them once.
func CreateServiceAccountWithCredentials(
	ctx context.Context,
	saRepo *serviceaccount.Repository,
	principals *principal.Repository,
	oauthRepo *platformauth.OAuthClientRepo,
	uow *usecasepgx.UnitOfWork,
	cmd CreateCommand,
	ec usecase.ExecutionContext,
) (CreateWithCredentialsResult, error) {
	var zero CreateWithCredentialsResult

	code := strings.ToLower(strings.TrimSpace(cmd.Code))
	if code == "" {
		return zero, usecase.Validation("CODE_REQUIRED", "code is required")
	}
	if !validate.CodePattern.MatchString(code) {
		return zero, usecase.Validation("INVALID_CODE_FORMAT",
			"code must start with a lowercase letter and contain only lowercase alphanumeric and hyphens")
	}
	if strings.TrimSpace(cmd.Name) == "" {
		return zero, usecase.Validation("NAME_REQUIRED", "name is required")
	}
	existing, err := saRepo.FindByCode(ctx, code)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_code failed", err)
	}
	if existing != nil {
		return zero, usecase.Conflict("CODE_EXISTS", "Service account with code '"+code+"' already exists")
	}

	sa := serviceaccount.New(code, strings.TrimSpace(cmd.Name))
	sa.Description = cmd.Description
	sa.Scope = cmd.Scope
	sa.ApplicationID = cmd.ApplicationID
	if cmd.ClientIDs != nil {
		sa.ClientIDs = cmd.ClientIDs
	}
	authToken := generateAuthToken()
	signingSecret := generateSigningSecret()
	sa.WebhookCredentials = serviceaccount.WebhookCredentials{
		AuthType:      serviceaccount.AuthBearer,
		Token:         &authToken,
		SigningSecret: &signingSecret,
	}

	saPrincipal := principal.NewService(sa.ID, sa.Name)

	plaintext, ref, err := generateOAuthClientSecret()
	if err != nil {
		return zero, usecase.Internal("SECRET", "generate client secret failed", err)
	}
	oauthClientID := tsid.Generate(tsid.OAuthClient)
	oc := platformauth.NewOAuthClient(oauthClientID, sa.Name+" Client", platformauth.OAuthClientConfidential)
	oc.SetSecretRef(ref)
	oc.PrincipalID = &saPrincipal.ID
	oc.GrantTypes = []string{"client_credentials", "refresh_token"}
	oc.Scopes = []string{"openid"}

	res := usecasepgx.Run(ctx, uow, func(s *usecasepgx.TxScopedUnitOfWork) usecase.Result[authops.OAuthClientCreated] {
		// 1. Service account.
		if r := usecasepgx.CommitScoped(ctx, s, sa, saRepo,
			NewServiceAccountCreatedEvent(ec, sa.ID, sa.Code, sa.Name), cmd); !usecase.IsSuccess(r) {
			_, e := usecase.Into(r)
			return usecase.Failure[authops.OAuthClientCreated](e)
		}
		// 2. Linked SERVICE principal (persistence detail of SA creation).
		//    When the SA is created for a specific application, confine it
		//    exactly like the application-provision flow: AllApplications=false
		//    plus a single application-access grant, so the token's
		//    `applications` claim carries only that app and
		//    sdksync.requireAppAccess confines its writes. The id is stored
		//    as supplied (no existence check), matching the posture of
		//    iam_service_accounts.application_id on this endpoint.
		persistPrincipal := func(tx pgx.Tx) error {
			return principals.Persist(ctx, saPrincipal, usecasepgx.WrapTxForBootstrap(tx))
		}
		if cmd.ApplicationID != nil && strings.TrimSpace(*cmd.ApplicationID) != "" {
			saPrincipal.AllApplications = false
			saPrincipal.AccessibleApplicationIDs = []string{*cmd.ApplicationID}
			persistPrincipal = func(tx pgx.Tx) error {
				return principal.AppAccessPersister{Repository: principals}.Persist(
					ctx, saPrincipal, usecasepgx.WrapTxForBootstrap(tx))
			}
		}
		if err := s.WithTx(ctx, persistPrincipal); err != nil {
			return usecase.Failure[authops.OAuthClientCreated](
				usecase.Internal("PERSIST", "service principal persist failed", err))
		}
		// 3. Confidential OAuth client (last write → its result is the Run result).
		return usecasepgx.CommitScoped(ctx, s, oc, oauthRepo,
			authops.NewOAuthClientCreatedEvent(ec, oc.ID, oc.ClientID, oc.ClientName), cmd)
	})
	if _, err := usecase.Into(res); err != nil {
		return zero, err
	}

	return CreateWithCredentialsResult{
		ServiceAccount:    sa,
		PrincipalID:       saPrincipal.ID,
		OAuthClientRowID:  oc.ID,
		OAuthClientID:     oc.ClientID,
		OAuthClientSecret: plaintext,
		AuthToken:         authToken,
		SigningSecret:     signingSecret,
	}, nil
}

// generateOAuthClientSecret returns a fresh URL-safe secret + its
// encrypted reference (stored in client_secret_ref; verified at
// /oauth/token by decrypt-and-compare — Rust parity).
func generateOAuthClientSecret() (plaintext, ref string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(b)
	enc, err := encryption.FromEnv()
	if err != nil {
		return "", "", err
	}
	if enc == nil {
		return "", "", errors.New("FLOWCATALYST_APP_KEY not configured; cannot encrypt client secret")
	}
	ref, err = enc.Encrypt(plaintext)
	if err != nil {
		return "", "", err
	}
	return plaintext, ref, nil
}
