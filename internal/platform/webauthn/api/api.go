// Package api wires the WebAuthn HTTP routes under /auth/webauthn/* via huma.
//
// Six endpoints mirror fc-platform/src/webauthn/api.rs:
//
//	POST /auth/webauthn/register/begin          — issue registration challenge
//	POST /auth/webauthn/register/complete       — verify attestation, persist
//	POST /auth/webauthn/authenticate/begin      — issue authentication challenge
//	POST /auth/webauthn/authenticate/complete   — verify assertion, set session
//	GET  /auth/webauthn/credentials             — list user's passkeys
//	DELETE /auth/webauthn/credentials/{id}      — revoke a passkey
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-webauthn/webauthn/protocol"
	gowebauthn "github.com/go-webauthn/webauthn/webauthn"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/provider"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/notify"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	platformmw "github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/middleware"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/webauthn"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/webauthn/operations"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// State bundles deps.
type State struct {
	Service    *webauthn.Service
	Principals *principal.Repository
	Creds      *webauthn.Repository
	UoW        *usecasepgx.UnitOfWork

	// Provider mints the fc_session JWT issued on a successful passkey
	// login — the same token platformmw.Authenticator verifies for
	// password and Bearer auth. Required by authenticate/complete to
	// establish a session; without it the browser "logs in" but holds no
	// cookie, so it has no permissions and bounces to /login on reload.
	Provider *provider.Provider
	// CookieSecure flips the session cookie's Secure flag (false on
	// fc-dev's HTTP localhost, true on fc-server's HTTPS). Mirrors
	// login.Config.CookieSecure.
	CookieSecure bool
	// SessionTTL is the session cookie lifetime. Defaults to 24h when zero
	// (matches login.SessionTTL).
	SessionTTL time.Duration
	// Notifier (optional) sends the best-effort "a new passkey was registered"
	// security email after a successful registration.
	Notifier *notify.Notifier
}

const tag = "webauthn"

