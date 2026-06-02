// Package operations holds the EventType use cases. Each file is one
// write operation; the file shape (Command, UseCase, Validate/Authorize/
// Execute) mirrors fc-platform/src/event_type/operations/*.rs.
package operations

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

// EventTypeCreated is the domain event emitted on successful creation.
// Wire format includes the CloudEvents metadata flattened via MarshalJSON.
type EventTypeCreated struct {
	Metadata    usecase.EventMetadata
	EventTypeID string
	Code        string
	Name        string
	Description *string
	Application string
	Subdomain   string
	Aggregate   string
	EventName   string
	ClientID    *string
}

// EventTypeUpdated is emitted on update.
type EventTypeUpdated struct {
	Metadata    usecase.EventMetadata
	EventTypeID string
	Name        string
	Description *string
}

// EventTypeDeleted is emitted on delete.
type EventTypeDeleted struct {
	Metadata    usecase.EventMetadata
	EventTypeID string
	Code        string
}

// EventTypeSchemaAdded is emitted when a schema version is added.
// The `Version` field carries the schema version string (e.g. "1.0");
// it cannot be named `SpecVersion` because that collides with the
// DomainEvent.SpecVersion() method (the CloudEvents spec_version).
type EventTypeSchemaAdded struct {
	Metadata    usecase.EventMetadata
	EventTypeID string
	Version     string
}

// Event type strings, source, subject builders. Matches the
// platform_event_types.rs catalog byte-for-byte (drop-in parity).
const (
	EventTypeCreatedType          = "platform:admin:eventtype:created"
	EventTypeUpdatedType          = "platform:admin:eventtype:updated"
	EventTypeDeletedType          = "platform:admin:eventtype:deleted"
	EventTypeArchivedType         = "platform:admin:eventtype:archived"
	EventTypeSchemaAddedType      = "platform:admin:eventtype:schema-added"
	EventTypeSchemaFinalisedType  = "platform:admin:eventtype:schema-finalised"
	EventTypeSchemaDeprecatedType = "platform:admin:eventtype:schema-deprecated"
	EventTypesSyncedType          = "platform:admin:eventtypes:synced"
	EventTypeSourceConst          = "platform:admin"
)

// EventTypesSynced is the rollup event emitted by SyncEventTypesUseCase.
type EventTypesSynced struct {
	Metadata        usecase.EventMetadata
	ApplicationCode string
	Created         uint32
	Updated         uint32
	Deleted         uint32
	SyncedCodes     []string
}

func (e EventTypesSynced) EventID() string       { return e.Metadata.EventID }
func (e EventTypesSynced) EventType() string     { return EventTypesSyncedType }
func (e EventTypesSynced) SpecVersion() string   { return "1.0" }
func (e EventTypesSynced) Source() string        { return EventTypeSourceConst }
func (e EventTypesSynced) Subject() string       { return "platform.eventtypes." + e.ApplicationCode }
func (e EventTypesSynced) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypesSynced) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypesSynced) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypesSynced) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypesSynced) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypesSynced) MessageGroup() string  { return "platform:eventtypes:" + e.ApplicationCode }
func (e EventTypesSynced) ToDataJSON() ([]byte, error) {
	syncedCodes := e.SyncedCodes
	if syncedCodes == nil {
		syncedCodes = []string{}
	}
	return json.Marshal(struct {
		ApplicationCode string   `json:"applicationCode"`
		Created         uint32   `json:"created"`
		Updated         uint32   `json:"updated"`
		Deleted         uint32   `json:"deleted"`
		SyncedCodes     []string `json:"syncedCodes"`
	}{e.ApplicationCode, e.Created, e.Updated, e.Deleted, syncedCodes})
}

// suppress unused-import on fmt; it's referenced by some operation files
// that share this events.go's helpers.
var _ = fmt.Sprintf

func subjectFor(id string) string { return "platform.eventtype." + id }

// ── DomainEvent impls ─────────────────────────────────────────────────────

