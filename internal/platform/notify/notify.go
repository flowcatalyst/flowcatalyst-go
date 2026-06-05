// Package notify sends best-effort security-notification emails — "your
// password changed", "a new passkey was registered", "2FA was reset", etc.
//
// Every send is best-effort: a delivery failure is logged, never returned, so
// a notification can never block the security action that triggered it. A nil
// *Notifier is a valid no-op, so call sites can stay unconditional.
package notify

import (
	"context"
	"log/slog"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/email"
)

// Notifier renders + sends security notifications over an email.Service.
type Notifier struct{ svc email.Service }

// New wires a Notifier. svc is typically email.FromEnv() (a LogService when
// SMTP isn't configured, so this still "works" in dev).
func New(svc email.Service) *Notifier { return &Notifier{svc: svc} }

// send is the best-effort core: nil receiver / nil service → no-op; errors are
// logged and swallowed.
func (n *Notifier) send(ctx context.Context, to, subject, body string) {
	if n == nil || n.svc == nil || to == "" {
		return
	}
	if err := n.svc.Send(ctx, email.Message{To: to, Subject: subject, HTMLBody: body}); err != nil {
		slog.Warn("security notification not delivered", "to", to, "subject", subject, "err", err)
	}
}

const footer = "<p style=\"color:#888;font-size:12px\">If this wasn't you, " +
	"contact your administrator immediately.</p>"

// AccountCreated welcomes a newly-created internal user.
func (n *Notifier) AccountCreated(ctx context.Context, to string) {
	n.send(ctx, to, "Your account has been created",
		"<p>Your FlowCatalyst account has been created.</p>"+
			"<p>Sign in to get started. If two-factor authentication is required "+
			"for your organisation, you'll be guided through setting it up.</p>")
}

// PasswordChanged confirms a password change/reset.
func (n *Notifier) PasswordChanged(ctx context.Context, to string) {
	n.send(ctx, to, "Your password was changed",
		"<p>Your FlowCatalyst password was just changed.</p>"+footer)
}

// TwoFactorEnrolled confirms a new second factor was added.
func (n *Notifier) TwoFactorEnrolled(ctx context.Context, to, method string) {
	n.send(ctx, to, "Two-factor authentication enabled",
		"<p>A new two-factor method ("+methodLabel(method)+") was added to your "+
			"account.</p>"+footer)
}

// TwoFactorMethodRemoved confirms a second factor was removed.
func (n *Notifier) TwoFactorMethodRemoved(ctx context.Context, to, method string) {
	n.send(ctx, to, "Two-factor method removed",
		"<p>A two-factor method ("+methodLabel(method)+") was removed from your "+
			"account.</p>"+footer)
}

// TwoFactorReset tells the user their 2FA was cleared (admin reset / lost
// device) and must be set up again.
func (n *Notifier) TwoFactorReset(ctx context.Context, to string) {
	n.send(ctx, to, "Two-factor authentication was reset",
		"<p>Your two-factor authentication has been reset. You'll be asked to set "+
			"it up again the next time you sign in.</p>"+footer)
}

// RecoveryCodesRegenerated confirms a fresh recovery-code set was issued.
func (n *Notifier) RecoveryCodesRegenerated(ctx context.Context, to string) {
	n.send(ctx, to, "New recovery codes generated",
		"<p>A new set of two-factor recovery codes was generated for your account. "+
			"Your previous codes no longer work.</p>"+footer)
}

// RecoveryCodeUsed warns that a backup code was used to sign in.
func (n *Notifier) RecoveryCodeUsed(ctx context.Context, to string) {
	n.send(ctx, to, "A recovery code was used to sign in",
		"<p>One of your two-factor recovery codes was just used to sign in.</p>"+footer)
}

// NewPasskey confirms a passkey was registered.
func (n *Notifier) NewPasskey(ctx context.Context, to string) {
	n.send(ctx, to, "A new passkey was registered",
		"<p>A new passkey (security key / device) was registered to your "+
			"account.</p>"+footer)
}

// NewTrustedDevice confirms a browser was remembered for 2FA.
func (n *Notifier) NewTrustedDevice(ctx context.Context, to, label string) {
	body := "<p>A device was just remembered so it can skip two-factor prompts.</p>"
	if label != "" {
		body += "<p style=\"color:#555\">" + label + "</p>"
	}
	n.send(ctx, to, "A new device was remembered", body+footer)
}

func methodLabel(method string) string {
	switch method {
	case "TOTP":
		return "authenticator app"
	case "EMAIL_PIN":
		return "email code"
	default:
		return method
	}
}
