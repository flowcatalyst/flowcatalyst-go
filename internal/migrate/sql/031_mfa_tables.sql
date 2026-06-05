-- +goose Up
-- FlowCatalyst 2FA / MFA tables (internal users only).
--
-- Adds per-domain 2FA enforcement to the email-domain-mapping aggregate,
-- per-user enrolled factors (TOTP / email PIN), single-use recovery codes,
-- pending email-PIN challenges, and remembered ("trusted") devices. Federated
-- (OIDC) users never get rows here — gated at the application layer, same as
-- webauthn_credentials. See docs/2fa-implementation-plan.md.

-- =============================================================================
-- tnt_email_domain_mappings - per-domain 2FA enforcement + remember-device
-- =============================================================================
ALTER TABLE tnt_email_domain_mappings
    ADD COLUMN IF NOT EXISTS require_2fa BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE tnt_email_domain_mappings
    ADD COLUMN IF NOT EXISTS remember_device_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE tnt_email_domain_mappings
    ADD COLUMN IF NOT EXISTS remember_device_days INTEGER NOT NULL DEFAULT 30;

-- Allowed 2FA methods for the domain ('TOTP' | 'EMAIL_PIN'). Junction mirrors
-- the existing allowed_roles / granted_clients style (no FK ON DELETE CASCADE;
-- the repo clears rows explicitly). At least one row is required by the
-- application layer when require_2fa is true.
CREATE TABLE IF NOT EXISTS tnt_email_domain_mapping_2fa_methods (
    id SERIAL PRIMARY KEY,
    email_domain_mapping_id VARCHAR(17) NOT NULL,
    method VARCHAR(20) NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tnt_edm_2fa_methods_mapping
    ON tnt_email_domain_mapping_2fa_methods (email_domain_mapping_id);

-- =============================================================================
-- iam_password_reset_tokens - purpose ('reset'|'invite') + reset_2fa flag
-- =============================================================================
ALTER TABLE iam_password_reset_tokens
    ADD COLUMN IF NOT EXISTS purpose VARCHAR(20) NOT NULL DEFAULT 'reset';
ALTER TABLE iam_password_reset_tokens
    ADD COLUMN IF NOT EXISTS reset_2fa BOOLEAN NOT NULL DEFAULT FALSE;

-- =============================================================================
-- iam_user_mfa_methods - enrolled second factors per user
-- =============================================================================
-- method: 'TOTP' | 'EMAIL_PIN'. secret_encrypted holds the AES-256-GCM
-- envelope of the TOTP shared secret (NULL for EMAIL_PIN, which uses the
-- user's email). confirmed_at is NULL until the user verifies a first code;
-- unconfirmed rows are pending enrollments and never satisfy a challenge.
CREATE TABLE IF NOT EXISTS iam_user_mfa_methods (
    id               VARCHAR(17) PRIMARY KEY,
    principal_id     VARCHAR(17) NOT NULL REFERENCES iam_principals(id) ON DELETE CASCADE,
    method           VARCHAR(20) NOT NULL,
    secret_encrypted TEXT,
    confirmed_at     TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_iam_user_mfa_methods_principal_method
    ON iam_user_mfa_methods (principal_id, method);
CREATE INDEX IF NOT EXISTS idx_iam_user_mfa_methods_principal
    ON iam_user_mfa_methods (principal_id);

-- =============================================================================
-- iam_user_mfa_recovery_codes - single-use backup codes
-- =============================================================================
-- One row per code; code_hash is the SHA-256 of the printable code. used_at is
-- stamped on redemption (single-use). Regeneration deletes the whole set.
CREATE TABLE IF NOT EXISTS iam_user_mfa_recovery_codes (
    id           VARCHAR(17) PRIMARY KEY,
    principal_id VARCHAR(17) NOT NULL REFERENCES iam_principals(id) ON DELETE CASCADE,
    code_hash    VARCHAR(64) NOT NULL,
    used_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_iam_user_mfa_recovery_codes_principal
    ON iam_user_mfa_recovery_codes (principal_id);
CREATE INDEX IF NOT EXISTS idx_iam_user_mfa_recovery_codes_hash
    ON iam_user_mfa_recovery_codes (code_hash);

-- =============================================================================
-- iam_mfa_email_pins - pending email-PIN challenges (short-lived)
-- =============================================================================
-- purpose: 'login' (challenge) | 'enroll' (proves inbox control at enrollment).
-- pin_hash is the SHA-256 of the numeric PIN. attempts is incremented on each
-- wrong guess; the row is consumed on success or deleted on expiry.
CREATE TABLE IF NOT EXISTS iam_mfa_email_pins (
    id           VARCHAR(17) PRIMARY KEY,
    principal_id VARCHAR(17) NOT NULL REFERENCES iam_principals(id) ON DELETE CASCADE,
    purpose      VARCHAR(20) NOT NULL DEFAULT 'login',
    pin_hash     VARCHAR(64) NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_iam_mfa_email_pins_principal
    ON iam_mfa_email_pins (principal_id);
CREATE INDEX IF NOT EXISTS idx_iam_mfa_email_pins_expires
    ON iam_mfa_email_pins (expires_at);

-- =============================================================================
-- iam_mfa_trusted_devices - remembered devices (skip challenge until expiry)
-- =============================================================================
-- token_hash is the SHA-256 of the random trusted-device token carried in the
-- __Host-fc_td cookie; only the hash is stored so a DB read can't mint a
-- cookie. Revoked (deleted) on password change / 2FA reset / explicit removal.
CREATE TABLE IF NOT EXISTS iam_mfa_trusted_devices (
    id           VARCHAR(17) PRIMARY KEY,
    principal_id VARCHAR(17) NOT NULL REFERENCES iam_principals(id) ON DELETE CASCADE,
    token_hash   VARCHAR(64) NOT NULL UNIQUE,
    label        VARCHAR(255),
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_iam_mfa_trusted_devices_principal
    ON iam_mfa_trusted_devices (principal_id);
CREATE INDEX IF NOT EXISTS idx_iam_mfa_trusted_devices_hash
    ON iam_mfa_trusted_devices (token_hash);
