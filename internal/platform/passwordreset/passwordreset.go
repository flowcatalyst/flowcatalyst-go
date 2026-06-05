// Package passwordreset is the port of fc-platform/src/password_reset.
// Stores short-lived single-use reset tokens. Writes are infrastructure
// processing (auth/password-reset-request directly inserts; the consume
// step uses DELETE ... RETURNING).
package passwordreset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// Purpose distinguishes a normal password reset from a first-time account
// invite (which carries a longer TTL and a different email template).
type Purpose string

const (
	// PurposeReset is the standard "forgot password" / admin-triggered reset.
	PurposeReset Purpose = "reset"
	// PurposeInvite is the first-time "set your password" account invite.
	PurposeInvite Purpose = "invite"
)

// ParsePurpose is the lenient parser. Unknown → reset.
func ParsePurpose(s string) Purpose {
	if s == string(PurposeInvite) {
		return PurposeInvite
	}
	return PurposeReset
}

// Token is the reset-token record. The plaintext token is sent via
// email; we store only its hash.
type Token struct {
	ID          string  `json:"id"`
	PrincipalID string  `json:"principalId"`
	TokenHash   string  `json:"tokenHash"`
	Purpose     Purpose `json:"purpose"`
	// Reset2FA, when set, clears the user's enrolled second factors on
	// confirm and forces re-enrollment (lost-device recovery path).
	Reset2FA  bool      `json:"reset2fa"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// New constructs a standard reset Token.
func New(principalID, tokenHash string, expiresAt time.Time) *Token {
	return &Token{
		ID:          tsid.Generate(tsid.PasswordResetToken),
		PrincipalID: principalID,
		TokenHash:   tokenHash,
		Purpose:     PurposeReset,
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now().UTC(),
	}
}

// IsExpired reports whether the token's expiry has passed.
func (t *Token) IsExpired() bool { return time.Now().After(t.ExpiresAt) }

// IsValid reports whether the token is not yet expired (single-use is
// enforced at the storage layer by deleting on consume).
func (t *Token) IsValid() bool { return !t.IsExpired() }

// Repository persists tokens. No UoW: this is short-lived session state.
type Repository struct{ pool *pgxpool.Pool }

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Insert persists a new token.
func (r *Repository) Insert(ctx context.Context, t *Token) error {
	purpose := t.Purpose
	if purpose == "" {
		purpose = PurposeReset
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO iam_password_reset_tokens
		     (id, principal_id, token_hash, purpose, reset_2fa, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.PrincipalID, t.TokenHash, string(purpose), t.Reset2FA, t.ExpiresAt, t.CreatedAt)
	return err
}

// Consume deletes the token (single-use) and returns it if present and
// not expired. The DELETE ... RETURNING is race-free.
func (r *Repository) Consume(ctx context.Context, tokenHash string) (*Token, error) {
	row := r.pool.QueryRow(ctx,
		`DELETE FROM iam_password_reset_tokens
		   WHERE token_hash = $1 AND expires_at > NOW()
		 RETURNING id, principal_id, token_hash, purpose, reset_2fa, expires_at, created_at`,
		tokenHash)
	return scanToken(row)
}

// FindByTokenHash returns the token for the given hash WITHOUT consuming it
// (used by the validate endpoint, which must not delete). Returns (nil, nil)
// when absent. Expiry is reported by the caller via Token.IsExpired.
func (r *Repository) FindByTokenHash(ctx context.Context, tokenHash string) (*Token, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, principal_id, token_hash, purpose, reset_2fa, expires_at, created_at
		   FROM iam_password_reset_tokens WHERE token_hash = $1`,
		tokenHash)
	return scanToken(row)
}

// scanToken reads a token row, mapping pgx.ErrNoRows to (nil, nil).
func scanToken(row pgx.Row) (*Token, error) {
	var t Token
	var purpose string
	if err := row.Scan(&t.ID, &t.PrincipalID, &t.TokenHash, &purpose, &t.Reset2FA, &t.ExpiresAt, &t.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan token: %w", err)
	}
	t.Purpose = ParsePurpose(purpose)
	return &t, nil
}

// DeleteByPrincipalID removes every reset token for a principal. Used to
// invalidate outstanding tokens before issuing a new one and after a
// successful reset (single-use across the whole set).
func (r *Repository) DeleteByPrincipalID(ctx context.Context, principalID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM iam_password_reset_tokens WHERE principal_id = $1`, principalID)
	return err
}

// PurgeExpired removes all expired tokens. Run periodically by a janitor.
func (r *Repository) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM iam_password_reset_tokens WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
