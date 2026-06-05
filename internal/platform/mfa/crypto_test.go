package mfa

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func totpCodeAt(t *testing.T, secret string, when time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, when, totp.ValidateOpts{
		Period:    totpPeriod,
		Digits:    totpDigits,
		Algorithm: totpAlgo,
	})
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

func TestValidateTOTP_CurrentCode(t *testing.T) {
	key, err := newTOTPKey("FlowCatalyst", "user@example.com")
	if err != nil {
		t.Fatalf("newTOTPKey: %v", err)
	}
	now := time.Now()
	code := totpCodeAt(t, key.Secret(), now)

	ok, step := validateTOTP(key.Secret(), code, now)
	if !ok {
		t.Fatal("expected current code to validate")
	}
	if want := now.Unix() / totpPeriod; step != want {
		t.Fatalf("matched step = %d, want %d", step, want)
	}
}

func TestValidateTOTP_SkewWindow(t *testing.T) {
	key, _ := newTOTPKey("FlowCatalyst", "user@example.com")
	now := time.Now()

	// A code from one step ago must still validate (±1 skew); from far away
	// must not.
	prev := totpCodeAt(t, key.Secret(), now.Add(-totpPeriod*time.Second))
	if ok, _ := validateTOTP(key.Secret(), prev, now); !ok {
		t.Fatal("previous-step code should validate within skew")
	}
	far := totpCodeAt(t, key.Secret(), now.Add(-10*totpPeriod*time.Second))
	if ok, _ := validateTOTP(key.Secret(), far, now); ok {
		t.Fatal("far-past code should NOT validate")
	}
}

func TestValidateTOTP_ReplayStepMonotonic(t *testing.T) {
	key, _ := newTOTPKey("FlowCatalyst", "user@example.com")
	now := time.Now()
	_, step1 := validateTOTP(key.Secret(), totpCodeAt(t, key.Secret(), now), now)
	later := now.Add(totpPeriod * time.Second)
	_, step2 := validateTOTP(key.Secret(), totpCodeAt(t, key.Secret(), later), later)
	if step2 <= step1 {
		t.Fatalf("expected step to advance: step1=%d step2=%d", step1, step2)
	}
}

func TestRandomDigits(t *testing.T) {
	for _, n := range []int{4, 6, 8} {
		s, err := randomDigits(n)
		if err != nil {
			t.Fatalf("randomDigits(%d): %v", n, err)
		}
		if len(s) != n {
			t.Fatalf("len=%d want %d (%q)", len(s), n, s)
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				t.Fatalf("non-digit in %q", s)
			}
		}
	}
}

func TestRandomRecoveryCode_Format(t *testing.T) {
	c, err := randomRecoveryCode()
	if err != nil {
		t.Fatalf("randomRecoveryCode: %v", err)
	}
	parts := strings.Split(c, "-")
	if len(parts) != 2 || len(parts[0]) != 5 || len(parts[1]) != 5 {
		t.Fatalf("unexpected format: %q", c)
	}
	for _, ch := range strings.ReplaceAll(c, "-", "") {
		if !strings.ContainsRune(recoveryAlphabet, ch) {
			t.Fatalf("char %q not in alphabet (%q)", ch, c)
		}
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	got := normalizeRecoveryCode("  a7k2m 9pqrt ")
	if want := "A7K2M9PQRT"; got != want {
		t.Fatalf("normalize = %q, want %q", got, want)
	}
	// The same code with the canonical dash must normalise identically.
	if normalizeRecoveryCode("A7K2M-9PQRT") != got {
		t.Fatal("dashed and spaced forms should normalise the same")
	}
}

func TestRandomTokenUnique(t *testing.T) {
	a, err := randomToken()
	if err != nil {
		t.Fatalf("randomToken: %v", err)
	}
	b, _ := randomToken()
	if a == b || a == "" {
		t.Fatalf("tokens should be unique and non-empty (%q,%q)", a, b)
	}
}
