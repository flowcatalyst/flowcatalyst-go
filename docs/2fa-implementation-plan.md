# Two-Factor Authentication (2FA) for internal users — implementation plan

Status: **All 7 phases complete.** Not committed (per repo convention).
Owner: andrew@belac.io. Started 2026-06-05.

## How to run / test

- Backend: `go build ./...`, `go vet ./...`, `make api-diff` (lockfile is current).
- mfa crypto unit tests: `go test ./internal/platform/mfa/`.
- mfa service vs embedded Postgres: `FC_MFA_PG_TEST=1 go test ./internal/platform/mfa/ -run TestServicePG`.
- mfatoken security tests: `go test ./internal/platform/auth/mfatoken/`.
- Frontend: `cd frontend && npx vue-tsc -b && npm run build`.
- Enable in an env: set `FLOWCATALYST_APP_KEY` (for TOTP secret encryption) and
  the `SMTP_*` vars (email PIN + notifications; logs only when unset). Configure
  a domain's mapping with require-2FA + allowed methods on the email-domain screen.

## Goal

Add 2FA for **internal** (password/passkey) users of the sign-in / OIDC server.
Federated (OIDC) users are out of scope and excluded structurally (they have no
password and never traverse these flows). Enforcement is configured per email
domain on the existing **email domain management** screen.

## Confirmed product decisions

1. **Mechanisms:** per-domain choice of **email PIN** and/or **virtual device
   (TOTP authenticator)**. At least one must be selected when 2FA is required.
