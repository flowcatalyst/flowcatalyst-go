package sessiontoken_test

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/sessiontoken"
)

func mustKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func TestMintAndValidate_RoundTrip(t *testing.T) {
	key := mustKey(t)

	in := sessiontoken.Claims{
		Subject:      "prn_abc",
		Scope:        "ANCHOR",
		Email:        "admin@example.com",
		Clients:      []string{"clt_1", "clt_2"},
		Roles:        []string{"platform:super-admin"},
		Applications: []string{"app_platform"},
		Permissions:  []string{"platform:*:*:*", "*"},
	}

	tok, err := sessiontoken.Mint(in, key, "http://localhost", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok == "" {
		t.Fatalf("mint returned empty token")
	}

	out, err := sessiontoken.Validate(tok, &key.PublicKey, sessiontoken.Expect{Issuer: "http://localhost", Audience: "http://localhost"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if out.Subject != in.Subject {
		t.Errorf("Subject=%q want %q", out.Subject, in.Subject)
	}
	if out.Scope != in.Scope {
		t.Errorf("Scope=%q want %q", out.Scope, in.Scope)
	}
	if out.Email != in.Email {
		t.Errorf("Email=%q want %q", out.Email, in.Email)
	}
	if got, want := len(out.Clients), len(in.Clients); got != want {
		t.Errorf("Clients len=%d want %d", got, want)
	}
	if got, want := len(out.Permissions), len(in.Permissions); got != want {
		t.Errorf("Permissions len=%d want %d", got, want)
	}
}

func TestValidate_RejectsBadSignature(t *testing.T) {
	k1 := mustKey(t)
	k2 := mustKey(t)

	tok, err := sessiontoken.Mint(sessiontoken.Claims{
		Subject: "prn_abc",
		Scope:   "ANCHOR",
	}, k1, "iss", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	if _, err := sessiontoken.Validate(tok, &k2.PublicKey, sessiontoken.Expect{}); err == nil {
		t.Fatalf("validate should fail with mismatched key")
	}
}

func TestValidate_RejectsExpired(t *testing.T) {
	key := mustKey(t)

	tok, err := sessiontoken.Mint(sessiontoken.Claims{
		Subject: "prn_abc",
		Scope:   "ANCHOR",
	}, key, "iss", -time.Minute) // already expired
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	_, err = sessiontoken.Validate(tok, &key.PublicKey, sessiontoken.Expect{})
	if err == nil {
		t.Fatalf("validate should reject expired token")
	}
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

// TestValidate_EnforcesIssuer pins the issuer expectation: a token minted
// under a different iss must not validate when an issuer is expected.
func TestValidate_EnforcesIssuer(t *testing.T) {
	key := mustKey(t)
	tok, err := sessiontoken.Mint(sessiontoken.Claims{Subject: "prn_abc"}, key, "https://evil.example", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := sessiontoken.Validate(tok, &key.PublicKey, sessiontoken.Expect{Issuer: "https://platform.example"}); err == nil {
		t.Fatalf("validate should reject mismatched issuer")
	}
}

// TestValidate_RejectsForeignAudience pins the cross-purpose guard: an OIDC
// ID token minted for a third-party RP (aud = the RP's client_id) is signed
// with the same platform key, and must NOT validate as a platform bearer.
// Tokens without an aud claim (session cookies) must keep validating.
func TestValidate_RejectsForeignAudience(t *testing.T) {
	key := mustKey(t)
	expect := sessiontoken.Expect{Issuer: "https://platform.example", Audience: "https://platform.example"}

	// Hand-mint an ID-token-shaped JWT: same key, same iss, foreign aud.
	idTok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://platform.example",
		"sub": "prn_abc",
		"aud": "oac_third_party_rp",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := sessiontoken.Validate(idTok, &key.PublicKey, expect); err == nil {
		t.Fatalf("an ID token minted for a relying party must not validate as a platform bearer")
	}

	// Platform-audience bearer (access-token shape) passes.
	accTok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://platform.example",
		"sub": "prn_abc",
		"aud": "https://platform.example",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := sessiontoken.Validate(accTok, &key.PublicKey, expect); err != nil {
		t.Fatalf("platform-audience bearer should validate: %v", err)
	}

	// No-aud token (session cookie shape) passes.
	cookieTok, err := sessiontoken.Mint(sessiontoken.Claims{Subject: "prn_abc"}, key, "https://platform.example", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := sessiontoken.Validate(cookieTok, &key.PublicKey, expect); err != nil {
		t.Fatalf("aud-less session token should validate: %v", err)
	}
}

func TestMint_RejectsEmptySubject(t *testing.T) {
	key := mustKey(t)
	_, err := sessiontoken.Mint(sessiontoken.Claims{Scope: "ANCHOR"}, key, "iss", time.Hour)
	if err == nil {
		t.Fatalf("mint should reject empty subject")
	}
}

func TestMint_RejectsNilKey(t *testing.T) {
	_, err := sessiontoken.Mint(sessiontoken.Claims{Subject: "prn_abc"}, nil, "iss", time.Hour)
	if err == nil {
		t.Fatalf("mint should reject nil key")
	}
}
