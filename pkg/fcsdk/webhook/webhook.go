// Package webhook implements webhook signature verification for SDK
// consumers receiving FlowCatalyst webhooks.
//
// The signing algorithm MUST match the router's sign_webhook in
// internal/router/mediator.go byte-for-byte. Both sides verify against
// a shared test vector in tests/golden/webhook/.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// SignatureHeader matches the router's outbound header name.
const SignatureHeader = "X-FLOWCATALYST-SIGNATURE"

// TimestampHeader matches the router's outbound header name.
const TimestampHeader = "X-FLOWCATALYST-TIMESTAMP"

// Verifier validates inbound webhook signatures.
type Verifier struct {
	secret []byte
	// MaxClockSkew is how far the inbound timestamp may differ from now.
	// Defaults to 5 minutes to allow for clock drift between sender and receiver.
	MaxClockSkew time.Duration
}

// NewVerifier wires a verifier with the supplied signing secret.
func NewVerifier(secret string) *Verifier {
	return &Verifier{secret: []byte(secret), MaxClockSkew: 5 * time.Minute}
}

// Verify validates the signature header against the supplied body.
//
// `body` MUST be the raw bytes received over the wire (don't re-serialize
// — that would re-introduce JSON-library-dependent variation).
// `signature` is the hex from the X-FLOWCATALYST-SIGNATURE header.
// `timestamp` is the X-FLOWCATALYST-TIMESTAMP header.
func (v *Verifier) Verify(body []byte, signature, timestamp string) error {
	if signature == "" {
		return ErrMissingSignature
	}
	if timestamp == "" {
		return ErrMissingTimestamp
	}

	// Clock-skew check.
	ts, err := time.Parse("2006-01-02T15:04:05.000Z", timestamp)
	if err != nil {
		// Tolerate sub-millisecond precision if the sender chose to use it.
		ts, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return ErrBadTimestamp
		}
	}
	if v.MaxClockSkew > 0 {
		delta := time.Since(ts)
		if delta < 0 {
			delta = -delta
		}
		if delta > v.MaxClockSkew {
			return ErrStaleTimestamp
		}
	}

	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(timestamp))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(want), []byte(signature)) {
		return ErrBadSignature
	}
	return nil
}

// Errors.
var (
	ErrMissingSignature = errors.New("webhook: missing signature header")
	ErrMissingTimestamp = errors.New("webhook: missing timestamp header")
	ErrBadTimestamp     = errors.New("webhook: malformed timestamp")
	ErrStaleTimestamp   = errors.New("webhook: timestamp outside clock skew tolerance")
	ErrBadSignature     = errors.New("webhook: signature does not match")
)
