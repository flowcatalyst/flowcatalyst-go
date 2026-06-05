package mfa

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP parameters. SHA-1 / 6 digits / 30s is the de-facto standard that every
// authenticator app (Google Authenticator, 1Password, Authy, …) supports.
const (
	totpPeriod = 30 // seconds per step
	totpSkew   = 1  // accepted steps either side of now (±30s)
	totpDigits = otp.DigitsSix
	totpAlgo   = otp.AlgorithmSHA1
	totpSecret = 20 // secret bytes (160 bits)
)

// recoveryAlphabet is Crockford base32 minus visually ambiguous characters,
// uppercased so codes are easy to read back and type.
const recoveryAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// sha256Hex returns the lowercase-hex SHA-256 of s. Used for at-rest hashing of
// recovery codes, email PINs, and trusted-device tokens (none are passwords, so
// a fast hash with a high-entropy input is appropriate).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// constantTimeEqual compares two strings without leaking length-independent
// timing. Both are equal-length hex digests at the call sites.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// newTOTPKey generates a fresh TOTP secret for (issuer, account). The returned
// key exposes Secret() (base32, for manual entry) and URL() (otpauth://, for a
// QR code).
func newTOTPKey(issuer, account string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		Period:      totpPeriod,
		SecretSize:  totpSecret,
		Digits:      totpDigits,
		Algorithm:   totpAlgo,
	})
}

// validateTOTP checks code against the base32 secret at time now, allowing
// ±totpSkew steps. On success it returns the matched unix time-step so the
// caller can reject replay of an already-used step.
func validateTOTP(secret, code string, now time.Time) (ok bool, step int64) {
	opts := totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      totpSkew,
		Digits:    totpDigits,
		Algorithm: totpAlgo,
	}
	base := now.Unix() / totpPeriod
	for i := -int64(totpSkew); i <= int64(totpSkew); i++ {
		s := base + i
		candidate, err := totp.GenerateCodeCustom(secret, time.Unix(s*totpPeriod, 0), opts)
		if err != nil {
			continue
		}
		if constantTimeEqual(candidate, code) {
			return true, s
		}
	}
	return false, 0
}

// stepFromTime maps a timestamp back to its TOTP step (for replay comparison).
func stepFromTime(t time.Time) int64 { return t.Unix() / totpPeriod }

// timeForStep returns a representative timestamp for a step (stored as
// last_used_at so the next verify can derive the same step exactly).
func timeForStep(step int64) time.Time { return time.Unix(step*totpPeriod, 0).UTC() }

// randomDigits returns an n-digit numeric string (zero-padded), drawn
// uniformly from crypto/rand.
func randomDigits(n int) (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	s := v.String()
	if len(s) < n {
		s = strings.Repeat("0", n-len(s)) + s
	}
	return s, nil
}

// randomRecoveryCode returns a single recovery code of 2 groups of 5 chars
// (e.g. "A7K2M-9PQRT") drawn from recoveryAlphabet via rejection sampling.
func randomRecoveryCode() (string, error) {
	const groupLen = 5
	out := make([]byte, 0, groupLen*2+1)
	for g := 0; g < 2; g++ {
		if g > 0 {
			out = append(out, '-')
		}
		for i := 0; i < groupLen; i++ {
			c, err := randomAlphabetChar(recoveryAlphabet)
			if err != nil {
				return "", err
			}
			out = append(out, c)
		}
	}
	return string(out), nil
}

// randomAlphabetChar returns one uniformly-random character from alphabet
// (rejection sampling to avoid modulo bias).
func randomAlphabetChar(alphabet string) (byte, error) {
	n := big.NewInt(int64(len(alphabet)))
	v, err := rand.Int(rand.Reader, n)
	if err != nil {
		return 0, err
	}
	return alphabet[v.Int64()], nil
}

// normalizeRecoveryCode canonicalises user-entered codes before hashing:
// uppercase, strip spaces and dashes so "a7k2m 9pqrt" matches "A7K2M-9PQRT".
func normalizeRecoveryCode(s string) string {
	r := strings.NewReplacer("-", "", " ", "")
	return r.Replace(strings.ToUpper(strings.TrimSpace(s)))
}

// trimPin strips surrounding whitespace from an entered email PIN.
func trimPin(s string) string { return strings.TrimSpace(s) }

// randomToken returns 32 cryptographically-random bytes as URL-safe base64
// (no padding) — the raw trusted-device cookie value. Only its hash is stored.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