func (e EventTypeCreated) EventID() string       { return e.Metadata.EventID }
func (e EventTypeCreated) EventType() string     { return EventTypeCreatedType }
func (e EventTypeCreated) SpecVersion() string   { return "1.0" }
func (e EventTypeCreated) Source() string        { return EventTypeSourceConst }
func (e EventTypeCreated) Subject() string       { return subjectFor(e.EventTypeID) }
func (e EventTypeCreated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypeCreated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypeCreated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypeCreated) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypeCreated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypeCreated) MessageGroup() string  { return e.Metadata.MessageGroup }
func (e EventTypeCreated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventTypeID string  `json:"eventTypeId"`
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Description *string `json:"description,omitempty"`
		Application string  `json:"application"`
		Subdomain   string  `json:"subdomain"`
		Aggregate   string  `json:"aggregate"`
		EventName   string  `json:"eventName"`
		ClientID    *string `json:"clientId,omitempty"`
	}{e.EventTypeID, e.Code, e.Name, e.Description, e.Application, e.Subdomain, e.Aggregate, e.EventName, e.ClientID})
}

func (e EventTypeUpdated) EventID() string       { return e.Metadata.EventID }
func (e EventTypeUpdated) EventType() string     { return EventTypeUpdatedType }
func (e EventTypeUpdated) SpecVersion() string   { return "1.0" }
func (e EventTypeUpdated) Source() string        { return EventTypeSourceConst }
func (e EventTypeUpdated) Subject() string       { return subjectFor(e.EventTypeID) }
func (e EventTypeUpdated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypeUpdated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypeUpdated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypeUpdated) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypeUpdated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypeUpdated) MessageGroup() string  { return e.Metadata.MessageGroup }
func (e EventTypeUpdated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventTypeID string  `json:"eventTypeId"`
		Name        string  `json:"name"`
		Description *string `json:"description,omitempty"`
	}{e.EventTypeID, e.Name, e.Description})
}

func (e EventTypeDeleted) EventID() string       { return e.Metadata.EventID }
func (e EventTypeDeleted) EventType() string     { return EventTypeDeletedType }
func (e EventTypeDeleted) SpecVersion() string   { return "1.0" }
func (e EventTypeDeleted) Source() string        { return EventTypeSourceConst }
func (e EventTypeDeleted) Subject() string       { return subjectFor(e.EventTypeID) }
func (e EventTypeDeleted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypeDeleted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypeDeleted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypeDeleted) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypeDeleted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypeDeleted) MessageGroup() string  { return e.Metadata.MessageGroup }
func (e EventTypeDeleted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventTypeID string `json:"eventTypeId"`
		Code        string `json:"code"`
	}{e.EventTypeID, e.Code})
}

func (e EventTypeSchemaAdded) EventID() string       { return e.Metadata.EventID }
func (e EventTypeSchemaAdded) EventType() string     { return EventTypeSchemaAddedType }
func (e EventTypeSchemaAdded) SpecVersion() string   { return "1.0" }
func (e EventTypeSchemaAdded) Source() string        { return EventTypeSourceConst }
func (e EventTypeSchemaAdded) Subject() string       { return subjectFor(e.EventTypeID) }
func (e EventTypeSchemaAdded) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypeSchemaAdded) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypeSchemaAdded) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypeSchemaAdded) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypeSchemaAdded) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypeSchemaAdded) MessageGroup() string  { return e.Metadata.MessageGroup }
func (e EventTypeSchemaAdded) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventTypeID string `json:"eventTypeId"`
		Version     string `json:"specVersion"`
	}{e.EventTypeID, e.Version})
}

// EventTypeArchived is emitted when an event type transitions
// CURRENT → ARCHIVED via the archive endpoint.
type EventTypeArchived struct {
	Metadata    usecase.EventMetadata
	EventTypeID string
	Code        string
}

