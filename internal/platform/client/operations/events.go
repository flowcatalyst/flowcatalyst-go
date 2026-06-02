package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	ClientCreatedType   = "platform:admin:client:created"
	ClientUpdatedType   = "platform:admin:client:updated"
	ClientActivatedType = "platform:admin:client:activated"
	ClientSuspendedType = "platform:admin:client:suspended"
	ClientNoteAddedType = "platform:admin:client:note-added"
	ClientDeletedType   = "platform:admin:client:deleted"
	Source              = "platform:admin"
)

func subjectFor(id string) string { return "platform.client." + id }
func groupFor(id string) string   { return "platform:client:" + id }

type ClientCreated struct {
	Metadata   usecase.EventMetadata
	ClientID   string
	Name       string
	Identifier string
}

func (e ClientCreated) EventID() string       { return e.Metadata.EventID }
func (e ClientCreated) EventType() string     { return ClientCreatedType }
func (e ClientCreated) SpecVersion() string   { return "1.0" }
func (e ClientCreated) Source() string        { return Source }
func (e ClientCreated) Subject() string       { return subjectFor(e.ClientID) }
func (e ClientCreated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientCreated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientCreated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientCreated) CausationID() string   { return e.Metadata.CausationID }
func (e ClientCreated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientCreated) MessageGroup() string  { return groupFor(e.ClientID) }
func (e ClientCreated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ClientID   string `json:"clientId"`
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
	}{e.ClientID, e.Name, e.Identifier})
}

type ClientUpdated struct {
	Metadata usecase.EventMetadata
	ClientID string
	Name     string
}

func (e ClientUpdated) EventID() string       { return e.Metadata.EventID }
func (e ClientUpdated) EventType() string     { return ClientUpdatedType }
func (e ClientUpdated) SpecVersion() string   { return "1.0" }
func (e ClientUpdated) Source() string        { return Source }
func (e ClientUpdated) Subject() string       { return subjectFor(e.ClientID) }
func (e ClientUpdated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientUpdated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientUpdated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientUpdated) CausationID() string   { return e.Metadata.CausationID }
func (e ClientUpdated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientUpdated) MessageGroup() string  { return groupFor(e.ClientID) }
func (e ClientUpdated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ClientID string `json:"clientId"`
		Name     string `json:"name"`
	}{e.ClientID, e.Name})
}

type ClientActivated struct {
	Metadata usecase.EventMetadata
	ClientID string
}

func (e ClientActivated) EventID() string       { return e.Metadata.EventID }
func (e ClientActivated) EventType() string     { return ClientActivatedType }
func (e ClientActivated) SpecVersion() string   { return "1.0" }
func (e ClientActivated) Source() string        { return Source }
func (e ClientActivated) Subject() string       { return subjectFor(e.ClientID) }
func (e ClientActivated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientActivated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientActivated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientActivated) CausationID() string   { return e.Metadata.CausationID }
func (e ClientActivated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientActivated) MessageGroup() string  { return groupFor(e.ClientID) }
func (e ClientActivated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ClientID string `json:"clientId"`
	}{e.ClientID})
}

type ClientSuspended struct {
	Metadata usecase.EventMetadata
	ClientID string
	Reason   string
}

func (e ClientSuspended) EventID() string       { return e.Metadata.EventID }
func (e ClientSuspended) EventType() string     { return ClientSuspendedType }
func (e ClientSuspended) SpecVersion() string   { return "1.0" }
func (e ClientSuspended) Source() string        { return Source }
func (e ClientSuspended) Subject() string       { return subjectFor(e.ClientID) }
func (e ClientSuspended) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientSuspended) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientSuspended) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientSuspended) CausationID() string   { return e.Metadata.CausationID }
func (e ClientSuspended) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientSuspended) MessageGroup() string  { return groupFor(e.ClientID) }
func (e ClientSuspended) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ClientID string `json:"clientId"`
		Reason   string `json:"reason"`
	}{e.ClientID, e.Reason})
}

type ClientNoteAdded struct {
	Metadata usecase.EventMetadata
	ClientID string
	Category string
	Text     string
}

func (e ClientNoteAdded) EventID() string       { return e.Metadata.EventID }
func (e ClientNoteAdded) EventType() string     { return ClientNoteAddedType }
func (e ClientNoteAdded) SpecVersion() string   { return "1.0" }
func (e ClientNoteAdded) Source() string        { return Source }
func (e ClientNoteAdded) Subject() string       { return subjectFor(e.ClientID) }
func (e ClientNoteAdded) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientNoteAdded) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientNoteAdded) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientNoteAdded) CausationID() string   { return e.Metadata.CausationID }
func (e ClientNoteAdded) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientNoteAdded) MessageGroup() string  { return groupFor(e.ClientID) }
func (e ClientNoteAdded) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ClientID string `json:"clientId"`
		Category string `json:"category"`
		Text     string `json:"text"`
	}{e.ClientID, e.Category, e.Text})
}

type ClientDeleted struct {
	Metadata   usecase.EventMetadata
	ClientID   string
	Identifier string
}

func (e ClientDeleted) EventID() string       { return e.Metadata.EventID }
func (e ClientDeleted) EventType() string     { return ClientDeletedType }
func (e ClientDeleted) SpecVersion() string   { return "1.0" }
func (e ClientDeleted) Source() string        { return Source }
func (e ClientDeleted) Subject() string       { return subjectFor(e.ClientID) }
func (e ClientDeleted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ClientDeleted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ClientDeleted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ClientDeleted) CausationID() string   { return e.Metadata.CausationID }
func (e ClientDeleted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ClientDeleted) MessageGroup() string  { return groupFor(e.ClientID) }
func (e ClientDeleted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ClientID   string `json:"clientId"`
		Identifier string `json:"identifier"`
	}{e.ClientID, e.Identifier})
}