2. **Passkey vs. password (clarified):** signing in **with a passkey** is
   passwordless and never challenged (it's inherently MFA). But a passkey does
   **not** exempt the password path — if the domain requires 2FA and the user
   chooses to sign in with a **password**, they must complete the email-PIN/TOTP
   second factor even if they also have a passkey. So the passkey is irrelevant
   to the password/2FA decision; only the passkey-login route bypasses 2FA.
3. **Recovery codes:** issued at enrollment — 8–10 single-use codes, shown once,
   hashed at rest, regenerable.
4. **Existing un-enrolled users:** when a domain flips 2FA on, existing users are
   forced to enroll at their **next password login** (interstitial state); the
   password verifies but no full session is granted until a factor is set up.
5. **Account-created email:** creating an INTERNAL user sends a "set your password"
   email; that flow (and password reset) forces 2FA enrollment when the domain
   requires it.
6. **Password reset can also reset 2FA:** admin "send password reset" carries an
   optional `reset2FA` flag; on confirm it clears the user's factors and forces
   re-enrollment (lost-device recovery path).
7. **Remember this device:** per-domain opt-in toggle (+ duration, default 30
   days). When enabled, the user may skip the challenge on that browser until
   expiry. **v1 pinning = hardened cookie only** (`__Host-` + HttpOnly + Secure +
   SameSite=Strict), no fingerprinting, no device key. Revoked on any password
   change or 2FA reset. Future hardening: non-extractable WebCrypto device key
   (`device_key_pub`), then WebAuthn — no schema break.

Naming stays **"2FA"** (not "Additional security").

## Architecture fit

- The **email domain management** screen is `EmailDomainMapping`
  (`internal/platform/emaildomainmapping`, `frontend/.../email-domains/`). Each
  mapping points at an Identity Provider whose `Type` is `INTERNAL` or `OIDC`, so
  the 2FA flag only bites for INTERNAL-type domains.
- Internal login is centralized at `POST /auth/login`
  (`internal/platform/auth/login/endpoint.go`). `/oauth/authorize` reuses the
  resulting `fc_session` cookie, so enforcing 2FA at `/auth/login` covers the
  OAuth/OIDC-provider path. Service accounts / client-credentials are unaffected.
- Passkey login (`/auth/webauthn/authenticate/complete`) is left untouched (no
  challenge).
- **Reuse:** AES-256-GCM `encryption.Service` (`FLOWCATALYST_APP_KEY`) for TOTP
  secrets; `passwordreset` token machinery for invite + reset links; SMTP
  `email.Service`; `authservice` JWT signer for short-lived pending/enroll tokens;
  `loginbackoff` + `ratelimit` for abuse control.
- **New dependency:** `github.com/pquerna/otp` for TOTP.

## Data model (migration `031_mfa_tables.sql`)

- `tnt_email_domain_mappings` += `require_2fa`, `remember_device_enabled`,
  `remember_device_days`; new junction `tnt_email_domain_mapping_2fa_methods
  (mapping_id, method)`. Validation: `require_2fa` ⇒ ≥1 method.
- `iam_password_reset_tokens` += `purpose ('reset'|'invite')` (invite = longer
  TTL), `reset_2fa BOOLEAN`.
- `iam_user_mfa_methods` — `id, principal_id, method ('TOTP'|'EMAIL_PIN'),
  secret_encrypted (TOTP only, AES-GCM), confirmed_at, last_used_at, created_at`;
  unique `(principal_id, method)`. Unconfirmed = pending enrollment.
- `iam_user_mfa_recovery_codes` — `id, principal_id, code_hash, used_at,
  created_at`. Single-use, hashed.
- `iam_mfa_email_pins` — pending email-PIN challenges: `id, principal_id, purpose,
  pin_hash, expires_at (~10m), attempts, created_at`.
- `iam_mfa_trusted_devices` — `id, principal_id, token_hash, label, expires_at,
  created_at, last_used_at`. Hashed token, revocable.

TSID prefixes (Go-only, not in Rust): MfaMethod `mfm`, MfaRecoveryCode `mrc`,
MfaEmailPin `mep`, MfaTrustedDevice `mtd`.

## Backend (new `internal/platform/mfa` package + endpoint changes)

- **Core service:** TOTP enroll/verify (±1 period skew, replay guard via
  `last_used_at`), email-PIN generate/send/verify, recovery-code generate/verify,
  trusted-device issue/verify/revoke. All constant-time.
- **`POST /auth/login`** returns one of: `ok` (+ cookie), `mfa_required`
  (`{mfaToken, methods}`, no cookie), or `enrollment_required` (`{enrollToken,
  allowedMethods}`). `mfaToken`/`enrollToken` = short-lived signed JWTs with
  single-purpose claims, rejected by the session auth middleware.
- **Challenge/verify (token-gated, mounted outside auth mw):**
  `POST /auth/2fa/challenge/email`, `POST /auth/2fa/verify`
  (`{mfaToken, method, code}` → TOTP / email-PIN / recovery code → mints session).
- **Enrollment (session- or enroll-token-gated):**
  `/auth/2fa/enroll/totp/begin|confirm`, `/auth/2fa/enroll/email/begin|confirm`.
  First confirmed method returns recovery codes; enroll-token flow mints session.
- **Self-service (session):** `GET /auth/2fa/methods`,
  `DELETE /auth/2fa/methods/{method}` (blocked if it would leave a required user
  blocked if it would leave a 2FA-required user with no confirmed factor),
  `POST /auth/2fa/recovery-codes/regenerate`,
  trusted-device list/revoke. Wires the Profile "Two-Factor Authentication" stub.
- **Set-password / reset / invite:** `/auth/password-reset/confirm` clears 2FA when
  the token's `reset_2fa` is set, then responds `enrollment_required` when the
  domain requires it.
- **Account-created email:** creating an INTERNAL user mints an `invite` token and
  sends a "set your password" email. OIDC users get no email.
- **Admin:** `POST /api/principals/{id}/reset-2fa`;
  `POST /api/principals/{id}/send-password-reset` += `reset2FA`; EDM API/DTO +=
  `require2FA`, `allowed2FAMethods`, `rememberDeviceEnabled`, `rememberDeviceDays`.
- **Audit events:** `UserMfaEnrolled / MethodRemoved / Reset /
  ChallengeSucceeded / ChallengeFailed`.

## Security notification emails (best-effort, SMTP-or-log)

Password changed; new passkey/device registered; 2FA method enrolled; 2FA method
removed / reset; recovery codes regenerated or a recovery code used; new trusted
device remembered. Delivery failure never blocks the action.

## Trusted-device pinning (v1 = layer 1)

Hardened cookie (`__Host-fc_td`, HttpOnly, Secure, SameSite=Strict, auth-path
scoped) + revocable server-side hashed record. No fingerprinting, no device key.
Bounded TTL; revoked on password change / 2FA reset. Honest caveat: a cookie
copied off the machine can be replayed until expiry — acceptable for v1, opt-in
per domain. Upgrade path: `device_key_pub` proof-of-possession, then passkeys.

## Frontend (Vue 3 / PrimeVue)

- EDM detail/create: "Require 2FA" toggle, allowed-methods multiselect (≥1 rule),
  "Allow remember device" toggle + duration (shown when 2FA required).
- `LoginPage.vue`: handle `mfa_required` / `enrollment_required`.
- New `TwoFactorSetupPage.vue` (reset/invite, forced-at-login, voluntary).
- `ResetPasswordPage.vue`: route into setup on `enrollment_required`.
- `ProfilePage.vue`: implement the 2FA card (methods, recovery codes, trusted
  devices).
- `api/twofactor.ts` for `/auth/2fa/*`; regenerate OpenAPI SDK for `/api` additions.

## Defaults

Email PIN: 6 digits, 10-min TTL, 5 attempts. TOTP issuer label: "FlowCatalyst".
Remember-device: 30 days. Invite-token TTL: 72h (vs 15-min reset).

## Build order (phases)

1. **Data model + entities/repos** ✅ *done*: migration `031_mfa_tables.sql`, TSIDs,
   `mfa` package entities + plain-pgx repo, EDM entity/sqlc/repo extension,
   passwordreset entity/SQL extension. Verified: `go build ./...`, `go vet`,
   embedded-PG migrate test (`FC_MIGRATE_PG_TEST=1`) applies to version 31.
2. **`mfa` core service** ✅ *done*: `github.com/pquerna/otp` (TOTP, ±1 skew,
   step-based replay guard), email-PIN issue/verify (attempt ceiling, hashed),
   recovery-code generation (single-use, hashed), trusted-device issue/verify/
   revoke, AES-GCM secret encryption via `encryption.Service`. Crypto unit tests
   pass; `go build`/`go vet`/`gofmt` clean.
3. **Login-flow integration** ✅ *done*: `/auth/login` now returns `ok` /
   `mfa_required` / `enrollment_required` (fails closed on eval error);
   trusted-device cookie skip. (Passkey does NOT exempt the password path — no
   webauthn dependency in the login flow.) New
   `mfatoken` pkg (HS256 pending/enroll JWTs derived from the RSA key — proven
   rejected by the RS256 session validator). New `/auth/2fa/*` chi endpoints
   (verify, email challenge, totp/email enroll begin+confirm) in
   `login/twofactor.go`; recovery codes surfaced on first enrollment; wired in
   `server/wire.go`. Tests: mfatoken (purpose/expiry/tamper/cross-key/
   session-rejection) green; `go build`/`vet`/`gofmt` clean.
4. **Set-password/reset/invite + notifications** ✅ *done*: shared `twofa.Policy`
   (login refactored onto it); `/auth/password-reset/confirm` now clears 2FA on
   a `reset_2fa` token, revokes remembered devices, and returns
   `enrollment_required` with an enroll token when the domain compels 2FA;
   admin `send-password-reset` accepts `reset2fa`; invite ("set your password")
   + welcome ("account created") emails on internal-user creation (both create
   paths); new `notify` pkg (best-effort, nil-safe) wired for password-changed,
   2FA reset/enrolled, recovery-codes regenerated/used, new trusted device, new
   passkey (webauthn register-complete). `go build`/`vet`/`gofmt` clean; auth +
   passwordreset + principal + webauthn tests green. NOTE: the
   `send-password-reset` body addition changes the OpenAPI spec — regenerate the
   SDK snapshot in Phase 6.
5. **Admin + EDM config API/DTO + self-service** ✅ *done*: EDM create/update/get
   DTOs + ops carry `require2fa`/`allowed2faMethods`/`rememberDeviceEnabled`/
   `rememberDeviceDays` with validation (≥1 valid method when required, on create
   AND the post-update state); admin `POST /api/principals/{id}/reset-2fa`;
   session-gated self-service (`/auth/2fa/status`, voluntary totp/email
   enroll begin+confirm, `DELETE /auth/2fa/methods/{method}` with last-factor
   guard, recovery-code regenerate, trusted-device list/revoke) mounted inside
   the auth middleware. `go build`/`vet`/`gofmt` clean; tests green.
6. **Frontend** ✅ *done*: `api/twofactor.ts` (challenge/enroll/self-service);
   `auth.ts` refactored (login returns a `LoginResult`; `applyLoginSuccess` split
   into `setSessionUser`+`redirectAfterLogin` so recovery codes show before
   navigation; `confirmPasswordReset` returns enrollment hand-off). New
   `TwoFactorChallenge.vue`, `TwoFactorSetup.vue` (token + session modes),
   `TwoFactorSection.vue`. Wired into `LoginPage` (mfa_required/enrollment_required
   steps), `ResetPasswordPage` (enroll after reset), `ProfilePage` (self-service
   card). EDM create + detail pages got the require-2FA toggle, allowed-methods
   checkboxes, and remember-device controls (+ types in `email-domain-mappings.ts`).
   `vue-tsc -b` clean; `npm run build` succeeds. TOTP shows secret + otpauth URI
   for manual entry (no QR lib added).
7. **Tests, audit events, rate limits, regen, docs** ✅ *done*: regenerated
   `api/openapi.lock.json` + the TS/Laravel SDK snapshots (`make api-bump` /
   `sdk-spec`) — `make api-diff` passes; per-(email,IP) brute-force backoff on
   `/auth/2fa/verify` (same store/policy as the password step); gated embedded-PG
   integration test of the mfa service (`FC_MFA_PG_TEST=1`, passes — TOTP
   enroll/confirm/replay, email-PIN enroll+login+single-use, recovery-code
   single-use, trusted-device issue/verify/revoke, reset); best-effort audit-log
   entries for 2FA enroll / method-removed / recovery-regenerated (login pkg) and
   admin reset (`2FA_RESET_BY_ADMIN`, principal API); this doc.

## Deferred (low priority)

- Frontend generated-SDK (`frontend/openapi/openapi.json` + `src/api/generated/`)
  resync — it had pre-existing drift (last synced May 19) unrelated to 2FA; the
  2FA/EDM frontend uses hand-written clients, so it builds without the regen.
  Run `npm run api:generate` against a live backend to refresh when convenient.
- QR-code rendering for TOTP setup (currently secret + otpauth URI for manual
  entry) — would need a QR lib (`qrcode`) added to the frontend.
- Full HTTP-level login→challenge→verify integration test (the service-level
  embedded-PG test + the unit tests cover the logic).
- Non-extractable device-key / WebAuthn trusted-device pinning; trusted-device
  token rotation + reuse detection; "new sign-in from new device" email.

## Deferred / future

Non-extractable device-key pinning; WebAuthn-as-remembered-device; trusted-device
token rotation + reuse detection; "new sign-in from new device" email.
</invoke>