func (e EventTypeArchived) EventID() string       { return e.Metadata.EventID }
func (e EventTypeArchived) EventType() string     { return EventTypeArchivedType }
func (e EventTypeArchived) SpecVersion() string   { return "1.0" }
func (e EventTypeArchived) Source() string        { return EventTypeSourceConst }
func (e EventTypeArchived) Subject() string       { return subjectFor(e.EventTypeID) }
func (e EventTypeArchived) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypeArchived) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypeArchived) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypeArchived) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypeArchived) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypeArchived) MessageGroup() string  { return e.Metadata.MessageGroup }
func (e EventTypeArchived) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventTypeID string `json:"eventTypeId"`
		Code        string `json:"code"`
	}{e.EventTypeID, e.Code})
}

// EventTypeSchemaFinalised is emitted when a spec version transitions
// FINALISING → CURRENT. If finalising forced another existing CURRENT
// spec to DEPRECATED (auto-deprecate by major-version), the version
// string of that side-effect is carried in DeprecatedVersion.
type EventTypeSchemaFinalised struct {
	Metadata          usecase.EventMetadata
	EventTypeID       string
	Version           string
	DeprecatedVersion *string
}

func (e EventTypeSchemaFinalised) EventID() string       { return e.Metadata.EventID }
func (e EventTypeSchemaFinalised) EventType() string     { return EventTypeSchemaFinalisedType }
func (e EventTypeSchemaFinalised) SpecVersion() string   { return "1.0" }
func (e EventTypeSchemaFinalised) Source() string        { return EventTypeSourceConst }
func (e EventTypeSchemaFinalised) Subject() string       { return subjectFor(e.EventTypeID) }
func (e EventTypeSchemaFinalised) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypeSchemaFinalised) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypeSchemaFinalised) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypeSchemaFinalised) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypeSchemaFinalised) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypeSchemaFinalised) MessageGroup() string  { return e.Metadata.MessageGroup }
func (e EventTypeSchemaFinalised) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventTypeID       string  `json:"eventTypeId"`
		Version           string  `json:"specVersion"`
		DeprecatedVersion *string `json:"deprecatedVersion,omitempty"`
	}{e.EventTypeID, e.Version, e.DeprecatedVersion})
}

// EventTypeSchemaDeprecated is emitted when a spec version transitions
// CURRENT → DEPRECATED via the deprecate endpoint.
type EventTypeSchemaDeprecated struct {
	Metadata    usecase.EventMetadata
	EventTypeID string
	Version     string
}

func (e EventTypeSchemaDeprecated) EventID() string       { return e.Metadata.EventID }
func (e EventTypeSchemaDeprecated) EventType() string     { return EventTypeSchemaDeprecatedType }
func (e EventTypeSchemaDeprecated) SpecVersion() string   { return "1.0" }
func (e EventTypeSchemaDeprecated) Source() string        { return EventTypeSourceConst }
func (e EventTypeSchemaDeprecated) Subject() string       { return subjectFor(e.EventTypeID) }
func (e EventTypeSchemaDeprecated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e EventTypeSchemaDeprecated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e EventTypeSchemaDeprecated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e EventTypeSchemaDeprecated) CausationID() string   { return e.Metadata.CausationID }
func (e EventTypeSchemaDeprecated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e EventTypeSchemaDeprecated) MessageGroup() string  { return e.Metadata.MessageGroup }
func (e EventTypeSchemaDeprecated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventTypeID string `json:"eventTypeId"`
		Version     string `json:"specVersion"`
	}{e.EventTypeID, e.Version})
}

// Helper for sanity checks that the event type code is parseable
// (avoids importing eventtype package into this file for cycle reasons).
func ensureFourPartCode(code string) error {
	count := 1
	for _, c := range code {
		if c == ':' {
			count++
		}
	}
	if count != 4 {
		return fmt.Errorf("code must be application:subdomain:aggregate:event")
	}
	return nil
}