// Register mounts the WebAuthn endpoints.
func Register(api huma.API, s *State) {
	huma.Register(api, huma.Operation{
		OperationID:   "webauthnRegisterBegin",
		Method:        http.MethodPost,
		Path:          "/auth/webauthn/register/begin",
		Summary:       "Begin a WebAuthn registration ceremony",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.registerBegin)

	huma.Register(api, huma.Operation{
		OperationID:   "webauthnRegisterComplete",
		Method:        http.MethodPost,
		Path:          "/auth/webauthn/register/complete",
		Summary:       "Complete a WebAuthn registration ceremony",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.registerComplete)

	huma.Register(api, huma.Operation{
		OperationID:   "webauthnAuthenticateBegin",
		Method:        http.MethodPost,
		Path:          "/auth/webauthn/authenticate/begin",
		Summary:       "Begin a WebAuthn authentication ceremony",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.authenticateBegin)

	huma.Register(api, huma.Operation{
		OperationID:   "webauthnAuthenticateComplete",
		Method:        http.MethodPost,
		Path:          "/auth/webauthn/authenticate/complete",
		Summary:       "Complete a WebAuthn authentication ceremony",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.authenticateComplete)

	huma.Register(api, huma.Operation{
		OperationID:   "listWebauthnCredentials",
		Method:        http.MethodGet,
		Path:          "/auth/webauthn/credentials",
		Summary:       "List the current user's WebAuthn credentials",
		Tags:          []string{tag},
		DefaultStatus: http.StatusOK,
	}, s.listCredentials)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteWebauthnCredential",
		Method:        http.MethodDelete,
		Path:          "/auth/webauthn/credentials/{id}",
		Summary:       "Revoke a WebAuthn credential",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, s.deleteCredential)
}

// ── register ─────────────────────────────────────────────────────────────

type registerBeginInput struct {
	Body RegisterBeginRequest
}

type registerBeginOutput struct {
	Body RegisterBeginResponse
}

func (s *State) registerBegin(ctx context.Context, in *registerBeginInput) (*registerBeginOutput, error) {
	ac := auth.FromContext(ctx)
	if ac == nil || ac.PrincipalID == "" {
		return nil, usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	p, err := s.Principals.FindByID(ctx, ac.PrincipalID)
	if err != nil || p == nil {
		return nil, httperror.NotFound("Principal", ac.PrincipalID)
	}
	email := ""
	if p.UserIdentity != nil {
		email = p.UserIdentity.Email
	}
	displayName := p.Name
	if in.Body.DisplayName != nil && *in.Body.DisplayName != "" {
		displayName = *in.Body.DisplayName
	}

	existing, err := s.Service.Credentials().LibraryCredentialsByPrincipal(ctx, p.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "list credentials failed", err)
	}
	user := &webauthn.PrincipalUser{
		PrincipalID: p.ID,
		DisplayName: displayName,
		Username:    email,
		Credentials: existing,
	}
	options, sessionData, err := s.Service.WebAuthn().BeginRegistration(user,
		gowebauthn.WithExclusions(credentialDescriptorsFor(existing)))
	if err != nil {
		return nil, usecase.Internal("WEBAUTHN", "begin registration failed", err)
	}
	stateID := newUUID()
	if err := s.Service.Ceremonies().StoreRegistration(ctx, stateID, p.ID, sessionData, &displayName); err != nil {
		return nil, usecase.Internal("REPO", "store ceremony failed", err)
	}
	return &registerBeginOutput{Body: RegisterBeginResponse{StateID: stateID, Options: options}}, nil
}

type registerCompleteInput struct {
	Body RegisterCompleteRequest
}

type registerCompleteOutput struct {
	Body RegisterCompleteResponse
}

func (s *State) registerComplete(ctx context.Context, in *registerCompleteInput) (*registerCompleteOutput, error) {
	ac := auth.FromContext(ctx)
	if ac == nil || ac.PrincipalID == "" {
		return nil, usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}

	// A passkey must carry a non-empty name so the owner can tell their
	// credentials apart when managing/revoking them. Validated before the
	// ceremony state is consumed so a name-less attempt can be retried.
	name := ""
	if in.Body.Name != nil {
		name = strings.TrimSpace(*in.Body.Name)
	}
	if name == "" {
		return nil, httperror.BadRequest("NAME_REQUIRED", "a passkey name is required")
	}

	consumed, err := s.Service.Ceremonies().ConsumeRegistration(ctx, in.Body.StateID)
	if err != nil || consumed == nil {
		return nil, httperror.BadRequest("STATE_NOT_FOUND",
			"registration ceremony state not found or expired")
	}
	if consumed.PrincipalID != ac.PrincipalID {
		return nil, httperror.Forbidden("registration ceremony belongs to a different principal")
	}

	p, err := s.Principals.FindByID(ctx, consumed.PrincipalID)
	if err != nil || p == nil {
		return nil, httperror.NotFound("Principal", consumed.PrincipalID)
	}
	user := &webauthn.PrincipalUser{PrincipalID: p.ID, DisplayName: p.Name}

	parsed, err := protocol.ParseCredentialCreationResponseBody(io.NopCloser(bytes.NewReader(in.Body.Credential)))
	if err != nil {
		return nil, httperror.BadRequest("INVALID_CREDENTIAL", err.Error())
	}
	cred, err := s.Service.WebAuthn().CreateCredential(user, consumed.Session, parsed)
	if err != nil {
		return nil, httperror.BadRequest("ATTESTATION_INVALID", err.Error())
	}

	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.Register(ctx, s.Creds, s.UoW,
		operations.RegisterCommand{StateID: in.Body.StateID, Response: *cred, Name: &name}, ec)
	if err != nil {
		return nil, err
	}
	if p.UserIdentity != nil {
		s.Notifier.NewPasskey(ctx, p.UserIdentity.Email)
	}
	return &registerCompleteOutput{Body: RegisterCompleteResponse{CredentialID: committed.Event().CredentialID}}, nil
}

// ── authenticate ─────────────────────────────────────────────────────────

type authenticateBeginInput struct {
	Body AuthenticateBeginRequest
}

type authenticateBeginOutput struct {
	Body AuthenticateBeginResponse
}

func (s *State) authenticateBegin(ctx context.Context, in *authenticateBeginInput) (*authenticateBeginOutput, error) {
	if in.Body.Email == "" {
		return nil, httperror.BadRequest("EMAIL_REQUIRED", "email is required")
	}
	p, _ := s.Principals.FindByEmail(ctx, in.Body.Email)
	if p == nil || !p.Active {
		return &authenticateBeginOutput{Body: AuthenticateBeginResponse{StateID: newUUID(), Options: decoyChallenge(s.Service.RPID())}}, nil
	}
	creds, err := s.Service.Credentials().LibraryCredentialsByPrincipal(ctx, p.ID)
	if err != nil || len(creds) == 0 {
		return &authenticateBeginOutput{Body: AuthenticateBeginResponse{StateID: newUUID(), Options: decoyChallenge(s.Service.RPID())}}, nil
	}
	user := &webauthn.PrincipalUser{
		PrincipalID: p.ID,
		DisplayName: p.Name,
		Credentials: creds,
	}
	options, sessionData, err := s.Service.WebAuthn().BeginLogin(user)
	if err != nil {
		return nil, usecase.Internal("WEBAUTHN", "begin login failed", err)
	}
	stateID := newUUID()
	if err := s.Service.Ceremonies().StoreAuthentication(ctx, stateID, &p.ID, sessionData); err != nil {
		return nil, usecase.Internal("REPO", "store ceremony failed", err)
	}
	return &authenticateBeginOutput{Body: AuthenticateBeginResponse{StateID: stateID, Options: options}}, nil
}

type authenticateCompleteInput struct {
	Body AuthenticateCompleteRequest
}

type authenticateCompleteOutput struct {
	// SetCookie carries the fc_session cookie so a passkey login
	// establishes a session exactly like POST /auth/login. huma maps this
	// field to the Set-Cookie response header.
	SetCookie string `header:"Set-Cookie"`
	Body      WebauthnAuthenticateCompleteResponse
}

func (s *State) authenticateComplete(ctx context.Context, in *authenticateCompleteInput) (*authenticateCompleteOutput, error) {
	// On success we mint an fc_session JWT and return it as a Set-Cookie,
	// so a passkey login establishes a session exactly like POST
	// /auth/login (and like the Rust webauthn authenticate_complete). The
	// SPA seeds permissions:[] from this response and then loads the real
	// set from /auth/me using the cookie — so the cookie, not the body, is
	// what unblocks the user. Without it the browser appears logged in but
	// has no session: no permissions, and a reload bounces to /login.
	consumed, err := s.Service.Ceremonies().ConsumeAuthentication(ctx, in.Body.StateID)
	if err != nil || consumed == nil || consumed.PrincipalID == nil {
		return nil, invalidCredentialsErr()
	}
	p, err := s.Principals.FindByID(ctx, *consumed.PrincipalID)
	if err != nil || p == nil || !p.Active {
		return nil, invalidCredentialsErr()
	}
	creds, err := s.Service.Credentials().LibraryCredentialsByPrincipal(ctx, p.ID)
	if err != nil || len(creds) == 0 {
		return nil, invalidCredentialsErr()
	}
	user := &webauthn.PrincipalUser{
		PrincipalID: p.ID,
		DisplayName: p.Name,
		Credentials: creds,
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(io.NopCloser(bytes.NewReader(in.Body.Credential)))
	if err != nil {
		return nil, invalidCredentialsErr()
	}
	cred, err := s.Service.WebAuthn().ValidateLogin(user, consumed.Session, parsed)
	if err != nil {
		return nil, invalidCredentialsErr()
	}

	ec := usecase.NewExecutionContext(p.ID)
	// Counter persistence failure is non-fatal; session still issued.
	_, _ = operations.Authenticate(ctx, s.Creds, s.UoW,
		operations.AuthenticateCommand{StateID: in.Body.StateID, UpdatedCredential: *cred}, ec)

	var email *string
	if p.UserIdentity != nil && p.UserIdentity.Email != "" {
		e := p.UserIdentity.Email
		email = &e
	}
	roles := make([]string, 0, len(p.Roles))
	for _, r := range p.Roles {
		roles = append(roles, r.Role)
	}

	// Establish the session: mint the fc_session JWT and serialize it as a
	// Set-Cookie with the same attributes as POST /auth/login.
	if s.Provider == nil {
		return nil, usecase.Internal("WIRING", "session provider not configured", nil)
	}
	ttl := s.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	token, err := s.Provider.MintSessionToken(ctx, p.ID, ttl)
	if err != nil {
		return nil, usecase.Internal("MINT_FAILED", "failed to mint session token", err)
	}
	cookie := http.Cookie{
		Name:     platformmw.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	}

	return &authenticateCompleteOutput{
		SetCookie: cookie.String(),
		Body: WebauthnAuthenticateCompleteResponse{
			PrincipalID: p.ID,
			Email:       email,
			Name:        p.Name,
			Roles:       roles,
		},
	}, nil
}

// ── credentials list / delete ────────────────────────────────────────────

type emptyInput struct{}

type listCredsOutput struct {
	Body []WebauthnCredentialSummary
}

func (s *State) listCredentials(ctx context.Context, _ *emptyInput) (*listCredsOutput, error) {
	ac := auth.FromContext(ctx)
	if ac == nil || ac.PrincipalID == "" {
		return nil, usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	rows, err := s.Service.Credentials().FindByPrincipal(ctx, ac.PrincipalID)
	if err != nil {
		return nil, usecase.Internal("REPO", "list credentials failed", err)
	}
	out := make([]WebauthnCredentialSummary, 0, len(rows))
	for i := range rows {
		out = append(out, credentialSummaryFromEntity(&rows[i]))
	}
	return &listCredsOutput{Body: out}, nil
}

type deleteCredInput struct {
	ID string `path:"id"`
}

type emptyOutput struct{}

func (s *State) deleteCredential(ctx context.Context, in *deleteCredInput) (*emptyOutput, error) {
	ac := auth.FromContext(ctx)
	if ac == nil || ac.PrincipalID == "" {
		return nil, usecase.Authorization("UNAUTHENTICATED", "authentication required")
	}
	// Ownership check: a principal may only revoke their OWN passkeys.
	// Confirm the credential is in the caller's set before revoking, so a
	// caller can't delete another user's credential by guessing its id.
	// (NotFound rather than Forbidden — don't reveal other users' credentials.)
	owned, err := s.Service.Credentials().FindByPrincipal(ctx, ac.PrincipalID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find credentials failed", err)
	}
	found := false
	for i := range owned {
		if owned[i].ID == in.ID {
			found = true
			break
		}
	}
	if !found {
		return nil, httperror.NotFound("Credential", in.ID)
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.Revoke(ctx, s.Creds, s.UoW,
		operations.RevokeCommand{ID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &emptyOutput{}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────

func invalidCredentialsErr() error {
	return usecase.Authorization("INVALID_CREDENTIALS", "Invalid credentials.")
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decoyChallenge builds an assertion challenge for the unknown-user / no-usable-
// credential case so the response is indistinguishable from a real one (anti-
// enumeration) — same shape, a random challenge, the REAL rpId, and one random
// allowCredentials entry (a real non-discoverable challenge lists the user's
// credential ids). It must carry the configured rpId: an empty rpId makes the
// browser throw "The RP ID \"\" is invalid for this domain", which both breaks
// the flow and reveals the decoy. Completion then fails like a wrong passkey.
func decoyChallenge(rpID string) map[string]any {
	chal := make([]byte, 32)
	_, _ = rand.Read(chal)
	fakeCredID := make([]byte, 32)
	_, _ = rand.Read(fakeCredID)
	return map[string]any{
		"publicKey": map[string]any{
			"challenge": base64.RawURLEncoding.EncodeToString(chal),
			"timeout":   60000,
			"rpId":      rpID,
			"allowCredentials": []any{
				map[string]any{
					"type": "public-key",
					"id":   base64.RawURLEncoding.EncodeToString(fakeCredID),
				},
			},
			"userVerification": "preferred",
		},
	}
}

func credentialDescriptorsFor(creds []gowebauthn.Credential) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(creds))
	for _, c := range creds {
		out = append(out, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: c.ID,
		})
	}
	return out
}
