package mfa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository persists 2FA factors, recovery codes, email-PIN challenges and
// trusted devices. Plain pgx (no UoW): these are short-lived auth/session
// state, mirroring the passwordreset repo. The principal FK is ON DELETE
// CASCADE, so deleting a user clears all of their rows automatically.
type Repository struct{ pool *pgxpool.Pool }

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// ── methods ──────────────────────────────────────────────────────────────

// InsertMethod persists a new (typically unconfirmed) factor.
func (r *Repository) InsertMethod(ctx context.Context, m *Method) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO iam_user_mfa_methods
		     (id, principal_id, method, secret_encrypted, confirmed_at, last_used_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		m.ID, m.PrincipalID, string(m.Type), m.SecretEncrypted, m.ConfirmedAt, m.LastUsedAt, m.CreatedAt)
	return err
}

// ConfirmMethod marks a factor enrolled (sets confirmed_at).
func (r *Repository) ConfirmMethod(ctx context.Context, id string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE iam_user_mfa_methods SET confirmed_at = $2 WHERE id = $1`, id, at)
	return err
}

// TouchMethodUsed stamps last_used_at (TOTP replay guard / audit).
func (r *Repository) TouchMethodUsed(ctx context.Context, id string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE iam_user_mfa_methods SET last_used_at = $2 WHERE id = $1`, id, at)
	return err
}

// FindMethod returns the user's factor of the given type, or (nil, nil).
func (r *Repository) FindMethod(ctx context.Context, principalID string, t MethodType) (*Method, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, principal_id, method, secret_encrypted, confirmed_at, last_used_at, created_at
		   FROM iam_user_mfa_methods
		  WHERE principal_id = $1 AND method = $2`,
		principalID, string(t))
	return scanMethod(row)
}

// FindMethodsByPrincipal returns every factor for a user (confirmed or not),
// ordered oldest-first.
func (r *Repository) FindMethodsByPrincipal(ctx context.Context, principalID string) ([]Method, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, principal_id, method, secret_encrypted, confirmed_at, last_used_at, created_at
		   FROM iam_user_mfa_methods
		  WHERE principal_id = $1
		  ORDER BY created_at`,
		principalID)
	if err != nil {
		return nil, fmt.Errorf("find methods: %w", err)
	}
	defer rows.Close()
	var out []Method
	for rows.Next() {
		m, err := scanMethodRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// DeleteMethod removes a single factor by type.
func (r *Repository) DeleteMethod(ctx context.Context, principalID string, t MethodType) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM iam_user_mfa_methods WHERE principal_id = $1 AND method = $2`,
		principalID, string(t))
	return err
}

// DeleteMethodsByPrincipal clears every factor for a user (2FA reset).
func (r *Repository) DeleteMethodsByPrincipal(ctx context.Context, principalID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM iam_user_mfa_methods WHERE principal_id = $1`, principalID)
	return err
}

// ── recovery codes ───────────────────────────────────────────────────────

