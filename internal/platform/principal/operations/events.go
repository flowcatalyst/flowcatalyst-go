package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	UserCreatedType         = "platform:iam:user:created"
	UserUpdatedType         = "platform:iam:user:updated"
	UserActivatedType       = "platform:iam:user:activated"
	UserDeactivatedType     = "platform:iam:user:deactivated"
	UserDeletedType         = "platform:iam:user:deleted"
	UserPasswordResetType   = "platform:iam:user:password-reset-completed"
	RolesAssignedType       = "platform:iam:user:roles-assigned"
	ApplicationAccessType   = "platform:iam:user:application-access-assigned"
	ClientAccessGrantedType = "platform:iam:user:client-access-granted"
	ClientAccessRevokedType = "platform:iam:user:client-access-revoked"
	PrincipalsSyncedType    = "platform:iam:principals:synced"
	Source                  = "platform:iam"
)

func subjectFor(id string) string { return "platform.principal." + id }
func groupFor(id string) string   { return "platform:principal:" + id }

// Note on naming: the entity's ID (the subject of the event — who was
// created/updated/etc.) is named `UserID` to avoid colliding with the
// DomainEvent `PrincipalID()` method, which returns the actor (the
// authenticated principal who performed the action) from Metadata.

type UserCreated struct {
	Metadata usecase.EventMetadata
	UserID   string
	Email    string
}

func (e UserCreated) EventID() string       { return e.Metadata.EventID }
func (e UserCreated) EventType() string     { return UserCreatedType }
func (e UserCreated) SpecVersion() string   { return "1.0" }
func (e UserCreated) Source() string        { return Source }
func (e UserCreated) Subject() string       { return subjectFor(e.UserID) }
func (e UserCreated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e UserCreated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e UserCreated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e UserCreated) CausationID() string   { return e.Metadata.CausationID }
func (e UserCreated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e UserCreated) MessageGroup() string  { return groupFor(e.UserID) }
func (e UserCreated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID string `json:"principalId"`
		Email  string `json:"email"`
	}{e.UserID, e.Email})
}

type UserUpdated struct {
	Metadata usecase.EventMetadata
	UserID   string
	Name     string
}

func (e UserUpdated) EventID() string       { return e.Metadata.EventID }
func (e UserUpdated) EventType() string     { return UserUpdatedType }
func (e UserUpdated) SpecVersion() string   { return "1.0" }
func (e UserUpdated) Source() string        { return Source }
func (e UserUpdated) Subject() string       { return subjectFor(e.UserID) }
func (e UserUpdated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e UserUpdated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e UserUpdated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e UserUpdated) CausationID() string   { return e.Metadata.CausationID }
func (e UserUpdated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e UserUpdated) MessageGroup() string  { return groupFor(e.UserID) }
func (e UserUpdated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID string `json:"principalId"`
		Name   string `json:"name"`
	}{e.UserID, e.Name})
}

type UserActivated struct {
	Metadata usecase.EventMetadata
	UserID   string
}

func (e UserActivated) EventID() string       { return e.Metadata.EventID }
func (e UserActivated) EventType() string     { return UserActivatedType }
func (e UserActivated) SpecVersion() string   { return "1.0" }
func (e UserActivated) Source() string        { return Source }
func (e UserActivated) Subject() string       { return subjectFor(e.UserID) }
func (e UserActivated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e UserActivated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e UserActivated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e UserActivated) CausationID() string   { return e.Metadata.CausationID }
func (e UserActivated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e UserActivated) MessageGroup() string  { return groupFor(e.UserID) }
func (e UserActivated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID string `json:"principalId"`
	}{e.UserID})
}

type UserDeactivated struct {
	Metadata usecase.EventMetadata
	UserID   string
}

func (e UserDeactivated) EventID() string       { return e.Metadata.EventID }
func (e UserDeactivated) EventType() string     { return UserDeactivatedType }
func (e UserDeactivated) SpecVersion() string   { return "1.0" }
func (e UserDeactivated) Source() string        { return Source }
func (e UserDeactivated) Subject() string       { return subjectFor(e.UserID) }
func (e UserDeactivated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e UserDeactivated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e UserDeactivated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e UserDeactivated) CausationID() string   { return e.Metadata.CausationID }
func (e UserDeactivated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e UserDeactivated) MessageGroup() string  { return groupFor(e.UserID) }
func (e UserDeactivated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID string `json:"principalId"`
	}{e.UserID})
}

