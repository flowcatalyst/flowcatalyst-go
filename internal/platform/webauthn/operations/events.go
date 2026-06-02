package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	PasskeyRegisteredType    = "platform:admin:passkey:registered"
	PasskeyAuthenticatedType = "platform:admin:passkey:authenticated"
	PasskeyRevokedType       = "platform:admin:passkey:revoked"
	Source                   = "platform:admin"
)

func subjectFor(id string) string { return "platform.passkey." + id }
func groupFor(id string) string   { return "platform:passkey:" + id }

type PasskeyRegistered struct {
	Metadata     usecase.EventMetadata
	CredentialID string
	UserID       string
	Name         *string
}

func (e PasskeyRegistered) EventID() string       { return e.Metadata.EventID }
func (e PasskeyRegistered) EventType() string     { return PasskeyRegisteredType }
func (e PasskeyRegistered) SpecVersion() string   { return "1.0" }
func (e PasskeyRegistered) Source() string        { return Source }
func (e PasskeyRegistered) Subject() string       { return subjectFor(e.CredentialID) }
func (e PasskeyRegistered) Time() time.Time       { return e.Metadata.OccurredAt }
func (e PasskeyRegistered) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e PasskeyRegistered) CorrelationID() string { return e.Metadata.CorrelationID }
func (e PasskeyRegistered) CausationID() string   { return e.Metadata.CausationID }
func (e PasskeyRegistered) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e PasskeyRegistered) MessageGroup() string  { return groupFor(e.CredentialID) }
func (e PasskeyRegistered) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		CredentialID string  `json:"credentialId"`
		UserID       string  `json:"userId"`
		Name         *string `json:"name,omitempty"`
	}{e.CredentialID, e.UserID, e.Name})
}

type PasskeyAuthenticated struct {
	Metadata     usecase.EventMetadata
	CredentialID string
	UserID       string
}

func (e PasskeyAuthenticated) EventID() string       { return e.Metadata.EventID }
func (e PasskeyAuthenticated) EventType() string     { return PasskeyAuthenticatedType }
func (e PasskeyAuthenticated) SpecVersion() string   { return "1.0" }
func (e PasskeyAuthenticated) Source() string        { return Source }
func (e PasskeyAuthenticated) Subject() string       { return subjectFor(e.CredentialID) }
func (e PasskeyAuthenticated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e PasskeyAuthenticated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e PasskeyAuthenticated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e PasskeyAuthenticated) CausationID() string   { return e.Metadata.CausationID }
func (e PasskeyAuthenticated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e PasskeyAuthenticated) MessageGroup() string  { return groupFor(e.CredentialID) }
func (e PasskeyAuthenticated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		CredentialID string `json:"credentialId"`
		UserID       string `json:"userId"`
	}{e.CredentialID, e.UserID})
}

type PasskeyRevoked struct {
	Metadata     usecase.EventMetadata
	CredentialID string
	UserID       string
}

func (e PasskeyRevoked) EventID() string       { return e.Metadata.EventID }
func (e PasskeyRevoked) EventType() string     { return PasskeyRevokedType }
func (e PasskeyRevoked) SpecVersion() string   { return "1.0" }
func (e PasskeyRevoked) Source() string        { return Source }
func (e PasskeyRevoked) Subject() string       { return subjectFor(e.CredentialID) }
func (e PasskeyRevoked) Time() time.Time       { return e.Metadata.OccurredAt }
func (e PasskeyRevoked) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e PasskeyRevoked) CorrelationID() string { return e.Metadata.CorrelationID }
func (e PasskeyRevoked) CausationID() string   { return e.Metadata.CausationID }
func (e PasskeyRevoked) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e PasskeyRevoked) MessageGroup() string  { return groupFor(e.CredentialID) }
func (e PasskeyRevoked) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		CredentialID string `json:"credentialId"`
		UserID       string `json:"userId"`
	}{e.CredentialID, e.UserID})
}