// InsertRecoveryCodes persists a fresh batch of recovery codes.
func (r *Repository) InsertRecoveryCodes(ctx context.Context, codes []*RecoveryCode) error {
	batch := &pgx.Batch{}
	for _, c := range codes {
		batch.Queue(
			`INSERT INTO iam_user_mfa_recovery_codes (id, principal_id, code_hash, used_at, created_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			c.ID, c.PrincipalID, c.CodeHash, c.UsedAt, c.CreatedAt)
	}
	br := r.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range codes {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert recovery codes: %w", err)
		}
	}
	return nil
}

// FindUnusedRecoveryCode returns an unused recovery code matching the hash, or
// (nil, nil).
func (r *Repository) FindUnusedRecoveryCode(ctx context.Context, principalID, codeHash string) (*RecoveryCode, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, principal_id, code_hash, used_at, created_at
		   FROM iam_user_mfa_recovery_codes
		  WHERE principal_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		principalID, codeHash)
	var c RecoveryCode
	if err := row.Scan(&c.ID, &c.PrincipalID, &c.CodeHash, &c.UsedAt, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find recovery code: %w", err)
	}
	return &c, nil
}

// MarkRecoveryCodeUsed stamps used_at (single-use). The guarded UPDATE is
// race-free: RowsAffected==0 means it was already consumed.
func (r *Repository) MarkRecoveryCodeUsed(ctx context.Context, id string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE iam_user_mfa_recovery_codes SET used_at = $2 WHERE id = $1 AND used_at IS NULL`,
		id, at)
	if err != nil {
		return false, fmt.Errorf("mark recovery code used: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// CountUnusedRecoveryCodes returns how many backup codes remain.
func (r *Repository) CountUnusedRecoveryCodes(ctx context.Context, principalID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM iam_user_mfa_recovery_codes
		  WHERE principal_id = $1 AND used_at IS NULL`,
		principalID).Scan(&n)
	return n, err
}

// DeleteRecoveryCodes clears a user's whole recovery-code set (regenerate /
// reset).
func (r *Repository) DeleteRecoveryCodes(ctx context.Context, principalID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM iam_user_mfa_recovery_codes WHERE principal_id = $1`, principalID)
	return err
}

// ── email PIN challenges ─────────────────────────────────────────────────

// InsertEmailPin persists a pending email-PIN challenge.
func (r *Repository) InsertEmailPin(ctx context.Context, p *EmailPin) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO iam_mfa_email_pins (id, principal_id, purpose, pin_hash, attempts, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.ID, p.PrincipalID, string(p.Purpose), p.PinHash, p.Attempts, p.ExpiresAt, p.CreatedAt)
	return err
}

// FindLatestEmailPin returns the most recent challenge for a (principal,
// purpose), or (nil, nil).
func (r *Repository) FindLatestEmailPin(ctx context.Context, principalID string, purpose EmailPinPurpose) (*EmailPin, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, principal_id, purpose, pin_hash, attempts, expires_at, created_at
		   FROM iam_mfa_email_pins
		  WHERE principal_id = $1 AND purpose = $2
		  ORDER BY created_at DESC
		  LIMIT 1`,
		principalID, string(purpose))
	var p EmailPin
	var purp string
	if err := row.Scan(&p.ID, &p.PrincipalID, &purp, &p.PinHash, &p.Attempts, &p.ExpiresAt, &p.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find email pin: %w", err)
	}
	p.Purpose = EmailPinPurpose(purp)
	return &p, nil
}

// IncrementEmailPinAttempts bumps the wrong-guess counter and returns the new
// value.
func (r *Repository) IncrementEmailPinAttempts(ctx context.Context, id string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`UPDATE iam_mfa_email_pins SET attempts = attempts + 1 WHERE id = $1 RETURNING attempts`,
		id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("increment email pin attempts: %w", err)
	}
	return n, nil
}

// DeleteEmailPin removes a single challenge (on success or exhaustion).
func (r *Repository) DeleteEmailPin(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM iam_mfa_email_pins WHERE id = $1`, id)
	return err
}

// DeleteEmailPinsByPrincipal clears outstanding challenges for a (principal,
// purpose) before issuing a fresh one.
func (r *Repository) DeleteEmailPinsByPrincipal(ctx context.Context, principalID string, purpose EmailPinPurpose) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM iam_mfa_email_pins WHERE principal_id = $1 AND purpose = $2`,
		principalID, string(purpose))
	return err
}

// PurgeExpiredEmailPins removes all expired challenges. Run periodically.
func (r *Repository) PurgeExpiredEmailPins(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM iam_mfa_email_pins WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ── trusted devices ──────────────────────────────────────────────────────

// InsertTrustedDevice persists a remembered device.
func (r *Repository) InsertTrustedDevice(ctx context.Context, d *TrustedDevice) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO iam_mfa_trusted_devices (id, principal_id, token_hash, label, expires_at, created_at, last_used_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.ID, d.PrincipalID, d.TokenHash, d.Label, d.ExpiresAt, d.CreatedAt, d.LastUsedAt)
	return err
}

// FindValidTrustedDevice returns a non-expired remembered device matching the
// (principal, token hash), or (nil, nil).
func (r *Repository) FindValidTrustedDevice(ctx context.Context, principalID, tokenHash string) (*TrustedDevice, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, principal_id, token_hash, label, expires_at, created_at, last_used_at
		   FROM iam_mfa_trusted_devices
		  WHERE principal_id = $1 AND token_hash = $2 AND expires_at > NOW()`,
		principalID, tokenHash)
	var d TrustedDevice
	if err := row.Scan(&d.ID, &d.PrincipalID, &d.TokenHash, &d.Label, &d.ExpiresAt, &d.CreatedAt, &d.LastUsedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find trusted device: %w", err)
	}
	return &d, nil
}

// FindTrustedDevicesByPrincipal lists a user's remembered devices (for the
// self-service management screen).
func (r *Repository) FindTrustedDevicesByPrincipal(ctx context.Context, principalID string) ([]TrustedDevice, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, principal_id, token_hash, label, expires_at, created_at, last_used_at
		   FROM iam_mfa_trusted_devices
		  WHERE principal_id = $1
		  ORDER BY created_at DESC`,
		principalID)
	if err != nil {
		return nil, fmt.Errorf("list trusted devices: %w", err)
	}
	defer rows.Close()
	var out []TrustedDevice
	for rows.Next() {
		var d TrustedDevice
		if err := rows.Scan(&d.ID, &d.PrincipalID, &d.TokenHash, &d.Label, &d.ExpiresAt, &d.CreatedAt, &d.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TouchTrustedDeviceUsed stamps last_used_at on redemption.
func (r *Repository) TouchTrustedDeviceUsed(ctx context.Context, id string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE iam_mfa_trusted_devices SET last_used_at = $2 WHERE id = $1`, id, at)
	return err
}

// DeleteTrustedDevice removes one remembered device by id (scoped to the
// owner to prevent cross-user revocation).
func (r *Repository) DeleteTrustedDevice(ctx context.Context, principalID, id string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM iam_mfa_trusted_devices WHERE id = $1 AND principal_id = $2`, id, principalID)
	return err
}

// DeleteTrustedDevicesByPrincipal revokes every remembered device for a user
// (password change / 2FA reset).
func (r *Repository) DeleteTrustedDevicesByPrincipal(ctx context.Context, principalID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM iam_mfa_trusted_devices WHERE principal_id = $1`, principalID)
	return err
}

// PurgeExpiredTrustedDevices removes all expired devices. Run periodically.
func (r *Repository) PurgeExpiredTrustedDevices(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM iam_mfa_trusted_devices WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ── scan helpers ─────────────────────────────────────────────────────────

func scanMethod(row pgx.Row) (*Method, error) {
	var m Method
	var t string
	if err := row.Scan(&m.ID, &m.PrincipalID, &t, &m.SecretEncrypted, &m.ConfirmedAt, &m.LastUsedAt, &m.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan method: %w", err)
	}
	m.Type = MethodType(t)
	return &m, nil
}

func scanMethodRow(rows pgx.Rows) (*Method, error) {
	var m Method
	var t string
	if err := rows.Scan(&m.ID, &m.PrincipalID, &t, &m.SecretEncrypted, &m.ConfirmedAt, &m.LastUsedAt, &m.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan method row: %w", err)
	}
	m.Type = MethodType(t)
	return &m, nil
}