type UserDeleted struct {
	Metadata usecase.EventMetadata
	UserID   string
	Email    string
}

func (e UserDeleted) EventID() string       { return e.Metadata.EventID }
func (e UserDeleted) EventType() string     { return UserDeletedType }
func (e UserDeleted) SpecVersion() string   { return "1.0" }
func (e UserDeleted) Source() string        { return Source }
func (e UserDeleted) Subject() string       { return subjectFor(e.UserID) }
func (e UserDeleted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e UserDeleted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e UserDeleted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e UserDeleted) CausationID() string   { return e.Metadata.CausationID }
func (e UserDeleted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e UserDeleted) MessageGroup() string  { return groupFor(e.UserID) }
func (e UserDeleted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID string `json:"principalId"`
		Email  string `json:"email"`
	}{e.UserID, e.Email})
}

type UserPasswordReset struct {
	Metadata usecase.EventMetadata
	UserID   string
}

func (e UserPasswordReset) EventID() string       { return e.Metadata.EventID }
func (e UserPasswordReset) EventType() string     { return UserPasswordResetType }
func (e UserPasswordReset) SpecVersion() string   { return "1.0" }
func (e UserPasswordReset) Source() string        { return Source }
func (e UserPasswordReset) Subject() string       { return subjectFor(e.UserID) }
func (e UserPasswordReset) Time() time.Time       { return e.Metadata.OccurredAt }
func (e UserPasswordReset) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e UserPasswordReset) CorrelationID() string { return e.Metadata.CorrelationID }
func (e UserPasswordReset) CausationID() string   { return e.Metadata.CausationID }
func (e UserPasswordReset) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e UserPasswordReset) MessageGroup() string  { return groupFor(e.UserID) }
func (e UserPasswordReset) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID string `json:"principalId"`
	}{e.UserID})
}

// RolesAssigned — emitted when assign_roles replaces the user's role set.
type RolesAssigned struct {
	Metadata usecase.EventMetadata
	UserID   string
	Roles    []string
	Added    []string
	Removed  []string
}

func (e RolesAssigned) EventID() string       { return e.Metadata.EventID }
func (e RolesAssigned) EventType() string     { return RolesAssignedType }
func (e RolesAssigned) SpecVersion() string   { return "1.0" }
func (e RolesAssigned) Source() string        { return Source }
func (e RolesAssigned) Subject() string       { return subjectFor(e.UserID) }
func (e RolesAssigned) Time() time.Time       { return e.Metadata.OccurredAt }
func (e RolesAssigned) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e RolesAssigned) CorrelationID() string { return e.Metadata.CorrelationID }
func (e RolesAssigned) CausationID() string   { return e.Metadata.CausationID }
func (e RolesAssigned) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e RolesAssigned) MessageGroup() string  { return groupFor(e.UserID) }
func (e RolesAssigned) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PrincipalID string   `json:"principalId"`
		Roles       []string `json:"roles"`
		Added       []string `json:"added"`
		Removed     []string `json:"removed"`
	}{e.UserID, defaultEmpty(e.Roles), defaultEmpty(e.Added), defaultEmpty(e.Removed)})
}

// ApplicationAccessAssigned — emitted when the user's application-access
// set is updated. UserID populates "userId" in the payload (matches the
// Rust schema, where this event uses userId not principalId — the Rust
// authors deliberately diverged to keep the API close to the frontend's
// vocabulary).
type ApplicationAccessAssigned struct {
	Metadata       usecase.EventMetadata
	UserID         string
	ApplicationIDs []string
	Added          []string
	Removed        []string
}

func (e ApplicationAccessAssigned) EventID() string       { return e.Metadata.EventID }
func (e ApplicationAccessAssigned) EventType() string     { return ApplicationAccessType }
func (e ApplicationAccessAssigned) SpecVersion() string   { return "1.0" }
func (e ApplicationAccessAssigned) Source() string        { return Source }
func (e ApplicationAccessAssigned) Subject() string       { return subjectFor(e.UserID) }
func (e ApplicationAccessAssigned) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ApplicationAccessAssigned) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ApplicationAccessAssigned) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ApplicationAccessAssigned) CausationID() string   { return e.Metadata.CausationID }
func (e ApplicationAccessAssigned) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ApplicationAccessAssigned) MessageGroup() string  { return groupFor(e.UserID) }
func (e ApplicationAccessAssigned) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID         string   `json:"userId"`
		ApplicationIDs []string `json:"applicationIds"`
		Added          []string `json:"added"`
		Removed        []string `json:"removed"`
	}{e.UserID, defaultEmpty(e.ApplicationIDs), defaultEmpty(e.Added), defaultEmpty(e.Removed)})
}

