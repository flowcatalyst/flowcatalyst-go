package webauthn

import (
	"github.com/go-webauthn/webauthn/webauthn"
)

// Service wires the go-webauthn library instance + our persistence repos.
// One Service per process; constructed at startup with the platform's
// RP ID, RP origin, and display name configuration.
type Service struct {
	wa         *webauthn.WebAuthn
	creds      *Repository
	ceremonies *CeremonyRepository
}

// Config is the construction-time settings.
type Config struct {
	RPDisplayName string   // e.g. "FlowCatalyst"
	RPID          string   // domain — e.g. "flowcatalyst.example.com"
	RPOrigins     []string // full origins — e.g. ["https://flowcatalyst.example.com"]
}

// NewService wires a Service. Returns an error if the WebAuthn library
// can't construct itself from the supplied config (typically a malformed RP ID).
func NewService(cfg Config, creds *Repository, ceremonies *CeremonyRepository) (*Service, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: cfg.RPDisplayName,
		RPID:          cfg.RPID,
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		return nil, err
	}
	return &Service{wa: wa, creds: creds, ceremonies: ceremonies}, nil
}

// WebAuthn exposes the underlying library instance.
func (s *Service) WebAuthn() *webauthn.WebAuthn { return s.wa }

// Credentials exposes the persisted-credentials repo.
func (s *Service) Credentials() *Repository { return s.creds }

// Ceremonies exposes the short-lived ceremony state repo.
func (s *Service) Ceremonies() *CeremonyRepository { return s.ceremonies }

// PrincipalUser is the go-webauthn User adapter for a FlowCatalyst principal.
// The library calls these methods during ceremony driving; we look up the
// principal's persisted credentials lazily.
type PrincipalUser struct {
	PrincipalID string
	DisplayName string
	Username    string
	Credentials []webauthn.Credential
}

// WebAuthnID returns the user's RP-scoped ID (used as the WebAuthn user handle).
func (u *PrincipalUser) WebAuthnID() []byte { return []byte(u.PrincipalID) }

// WebAuthnName returns the user's username (typically email).
func (u *PrincipalUser) WebAuthnName() string { return u.Username }

// WebAuthnDisplayName returns the human-readable name shown by the authenticator UI.
func (u *PrincipalUser) WebAuthnDisplayName() string { return u.DisplayName }

// WebAuthnCredentials returns the user's registered credentials.
func (u *PrincipalUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }
