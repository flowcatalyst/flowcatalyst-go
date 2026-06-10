package mfatoken

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/sessiontoken"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	return k
}

func TestMintParseRoundTrip(t *testing.T) {
	iss := NewIssuer(testKey(t), "https://fc.example")
	tok, err := iss.Mint("prn_123", PurposePending, 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims, err := iss.Parse(tok, PurposePending)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Subject != "prn_123" || claims.Purpose != PurposePending {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestWrongPurposeRejected(t *testing.T) {
	iss := NewIssuer(testKey(t), "iss")
	tok, _ := iss.Mint("prn_1", PurposePending, time.Minute)
	if _, err := iss.Parse(tok, PurposeEnroll); err == nil {
		t.Fatal("a pending token must NOT parse as an enroll token")
	}
}

func TestExpiredRejected(t *testing.T) {
	iss := NewIssuer(testKey(t), "iss")
	tok, _ := iss.Mint("prn_1", PurposePending, -time.Minute) // already expired
	if _, err := iss.Parse(tok, PurposePending); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestTamperedRejected(t *testing.T) {
	iss := NewIssuer(testKey(t), "iss")
	tok, _ := iss.Mint("prn_1", PurposePending, time.Minute)
	bad := tok[:len(tok)-2] + "xx"
	if _, err := iss.Parse(bad, PurposePending); err == nil {
		t.Fatal("tampered token must be rejected")
	}
}

func TestDifferentKeyRejected(t *testing.T) {
	a := NewIssuer(testKey(t), "iss")
	b := NewIssuer(testKey(t), "iss") // different RSA key → different HMAC secret
	tok, _ := a.Mint("prn_1", PurposePending, time.Minute)
	if _, err := b.Parse(tok, PurposePending); err == nil {
		t.Fatal("token from a different key must be rejected")
	}
}

// TestRejectedBySessionValidator is the load-bearing security property: a
// pending/enroll token is HS256 and must be rejected by the RS256-only session
// validator, so it can never be replayed as an fc_session cookie.
func TestRejectedBySessionValidator(t *testing.T) {
	key := testKey(t)
	iss := NewIssuer(key, "iss")
	tok, _ := iss.Mint("prn_1", PurposePending, time.Minute)
	if _, err := sessiontoken.Validate(tok, &key.PublicKey, sessiontoken.Expect{}); err == nil {
		t.Fatal("mfa pending token must NOT validate as a session token")
	}
}