// ClientAccessGranted — emitted when a PARTNER user is granted access
// to a specific client.
type ClientAccessGranted struct {
	Metadata usecase.EventMetadata
	UserID   string
	ClientID string
}

func (e ClientAccessGranted) EventID() string       { return e.Metadata.EventID }
func (e ClientAccessGranted) EventType() string     { return ClientAccessGrantedType }
func (e ClientAccessGranted) SpecVersion() string   { return "1.0" }
func (e ClientAccessGranted) Source() string        { return Source }
func (e ClientAccessGranted) Subject() string       { return subjectFor(e.UserID) }
func (e ClientAccessGranted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientAccessGranted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientAccessGranted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientAccessGranted) CausationID() string   { return e.Metadata.CausationID }
func (e ClientAccessGranted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientAccessGranted) MessageGroup() string  { return groupFor(e.UserID) }
func (e ClientAccessGranted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PrincipalID string `json:"principalId"`
		ClientID    string `json:"clientId"`
	}{e.UserID, e.ClientID})
}

// ClientAccessRevoked — emitted when a PARTNER user loses access to a
// specific client.
type ClientAccessRevoked struct {
	Metadata usecase.EventMetadata
	UserID   string
	ClientID string
}

func (e ClientAccessRevoked) EventID() string       { return e.Metadata.EventID }
func (e ClientAccessRevoked) EventType() string     { return ClientAccessRevokedType }
func (e ClientAccessRevoked) SpecVersion() string   { return "1.0" }
func (e ClientAccessRevoked) Source() string        { return Source }
func (e ClientAccessRevoked) Subject() string       { return subjectFor(e.UserID) }
func (e ClientAccessRevoked) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientAccessRevoked) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientAccessRevoked) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientAccessRevoked) CausationID() string   { return e.Metadata.CausationID }
func (e ClientAccessRevoked) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientAccessRevoked) MessageGroup() string  { return groupFor(e.UserID) }
func (e ClientAccessRevoked) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PrincipalID string `json:"principalId"`
		ClientID    string `json:"clientId"`
	}{e.UserID, e.ClientID})
}

func defaultEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// PrincipalsSynced is the rollup emitted by the SDK app-scoped principal sync
// (SyncPrincipals). "Deactivated" counts principals from which SDK_SYNC role
// assignments were stripped during removeUnlisted (the principals themselves
// are not deleted) — matching the Rust PrincipalsSynced semantics.
type PrincipalsSynced struct {
	Metadata        usecase.EventMetadata
	ApplicationCode string
	Created         uint32
	Updated         uint32
	Deactivated     uint32
	SyncedEmails    []string
}

func (e PrincipalsSynced) EventID() string       { return e.Metadata.EventID }
func (e PrincipalsSynced) EventType() string     { return PrincipalsSyncedType }
func (e PrincipalsSynced) SpecVersion() string   { return "1.0" }
func (e PrincipalsSynced) Source() string        { return Source }
func (e PrincipalsSynced) Subject() string       { return "platform.principals." + e.ApplicationCode }
func (e PrincipalsSynced) Time() time.Time       { return e.Metadata.OccurredAt }
func (e PrincipalsSynced) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e PrincipalsSynced) CorrelationID() string { return e.Metadata.CorrelationID }
func (e PrincipalsSynced) CausationID() string   { return e.Metadata.CausationID }
func (e PrincipalsSynced) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e PrincipalsSynced) MessageGroup() string  { return "platform:principals" }
func (e PrincipalsSynced) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ApplicationCode string   `json:"applicationCode"`
		Created         uint32   `json:"created"`
		Updated         uint32   `json:"updated"`
		Deactivated     uint32   `json:"deactivated"`
		SyncedEmails    []string `json:"syncedEmails"`
	}{e.ApplicationCode, e.Created, e.Updated, e.Deactivated, e.SyncedEmails})
}
