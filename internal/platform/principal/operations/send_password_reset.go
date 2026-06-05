package operations

import (
	"context"
	"errors"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

// PasswordResetEmailer is the side-effect dependency for
// SendPasswordReset. Implementations create a single-use reset token and
// deliver the link by email; the use case itself is purely about
// eligibility checks so it stays free of email-transport concerns.
//
// Wire an implementation onto principal/api.State.Emailer when the
// platform's mailer config is ready; until then the endpoint surfaces
// the same "not configured" error Rust does.
type PasswordResetEmailer interface {
	// SendResetEmail mints a reset token and emails the link. reset2FA, when
	// true, flags the token to also clear the user's enrolled 2FA on confirm.
	SendResetEmail(ctx context.Context, p *principal.Principal, reset2FA bool) error
}

// SendPasswordResetCommand triggers an admin-side reset email.
type SendPasswordResetCommand struct {
	ID string `json:"id"`
	// Reset2FA also clears the user's enrolled second factors when they
	// complete the reset (lost-device recovery).
	Reset2FA bool `json:"reset2fa"`
}

// SendPasswordReset validates that `id` is a user principal eligible
// for an internal password reset (not OIDC-federated, has an email) and
// asks the configured emailer to dispatch the reset link. Mirrors
// Rust crates/fc-platform/src/principal/api.rs::send_password_reset.
func SendPasswordReset(
	ctx context.Context,
	repo *principal.Repository,
	emailer PasswordResetEmailer,
	cmd SendPasswordResetCommand,
	_ usecase.ExecutionContext,
) error {
	if strings.TrimSpace(cmd.ID) == "" {
		return usecase.Validation("ID_REQUIRED", "id is required")
	}
	if emailer == nil {
		return usecase.Internal("EMAILER_NOT_CONFIGURED", "Password reset emailer not configured", nil)
	}

	p, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return httperror.NotFound("Principal", cmd.ID)
	}

	if !p.IsUser() {
		return usecase.Validation("NOT_USER", "Password reset only applies to user accounts")
	}
	if p.ExternalIdentity != nil {
		return usecase.Validation("OIDC_USER",
			"Cannot send password reset for OIDC-federated users — they manage credentials at their IDP")
	}
	if p.UserIdentity == nil || strings.TrimSpace(p.UserIdentity.Email) == "" {
		return usecase.Validation("NO_EMAIL", "User does not have an email address on file")
	}

	if err := emailer.SendResetEmail(ctx, p, cmd.Reset2FA); err != nil {
		return usecase.Internal("EMAILER", "send_reset_email failed", err)
	}
	return nil
}

// Sentinel so callers can branch on "emailer not wired" if they want a
// nicer UX, rather than relying on the error message text.
var ErrEmailerNotConfigured = errors.New("password reset emailer not configured")
