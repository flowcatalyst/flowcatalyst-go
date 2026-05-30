package api

import (
	"regexp"
	"testing"
)

func TestHashTokenIsLowercaseHexSHA256(t *testing.T) {
	h := hashToken("test-token-value")
	if len(h) != 64 {
		t.Fatalf("SHA-256 hex must be 64 chars; got %d", len(h))
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(h) {
		t.Fatalf("hash must be lowercase hex; got %q", h)
	}
	// Known SHA-256 of the empty string (matches the Rust test vector).
	if got := hashToken(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty-string hash mismatch; got %q", got)
	}
	// Deterministic + collision-distinct.
	if hashToken("same") != hashToken("same") {
		t.Fatal("hash must be deterministic")
	}
	if hashToken("a") == hashToken("b") {
		t.Fatal("different inputs must hash differently")
	}
}

func TestGenerateRawToken(t *testing.T) {
	tok, err := generateRawToken()
	if err != nil {
		t.Fatalf("generateRawToken: %v", err)
	}
	// 32 bytes → URL-safe base64 no-pad → 43 chars (1:1 with Rust).
	if len(tok) != 43 {
		t.Fatalf("expected 43 chars for 32 bytes base64 no-pad; got %d (%q)", len(tok), tok)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(tok) {
		t.Fatalf("token must be URL-safe base64; got %q", tok)
	}
	tok2, _ := generateRawToken()
	if tok == tok2 {
		t.Fatal("tokens must be unique")
	}
	// A generated token hashes to a valid stored hash.
	if h := hashToken(tok); len(h) != 64 {
		t.Fatalf("generated token must hash to 64-char hex; got %d", len(h))
	}
}
