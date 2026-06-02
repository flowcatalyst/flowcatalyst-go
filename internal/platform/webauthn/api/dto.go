// dto.go contains the wire-format types for the WebAuthn API.
package api

import (
	"encoding/json"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httpcompat"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/jsontime"
	wa "github.com/flowcatalyst/flowcatalyst-go/internal/platform/webauthn"
)

// RegisterBeginRequest is the wire body for POST /auth/webauthn/register/begin.
type RegisterBeginRequest struct {
	DisplayName *string `json:"displayName,omitempty"`
}

// RegisterBeginResponse is returned by POST /auth/webauthn/register/begin.
type RegisterBeginResponse struct {
	StateID string `json:"stateId"`
	Options any    `json:"options"`
}

// RegisterCompleteRequest is the wire body for
// POST /auth/webauthn/register/complete.
type RegisterCompleteRequest struct {
	StateID    string          `json:"stateId"`
	Name       *string         `json:"name,omitempty"`
	Credential json.RawMessage `json:"credential"`
}

// RegisterCompleteResponse is returned by register/complete.
type RegisterCompleteResponse struct {
	CredentialID string `json:"credentialId"`
}

// AuthenticateBeginRequest is the wire body for
// POST /auth/webauthn/authenticate/begin.
type AuthenticateBeginRequest struct {
	Email string `json:"email"`
}

// AuthenticateBeginResponse is returned by authenticate/begin.
type AuthenticateBeginResponse struct {
	StateID string `json:"stateId"`
	Options any    `json:"options"`
}

// AuthenticateCompleteRequest is the wire body for
// POST /auth/webauthn/authenticate/complete.
type AuthenticateCompleteRequest struct {
	StateID    string          `json:"stateId"`
	Credential json.RawMessage `json:"credential"`
}

// WebauthnAuthenticateCompleteResponse is returned by
// POST /auth/webauthn/authenticate/complete on success.
type WebauthnAuthenticateCompleteResponse struct {
	PrincipalID string   `json:"principalId"`
	Email       *string  `json:"email"`
	Name        string   `json:"name"`
	Roles       []string `json:"roles"`
}

// WebauthnCredentialSummary is the public, safe-to-expose view of a passkey
// returned by GET /auth/webauthn/credentials. It deliberately omits the raw
// credential blob and the principalId.
type WebauthnCredentialSummary struct {
	ID         string           `json:"id"`
	Name       *string          `json:"name,omitempty"`
	CreatedAt  httpcompat.Time  `json:"createdAt"`
	LastUsedAt *httpcompat.Time `json:"lastUsedAt,omitempty"`
}

func credentialSummaryFromEntity(c *wa.Credential) WebauthnCredentialSummary {
	var lastUsed *httpcompat.Time
	if c.LastUsedAt != nil {
		v := jsontime.New(*c.LastUsedAt)
		lastUsed = &v
	}
	return WebauthnCredentialSummary{
		ID:         c.ID,
		Name:       c.Name,
		CreatedAt:  jsontime.New(c.CreatedAt),
		LastUsedAt: lastUsed,
	}
}
