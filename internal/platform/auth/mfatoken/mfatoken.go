// Package mfatoken mints and validates the short-lived, single-purpose tokens
// that carry a half-authenticated user between /auth/login and the /auth/2fa/*
// endpoints:
//
//   - PurposePending — the password verified; the user must pass a 2FA
//     challenge before a session is issued.
//   - PurposeEnroll  — the password verified, but the domain requires 2FA and
//     the user has no factor yet; they must enroll before a session is issued.
//
// These are deliberately signed with HS256 using a secret DERIVED from the RSA
// session-signing key. Two properties fall out of that choice:
//
//   - Stable across instances/restarts (same RSA key → same secret), so a token
//     minted by one node validates on another — unlike a random per-process key.
//   - Rejected by the session middleware, which only accepts RS256
//     (sessiontoken.Validate enforces WithValidMethods{RS256}). A pending/enroll
//     token therefore can NEVER be replayed as a session cookie.
package mfatoken

import (
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Purpose is the single allowed use of a token.
type Purpose string

const (
	// PurposePending gates the 2FA challenge endpoints (verify, email challenge).
	PurposePending Purpose = "mfa_pending"
	// PurposeEnroll gates the 2FA enrollment endpoints.
	PurposeEnroll Purpose = "mfa_enroll"
)

// Issuer mints + validates tokens.
type Issuer struct {
	secret []byte
	issuer string
}

// NewIssuer derives a stable HMAC secret from the RSA signing key. Panics only
// if key is nil (a programmer error at wiring time).
func NewIssuer(key *rsa.PrivateKey, issuer string) *Issuer {
	sum := sha256.Sum256(append([]byte("fc-mfa-token-v1|"), key.D.Bytes()...))
	return &Issuer{secret: sum[:], issuer: issuer}
}

// Claims is the validated payload.
type Claims struct {
	Subject string
	Purpose Purpose
}

// Mint signs a token for subject with the given purpose and TTL.
func (i *Issuer) Mint(subject string, purpose Purpose, ttl time.Duration) (string, error) {
	if subject == "" {
		return "", errors.New("mfatoken: subject is required")
	}
	now := time.Now().UTC()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": i.issuer,
		"sub": subject,
		"prp": string(purpose),
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": now.Add(ttl).Unix(),
	})
	return tok.SignedString(i.secret)
}

// Parse validates the signature, expiry, issuer and that the purpose matches
// want. Returns the claims on success.
func (i *Issuer) Parse(token string, want Purpose) (*Claims, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return i.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(i.issuer),
	)
	if err != nil {
		return nil, err
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("mfatoken: invalid token")
	}
	sub, _ := mc["sub"].(string)
	prp, _ := mc["prp"].(string)
	if sub == "" {
		return nil, errors.New("mfatoken: missing sub")
	}
	if Purpose(prp) != want {
		return nil, fmt.Errorf("mfatoken: wrong purpose %q (want %q)", prp, want)
	}
	return &Claims{Subject: sub, Purpose: Purpose(prp)}, nil
}
