package mfa

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"github.com/flowcatalyst/flowcatalyst-go/internal/migrate"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/email"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/encryption"
)

// captureMailer records the last email so the test can read the PIN out of the
// body (renderEmailPin embeds the plaintext code).
type captureMailer struct{ last email.Message }

func (c *captureMailer) Send(_ context.Context, m email.Message) error {
	c.last = m
	return nil
}

var sixDigits = regexp.MustCompile(`(\d{6})`)

func (c *captureMailer) pin(t *testing.T) string {
	t.Helper()
	m := sixDigits.FindStringSubmatch(c.last.HTMLBody)
	if m == nil {
		t.Fatalf("no PIN found in email body: %q", c.last.HTMLBody)
	}
	return m[1]
}

// TestServicePG exercises the DB-backed service behaviors against embedded
// Postgres. Gated like the migrate drop-in test.
//
//	FC_MFA_PG_TEST=1 go test ./internal/platform/mfa/ -run TestServicePG -v
func TestServicePG(t *testing.T) {
	if os.Getenv("FC_MFA_PG_TEST") == "" {
		t.Skip("set FC_MFA_PG_TEST=1 to run the embedded-Postgres mfa service test")
	}
	ctx := context.Background()

	const port = 15434
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).
		DataPath(filepath.Join(t.TempDir(), "data")).
		RuntimePath(filepath.Join(t.TempDir(), "runtime")).
		Username("postgres").Password("postgres").Database("flowcatalyst").
		StartTimeout(90 * time.Second))
	if err := pg.Start(); err != nil {
		t.Fatalf("start embedded pg: %v", err)
	}
	defer func() { _ = pg.Stop() }()

	pool, err := pgxpool.New(ctx, fmt.Sprintf(
		"postgresql://postgres:postgres@localhost:%d/flowcatalyst?sslmode=disable", port))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if err := migrate.Run(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const pid = "prn_testmfauser00" // 17 chars (VARCHAR(17))
	const addr = "user@example.com"
	if _, err := pool.Exec(ctx,
		`INSERT INTO iam_principals (id, type, scope, name, active, email)
		 VALUES ($1, 'USER', 'ANCHOR', 'Test User', TRUE, $2)`, pid, addr); err != nil {
		t.Fatalf("seed principal: %v", err)
	}

	key, err := encryption.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	enc, err := encryption.New(key)
	if err != nil {
		t.Fatalf("enc: %v", err)
	}
	mailer := &captureMailer{}
	svc := NewService(NewRepository(pool), enc, mailer, DefaultConfig())

	// ── TOTP enroll + confirm + replay ─────────────────────────────────────
	enr, err := svc.BeginTOTPEnrollment(ctx, pid, addr)
	if err != nil {
		t.Fatalf("begin totp: %v", err)
	}
	confirmCode, err := totp.GenerateCode(enr.Secret, time.Now())
	if err != nil {
		t.Fatalf("gen totp code: %v", err)
	}
	ok, err := svc.ConfirmTOTPEnrollment(ctx, pid, confirmCode)
	if err != nil || !ok {
		t.Fatalf("confirm totp: ok=%v err=%v", ok, err)
	}
	// Re-presenting the just-used code must be rejected by the replay guard.
	if replay, _ := svc.VerifyTOTP(ctx, pid, confirmCode); replay {
		t.Fatal("replay of a used TOTP step should be rejected")
	}
	if has, _ := svc.HasConfirmedMethod(ctx, pid); !has {
		t.Fatal("expected a confirmed method after TOTP enrollment")
	}

	// ── email PIN enroll + login challenge ─────────────────────────────────
	if err := svc.BeginEmailEnrollment(ctx, pid, addr); err != nil {
		t.Fatalf("begin email enroll: %v", err)
	}
	if ok, err := svc.ConfirmEmailEnrollment(ctx, pid, mailer.pin(t)); err != nil || !ok {
		t.Fatalf("confirm email enroll: ok=%v err=%v", ok, err)
	}
	if err := svc.SendLoginEmailPin(ctx, pid, addr); err != nil {
		t.Fatalf("send login pin: %v", err)
	}
	loginPin := mailer.pin(t)
	if ok, _ := svc.VerifyLoginEmailPin(ctx, pid, "000000"); ok {
		t.Fatal("wrong PIN should not verify")
	}
	if ok, err := svc.VerifyLoginEmailPin(ctx, pid, loginPin); err != nil || !ok {
		t.Fatalf("correct PIN should verify: ok=%v err=%v", ok, err)
	}
	// A consumed PIN can't be reused.
	if ok, _ := svc.VerifyLoginEmailPin(ctx, pid, loginPin); ok {
		t.Fatal("a consumed PIN should not verify again")
	}

	// ── recovery codes (single-use) ────────────────────────────────────────
	codes, err := svc.GenerateRecoveryCodes(ctx, pid)
	if err != nil || len(codes) != DefaultConfig().RecoveryCodeCount {
		t.Fatalf("generate recovery codes: n=%d err=%v", len(codes), err)
	}
	if ok, err := svc.VerifyRecoveryCode(ctx, pid, codes[0]); err != nil || !ok {
		t.Fatalf("recovery code should verify once: ok=%v err=%v", ok, err)
	}
	if ok, _ := svc.VerifyRecoveryCode(ctx, pid, codes[0]); ok {
		t.Fatal("recovery code should be single-use")
	}
	if left, _ := svc.RemainingRecoveryCodes(ctx, pid); left != DefaultConfig().RecoveryCodeCount-1 {
		t.Fatalf("expected %d remaining, got %d", DefaultConfig().RecoveryCodeCount-1, left)
	}

	// ── trusted devices ────────────────────────────────────────────────────
	raw, err := svc.IssueTrustedDevice(ctx, pid, nil, time.Hour)
	if err != nil {
		t.Fatalf("issue trusted device: %v", err)
	}
	if ok, err := svc.VerifyTrustedDevice(ctx, pid, raw); err != nil || !ok {
		t.Fatalf("trusted device should verify: ok=%v err=%v", ok, err)
	}
	if err := svc.RevokeAllTrustedDevices(ctx, pid); err != nil {
		t.Fatalf("revoke devices: %v", err)
	}
	if ok, _ := svc.VerifyTrustedDevice(ctx, pid, raw); ok {
		t.Fatal("revoked trusted device should not verify")
	}

	// ── reset clears everything ────────────────────────────────────────────
	if err := svc.ResetAll(ctx, pid); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if has, _ := svc.HasConfirmedMethod(ctx, pid); has {
		t.Fatal("no methods should remain after reset")
	}
	if left, _ := svc.RemainingRecoveryCodes(ctx, pid); left != 0 {
		t.Fatalf("no recovery codes should remain after reset, got %d", left)
	}
}
